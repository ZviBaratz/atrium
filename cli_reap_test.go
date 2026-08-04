package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// scanTime is the instant the fixtures below pretend to have been scanned at. The
// PID-reuse guard compares a captured start time against a re-read one, so the two
// have to be the same value for a process that did not change — real clocks make that
// a coin flip, so nothing here reads one.
var scanTime = time.Date(2026, 8, 4, 10, 4, 17, 0, time.UTC)

// fakeProcs is a process table the reaper can signal without anything dying for real.
// It records every signal in order, which is what the ladder tests assert on: the
// question is never only "did it end up dead" but "what was it sent, and in what
// order".
type fakeProcs struct {
	started map[int]time.Time
	alive   map[int]bool
	// ignoresTerm names processes that survive SIGTERM and die only on SIGKILL —
	// the case the second rung of the ladder exists for.
	ignoresTerm map[int]bool
	// immortal names processes that survive everything, so "signalled and still
	// there" stays distinguishable from "killed".
	immortal map[int]bool
	sent     []string
}

func newFakeProcs() *fakeProcs {
	return &fakeProcs{
		started:     map[int]time.Time{},
		alive:       map[int]bool{},
		ignoresTerm: map[int]bool{},
		immortal:    map[int]bool{},
	}
}

// add registers a live process started at scanTime.
func (f *fakeProcs) add(pids ...int) *fakeProcs {
	for _, pid := range pids {
		f.alive[pid] = true
		f.started[pid] = scanTime
	}
	return f
}

// install swaps the reaper's signalling seams. They are package-level shared state,
// so no test using them may run in parallel. reapSleep becomes a no-op: the ladder's
// waits are real budgets in production and pure latency in a test.
func (f *fakeProcs) install(t *testing.T) *fakeProcs {
	t.Helper()
	origStart, origSignal, origAlive, origSleep := reapStartTime, reapSignal, reapAlive, reapSleep
	t.Cleanup(func() {
		reapStartTime, reapSignal, reapAlive, reapSleep = origStart, origSignal, origAlive, origSleep
	})

	reapStartTime = func(pid int) (time.Time, bool) {
		started, ok := f.started[pid]
		return started, ok
	}
	reapAlive = func(pid int) bool { return f.alive[pid] }
	reapSignal = func(pid int, sig syscall.Signal) error {
		name := "SIGTERM"
		if sig == syscall.SIGKILL {
			name = "SIGKILL"
		}
		f.sent = append(f.sent, fmt.Sprintf("%s->%d", name, pid))
		if f.immortal[pid] {
			return nil
		}
		if sig == syscall.SIGKILL || !f.ignoresTerm[pid] {
			f.alive[pid] = false
		}
		return nil
	}
	reapSleep = func(time.Duration) {}
	return f
}

// stubReapCheck fixes what the scan reports. Every test here stubs it: the real one
// probes the ambient tmux socket, and package main has no TestMain sandboxing
// TMUX_TMPDIR, so an unstubbed call would reach the developer's live fleet.
func stubReapCheck(t *testing.T, res doctor.OrphanResult) {
	t.Helper()
	orig := reapCheck
	t.Cleanup(func() { reapCheck = orig })
	reapCheck = func(context.Context) doctor.OrphanResult { return res }
}

// orphan builds an unreachable server fixture: proven unreachable, which is the only
// class reap kills by default.
func orphan(pid int, kids ...tmux.ChildProc) tmux.OrphanServer {
	return tmux.OrphanServer{
		PID: pid, Socket: "atrium", SocketPath: "/tmp/tmux-1000/atrium",
		Reachable: false, ReachableKnown: true, Started: scanTime, Children: kids,
	}
}

func kid(pid int) tmux.ChildProc {
	return tmux.ChildProc{PID: pid, Comm: "claude", Started: scanTime}
}

func result(servers ...tmux.OrphanServer) doctor.OrphanResult {
	return doctor.OrphanResult{Supported: true, Servers: servers, Now: time.Now()}
}

// TestReapWithoutKillSignalsNothing is the property the default has to have: `reap`
// on its own is a report. Asserting on the recorded signals rather than on the output
// is the point — a listing that also killed would print exactly the same listing.
func TestReapWithoutKillSignalsNothing(t *testing.T) {
	procs := newFakeProcs().add(1499239, 1499240).install(t)
	stubReapCheck(t, result(orphan(1499239, kid(1499240))))

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\ny\ny\n"), reapOpts{}))

	require.Empty(t, procs.sent, "`reap` without --kill must not signal anything, even with a stdin full of yes")
	require.True(t, procs.alive[1499239])
	require.Contains(t, out.String(), "pid 1499239")
	require.Contains(t, out.String(), "reap --kill")
}

// TestReapKillTargetsOnlyProvenUnreachableServers.
//
// Reachable servers are recoverable with the tmux command doctor prints, so they are
// left alone without --all. Unknown-reachability servers are never targeted at all:
// when tmux cannot be run nothing was established, and the ambient live server could
// not be excluded either — so those rows may be the running fleet.
func TestReapKillTargetsOnlyProvenUnreachableServers(t *testing.T) {
	unreachable := orphan(10)
	reachable := orphan(20)
	reachable.Reachable = true
	unknown := orphan(30)
	unknown.ReachableKnown = false

	servers := []tmux.OrphanServer{unreachable, reachable, unknown}

	require.Equal(t, []int{10}, pidsOf(reapTargets(servers, false)),
		"by default only a server proven unreachable may be killed")
	require.Equal(t, []int{10, 20}, pidsOf(reapTargets(servers, true)),
		"--all adds the reachable server")

	for _, all := range []bool{false, true} {
		require.NotContains(t, pidsOf(reapTargets(servers, all)), 30,
			"a server whose reachability could not be established is never a target (--all=%v)", all)
	}
}

func pidsOf(servers []tmux.OrphanServer) []int {
	pids := make([]int, 0, len(servers))
	for _, s := range servers {
		pids = append(pids, s.PID)
	}
	return pids
}

// TestReapKillDefaultsToNo covers every answer that is not an explicit yes. A
// confirmation whose default direction is wrong is worse than no confirmation: the
// user who hits enter to see what happens loses an agent's unpushed work.
func TestReapKillDefaultsToNo(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", "N\n", "no\n", "yes please\n", "", "  \n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			procs := newFakeProcs().add(10, 11).install(t)
			stubReapCheck(t, result(orphan(10, kid(11))))

			var out bytes.Buffer
			require.NoError(t, runReap(t.Context(), &out, strings.NewReader(answer), reapOpts{kill: true}))

			require.Empty(t, procs.sent, "answer %q must not be read as consent", answer)
			require.True(t, procs.alive[10])
			require.Contains(t, out.String(), "skipped pid 10")
		})
	}
}

// TestReapKillPromptNamesEveryProcessThatDies. The prompt is the whole safety
// mechanism — the user's consent is only meaningful if it is informed, and what dies
// with a tmux server is its agents, not the server.
func TestReapKillPromptNamesEveryProcessThatDies(t *testing.T) {
	newFakeProcs().add(10, 11, 12, 13).install(t)
	stubReapCheck(t, result(orphan(10, kid(11), kid(12), kid(13))))

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("n\n"), reapOpts{kill: true}))

	got := out.String()
	require.Contains(t, got, "these 3 processes die with it, and may hold work that was never pushed")
	for _, pid := range []int{11, 12, 13} {
		require.Contains(t, got, fmt.Sprintf("pid %d", pid), "the prompt must name child pid %d", pid)
	}
	require.Contains(t, got, "kill? [y/N]")
}

// TestReapKillEscalatesAndVerifiesTheChildren walks the whole ladder: SIGTERM, then
// SIGKILL for what ignored it, then a re-check of every captured child with a direct
// signal to any survivor.
//
// The child that outlives the server is the case that matters. Killing the server
// does normally take its agents down — the kernel hangs up their ptys — but that is a
// consequence, not something this code is entitled to assume, and trusting it is the
// difference between reaping the orphan and quietly halving it.
func TestReapKillEscalatesAndVerifiesTheChildren(t *testing.T) {
	procs := newFakeProcs().add(10, 11, 12).install(t)
	procs.ignoresTerm[10] = true // the server refuses SIGTERM
	stubReapCheck(t, result(orphan(10, kid(11), kid(12))))

	// Killing the server takes child 11 with it; 12 survives and must be signalled.
	origSignal := reapSignal
	reapSignal = func(pid int, sig syscall.Signal) error {
		err := origSignal(pid, sig)
		if pid == 10 && !procs.alive[10] {
			procs.alive[11] = false
		}
		return err
	}

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true}))

	require.Equal(t, []string{"SIGTERM->10", "SIGKILL->10", "SIGTERM->12"}, procs.sent,
		"the server escalates to SIGKILL, the child that died with it is not signalled, "+
			"and the child that outlived it is")
	require.False(t, procs.alive[10])
	require.False(t, procs.alive[12])
	require.Contains(t, out.String(), "killed pid 10")
	require.Contains(t, out.String(), "killed child pid 12")
	require.Contains(t, out.String(), "1 killed, 0 skipped, 0 survived")
}

// TestReapKillRefusesAPIDWhoseStartTimeMoved is the PID-reuse guard. Between the scan
// and the signal the orphan can exit and its pid be recycled onto something else
// entirely; the start time is what tells those apart, and it is re-read immediately
// before the first signal rather than trusted from the scan.
func TestReapKillRefusesAPIDWhoseStartTimeMoved(t *testing.T) {
	procs := newFakeProcs().add(10, 11).install(t)
	procs.started[10] = scanTime.Add(3 * time.Hour) // a different process on the same pid

	stubReapCheck(t, result(orphan(10, kid(11))))

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true}))

	require.Empty(t, procs.sent, "a recycled pid must never be signalled, even after a yes")
	require.True(t, procs.alive[10])
	require.Contains(t, out.String(), "no longer the process that was scanned")
	require.Contains(t, out.String(), "0 killed, 1 skipped, 0 survived")
}

// TestReapKillRefusesAChildWhoseStartTimeMoved: a child's pid was read before its
// parent's death, so it is staler than the server's and needs the same guard.
func TestReapKillRefusesAChildWhoseStartTimeMoved(t *testing.T) {
	procs := newFakeProcs().add(10, 11).install(t)
	stubReapCheck(t, result(orphan(10, kid(11))))
	// The child's pid gets recycled while the server is being killed.
	origSignal := reapSignal
	reapSignal = func(pid int, sig syscall.Signal) error {
		if pid == 10 {
			procs.started[11] = scanTime.Add(time.Hour)
		}
		return origSignal(pid, sig)
	}

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true}))

	require.Equal(t, []string{"SIGTERM->10"}, procs.sent, "the recycled child pid must not be signalled")
	require.True(t, procs.alive[11])
	require.Contains(t, out.String(), "left pid 11 alone")
}

// TestReapKillReportsASurvivorAndExitsNonzero. A server that took SIGKILL and is
// still there is the one case a script must be able to notice, so it is the exit
// code rather than a line in the output.
func TestReapKillReportsASurvivorAndExitsNonzero(t *testing.T) {
	procs := newFakeProcs().add(10).install(t)
	procs.immortal[10] = true
	stubReapCheck(t, result(orphan(10)))

	var out bytes.Buffer
	err := runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true})

	require.Error(t, err)
	require.Contains(t, err.Error(), "survived SIGKILL")
	require.Equal(t, []string{"SIGTERM->10", "SIGKILL->10"}, procs.sent)
	require.Contains(t, out.String(), "0 killed, 0 skipped, 1 survived")
}

// TestReapKillYesSkipsThePromptButNotTheGuard: --yes is for scripts, and it waives
// the confirmation only. The PID-reuse guard is not a prompt, and a script has even
// less chance than a human of noticing that it killed the wrong process.
func TestReapKillYesSkipsThePromptButNotTheGuard(t *testing.T) {
	procs := newFakeProcs().add(10, 20).install(t)
	procs.started[20] = scanTime.Add(time.Hour)
	stubReapCheck(t, result(orphan(10), orphan(20)))

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader(""), reapOpts{kill: true, yes: true}))

	require.Equal(t, []string{"SIGTERM->10"}, procs.sent)
	require.NotContains(t, out.String(), "kill? [y/N]", "--yes must not print a prompt it will not read")
	require.Contains(t, out.String(), "1 killed, 1 skipped, 0 survived")
}

// TestReapDeletesNothing pins the rule the whole design is built around: reap kills
// processes and removes no files. A killed server's socket file is inert, and a
// sweep over the directory holding it is what cost thirteen live sessions in #584.
func TestReapDeletesNothing(t *testing.T) {
	newFakeProcs().add(10).install(t)
	stubReapCheck(t, doctor.OrphanResult{
		Supported: true,
		Servers:   []tmux.OrphanServer{orphan(10)},
		SocketDir: "/tmp/tmux-1000",
		Stale:     []tmux.StaleSocket{{Path: "/tmp/tmux-1000/atrium-old"}},
		Now:       time.Now(),
	})

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true}))

	// The stale file is reported with the command to remove it, and reap does not run
	// it — there is no "removed" anywhere in the output.
	require.Contains(t, out.String(), "rm -- /tmp/tmux-1000/atrium-old")
	require.NotContains(t, out.String(), "removed")
	require.NotContains(t, out.String(), "deleted socket")
}

// TestReapKillOnAnUnsupportedPlatformFails rather than reporting a quiet success: a
// scan that could not run has not established that there is nothing to kill.
func TestReapKillOnAnUnsupportedPlatformFails(t *testing.T) {
	newFakeProcs().install(t)
	stubReapCheck(t, doctor.OrphanResult{Supported: false, Now: time.Now()})

	err := runReap(t.Context(), &bytes.Buffer{}, strings.NewReader(""), reapOpts{kill: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Linux only")
}

// TestReapRejectsAllAndYesWithoutKill: both flags only mean anything alongside
// --kill, and `reap --yes` reads like it would be enough to make it kill.
func TestReapRejectsAllAndYesWithoutKill(t *testing.T) {
	newFakeProcs().install(t)
	stubReapCheck(t, result(orphan(10)))

	for _, opts := range []reapOpts{{all: true}, {yes: true}} {
		err := runReap(t.Context(), &bytes.Buffer{}, strings.NewReader(""), opts)
		require.Error(t, err)
		require.Contains(t, err.Error(), "only apply to --kill")
	}
}

// TestReapKillWithNothingToKillSaysSo, rather than printing a listing and stopping —
// the user asked for a kill and needs to know none happened.
func TestReapKillWithNothingToKillSaysSo(t *testing.T) {
	procs := newFakeProcs().add(20).install(t)
	reachable := orphan(20)
	reachable.Reachable = true
	stubReapCheck(t, result(reachable))

	var out bytes.Buffer
	require.NoError(t, runReap(t.Context(), &out, strings.NewReader("y\n"), reapOpts{kill: true}))
	require.Contains(t, out.String(), "nothing to kill")
	require.Empty(t, procs.sent)
}

// TestReapKillRefusesAnIncompleteScan applies "positive proof only" to the inventory
// itself.
//
// A truncated /proc walk attributes children from a partial table, so a server's
// Children list can be short — and that list is exactly what the prompt shows before
// the user consents. Killing on it would take consent obtained against an
// understatement of what dies: the user agrees to lose one shell and loses thirteen
// agents. The fixture drives it with --yes and a stdin full of "y" on purpose, so a
// pass cannot come from the prompt merely defaulting to no; the refusal has to happen
// before anything is asked.
func TestReapKillRefusesAnIncompleteScan(t *testing.T) {
	for _, tc := range []struct {
		name string
		gaps tmux.ScanGaps
	}{
		{"socket table unreadable", tmux.ScanGaps{SocketTableUnread: true}},
		{"proc walk truncated", tmux.ScanGaps{ProcTableTruncated: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			procs := newFakeProcs().add(1499239, 1499240).install(t)
			res := result(orphan(1499239, kid(1499240)))
			res.Gaps = tc.gaps
			stubReapCheck(t, res)

			var out bytes.Buffer
			err := runReap(t.Context(), &out, strings.NewReader("y\ny\ny\n"),
				reapOpts{kill: true, yes: true})
			require.Error(t, err, "an incomplete inventory must not be killed on")
			require.Contains(t, err.Error(), "incomplete scan")
			require.Empty(t, procs.sent,
				"nothing may be signalled when the scan could not see what it would destroy")
		})
	}
}
