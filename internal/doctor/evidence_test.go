package doctor

// Fault injection for the "I actually looked" channels.
//
// Every negative claim this package prints — "none", "none in <dir>", "no live atrium
// tmux server", "none configured" — is assembled from an independently fallible read. In
// Go a failed read returns a zero value, and the zero value of a collection is *empty*,
// which is byte-identical to a true negative. So each source carries an out-of-band
// completeness flag, each consumer branches on it, and the rule is RenderGates': "the
// check ran and found nothing" and "the check silently had nothing to say" must not look
// identical.
//
// That rule has been broken repeatedly in this one subsystem. #593 (an orphan scan that
// found nothing vs one that could not see), #599 (an empty fleet vs a live server that
// could not be found) and #600 (the stale-socket list saying "none" when it probed
// nothing) are each a fix for one instance of it. Every one shipped with build, vet, lint
// and the suite green, because no test made a source fail — each was found by running the
// real binary or by review, and each fix then invented a bespoke seam after the fact.
//
// This file is the standing version of that test. Four parts, and only the first is
// about sources that exist today:
//
//   - evidenceCases injects a failure at each source and asserts the report never claims
//     health, and names the gap.
//   - TestEveryEvidenceChannelIsCovered cross-checks those rows against the audited types
//     by reflection, and FAILS on a completeness flag no row exercises. A flag added to a
//     type already listed here breaks it rather than shipping quietly.
//   - TestNoEvidenceFlagEscapesTheAuditedSet closes the hole reflection cannot: it reads
//     the sources, because reflect enumerates a type's fields and never a package's types.
//     A flag on a brand-new type — which is how every source below arrived — fails there.
//   - TestAuditedTypesHaveNotGrownAField is the backstop for all three, because all three
//     begin by asking whether a field *name* looks like a flag. #607 added
//     ScanGaps.EmptyFleetUnproven under a word none of them knew and the whole file stayed
//     green, so a field count per audited type now fails on any new field under any name.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// evidenceTypes are the types whose completeness flags this file is responsible for.
// A flag anywhere in here needs a row in evidenceCases naming it.
//
// tmux.StaleScan.DirFromServer is deliberately absent. It is not a completeness flag:
// the pass over the directory was complete and its result is proven, so a caller words
// the sentence differently rather than hedging it — a wrong subject rather than an
// unproven claim. StaleScan.DirFromServer's own doc comment argues it.

// Named rather than cited by line: a line number in another file is a claim that goes
// stale the next time that file grows, and this one already had.
//
// Only the top-level fields of each type are walked, which is why Filesystem and OOMAgent
// are listed in their own right rather than reached through the results that hold them.
// A nested struct that is not itself listed contributes nothing.
//
// The list is hand-maintained, so TestNoEvidenceFlagEscapesTheAuditedSet checks it
// against the packages' sources: reflection can enumerate a type's fields but not a
// package's types, so a flag on a type nobody added here would otherwise be invisible to
// both halves of this file.
var evidenceTypes = []reflect.Type{
	reflect.TypeOf(tmux.ScanGaps{}),
	reflect.TypeOf(tmux.StaleGaps{}),
	reflect.TypeOf(tmux.OrphanServer{}),
	reflect.TypeOf(OOMResult{}),
	reflect.TypeOf(OOMAgent{}),
	reflect.TypeOf(PressureResult{}),
	reflect.TypeOf(Filesystem{}),
	reflect.TypeOf(CapacityResult{}),
}

// evidenceScanPackages are the directories whose struct fields must all be accounted
// for. Relative to this package's directory, which is where go test runs.
var evidenceScanPackages = []string{".", filepath.Join("..", "..", "session", "tmux")}

// evidenceOutOfScope is the escape hatch for a flag-word field that is genuinely not a
// completeness channel, keyed "Package.Type.Field" with the reason as the value.
//
// Empty, and the emptiness is the claim: every such field in both packages is audited.
// A future entry needs an argument, not just a key — the words are conventional enough
// that a field wearing one and meaning something else is the exception.
var evidenceOutOfScope = map[string]string{}

// evidenceTypeFields is how many fields each audited type has, keyed "Package.Type".
//
// It exists because the flag-word convention is a naming rule, and a naming rule cannot be
// enforced. #607 added ScanGaps.EmptyFleetUnproven and every check in this file stayed
// green: "Unproven" was not a word here, so the new completeness flag was invisible to the
// harness written to catch new completeness flags. Adding the word fixes that instance; a
// count fixes the class, because a field appearing on one of these types fails here under
// any name, and the author then has to say which kind it is.
//
// A number per type rather than a whole-struct golden: the failure message can name the
// type that grew, and the diff when one changes is one line.
var evidenceTypeFields = map[string]int{
	"tmux.ScanGaps":  4,
	"tmux.StaleGaps": 2,
	// 9 since #614 added OrphanServer.ConnectedClients, which is deliberately not a
	// completeness channel and so has no row in evidenceCases. Every flag on this list
	// records what a source failed to establish, and this records what one established:
	// the count of clients holding a connection to the server, read from /proc/net/unix.
	// A failed read cannot fabricate it — an unreadable table sets ScanGaps.SocketTableUnread
	// and the row is dropped entirely, which is the channel already covered here — and no
	// caller reads it to decide whether the report may claim health. What it does gate is a
	// kill: `reap --kill --yes` refuses a target holding one.
	"tmux.OrphanServer":     9,
	"doctor.OOMResult":      9,
	"doctor.OOMAgent":       5,
	"doctor.PressureResult": 12,
	"doctor.Filesystem":     13,
	"doctor.CapacityResult": 4,
}

// evidenceFlagWords are the spellings this codebase uses for "the source was actually
// read". Matched as substrings of a field name, so ReachableKnown, SocketTableUnread,
// ProcTableTruncated, LiveServerUnknown, Unprobed and EmptyFleetUnproven are all caught.
//
// A new flag following none of these conventions escapes both the reflection check and the
// source scan, and that is not a theoretical limit: "Unproven" is on this list because
// #607 added ScanGaps.EmptyFleetUnproven — a completeness flag by any reading of its own
// doc comment — and the harness that exists to catch exactly that saw nothing, because no
// word here matched. It is the one soft edge in the mechanism, so evidenceTypeFields below
// backs it up with a field count per audited type, which no naming choice can slip past.
var evidenceFlagWords = []string{"Known", "Unknown", "Unread", "Truncated", "Unprobed", "Unproven"}

// isEvidenceFlag reports whether a field name is one of the completeness channels. The
// words deliberately overlap — "Unknown" contains "Known" — so LiveServerUnknown matches
// whichever is tried first and the order of evidenceFlagWords carries no meaning.
func isEvidenceFlag(name string) bool {
	for _, w := range evidenceFlagWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// evidenceCase is one fallible source driven to failure.
//
// covers names the completeness flags this row exercises, as "Type.Field". It is a
// claim, and TestEveryEvidenceChannelIsCovered checks it both ways: every flag in
// evidenceTypes must be covered by some row, and every name a row claims must exist.
type evidenceCase struct {
	name string
	// covers is the flags this row is responsible for, as "Type.Field".
	covers []string
	// render produces the rendered doctor section with this one source failed.
	render func(t *testing.T) string
	// forbids are the health claims that would be a fabrication here. Each is what the
	// section prints when the same source answers *and* finds nothing.
	forbids []string
	// names are what the section must say instead, so the gap is reported rather than
	// merely withheld.
	names []string
	// why is one line for whoever trips this row.
	why string
}

// orphanSectionWith renders the orphan section from stubbed scan seams, so the row
// exercises CheckOrphans' assembly rather than a hand-built result.
func orphanSectionWith(t *testing.T, servers []tmux.OrphanServer, gaps tmux.ScanGaps, stale tmux.StaleScan) string {
	t.Helper()
	prevOrphan, prevStale := orphanScan, staleScan
	t.Cleanup(func() { orphanScan, staleScan = prevOrphan, prevStale })

	orphanScan = func(context.Context) ([]tmux.OrphanServer, bool, tmux.ScanGaps) {
		return servers, true, gaps
	}
	staleScan = func(context.Context) tmux.StaleScan { return stale }
	return RenderOrphans(CheckOrphans(context.Background()))
}

// oomSectionWith renders the OOM section from stubbed discovery and /proc readers, with
// the live server located.
func oomSectionWith(t *testing.T, panes []paneRef, scores map[int][2]int) string {
	t.Helper()
	return oomSection(t, func(context.Context) (int, []paneRef, bool, bool) {
		return 4242, panes, true, true
	}, scores)
}

// oomSectionUnasked renders the OOM section with the live-server probe never answered —
// tmux absent, or its budget spent. Distinct from an empty fleet, which answers
// (0, nil, false, true).
func oomSectionUnasked(t *testing.T) string {
	t.Helper()
	return oomSection(t, func(context.Context) (int, []paneRef, bool, bool) {
		return 0, nil, false, false
	}, nil)
}

func oomSection(t *testing.T, discover func(context.Context) (int, []paneRef, bool, bool), scores map[int][2]int) string {
	t.Helper()
	prevDiscover, prevRead := oomDiscover, oomRead
	t.Cleanup(func() { oomDiscover, oomRead = prevDiscover, prevRead })

	oomDiscover = discover
	oomRead = func(pid int) (int, int, bool) {
		v, ok := scores[pid]
		return v[0], v[1], ok
	}
	// gatherOOM, not CheckOOM: the platform gate in CheckOOM returns before reading any
	// seam off Linux, so these rows would assert against "unavailable — oom_score_adj is
	// Linux-only" on the macOS job and prove nothing there. Same reason
	// pressureSectionWith calls gatherPressure.
	return RenderOOM(gatherOOM(context.Background()))
}

// pressureSectionWith renders the pressure section from canned readings, reusing
// pressure_test.go's seam swap.
//
// Some rows hand a seam a non-zero value together with ok=false — a reading the source
// could not vouch for. Production's readers all zero on failure, so that pairing does
// not occur today, and it is the point: a consumer that prints the value because it
// happens to be zero passes a test built from zeroes while still ignoring the flag. The
// contract is that the flag decides, so the value is chosen to be recognisable in the
// output if it ever leaks there.
func pressureSectionWith(t *testing.T, s pressureSeams) string {
	t.Helper()
	withPressureSeams(t, s)
	return RenderPressure(gatherPressure(context.Background()))
}

// aReachableServer is a row that answered its own socket — the class whose remedy is a
// kill-server aimed straight at it, and therefore the class that must lose that remedy
// when the live server could not be excluded by pid.
func aReachableServer() tmux.OrphanServer {
	return tmux.OrphanServer{
		PID: 4242, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
		Reachable: true, ReachableKnown: true, Started: time.Now().Add(-time.Hour),
	}
}

// measuredFS is a filesystem statfs could answer for, used as the healthy control
// alongside a path it could not.
func measuredFS() fsStat {
	return fsStat{TotalBytes: 100 * gib, AvailBytes: 50 * gib, TotalInodes: 1000, FreeInodes: 900, Dev: 1}
}

// unvouchedTmpfs is a tmpfs at half its own cap — under tmpfsWarnPct, so it raises no
// warning of its own — holding more than tmpfsRAMSharePct of the 32 GiB total the RAM
// row leaves unread. It exists so the RAM-share threshold has something to fire on if it
// ever consults that unread total.
func unvouchedTmpfs() fsStat {
	return fsStat{TotalBytes: 20 * gib, AvailBytes: 10 * gib, TotalInodes: 1000, FreeInodes: 900,
		Tmpfs: true, Dev: 1}
}

func evidenceCases() []evidenceCase {
	return []evidenceCase{
		{
			name:   "socket table unreadable",
			covers: []string{"ScanGaps.SocketTableUnread"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, nil, tmux.ScanGaps{SocketTableUnread: true}, tmux.StaleScan{})
			},
			forbids: []string{"  none\n"},
			names:   []string{"/proc/net/unix could not be read"},
			why:     "a server is identified by the socket it listens on, so with that table gone every candidate is dropped and the list is empty because nothing was looked at",
		},
		{
			name:   "proc walk truncated",
			covers: []string{"ScanGaps.ProcTableTruncated"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, nil, tmux.ScanGaps{ProcTableTruncated: true}, tmux.StaleScan{})
			},
			forbids: []string{"  none\n"},
			names:   []string{"did not finish"},
			why:     "a server may be missing, and a server that is listed may show fewer children than it holds",
		},
		{
			name:   "live server unidentified beside a reachable row",
			covers: []string{"ScanGaps.LiveServerUnknown"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, []tmux.OrphanServer{aReachableServer()},
					tmux.ScanGaps{LiveServerUnknown: true}, tmux.StaleScan{})
			},
			// The remedy, not the word "none": this gap leaves the inventory complete and
			// constrains only what may be done with it. A reachable row may BE the live
			// server, and the command that would stop it is the command that would stop
			// the fleet.
			forbids: []string{"kill-server"},
			names:   []string{"could not be identified"},
			why:     "with no pid to exclude by, a reachable row may be this Atrium's own server",
		},
		{
			name:   "the empty-fleet answer could not be verified",
			covers: []string{"ScanGaps.EmptyFleetUnproven"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, []tmux.OrphanServer{aReachableServer()},
					tmux.ScanGaps{EmptyFleetUnproven: true}, tmux.StaleScan{})
			},
			// This row's whole content is the names, which is the inverse of the zram row
			// and deliberate. #607 decided the remedy *stays* here — tmux demonstrably
			// works, the server answers, and a re-run would only repeat the same wrong
			// answer — so there is no health claim to forbid. Dropping the flag from the
			// guard makes the row fall through to the plain reachable case, and the only
			// difference is the caution. Its absence is therefore the whole defect.
			names: []string{"caution", "may be a live fleet", "refuses to take it"},
			why:   "an unverified empty-fleet answer must not let a reachable row keep an unqualified remedy",
		},
		{
			name:   "reachability could not be probed",
			covers: []string{"OrphanServer.ReachableKnown"},
			render: func(t *testing.T) string {
				s := aReachableServer()
				s.Reachable, s.ReachableKnown = false, false
				return orphanSectionWith(t, []tmux.OrphanServer{s}, tmux.ScanGaps{}, tmux.StaleScan{})
			},
			forbids: []string{"kill-server", "UNREACHABLE"},
			names:   []string{"reachability unknown"},
			why: "an unanswered probe establishes nothing, and this class holds a live server whose " +
				"socket is merely unopenable as well as a host where tmux could not run (#730) — so " +
				"neither the orphan verdict nor a remedy aimed at one may be printed for it",
		},
		{
			name:   "socket directory unlistable",
			covers: []string{"StaleGaps.DirUnread"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, nil, tmux.ScanGaps{}, tmux.StaleScan{
					Dir: "/tmp/tmux-1000", DirFromServer: true,
					Gaps: tmux.StaleGaps{DirUnread: true},
				})
			},
			forbids: []string{"none in", "  none\n"},
			names:   []string{"could not be listed"},
			why:     "nothing about the directory's contents was seen, which is not the same as no stale files",
		},
		{
			name:   "socket files could not be classified",
			covers: []string{"StaleGaps.Unprobed"},
			render: func(t *testing.T) string {
				return orphanSectionWith(t, nil, tmux.ScanGaps{}, tmux.StaleScan{
					Dir: "/tmp/tmux-1000", DirFromServer: true,
					Gaps: tmux.StaleGaps{Unprobed: 2},
				})
			},
			forbids: []string{"none in", "  none\n"},
			names:   []string{"could not be classified", "2 socket files"},
			why:     "absence of a probe answer is not evidence of an absent server, so those files stay off the list",
		},
		{
			name:   "the live-server probe was never answered",
			covers: []string{"OOMResult.LiveServerUnknown"},
			render: func(t *testing.T) string { return oomSectionUnasked(t) },
			// Not merely a wrong label: "start a session" is an instruction, and this is
			// the branch that used to hand it to a user whose fleet was already running.
			forbids: []string{"no live atrium tmux server", "start a session"},
			names:   []string{"not established"},
			why:     "tmux could not be asked, which says nothing about whether a server is on Atrium's socket",
		},
		{
			name:   "the server's own oom_score unreadable",
			covers: []string{"OOMResult.ServerKnown"},
			render: func(t *testing.T) string {
				return oomSectionWith(t, []paneRef{{PID: 111, Session: "repo_A"}},
					map[int][2]int{111: {1000, 300}})
			},
			// No verdict may print: the comparison the section exists for needs the
			// server's score, and inventing one either way is a guess about whether an
			// OOM kill sheds one session or all of them.
			forbids: []string{"outrank", "rank at or below"},
			names:   []string{"oom_score unknown"},
			why:     "the server's score is one half of every comparison this section makes",
		},
		{
			name:   "every agent's oom_score unreadable",
			covers: []string{"OOMAgent.Known"},
			render: func(t *testing.T) string {
				return oomSectionWith(t, []paneRef{{PID: 111, Session: "repo_A"}},
					map[int][2]int{4242: {800, 200}})
			},
			forbids: []string{"outrank", "rank at or below"},
			names:   []string{"unreadable"},
			why:     "a pane whose score could not be read is not a pane that outranks the server",
		},
		{
			name:   "swap unreadable",
			covers: []string{"PressureResult.SwapKnown"},
			render: func(t *testing.T) string {
				return pressureSectionWith(t, pressureSeams{ram: 32 * gib, ramOK: true,
					availRAM: 8 * gib, availOK: true, socketDir: "/tmp/tmux-1000"})
			},
			// "none configured" is the determined answer — a deliberate swapless host —
			// and it comes with advice. Printing it off a failed read invents the
			// configuration it then advises about.
			forbids: []string{"none configured"},
			names:   []string{"unknown"},
			why:     "a swapless host and an unreadable sysinfo(2) are different facts with different advice",
		},
		{
			name:   "available RAM unreadable",
			covers: []string{"PressureResult.AvailRAMKnown"},
			render: func(t *testing.T) string {
				return pressureSectionWith(t, pressureSeams{swapTotal: 8 * gib, swapFree: 8 * gib,
					swapOK: true, socketDir: "/tmp/tmux-1000"})
			},
			names: []string{"unknown"},
			why:   "an unread MemAvailable must not render as a figure",
		},
		{
			name:   "total RAM unreadable beside a readable MemAvailable",
			covers: []string{"PressureResult.RAMKnown"},
			render: func(t *testing.T) string {
				return pressureSectionWith(t, pressureSeams{swapTotal: 8 * gib, swapFree: 8 * gib,
					swapOK: true, availRAM: 8 * gib, availOK: true,
					ram: 32 * gib, socketDir: "/tmp/tmux-1000",
					fs: map[string]fsStat{"/tmp/tmux-1000": unvouchedTmpfs()}})
			},
			// Both of RAMKnown's consumers, and neither is reachable from the row above:
			// with AvailRAMKnown false too, availRAMValue returns on its first branch and
			// the tmpfs share is never computed. The total is the denominator of both —
			// the "of N" in the context row, and the 25%-of-RAM tmpfs threshold — so an
			// unread one must produce no figure and no warning rather than a plausible
			// one.
			forbids: []string{"of 32.0 GiB", "charged against RAM"},
			names:   []string{"8.0 GiB available"},
			why:     "an unread MemTotal is not a total to divide by, whatever value came back with it",
		},
		{
			name:   "zram share unreadable",
			covers: []string{"PressureResult.ZramKnown"},
			render: func(t *testing.T) string {
				return pressureSectionWith(t, pressureSeams{swapTotal: 8 * gib, swapFree: 4 * gib,
					swapOK: true, ram: 32 * gib, ramOK: true, socketDir: "/tmp/tmux-1000",
					zram: 4 * gib})
			},
			// The note is what tells a reader their swap is not real headroom. Printing
			// it off an unread /proc/swaps would assert a share nobody measured; NOT
			// printing it is the honest silence, so this row's whole content is the
			// forbid.
			forbids: []string{"is zram"},
			names:   []string{"swap"},
			why:     "an unread /proc/swaps says nothing about how much swap lives in RAM",
		},
		{
			name:   "the host's RAM total unreadable",
			covers: []string{"CapacityResult.RAMKnown"},
			// Hand-built, alone among these rows: CheckCapacity calls hostMemBytes
			// directly and there is no seam to fail. So this asserts the consumer only —
			// worth having anyway, because it is the flag's whole consumer today, and
			// because a RecommendedCap that ever derives from RAM would arrive with a row
			// already here. capacity_test.go reaches the same render from the other side.
			render: func(*testing.T) string {
				return RenderCapacity(CapacityResult{Threads: 4, RAMKnown: false, RecommendedCap: 2})
			},
			forbids: []string{"GiB"},
			names:   []string{"unknown"},
			why:     "a platform that could not report its RAM total has not reported 0.0 GiB of it",
		},
		{
			name:   "a watched filesystem unreadable",
			covers: []string{"Filesystem.Known"},
			render: func(t *testing.T) string {
				return pressureSectionWith(t, pressureSeams{swapTotal: 8 * gib, swapFree: 8 * gib,
					swapOK: true, ram: 32 * gib, ramOK: true, socketDir: "/tmp/tmux-1000",
					fs: map[string]fsStat{"/tmp/tmux-1000": measuredFS()}})
			},
			// A statfs that failed must not render as a filesystem with room. "0 MiB
			// used of 0 MiB (0%)" is the shape a zero-value fsStat would print.
			forbids: []string{"0 MiB used of 0 MiB"},
			names:   []string{"unreadable"},
			why:     "a path statfs could not answer for has no headroom figure, not a zero one",
		},

		// The account-pool parity section, whose negative claim is stronger than the
		// others': the rest print "none", this one prints NOTHING AT ALL when a pool is
		// in parity. So its failure mode is silence, and `names` is what carries these
		// rows — an empty section trivially satisfies every `forbids`.
		//
		// Its completeness channels are NOT reflection-audited. One reason, and it is
		// enough on its own: they live on config.DimensionState, and evidenceScanPackages
		// covers this package and session/tmux, not config.
		//
		// Naming is a second, narrower obstacle. DimensionState.Measured would have to be
		// spelled into evidenceFlagWords to be matched, and that word cannot be added:
		// doctor.Filesystem.Measured is a PATH, not a channel, so
		// TestEveryEvidenceChannelIsCovered would immediately demand a row claiming it is
		// one. Nothing deeper than that is true here — Measured's polarity is ordinary.
		// The Known fields on PressureResult and Filesystem read exactly the same way,
		// false meaning unknown, which is why an earlier version of this comment arguing
		// that no audited word could express that polarity was simply wrong.
		//
		// What guards these instead is the rows below plus config's own table tests; a
		// fourth axis on config.DirCapabilities is caught by
		// TestDimensionsIsTheWholeConstRange there, not here.
		{
			name:   "a pool member's config dir will not read",
			covers: nil,
			render: func(t *testing.T) string {
				return RenderParity(CheckParity(parityPool(), func(dir string) (config.DirCapabilities, bool) {
					if dir == "/b" {
						return config.DirCapabilities{}, false
					}
					return parityFullyMeasured(), true
				}))
			},
			forbids: []string{"in parity"},
			names:   []string{"capabilities unreadable for", "/b", "not evidence of parity"},
			why:     "a dir nothing could be read from is not a dir that agrees with its sibling, and this section's way of saying 'they agree' is to print nothing",
		},
		{
			name:   "one axis of one member could not be measured",
			covers: nil,
			render: func(t *testing.T) string {
				return RenderParity(CheckParity(parityPool(), func(dir string) (config.DirCapabilities, bool) {
					caps := parityFullyMeasured()
					if dir == "/b" {
						caps.MCPServers = config.DimensionState{} // the file that records them was absent
					}
					return caps, true
				}))
			},
			// The shape a dimension read as empty rather than unknown would print: the
			// member accused of lacking what its sibling has.
			forbids: []string{"has it"},
			names:   []string{"MCP server parity is unverified", "not evidence of parity"},
			why:     "mcpServers lives only in .claude.json, so a member without one has an unknown server set and must not be reported as configuring none",
		},
		{
			// A denial claude enforces and this build cannot express — a serverCommand
			// entry — travels through the same channel as an unreadable server list,
			// because availableMCPServers folds the two together. The row drives it
			// through the REAL reader so the fold is what is being tested: read as the
			// configured set, this member would be credited with the server it blocks.
			name:   "a member denies a server in a way this build cannot name",
			covers: nil,
			render: func(t *testing.T) string {
				cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
					{Name: "a", ConfigDir: fixtureDir(t, "rich"), Pool: "p"},
					{Name: "b", ConfigDir: fixtureDir(t, "cmddenial"), Pool: "p"},
				}}
				return RenderParity(CheckParity(cfg, config.ReadDirCapabilities))
			},
			forbids: []string{`MCP server "`},
			names:   []string{"MCP server parity is unverified", "not evidence of parity"},
			why:     "a denial this build cannot express, dropped rather than reported, credits the member with a server claude blocks for it",
		},
		{
			name:   "a member's connector setting could not be read",
			covers: nil,
			render: func(t *testing.T) string {
				return RenderParity(CheckParity(parityPool(), func(dir string) (config.DirCapabilities, bool) {
					caps := parityFullyMeasured()
					if dir == "/b" {
						caps.Connectors = config.ConnectorsUnknown
					}
					return caps, true
				}))
			},
			// Folded into either bucket it would fabricate a split, or hide one.
			forbids: []string{"in its own settings.json"},
			names:   []string{"connector setting could not be read", "not evidence of parity"},
			why:     "a setting that is neither JSON true nor false is not a state, and a tri-state exists so it cannot be reported as one",
		},
	}
}

// parityPool is the smallest pool that can be compared: two members, distinct dirs.
func parityPool() *config.Config {
	return &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/a", Pool: "p"},
		{Name: "b", ConfigDir: "/b", Pool: "p"},
	}}
}

// parityFullyMeasured is a dir that answered every axis and configures one server, so a
// row that fails ONE source is about that source rather than about four silent ones.
func parityFullyMeasured() config.DirCapabilities {
	measuredEmpty := config.DimensionState{Measured: true, Targets: map[string]string{}}
	return config.DirCapabilities{
		Plugins:      measuredEmpty,
		Marketplaces: measuredEmpty,
		MCPServers: config.DimensionState{
			Measured: true, Targets: map[string]string{"linear": `{"url":"https://mcp.linear.app/mcp"}`},
		},
		Connectors: config.ConnectorsOn,
	}
}

// TestFailedSourceNeverReadsAsAHealthyHost drives each fallible source to failure and
// asserts the section neither claims health nor stays silent about the gap.
//
// Rows are not parallel: the seams they swap are package-level shared state — the same
// rule session/tmux's own seam block states for itself.
func TestFailedSourceNeverReadsAsAHealthyHost(t *testing.T) {
	for _, tc := range evidenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.render(t)
			for _, claim := range tc.forbids {
				require.NotContains(t, out, claim,
					"%s: a failed read rendered as %q — %s\nsection:\n%s", tc.name, claim, tc.why, out)
			}
			for _, want := range tc.names {
				require.Contains(t, out, want,
					"%s: the gap went unnamed, expected %q — %s\nsection:\n%s", tc.name, want, tc.why, out)
			}
		})
	}
}

// TestEveryEvidenceChannelIsCovered is the half of this file that can see a source which
// does not exist yet.
//
// It walks evidenceTypes by reflection and requires every completeness flag to be named
// by some row in evidenceCases. A new fallible read whose flag nothing exercises fails
// here — which is precisely what did not happen for any of the instances above: each of
// those sources was added, shipped, and only then found to render a false negative.
//
// It checks the claims both ways. An unclaimed flag is the gap this exists for; a row
// claiming a flag that no longer exists is a stale assertion, and a test that silently
// covers nothing is the same class of defect one level up.
func TestEveryEvidenceChannelIsCovered(t *testing.T) {
	declared := map[string]bool{}
	for _, tc := range evidenceCases() {
		for _, f := range tc.covers {
			declared[f] = true
		}
	}

	actual := map[string]bool{}
	for _, typ := range evidenceTypes {
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() || !isEvidenceFlag(f.Name) {
				continue
			}
			key := typ.Name() + "." + f.Name
			actual[key] = true
			require.True(t, declared[key],
				"%s is a completeness flag with no row in evidenceCases: a source can fail and "+
					"nothing here asserts the report says so. Add a row naming it in covers.", key)
		}
	}

	for claim := range declared {
		require.True(t, actual[claim],
			"evidenceCases claims to cover %s, which is not a completeness flag in evidenceTypes — "+
				"the field was renamed or removed and the row now asserts nothing about it.", claim)
	}

	// An exact count, so dropping a type from evidenceTypes together with its rows —
	// which leaves both directions above consistent and coverage quietly smaller — has to
	// be a deliberate edit here too.
	require.Len(t, actual, evidenceFlagCount,
		"evidenceTypes now yields %d completeness flags, not %d. Adding a flag means adding "+
			"a row and this number; removing one means saying so here.", len(actual), evidenceFlagCount)
}

// evidenceFlagCount is how many completeness flags evidenceTypes holds. Stated rather
// than derived: a number computed from the same reflection it checks would agree with
// itself no matter what was deleted.
const evidenceFlagCount = 16

// TestAuditedTypesHaveNotGrownAField is the backstop for the flag-word convention.
//
// Every other check here starts by asking whether a field name looks like a completeness
// flag, so a flag named outside the convention is invisible to all of them — which is not
// hypothetical: see evidenceFlagWords on #607. This asks a question that has no naming in
// it. A field added to an audited type fails here whatever it is called, and classifying it
// is then the author's problem rather than a silent default.
//
// It says nothing about whether the new field needs a row; that is the next test's job once
// the field is named conventionally, or this test's failure message once it is not.
func TestAuditedTypesHaveNotGrownAField(t *testing.T) {
	require.Len(t, evidenceTypeFields, len(evidenceTypes),
		"every audited type needs a field count, and vice versa")

	for _, typ := range evidenceTypes {
		key := path.Base(typ.PkgPath()) + "." + typ.Name()
		want, listed := evidenceTypeFields[key]
		require.True(t, listed, "%s is audited but has no entry in evidenceTypeFields", key)
		require.Equal(t, want, typ.NumField(),
			"%s now has %d fields, not %d. If the new one is a completeness channel it needs a "+
				"row in evidenceCases and a name matching evidenceFlagWords; if it is not, say so "+
				"here by updating the count. Do not skip this by naming it off-convention — that "+
				"is exactly how ScanGaps.EmptyFleetUnproven shipped unwatched.",
			key, typ.NumField(), want)
	}
}

// TestNoEvidenceFlagEscapesTheAuditedSet is what makes evidenceTypes' hand-maintenance
// safe, and the file header's claim about unwritten sources true.
//
// TestEveryEvidenceChannelIsCovered walks types by reflection, so a flag on a type nobody
// remembered to list is invisible to it: reflect enumerates a type's fields, never a
// package's types. That is not a hypothetical shape — every one of the sources this file
// covers arrived on a struct that did not exist the release before.
//
// So this parses the sources instead and requires every flag-word field in either package
// to sit on an audited type, or to carry a reason in evidenceOutOfScope. Parsing also
// ignores build constraints, which is the behaviour wanted here: a flag declared in a
// _linux.go or _darwin.go file is caught on whichever platform the suite runs.
func TestNoEvidenceFlagEscapesTheAuditedSet(t *testing.T) {
	audited := map[string]bool{}
	for _, typ := range evidenceTypes {
		audited[path.Base(typ.PkgPath())+"."+typ.Name()] = true
	}

	found := 0
	scanned := map[string]bool{}
	for _, dir := range evidenceScanPackages {
		for _, f := range flagFieldsIn(t, dir) {
			found++
			scanned[f.key] = true
			if reason := evidenceOutOfScope[f.key]; reason != "" {
				continue
			}
			require.True(t, audited[f.typeKey],
				"%s (%s) is a %q field on a type no test in this file audits. Add the type to "+
					"evidenceTypes and give the flag a row, or explain it in evidenceOutOfScope.",
				f.key, f.pos, f.word)
		}
	}

	// Both directions, the same discipline `covers` gets: an exemption whose field has been
	// renamed away silently exempts nothing, and the next field to take that name inherits
	// the exemption without anyone arguing for it.
	for key := range evidenceOutOfScope {
		require.True(t, scanned[key],
			"evidenceOutOfScope exempts %s, which no longer exists in the scanned sources — "+
				"the field was renamed or removed and the exemption now covers nothing.", key)
	}
	// Tied to the ledger rather than to zero: dropping session/tmux from the scan leaves a
	// parse that still finds fields and still passes, while the package where most of
	// these instances landed stops being watched for new types.
	require.GreaterOrEqual(t, found, evidenceFlagCount,
		"the scan found %d flag-word fields but %d are audited by reflection, so it is not "+
			"reaching every package that declares one — check evidenceScanPackages",
		found, evidenceFlagCount)
}

// flagField is one flag-word struct field found in the sources.
type flagField struct {
	key     string // Package.Type.Field
	typeKey string // Package.Type
	word    string // the field name
	pos     string // file:line, for the failure message only
}

// flagFieldsIn parses every non-test .go file in dir and returns its flag-word struct
// fields. Embedded and anonymous fields have no name to match and are skipped.
//
// File by file rather than by package, so no build constraint is consulted and a
// _linux.go or _darwin.go declaration is seen on every platform. (This is what
// parser.ParseDir was deprecated for not doing; here it is the requirement.)
func flagFieldsIn(t *testing.T, dir string) []flagField {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err, "globbing %s", dir)
	require.NotEmpty(t, paths, "no .go files under %s — the path is wrong", dir)

	fset := token.NewFileSet()
	var out []flagField
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "parsing %s", p)

		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if !isEvidenceFlag(name.Name) {
						continue
					}
					typeKey := file.Name.Name + "." + spec.Name.Name
					out = append(out, flagField{
						key:     typeKey + "." + name.Name,
						typeKey: typeKey,
						word:    name.Name,
						pos:     fset.Position(name.Pos()).String(),
					})
				}
			}
			return true
		})
	}
	return out
}

// TestPressureWarnedIsNotEvidenceOfAHealthyHost pins the one place in this package where
// a failed read still collapses into a plain false.
//
// PressureWarned answers "is this host in trouble" from the *Warn fields, and every one
// of those is false when its source could not be read — so an unreadable sysinfo(2) and
// a healthy host return the same bool. Nothing reaches a user today: PressureWarned has
// no caller outside tests, and RenderPressure hedges honestly, which this asserts as the
// standing contract.
//
// #595 is the open issue that would give it a caller (a TUI warning). A consumer that
// reads the bool alone inherits exactly the class this file is about, so the pair —
// false bool, honest render — is what must hold, and the render half is the load-bearing
// one. If #595 lands, it needs a completeness channel of its own and a row above.
func TestPressureWarnedIsNotEvidenceOfAHealthyHost(t *testing.T) {
	withPressureSeams(t, pressureSeams{socketDir: "/tmp/tmux-1000"})
	r := gatherPressure(context.Background())

	// TestRenderPressureReportsUnknownsAsUnknown already pins the bool from a hand-built
	// result; this reaches it through gatherPressure, so the assembly produced the false.
	require.False(t, PressureWarned(r),
		"documenting today's behaviour: with every reading unread, the predicate is false")

	out := RenderPressure(r)
	require.Contains(t, out, "unknown",
		"the render is the only thing distinguishing an unread host from a healthy one:\n%s", out)
	require.NotContains(t, out, "none configured",
		"an unread swap reading must not render as a deliberate swapless host:\n%s", out)
}
