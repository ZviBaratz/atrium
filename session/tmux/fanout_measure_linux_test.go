//go:build linux

package tmux

// The #800 attach-fanout measurement harness.
//
// Atrium holds one `tmux attach-session` pty client per started session for that
// session's whole life (Session.Restore, called from Start). #548 filed that fanout
// as "bounded today, unbounded in N" and was explicit that it must be measured before
// anything is optimised. This is the measurement.
//
// It is opt-in for the usual reason an expensive live probe is — it builds real tmux
// sessions and then sits still for tens of seconds watching /proc, which is minutes
// the gate should not spend — following session/fork_live_test.go's ATRIUM_LIVE_FORK
// precedent: skip when the gate is unset, fail loudly once it is set and something
// else is missing.
//
// The number it exists to produce is a *difference*: the same fleet, priced with its
// attach clients and then without them. An absolute reading of a fleet that always
// has clients cannot separate what the clients cost from what the sessions cost, and
// a harness that reports the same figure either way is measuring nothing.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/stretchr/testify/require"
)

const (
	// measureFanoutEnv gates the harness.
	measureFanoutEnv = "ATRIUM_MEASURE_FANOUT"
	// measureFanoutSizesEnv overrides the fleet sizes, as a comma-separated list.
	measureFanoutSizesEnv = "ATRIUM_MEASURE_FANOUT_SIZES"
	// measureFanoutWindowEnv overrides the sampling window, in seconds.
	measureFanoutWindowEnv = "ATRIUM_MEASURE_FANOUT_WINDOW"
)

// defaultFanoutSizes are the fleet sizes measured when nothing overrides them: a
// single session to read the fixed cost, and two larger fleets so the per-session
// slope is a line through three points rather than an assertion about one.
var defaultFanoutSizes = []int{1, 5, 15}

// defaultFanoutWindow is how long each arm is watched. Long enough that a client
// burning even 1% of a core would move procfs's 10ms tick counter several times, so
// a reported zero means zero rather than "below the resolution".
const defaultFanoutWindow = 10 * time.Second

// fanoutMeasureRequested reports whether the raw gate value asks for the run.
//
// Split from the env read so the opt-in itself is testable: the failure mode worth
// guarding is not "the gate refuses" but "the gate quietly stops refusing" and the
// measurement starts costing the gate minutes on every CI run.
func fanoutMeasureRequested(raw string) bool { return raw == "1" }

// TestAttachFanoutCostIsOptIn pins the gate's truth table. Only an explicit "1" runs
// the measurement; everything else, including the plausible-looking "true" and "yes",
// leaves it skipped.
func TestAttachFanoutCostIsOptIn(t *testing.T) {
	for raw, want := range map[string]bool{
		"":     false,
		"0":    false,
		"true": false,
		"yes":  false,
		"1":    true,
	} {
		require.Equal(t, want, fanoutMeasureRequested(raw), "gate value %q", raw)
	}
}

// paneProgram is what a measured session runs.
type paneProgram struct {
	mode string
	argv string
}

// paneModes are the two pane behaviours measured.
//
// The active mode streams as fast as the pty will take it, which is far above what a
// coding agent produces — deliberately, because it is an upper bound. If the clients
// are free under a pane that never stops writing, the question of whether they are
// free under a real agent does not need asking.
var paneModes = []paneProgram{
	{mode: "idle", argv: "sleep 3600"},
	{mode: "active", argv: `i=0; while :; do i=$((i+1)); printf '%s %d\n' "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "$i"; done`},
}

// fanoutSample is one arm's reading: what the fleet held, and what it spent over the
// window.
type fanoutSample struct {
	arm string

	clients    int
	ptmx       int
	eventPoll  int
	pidFD      int
	fdTotal    int
	goroutines int

	selfCPU      time.Duration
	selfChildCPU time.Duration
	clientCPU    time.Duration

	// serverCPU is meaningful only when serverCPUKnown is set. A server that exits
	// between the two readings would otherwise yield after-minus-before as a large
	// NEGATIVE number, which is indistinguishable from the genuine negative this
	// harness found at N=1 and spends a paragraph of the record interpreting.
	serverCPU      time.Duration
	serverCPUKnown bool

	// clientPrivate is the clients' combined memory; the two flags beside it say what
	// kind of number it is. procCost.Private is Pss where the kernel offers
	// smaps_rollup and resident size where it does not, and the two differ by roughly
	// an order of magnitude on processes that share as much as N tmux clients do —
	// so printing the sum unlabelled would let a fallback reading be quoted as a Pss.
	clientPrivate      int64
	clientPrivateIsPss bool
	clientsUnpriced    int
}

// TestAttachFanoutCost is the measurement. It is not an assertion about a threshold —
// there is no agreed budget to assert against, and inventing one here would smuggle
// the verdict into the harness. It prints what the fleet costs; the verdict is
// written from the numbers, in the record.
//
// The one thing it does assert is that the two arms differ in client count, because a
// control that did not actually remove the clients would make every difference below
// it read as "the fanout is free".
func TestAttachFanoutCost(t *testing.T) {
	if !fanoutMeasureRequested(os.Getenv(measureFanoutEnv)) {
		t.Skipf("attach-fanout measurement is off; run scripts/measure-fanout.sh to set %s=1", measureFanoutEnv)
	}
	// Past the gate, a missing prerequisite is a failure and not a skip: someone asked
	// for the measurement, and a silent skip would answer them with nothing. /proc is
	// not among the prerequisites checked here — this file is linux-tagged, so off
	// Linux it does not exist to be run, and scripts/measure-fanout.sh refuses there
	// with a reason rather than leaving the caller to wonder.
	testutil.RequireTmux(t)

	window := fanoutWindow(t)
	baseline := measureArm(t, "baseline (no sessions)", nil, window)
	t.Log("\n" + fanoutHeader())
	t.Log(baseline.row())

	for _, size := range fanoutSizes(t) {
		for _, pane := range paneModes {
			sessions := startFleet(t, size, pane)

			with := measureArm(t, fmt.Sprintf("N=%d %s with-clients", size, pane.mode), sessions, window)
			for _, s := range sessions {
				dropAttachClient(t, s)
			}
			settle()
			without := measureArm(t, fmt.Sprintf("N=%d %s no-clients", size, pane.mode), sessions, window)

			require.Positive(t, with.clients, "the with-clients arm must actually hold clients")
			require.Zero(t, without.clients, "the control must actually have dropped them")
			// The pty count is the fanout itself, so a classifier that stopped
			// recognising a master would report the fleet holding none — "the fanout is
			// free", arrived at by a broken instrument rather than by measurement. The
			// count is asserted, not just printed, for the same reason the client count
			// above is.
			require.Equal(t, with.clients, with.ptmx,
				"every attach client should hold exactly one pty master; a mismatch means classifyFD "+
					"does not recognise this host's spelling of it")
			require.Zero(t, without.ptmx, "dropping the clients must drop their pty masters")

			t.Log(with.row())
			t.Log(without.row())
			t.Log(marginalRow(size, pane.mode, with, without))

			closeFleet(t, sessions)
		}
	}
}

// fanoutSizes resolves the fleet sizes to measure.
func fanoutSizes(t *testing.T) []int {
	raw := os.Getenv(measureFanoutSizesEnv)
	if raw == "" {
		return defaultFanoutSizes
	}
	var sizes []int
	for _, f := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		require.NoErrorf(t, err, "%s=%q", measureFanoutSizesEnv, raw)
		require.Positivef(t, n, "%s=%q", measureFanoutSizesEnv, raw)
		sizes = append(sizes, n)
	}
	return sizes
}

// fanoutWindow resolves how long each arm is watched.
func fanoutWindow(t *testing.T) time.Duration {
	raw := os.Getenv(measureFanoutWindowEnv)
	if raw == "" {
		return defaultFanoutWindow
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	require.NoErrorf(t, err, "%s=%q", measureFanoutWindowEnv, raw)
	require.Positivef(t, n, "%s=%q", measureFanoutWindowEnv, raw)
	return time.Duration(n) * time.Second
}

// startFleet builds size real sessions running pane's program and returns them.
func startFleet(t *testing.T, size int, pane paneProgram) []*Session {
	dir := t.TempDir()
	sessions := make([]*Session, 0, size)
	for i := 0; i < size; i++ {
		// Randomised, per the package's live-test convention, so a parallel run on the
		// same sandbox socket cannot collide.
		name := fmt.Sprintf("fanout-%s-%d-%d", pane.mode, i, rand.Int31())
		s := NewSession(context.Background(), name, pane.argv)
		require.NoError(t, s.Start(dir), "starting session %d of %d", i+1, size)
		sessions = append(sessions, s)
	}
	settle()
	return sessions
}

// closeFleet tears the fleet down. Failures are reported rather than fatal: a
// half-torn-down fleet must not hide the numbers already collected, and the package's
// sandboxed TMUX_TMPDIR reaps whatever is left.
func closeFleet(t *testing.T, sessions []*Session) {
	for _, s := range sessions {
		if err := s.Close(); err != nil {
			t.Logf("closing %s: %v", s.sanitizedName, err)
		}
	}
	settle()
}

// dropAttachClient puts one session into the state a lazy attach would leave it in:
// no client process, no pty master, and no reaper goroutine waiting on either.
//
// It closes the master directly because there is no production path that drops a
// *background* client today — Detach and DetachSafely both return early on a session
// that was never interactively attached, and Close kills the session outright. That
// gap is not an accident of this harness; it is part of what #548 is asking about,
// since a lazy-attach implementation would have to grow exactly this operation.
func dropAttachClient(t *testing.T, s *Session) {
	t.Helper()
	if s.ptmx == nil {
		return
	}
	require.NoError(t, s.ptmx.Close())
	s.ptmx = nil
}

// settle gives tmux and the runtime time to finish reacting before a reading is
// taken: a client that was just asked to go away is still a process for a moment.
func settle() { time.Sleep(1500 * time.Millisecond) }

// measureArm prices one arm over the window.
//
// Every CPU figure is a difference between two readings of a cumulative counter, so
// it describes the window and not the process's lifetime. Memory is a point reading
// at the end, because it is a level rather than a rate.
func measureArm(t *testing.T, arm string, sessions []*Session, window time.Duration) fanoutSample {
	t.Helper()

	clientsBefore := attachClientPids(t)
	serverPid := serverPidFor(t, sessions)

	selfBefore, ok := readProcCost(os.Getpid())
	require.True(t, ok, "%s: pricing the test process", arm)
	serverBefore, serverOK := procCostOf(serverPid)
	beforeByPid := costByPid(clientsBefore)

	time.Sleep(window)

	clientsAfter := attachClientPids(t)
	selfAfter, ok := readProcCost(os.Getpid())
	require.True(t, ok, "%s: re-pricing the test process", arm)
	serverAfter, serverAfterOK := procCostOf(serverPid)

	fds, ok := countFDClasses(os.Getpid())
	require.True(t, ok, "%s: counting descriptors", arm)

	sample := fanoutSample{
		arm:                arm,
		clients:            len(clientsAfter),
		ptmx:               fds.Ptmx,
		eventPoll:          fds.EventPoll,
		pidFD:              fds.PidFD,
		fdTotal:            fds.Total,
		goroutines:         runtime.NumGoroutine(),
		selfCPU:            selfAfter.CPU - selfBefore.CPU,
		selfChildCPU:       selfAfter.ChildCPU - selfBefore.ChildCPU,
		clientPrivateIsPss: true,
	}
	// Both ends, not either: a difference is only a rate when both of its terms were
	// read. Missing the "after" is the dangerous half — it would print the whole
	// before-reading as a negative window.
	if serverOK && serverAfterOK {
		sample.serverCPU, sample.serverCPUKnown = serverAfter.CPU-serverBefore.CPU, true
	}
	// Only clients present at both ends are charged: one that appeared mid-window has
	// no "before" to subtract, and charging its whole lifetime to this window would
	// invent CPU the arm did not spend.
	for _, pid := range clientsAfter {
		before, seen := beforeByPid[pid]
		if !seen {
			continue
		}
		after, ok := readProcCost(pid)
		if !ok {
			continue
		}
		sample.clientCPU += after.CPU - before.CPU
		if !after.PrivateKnown {
			sample.clientsUnpriced++
			continue
		}
		if !after.PrivateIsPss {
			sample.clientPrivateIsPss = false
		}
		sample.clientPrivate += after.Private
	}
	return sample
}

// costByPid prices each pid, dropping the ones that will not answer.
func costByPid(pids []int) map[int]procCost {
	out := make(map[int]procCost, len(pids))
	for _, pid := range pids {
		if cost, ok := readProcCost(pid); ok {
			out[pid] = cost
		}
	}
	return out
}

// procCostOf prices a pid, treating 0 as "no such process" rather than as procfs's
// own aggregate.
func procCostOf(pid int) (procCost, bool) {
	if pid == 0 {
		return procCost{}, false
	}
	return readProcCost(pid)
}

// attachClientPids lists this process's live `tmux attach-session` children.
//
// Children of *this* process, not every tmux client on the host: the developer's own
// Atrium is very likely running while this measurement is, and counting its thirty
// clients as the harness's would be the single easiest way to produce a confidently
// wrong number.
func attachClientPids(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir(procRoot)
	require.NoError(t, err)

	self := os.Getpid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		stat, ok := parseStat(string(raw))
		if !ok || stat.PPid != self {
			continue
		}
		if !strings.Contains(procCmdline(pid), "attach-session") {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// serverPidFor finds the tmux server serving the fleet, by asking one of its sessions
// rather than by scanning for a process that looks like one.
func serverPidFor(t *testing.T, sessions []*Session) int {
	t.Helper()
	if len(sessions) == 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := tmuxCommand(ctx, "display-message", "-p", "-t", sessions[0].sanitizedName, "#{pid}").Output()
	if err != nil {
		t.Logf("could not resolve the tmux server pid: %v", err)
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Logf("tmux reported a server pid this cannot read: %q", out)
		return 0
	}
	return pid
}

// procCmdline reads a process's argv as a space-joined string, empty when it cannot
// be read.
func procCmdline(pid int) string {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(raw), "\x00", " ")
}

// fanoutHeader is the column header the rows below line up under.
func fanoutHeader() string {
	return fmt.Sprintf("%-28s %7s %5s %6s %6s %6s %6s %10s %10s %10s %10s %16s",
		"arm", "clients", "ptmx", "epoll", "pidfd", "fds", "gorout",
		"self_cpu", "child_cpu", "srv_cpu", "clnt_cpu", "clnt_mem")
}

// row renders one arm as a line under fanoutHeader.
func (s fanoutSample) row() string {
	return fmt.Sprintf("%-28s %7d %5d %6d %6d %6d %6d %10s %10s %10s %10s %16s",
		s.arm, s.clients, s.ptmx, s.eventPoll, s.pidFD, s.fdTotal, s.goroutines,
		s.selfCPU, s.selfChildCPU, s.serverCPUCell(), s.clientCPU, s.clientMemCell())
}

// serverCPUCell renders the server column, distinguishing a measured zero from a
// window whose two readings could not both be taken.
func (s fanoutSample) serverCPUCell() string {
	if !s.serverCPUKnown {
		return "n/a"
	}
	return s.serverCPU.String()
}

// clientMemCell renders the clients' combined memory with what kind of number it is.
//
// The column is "clnt_mem" and not "clnt_pss" because it is only a Pss where the
// kernel offered smaps_rollup for every client sampled; the statm fallback is a
// resident size, which counts shared pages once per process. An unlabelled column
// would let that fallback be quoted as a Pss and overstate the marginal client by
// roughly an order of magnitude.
func (s fanoutSample) clientMemCell() string {
	cell := humanBytes(s.clientPrivate)
	if s.clientPrivate > 0 && !s.clientPrivateIsPss {
		cell += " rss"
	}
	if s.clientsUnpriced > 0 {
		cell += fmt.Sprintf(" +%d?", s.clientsUnpriced)
	}
	return cell
}

// marginalRow renders the with-minus-without difference — the number the whole
// harness exists for — divided by the fleet size, so it reads as the cost of one
// more session rather than of this particular fleet.
func marginalRow(size int, mode string, with, without fanoutSample) string {
	per := func(d time.Duration) time.Duration { return d / time.Duration(size) }
	return fmt.Sprintf("  -> per client at N=%d %s: ptmx %+d, epoll %+d, pidfd %+d, fds %+d, goroutines %+d, "+
		"self_cpu %+v, srv_cpu %v, client_cpu %v, client_mem %s",
		size, mode,
		perInt(with.ptmx-without.ptmx, size),
		perInt(with.eventPoll-without.eventPoll, size),
		perInt(with.pidFD-without.pidFD, size),
		perInt(with.fdTotal-without.fdTotal, size),
		perInt(with.goroutines-without.goroutines, size),
		per(with.selfCPU-without.selfCPU),
		serverDeltaPerClient(with, without, size),
		per(with.clientCPU),
		with.clientMemPerClient(size))
}

// serverDeltaPerClient renders the per-client server difference, or "n/a" when either
// arm could not read the server at both ends. Subtracting an unknown from a known one
// would produce a number with no meaning and no marking.
func serverDeltaPerClient(with, without fanoutSample, size int) string {
	if !with.serverCPUKnown || !without.serverCPUKnown || size == 0 {
		return "n/a"
	}
	return ((with.serverCPU - without.serverCPU) / time.Duration(size)).String()
}

// clientMemPerClient renders the per-client share of the combined memory, carrying the
// same labels the whole-fleet cell does.
func (s fanoutSample) clientMemPerClient(size int) string {
	per := s
	per.clientPrivate = divBytes(s.clientPrivate, size)
	return per.clientMemCell()
}

// perInt divides a count by the fleet size, rounding toward zero — these are whole
// descriptors and whole goroutines, so a fractional answer means the cost is not
// per-session and the raw difference in the rows above is the honest figure.
func perInt(delta, size int) int {
	if size == 0 {
		return delta
	}
	return delta / size
}

// divBytes divides a byte count by the fleet size.
func divBytes(b int64, size int) int64 {
	if size == 0 {
		return b
	}
	return b / int64(size)
}

// humanBytes renders a byte count in KiB/MiB, so a table of them can be read without
// counting digits.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
