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

// ScanGaps records the ways one inventory pass failed to be exhaustive. It exists for
// the same reason ReachableKnown does: this scan reports a class of failure that is
// invisible by construction, so "the inventory ran and found no servers" and "the
// inventory could not see" must not render as the same sentence. Without it a read
// error on either source below produces an empty list, which is indistinguishable
// from a clean host.
//
// The fields are independent rather than one bool because the two gaps have different
// consequences and therefore different remedies — see the renderer.
type ScanGaps struct {
	// SocketTableUnread: /proc/net/unix could not be read. A server is identified by
	// the socket it is listening on, so with this table missing every candidate loses
	// its path and is dropped — the scan reports nothing at all, not merely less.
	SocketTableUnread bool
	// ProcTableTruncated: the walk over /proc did not finish, because the context
	// expired part-way. Servers may be absent from the result, and a server that *is*
	// reported may carry an undercounted Children list, since children are attributed
	// from the same partial table.
	ProcTableTruncated bool
}

// Any reports whether the pass left anything unseen, and so whether an empty or short
// result is allowed to be read as proof.
func (g ScanGaps) Any() bool { return g.SocketTableUnread || g.ProcTableTruncated }

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
)

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
	live, liveKnown := ambientPID(ctx)
	cands, gaps := scanCandidates(ctx)
	return assembleServers(ctx, cands, live, liveKnown), true, gaps
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
		owner, known := socketOwner(ctx, c.SocketPath)
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

// ScanStaleSockets lists the socket files in Atrium's socket directory that nothing
// is listening on, and the directory it looked in.
//
// It is read-only and stays that way: it removes nothing, and neither does any
// caller. A sweep over this directory is what #584 shipped and #586 removed after it
// took the live socket and thirteen running sessions with it; the whole remedy here
// is to name the files and let the user decide.
//
// A file is reported only when it passes ownsSocketName *and* the probe positively
// answers that nothing is there. A probe that could not run leaves the file
// unreported: absence of an answer is not evidence.
func ScanStaleSockets(ctx context.Context) (stale []StaleSocket, dir string) {
	dir = socketDir(ctx)
	return staleSocketsIn(ctx, dir), dir
}

// staleSocketsIn is ScanStaleSockets over an explicit directory, split out so the
// filtering is testable without depending on where this host keeps its sockets.
func staleSocketsIn(ctx context.Context, dir string) []StaleSocket {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
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
		// answered owns its file, and a probe that could not run is not evidence
		// about anything — both leave the file unreported.
		owner, known := socketOwner(ctx, path)
		if !known || owner != 0 {
			continue
		}
		stale = append(stale, StaleSocket{Path: path, ModTime: info.ModTime()})
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].Path < stale[j].Path })
	return stale
}

// socketDir returns the directory Atrium's tmux socket lives in.
//
// It asks the live server first — `#{socket_path}` is the server's own answer, so it
// assumes nothing about tmux's layout. Only with no server running does it fall back
// to reconstructing $TMUX_TMPDIR/tmux-<uid>, mirroring tmux's own rule that an empty
// *or missing* TMUX_TMPDIR means /tmp. Getting that fallback wrong could only ever
// make this report list the wrong directory's files, never delete anything — but it
// is also what the printed remedy names, so it follows tmux rather than guessing.
func socketDir(ctx context.Context) string {
	if out, err := tmuxCommand(ctx, "display-message", "-p", "#{socket_path}").Output(); err == nil {
		if path := strings.TrimSpace(string(out)); path != "" {
			return filepath.Dir(path)
		}
	}
	root := os.Getenv("TMUX_TMPDIR")
	if info, err := os.Stat(root); root == "" || err != nil || !info.IsDir() {
		// Literally "/tmp", not os.TempDir(): os.TempDir() honours $TMPDIR and tmux
		// does not, so on any host with TMPDIR set — every macOS one, where it is a
		// per-user /var/folders/… path — os.TempDir() names a directory tmux never
		// binds in, and the stale-socket list would report "none in <wrong dir>".
		root = tmuxDefaultTmpdir
	}
	return filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
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

// ambientServerPID returns the pid of the tmux server on Atrium's socket under the
// ambient environment — the one this Atrium is running on, which is never an orphan.
// found is false when no server is running there, which is the empty-fleet case
// rather than an error: there is then simply nothing to exclude.
func ambientServerPID(ctx context.Context) (pid int, found bool) {
	out, err := tmuxCommand(ctx, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
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
	pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return pid, true
}
