package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// orphanBrands are the socket-name stems Atrium binds, in both its brands. A scan
// keys on both unconditionally rather than on config.RuntimeName(), which resolves
// from the *scanning* process's own HOME: an orphan is by definition a server some
// other run started, possibly under another HOME, so keying on the local brand alone
// would make a legacy install blind to "atrium" orphans and a fresh one blind to
// "claudesquad" orphans. clean.sh already handles both this way.
var orphanBrands = []string{"atrium", "claudesquad"}

// tmuxDefaultTmpdir is where tmux puts its socket directory when TMUX_TMPDIR is
// empty or names a directory that is not there. tmux hardcodes /tmp for that case
// and consults no other variable, so reconstructing its layout must hardcode it too.
const tmuxDefaultTmpdir = "/tmp"

// ChildProc is one process an orphaned tmux server is the parent of — in practice
// the agent's shell and the agent itself. Started is captured so the reaper can
// apply the same PID-reuse guard to a child that it applies to the server; a child's
// pid is staler than its parent's by the time signalling reaches it.
type ChildProc struct {
	PID     int
	Comm    string
	Started time.Time
}

// OrphanServer is one Atrium-owned tmux server found by process scan that is not the
// live server this Atrium runs on.
//
// Reachable and ReachableKnown are separate on purpose. Reachability is decided by
// running tmux against the socket, so "the probe said nothing is there" and "the
// probe could not run" are different facts with opposite safety consequences: with
// tmux off PATH nothing can be probed *and* the live server cannot be excluded, so
// every live session's server would look unreachable. Only ReachableKnown &&
// !Reachable is positive proof of an orphan nothing can address.
type OrphanServer struct {
	PID int
	// Socket is the socket name: the base of SocketPath. It is never taken from the
	// process's argv, which carries injected GH_TOKEN / GITHUB_PERSONAL_ACCESS_TOKEN
	// values.
	Socket string
	// SocketPath is the path the server is bound to, read from /proc/net/unix. The
	// kernel keeps it after the file is unlinked, which is what makes an orphan whose
	// TMUX_TMPDIR root was deleted identifiable at all (#547). Always non-empty: a
	// process with no listening socket is not a server (see assembleServers).
	SocketPath string
	// Reachable reports that SocketPath answers, and answers with this server's own
	// pid. Meaningful only when ReachableKnown.
	Reachable      bool
	ReachableKnown bool
	// Started is when the server process started, and doubles as the PID-reuse guard:
	// the reaper re-reads it immediately before signalling and refuses on a mismatch.
	Started time.Time
	// CWDDeleted reports that the server's working directory has been removed — the
	// signature of a run whose temp root was cleaned up around it.
	CWDDeleted bool
	Children   []ChildProc
}

// ScanGaps records the questions one scan pass could not answer. It exists for the
// same reason ReachableKnown does: this scan reports a class of failure that is
// invisible by construction, so "the inventory ran and found no servers" and "the
// inventory could not see" must not render as the same sentence. Without it a read
// error on either source below produces an empty list, which is indistinguishable
// from a clean host.
//
// The fields are independent bools rather than one because each unanswered question
// has its own consequence and therefore its own remedy — see the renderer. They are
// also not all of a kind: the first two are gaps in the *inventory* and are what
// Any() reports, while the last two leave the inventory complete and constrain only
// what may be done with it. Any()'s doc comment is where that split is argued, and
// LiveServerUnidentified() is the one predicate those two share.
type ScanGaps struct {
	// SocketTableUnread: /proc/net/unix could not be read. A server is identified by
	// the socket it is listening on, so with this table missing every candidate loses
	// its path and is dropped — the scan reports nothing at all, not merely less.
	SocketTableUnread bool
	// ProcTableTruncated: the walk over /proc did not cover it — either the directory
	// could not be listed at all, or the context expired part-way. Servers may be
	// absent from the result, and a server that *is* reported may carry an
	// undercounted Children list, since children are attributed from the same partial
	// table.
	ProcTableTruncated bool
	// LiveServerUnknown: the ambient probe was never answered — tmux absent, or its share
	// of the budget spent — so which server this Atrium is running on was not established
	// at all. Nothing can then be excluded by pid, so the live server may itself be one of
	// the rows below. It answers its own socket, so it arrives classified Reachable: never
	// a default kill target, but `--all` targets reachable servers by design, and the
	// report's remedy for a reachable server is a `kill-server` aimed straight at it.
	//
	// Deliberately NOT counted by Any(). See that method.
	LiveServerUnknown bool
	// EmptyFleetUnproven: the ambient probe *did* answer — "no server on the socket it
	// asked about" — and this scan's own inventory contradicts reading that as "no live
	// server on this host", because a reachable Atrium-owned server is listed below.
	//
	// The probe addresses `-L socketName()` (see ambientServerPID), a socket resolved from
	// *this* process's HOME against *this* process's TMUX_TMPDIR, so its determination is
	// about the place it looked and nothing wider. A reachable server it says is not there
	// means it looked elsewhere: another TMUX_TMPDIR, a deleted one (tmux then resolves -L
	// against /tmp), the other brand, or a managed config that failed to parse. The
	// consequence is precisely LiveServerUnknown's — a live pid of 0 excludes no process in
	// assembleServers, so the live server may be one of these Reachable rows — which is why
	// the two share LiveServerUnidentified() (#603).
	//
	// A separate field rather than folded into LiveServerUnknown, and mutually exclusive
	// with it by construction (that one needs !liveKnown, this one liveKnown), because the
	// remedies diverge: an unanswered probe is fixed by re-running, while this one re-runs
	// to the same answer about the same wrong socket — and here tmux demonstrably works, so
	// the row can be inspected with `tmux -S <path> ls` instead of merely feared.
	//
	// Its subject is *this run's own* server, and that bound is deliberate rather than an
	// oversight. When the ambient probe does name a pid, this stays false even though a second
	// fleet may be running under another TMUX_TMPDIR or the other brand: that fleet arrives
	// Reachable, keeps its verified `kill-server`, and is a target under `--all` — which is
	// what `--all` is for, and what reapTargets' doc comment already says about a second
	// Atrium. The guard protects the fleet the reaper cannot see past its own environment, not
	// every fleet on the host.
	//
	// Deliberately NOT counted by Any() either. See that method.
	EmptyFleetUnproven bool
}

// Any reports whether the *inventory* left anything unseen, and so whether an empty or
// short result is allowed to be read as proof. Callers use it to refuse to kill at all.
//
// LiveServerUnknown and EmptyFleetUnproven are both excluded on purpose, and the
// exclusion is load-bearing rather than an oversight. Neither makes the inventory
// incomplete: every candidate was still seen and still classified, and a live server that
// could not be identified by pid is still positively Reachable, which the default target
// set already excludes. The last regression on this code came from refusing too eagerly —
// a self-inflicted gap became a self-inflicted refusal on exactly the host that needed
// reaping — so the remedy for those two is scoped to the decisions they actually affect:
// `--all`, plus the report's remedy line. TestScanGapsAny pins it.
func (g ScanGaps) Any() bool { return g.SocketTableUnread || g.ProcTableTruncated }

// LiveServerUnidentified reports that no row this scan produced has been proven not to be
// this Atrium's own tmux server. It exists so that "identified" means one thing — a live pid
// was positively determined, and so excluded — wherever that question is asked.
//
// Its two causes reach an identical state: the exclusion in assembleServers matched
// nothing, while a live server answers its own socket and so arrives Reachable. Neither is
// an inventory gap, which is why Any() counts neither.
//
// Which callers use this, and which do not, follows from what each one has to say. The
// `--all` guard asks only whether it may act, so it keys on this (cli_reap.go). The report
// has to word a remedy, and the two causes take different ones — a re-run fixes an
// unanswered probe and cannot fix a wrong-place answer — so renderOrphanServer branches on
// the fields themselves instead.
func (g ScanGaps) LiveServerUnidentified() bool {
	return g.LiveServerUnknown || g.EmptyFleetUnproven
}

// candidate is one process the platform inventory judged worth classifying: owned by
// this uid and plausibly a tmux server. Ownership is decided from these fields by
// assembleServers, not by the inventory.
type candidate struct {
	PID        int
	SocketPath string
	Started    time.Time
	CWDDeleted bool
	Children   []ChildProc
}

// Seams, mirroring internal/doctor/oom.go's, so the assembly below is testable
// without a live tmux server or a real /proc. They are package-level shared state:
// a test that swaps one must not run in parallel.
var (
	scanCandidates = inventoryCandidates
	ambientPID     = ambientServerPID
	socketOwner    = probeSocketOwner
	// socketPathQuery is the live server's answer for where its own socket is. It is a
	// seam for the same reason candidatesIn's injected table is one (#593): both
	// branches of SocketDir set a fact the report words itself around, and a fact no
	// test can drive is one a deleted assignment keeps green.
	socketPathQuery = liveSocketPath
)

// orphanProbeBudget is the share of a scan any single `tmux … display-message` probe
// may take. The scan-wide budget its callers set (doctor.ProbeTimeout) is still the
// ceiling; this bounds what one socket can spend of it.
//
// Without it one wedged server consumed the whole scan, and with a short walk now
// reported as a gap that became a blank report: the ambient probe could burn every
// millisecond of the budget, the /proc walk then found ctx.Err() already set on its
// first entry, and the scan reported a truncation it had inflicted on itself. Since
// `reap --kill` refuses on a gap, a wedged live server made the reaper decline — in
// exactly the situation it exists for. A probe that does not answer inside this window
// leaves that server's reachability unknown, which is the honest answer and one every
// caller already handles.
const orphanProbeBudget = 3 * time.Second

// ScanServers returns the Atrium-owned tmux servers running under this uid, other
// than the live one on the ambient socket, ordered by pid. supported is false off
// Linux, where there is no /proc to inventory and the callers render the section as
// unavailable.
//
// gaps reports whether the inventory could actually see everything. An empty servers
// slice means "none found" only when gaps.Any() is false; otherwise the scan was
// blind, and callers must not read the emptiness as a clean host. gaps is always zero
// when supported is false, so the unsupported branch is the only thing a caller has to
// explain there.
//
// It is read-only: it reads /proc and issues `tmux -S … display-message`, which
// cannot mutate a session. Nothing here signals, unlinks or deletes anything.
func ScanServers(ctx context.Context) (servers []OrphanServer, supported bool, gaps ScanGaps) {
	if !orphanScanSupported {
		return nil, false, ScanGaps{}
	}
	// The /proc walk goes before any tmux command, and that order is load-bearing now
	// that a short walk is reported as a gap: the walk is local reads costing
	// milliseconds, while a probe against a wedged server costs its whole budget.
	// Probing first let a hung tmux manufacture a gap out of a walk that never ran.
	cands, gaps := scanCandidates(ctx)
	live, liveKnown := probeAmbient(ctx)
	gaps.LiveServerUnknown = !liveKnown
	servers = assembleServers(ctx, cands, live, liveKnown)
	// The empty-fleet answer, verified instead of assumed. `pid 0, known true` is tmux
	// reporting that nothing is on the socket *this process* addressed, which is only "this
	// host has no live Atrium server" when nothing else answers for one — and this scan has
	// just asked every candidate by absolute path, which resolves where -L may not. A
	// reachable Atrium-owned server that the ambient probe says is not there is that
	// contradiction, and it leaves the live server unexcluded exactly as an unanswered probe
	// does (#603).
	//
	// Unreachable rows are not counted. An unreachable candidate is what an orphan looks like
	// and is no evidence of a live fleet, so counting those would raise this on every host the
	// reaper exists for — #593's over-refusal, rebuilt.
	//
	// Neither are rows on a socket name no ambient probe could have been asking about, which
	// is what onAnAmbientSocket decides. Without that narrowing the cost was paid on the
	// ordinary host rather than the rare one: `live == 0` is the answer whenever no TUI is up,
	// so a single leaked `-precheck-` or verification socket server withheld `--all` for the
	// whole run, and re-running never cleared it. What still withholds it is a bare-brand row
	// — the shape a fleet under another TMUX_TMPDIR or the other brand has, which is #603's
	// case exactly.
	gaps.EmptyFleetUnproven = liveKnown && live == 0 && anyReachableAmbientCandidate(servers)
	return servers, true, gaps
}

// probeAmbient asks which server is on the ambient socket, under one probe's share of
// the budget. A wedged live server then costs this scan one probe rather than all of
// it, and spending that budget is one of the ways the answer comes back unknown.
//
// known, not found: the distinction ambientServerPID draws is the whole point of it,
// and a wrapper that renamed the result back would put the discarded word in front of
// every reader who arrives here first.
func probeAmbient(ctx context.Context) (pid int, known bool) {
	probeCtx, cancel := context.WithTimeout(ctx, orphanProbeBudget)
	defer cancel()
	return ambientPID(probeCtx)
}

// AmbientServerPID reports which tmux server is on Atrium's socket — the one this
// Atrium runs on — under one probe's share of the scan budget.
//
// known is the whole reason this is exported rather than reimplemented. "tmux ran and
// there is no server on that socket" is a determined answer and a safe one; "tmux could
// not be asked" — the binary absent, the probe's budget spent, a fork that failed under
// memory pressure — establishes nothing. A caller that collapses the two reports an
// empty fleet on a host that has a live one. That was #599 here, and it was still true
// in internal/doctor's OOM section afterwards, which ran its own copy of this probe with
// the classification left out.
//
// One home for the rule is the point: classifyPIDProbe's doc comment argues that a rule
// about what counts as evidence is the last thing that should be able to drift between
// two call sites, and a second caller is exactly how that drift happens.
func AmbientServerPID(ctx context.Context) (pid int, known bool) { return probeAmbient(ctx) }

// probeOwner asks which server is listening on path, under one probe's share of the
// budget. Every owner probe here goes through it, so a socket that never answers costs
// this scan one probe rather than all the others' time.
func probeOwner(ctx context.Context, path string) (pid int, known bool) {
	probeCtx, cancel := context.WithTimeout(ctx, orphanProbeBudget)
	defer cancel()
	return socketOwner(probeCtx, path)
}

// assembleServers turns platform candidates into classified servers: it drops the
// live server by pid, drops anything that is not listening on a socket Atrium owns,
// and resolves reachability. Split out from ScanServers so the classification is
// testable on every platform, since it is where the safety decisions are made.
//
// A bound, *listening* socket is what makes a process a server, and it is required
// rather than merely preferred. Measured on this host: 14 of 15 processes reaching
// this point were `tmux: client` attach proxies for live Atrium sessions. A client
// passes the comm prefilter and has no listening socket, so a design that fell back
// to reading `-L <name>` out of argv when the path was missing claimed every one of
// them — turning live attach clients into reap candidates. Nothing is lost by
// refusing them: a process that is not listening cannot be a server, and for an
// own-uid process on Linux a real server's row is always readable.
func assembleServers(ctx context.Context, cands []candidate, live int, liveKnown bool) []OrphanServer {
	var servers []OrphanServer
	for _, c := range cands {
		if liveKnown && c.PID == live {
			continue
		}
		// Defence in depth, not the only line holding this: with the argv fallback
		// gone, an empty path would also fail ownsSocketName below, since
		// filepath.Base("") is ".". Mutating this check away leaves the suite green.
		// It stays because it states the rule — a process that is not listening is
		// not a server — where a reader looking for that rule will find it, rather
		// than leaving it as an emergent property of two unrelated functions.
		if c.SocketPath == "" {
			continue
		}
		socket := filepath.Base(c.SocketPath)
		if !ownsSocketName(socket) {
			continue
		}
		owner, known := probeOwner(ctx, c.SocketPath)
		servers = append(servers, OrphanServer{
			PID:            c.PID,
			Socket:         socket,
			SocketPath:     c.SocketPath,
			Reachable:      known && owner == c.PID,
			ReachableKnown: known,
			Started:        c.Started,
			CWDDeleted:     c.CWDDeleted,
			Children:       c.Children,
		})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].PID < servers[j].PID })
	return servers
}

// AnyReachable reports whether any of these servers answered its own socket. The reaper asks
// it of its selected targets, to decide whether an unidentified live server may be among them:
// only --all can put a reachable row there, which makes it the precise test for "would this
// run kill something that might be the live fleet".
//
// Exported for that caller, and deliberately *not* what ScanServers checks its empty-fleet
// answer with — that one narrows further, to a row on a socket name an ambient probe could
// have been asking about. Two questions close enough that the difference is worth naming where
// both are defined (#603).
func AnyReachable(servers []OrphanServer) bool {
	for _, s := range servers {
		if s.Reachable {
			return true
		}
	}
	return false
}

// anyReachableAmbientCandidate reports whether any of these servers both answered its own
// socket and sits on a socket name some Atrium run addresses as its own. Together those are
// what make a row a candidate for the live server a determined-empty ambient answer failed to
// identify: it has to be running — it answered — and it has to be the kind of server that
// answer was about.
func anyReachableAmbientCandidate(servers []OrphanServer) bool {
	for _, s := range servers {
		if s.Reachable && s.OnAnAmbientSocket() {
			return true
		}
	}
	return false
}

// StaleSocket is a socket file in Atrium's socket directory that no server answers —
// the cosmetic half of #547. tmux never unlinks a socket when its server dies, so
// every killed probe and every crashed run leaves one behind, in the directory that
// also holds the live socket.
//
// ModTime dates the file, which is when its server bound it.
type StaleSocket struct {
	Path    string
	ModTime time.Time
}

// StaleGaps records the ways one stale-socket pass failed to establish what it
// reports. It exists for the same reason ScanGaps does: a file reaches the list only
// when a probe positively answered that nothing holds it, so a pass that could not
// read the directory and a pass whose every probe failed both produce an empty list —
// which is byte-identical to a directory that really is clean. "none in <dir>" is then
// a claim of health manufactured out of having seen nothing (#598).
//
// The two fields are independent because the remedies differ: an unlistable directory
// is an existence or permission problem, while unrunnable probes mean tmux could not be
// executed. Unlike ScanGaps neither makes any caller refuse to act, because nothing
// acts on this list — reap only prints it, and the remedy is an `rm --` line the user
// runs. These gaps cost the accuracy of a report and nothing else.
type StaleGaps struct {
	// DirUnread: the socket directory could not be listed at all, so nothing about its
	// contents is known — not "no stale files" but "no answer".
	DirUnread bool
	// Unprobed counts the socket files that were listed and belong to Atrium but could
	// not be classified: the owner probe did not run, because tmux is off PATH or the
	// scan's budget expired. Absence of an answer is not evidence of an absent server,
	// so those files stay off the list. Renderers must not name one of those causes as
	// though it were the only one — a remedy that says "check PATH" is wrong advice on a
	// host whose PATH is fine and whose budget ran out.
	//
	// A count rather than a bool so a caller can tell a directory whose every file is
	// unproven from a list that is merely one file short; a bool has to hedge both the
	// same way.
	//
	// A file the pass *understood* is never counted, and the exemptions are exemptions
	// because each is an answer rather than an open question: a foreign socket name and a
	// regular file are both positively identified as not-ours, and an entry whose Info()
	// fails has been removed between the listing and the stat — a socket that is gone is
	// not a stale socket being concealed. None of the three can manufacture a gap about a
	// directory that is in fact fully understood.
	Unprobed int
}

// Any reports whether the pass left anything unestablished, and so whether an empty
// list is allowed to be read as a clean directory.
func (g StaleGaps) Any() bool { return g.DirUnread || g.Unprobed > 0 }

// StaleScan is one stale-socket pass: what it found, where it looked, how it knows
// where that is, and what it could not see there.
type StaleScan struct {
	Stale []StaleSocket
	// Dir is the directory the pass listed, and the one a printed remedy names.
	Dir string
	// DirFromServer reports that Dir is the live server's own answer for where its
	// socket is, rather than a reconstruction of tmux's layout. When false there was no
	// server to ask, so Dir is where tmux *would* bind — which makes "none in Dir" a
	// true statement about a directory no server need ever have bound in.
	//
	// That is a wrong subject rather than an unproven claim, which is why it is a plain
	// fact here and not a StaleGaps field: the pass over Dir was complete and its
	// result is proven, so a caller words the sentence differently instead of hedging
	// it (#598).
	DirFromServer bool
	Gaps          StaleGaps
}

// ScanStaleSockets lists the socket files in Atrium's socket directory that nothing is
// listening on, alongside the directory it looked in and what it could not establish
// there.
//
// It is read-only and stays that way: it removes nothing, and neither does any
// caller. A sweep over this directory is what #584 shipped and #586 removed after it
// took the live socket and thirteen running sessions with it; the whole remedy here
// is to name the files and let the user decide.
//
// A file is reported only when it passes ownsSocketName *and* the probe positively
// answers that nothing is there. A probe that could not run leaves the file unreported
// and counted in Gaps.Unprobed: absence of an answer is not evidence, and an empty
// Stale is a clean directory only when Gaps.Any() is false.
func ScanStaleSockets(ctx context.Context) StaleScan {
	dir, fromServer := SocketDir(ctx)
	stale, gaps := staleSocketsIn(ctx, dir)
	return StaleScan{Stale: stale, Dir: dir, DirFromServer: fromServer, Gaps: gaps}
}

// staleSocketsIn is ScanStaleSockets over an explicit directory, split out so the
// filtering — and the gaps it reports — are testable without depending on where this
// host keeps its sockets.
func staleSocketsIn(ctx context.Context, dir string) ([]StaleSocket, StaleGaps) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Not "no stale sockets here": nothing here was seen at all. Reporting this as
		// an empty list is the conflation #598 fixed.
		return nil, StaleGaps{DirUnread: true}
	}
	var gaps StaleGaps
	var stale []StaleSocket
	for _, e := range entries {
		if !ownsSocketName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// Stale means the probe ran *and* found nothing listening. A server that
		// answered owns its file, and a probe that could not run is not evidence about
		// anything — both leave the file unreported, but only the second one leaves a
		// hole in the answer, so only it is counted.
		owner, known := probeOwner(ctx, path)
		if !known {
			gaps.Unprobed++
			continue
		}
		if owner != 0 {
			continue
		}
		stale = append(stale, StaleSocket{Path: path, ModTime: info.ModTime()})
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].Path < stale[j].Path })
	return stale, gaps
}

// probeSocketPath asks the live server for its socket path under one probe's share of
// the budget, exactly as probeAmbient and probeOwner do.
//
// The bound is not symmetry for its own sake. This probe runs before every per-file
// owner probe of the same scan, under one shared deadline, so unbounded it can spend the
// entire budget against a wedged server and leave every later probe to fail on an
// expired context. The stale section would then report a directory full of files it
// could not classify with nothing actually wrong with them — the self-inflicted gap
// orphanProbeBudget was introduced for, reached from the stale half instead of the
// ambient one.
func probeSocketPath(ctx context.Context) (path string, ok bool) {
	probeCtx, cancel := context.WithTimeout(ctx, orphanProbeBudget)
	defer cancel()
	return socketPathQuery(probeCtx)
}

// liveSocketPath asks the server on Atrium's socket where that socket is. ok is false
// when there is no server to ask and when tmux could not be run at all: both mean the
// same thing to SocketDir, which is that there is no server's answer to use. The two
// are worth no more than this because neither says anything about the *directory* —
// only about whether a server was there to name it. What the *caller* may then claim is
// narrower than "nothing is running", and renderStaleSockets says so.
func liveSocketPath(ctx context.Context) (path string, ok bool) {
	out, err := tmuxCommand(ctx, "display-message", "-p", "#{socket_path}").Output()
	if err != nil {
		return "", false
	}
	path = strings.TrimSpace(string(out))
	return path, path != ""
}

// SocketDir returns the directory Atrium's tmux socket lives in, and whether that is
// the live server's own answer for it.
//
// It asks the server first — `#{socket_path}` is the server's own answer, so it
// assumes nothing about tmux's layout. Only with no server running does it fall back
// to reconstructing $TMUX_TMPDIR/tmux-<uid>, mirroring tmux's own rule that an empty
// *or missing* TMUX_TMPDIR means /tmp. Getting that fallback wrong could only ever
// make this report list the wrong directory's files, never delete anything — but it
// is also what the printed remedy names, so it follows tmux rather than guessing.
//
// Exported because the doctor's host-pressure section needs the filesystem this
// directory sits on (#594), and reconstructing the path a second time there is how
// the two answers drift. It runs a tmux subprocess, so callers pass a bounded context;
// the probe itself then takes no more than orphanProbeBudget of whatever that allows.
//
// fromServer separates the two, because the fallback names where tmux *would* bind
// rather than where a server is bound. A caller can then say so, instead of asserting
// "none in <dir>" about a directory nothing has ever bound in (#598). A caller that
// only needs the path — the pressure section asks which filesystem this sits on — is
// free to discard it.
func SocketDir(ctx context.Context) (dir string, fromServer bool) {
	if path, ok := probeSocketPath(ctx); ok {
		return filepath.Dir(path), true
	}
	root := os.Getenv("TMUX_TMPDIR")
	if info, err := os.Stat(root); root == "" || err != nil || !info.IsDir() {
		// Literally "/tmp", not os.TempDir(): os.TempDir() honours $TMPDIR and tmux
		// does not, so on any host with TMPDIR set — every macOS one, where it is a
		// per-user /var/folders/… path — os.TempDir() names a directory tmux never
		// binds in, and the stale-socket list would report "none in <wrong dir>".
		root = tmuxDefaultTmpdir
	}
	return filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid())), false
}

// ownsSocketName reports whether base is a socket name Atrium binds: either brand
// exactly, or either brand followed by "-" and a suffix (the managed-config probe and
// the ad-hoc verification sockets).
//
// The separator is required. Without it "atriumfoo" — someone else's socket — would
// match, and this predicate is the only thing standing between the reaper and a
// process it has no business signalling. A name qualifies because it matches, never
// because it failed to look like someone else's (#584).
func ownsSocketName(base string) bool {
	for _, brand := range orphanBrands {
		if base == brand || strings.HasPrefix(base, brand+"-") {
			return true
		}
	}
	return false
}

// OnAnAmbientSocket reports whether this server sits on a socket name some Atrium run
// addresses as its own: a bare brand, with no suffix.
//
// It is the narrower half of the pair above. ownsSocketName decides what the reaper may
// consider at all, and accepts the suffixed forms because they are Atrium's litter; this
// decides which of those rows could be the server an *ambient* probe was asking about, and
// rejects them. Every Atrium addresses its own server as `-L socketName()` (tmuxCommand), and
// socketName() is config.RuntimeName() — never suffixed — so a row on
// "atrium-precheck-991-1" or "atrium-cfgparse-x7" is not the server any such probe could have
// found or missed, whatever directory it sits in. Those names belong to throwaway probes
// (probeSocketName), which hold a `sleep` and no sessions.
//
// Both brands rather than socketName() alone, for the reason orphanBrands exists: #603's
// brand-mismatch route is a live claudesquad fleet with this run installed as atrium, and
// keying on the local brand would leave exactly that case unguarded.
//
// This decides how much a caller may claim about a row, never whether it may be killed —
// nothing here is evidence that a bare-brand server *is* the live one, only that a suffixed
// one is not.
func (s OrphanServer) OnAnAmbientSocket() bool {
	for _, brand := range orphanBrands {
		if s.Socket == brand {
			return true
		}
	}
	return false
}

// ambientServerPID returns the pid of the tmux server on Atrium's socket under the
// ambient environment — the one this Atrium is running on, which is never an orphan.
//
// known carries the same distinction probeSocketOwner draws, and for the same reason:
// "tmux ran and there is no server on that socket" and "tmux could not be asked" are
// different facts with opposite safety consequences, and this function used to return
// false for both. Only known == false means the question was never answered at all.
//
// What a determined pid 0 establishes is narrower than "the fleet is empty", and the gap
// between the two was worth a guard. The socket asked about is `-L socketName()`
// (tmuxCommand), resolved from *this* process's HOME against *this* process's TMUX_TMPDIR,
// with this run's managed config appended — so pid 0 says "nothing answered where I
// looked". Wherever that is not where the fleet is (another TMUX_TMPDIR, a deleted one,
// which tmux resolves against /tmp, the other brand, a config that fails to parse), the
// live server is still in the candidate list, still unexcluded, and still answering its own
// socket. ScanServers is where that claim is checked against the inventory, and
// ScanGaps.EmptyFleetUnproven is what the check reports: nothing downstream may read pid 0
// as an empty fleet without it (#603).
//
// A non-zero exit means tmux ran and made a determination ("no server running on …");
// anything else — tmux absent, the probe's budget spent — leaves the question open.
func ambientServerPID(ctx context.Context) (pid int, known bool) {
	out, err := tmuxCommand(ctx, "display-message", "-p", "#{pid}").Output()
	return classifyPIDProbe(ctx, out, err)
}

// classifyPIDProbe turns the result of a `display-message -p '#{pid}'` probe into a pid
// and whether the question was answered at all. Both probes here go through it, so the
// rule exists once: they had identical copies, and a rule about what counts as evidence
// is the last thing that should be able to drift between two call sites.
//
// A non-zero exit means tmux ran and made a determination — "no server running on …",
// "error connecting to …" — which is evidence, and answers pid 0. Anything else means
// tmux never got to answer: it is absent, or the probe's budget was spent. That is not
// evidence, and known is false.
func classifyPIDProbe(ctx context.Context, out []byte, err error) (pid int, known bool) {
	// Checked first: a killed-by-deadline process also surfaces as an ExitError, which
	// would otherwise be read as tmux having reported an empty socket.
	if ctx.Err() != nil {
		return 0, false
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return 0, true
		}
		return 0, false
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if convErr != nil {
		return 0, false
	}
	return pid, true
}

// probeSocketOwner asks which server is listening on an absolute socket path.
//
// This is an identity test, not a file test: os.Stat answers "is there a file here
// now", and a restarted Atrium re-binds the same path, so a stale orphan carrying it
// would stat true and be classified reachable — with the remedy then aimed at the
// new, live server.
//
// It builds the command directly rather than through tmuxCommand, which prepends
// `-L <socketName()>`: addressing by absolute path is the whole point, since `-S`
// cannot resolve anywhere but the path given, while `-L` resolves against
// TMUX_TMPDIR and falls back to /tmp when that is empty or missing.
//
// known distinguishes "tmux ran and could not reach a server there" from "tmux could
// not run at all". Only the first is evidence. A non-zero exit means tmux ran and
// made a determination ("no server running on …", "error connecting to …"); anything
// else — tmux absent, the context expired — leaves the question open.
func probeSocketOwner(ctx context.Context, path string) (pid int, known bool) {
	out, err := exec.CommandContext(ctx, "tmux", "-S", path, "display-message", "-p", "#{pid}").Output()
	return classifyPIDProbe(ctx, out, err)
}
