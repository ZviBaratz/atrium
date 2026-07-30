package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"

	"charm.land/lipgloss/v2"
)

// The overlay reads live from the log ring: the all view shows every command, the
// failures filter drops successes, and expanding a failure reveals its stderr.
func TestCmdLogOverlay_FilterCycleAndExpand(t *testing.T) {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{Argv: "git status", Session: "alpha", Start: time.Now()})
	cmdlog.Add(cmdlog.Record{
		Argv: "git push -u origin alpha", Session: "alpha", Start: time.Now(),
		Err: true, Exit: 1, Stderr: "! [rejected] non-fast-forward",
	})

	o := NewCmdLogOverlay("alpha")
	o.SetSize(100, 24)

	all := stripANSI(o.Render())
	if !strings.Contains(all, "git status") || !strings.Contains(all, "git push") {
		t.Fatalf("all view should list both commands:\n%s", all)
	}

	// Tab → failures filter: the successful command must drop out.
	o.HandleKeyPress(keyMsg("tab"))
	fails := stripANSI(o.Render())
	if strings.Contains(fails, "git status") {
		t.Errorf("failures view must exclude the successful command:\n%s", fails)
	}
	if !strings.Contains(fails, "git push") {
		t.Errorf("failures view must include the failed command:\n%s", fails)
	}
	// stderr is hidden until expanded.
	if strings.Contains(fails, "non-fast-forward") {
		t.Errorf("stderr should be hidden before expansion:\n%s", fails)
	}

	// Enter expands the failure under the cursor (index 0) to show its stderr.
	o.HandleKeyPress(keyMsg("enter"))
	expanded := stripANSI(o.Render())
	if !strings.Contains(expanded, "non-fast-forward") {
		t.Errorf("expanded failure must show its stderr:\n%s", expanded)
	}
}

// With no session in scope, the per-session filter is skipped when cycling, so the
// user never lands on a guaranteed-empty view.
func TestCmdLogOverlay_SkipsEmptySessionFilter(t *testing.T) {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{Argv: "git status", Start: time.Now(), Err: true})
	o := NewCmdLogOverlay("") // no session
	o.SetSize(80, 20)
	// All -> Failures.
	o.HandleKeyPress(keyMsg("tab"))
	// Failures -> (Session skipped) -> All.
	o.HandleKeyPress(keyMsg("tab"))
	out := stripANSI(o.Render())
	if !strings.Contains(out, "Command Log — all") {
		t.Errorf("cycle with no session should land back on the all view:\n%s", out)
	}
}

// The summary line names where the subprocess CPU went, heaviest verb first. This
// is the only place in the UI that surfaces the cost a Go profiler cannot see
// (#546), so both halves are asserted: the total, and the attribution.
func TestCmdLogOverlay_SummaryReportsCPUByVerb(t *testing.T) {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{Argv: "tmux -L atrium capture-pane -p", Start: time.Now(), CPU: 4 * time.Millisecond})
	cmdlog.Add(cmdlog.Record{Argv: "git diff --numstat abc", Start: time.Now(), CPU: 11 * time.Millisecond})
	cmdlog.Add(cmdlog.Record{Argv: "git diff --numstat def", Start: time.Now(), CPU: 9 * time.Millisecond})

	o := NewCmdLogOverlay("")
	o.SetSize(100, 24)
	got := stripANSI(o.Render())

	if !strings.Contains(got, "3 commands") {
		t.Errorf("summary should count the records:\n%s", got)
	}
	if !strings.Contains(got, "24ms cpu") {
		t.Errorf("summary should total the child CPU (4+11+9=24ms):\n%s", got)
	}
	// The attribution, not just the total: without a per-verb split the number is
	// not something anyone can act on.
	if !strings.Contains(got, "git diff 20ms") {
		t.Errorf("summary should name the heaviest verb and its CPU:\n%s", got)
	}
	if !strings.Contains(got, "tmux capture-pane 4ms") {
		t.Errorf("summary should name the runner-up verb:\n%s", got)
	}
}

// A verb with no measured CPU is left out of the attribution rather than printed
// as "0s". Nothing is known about where its time went — on a platform with no
// rusage that is every verb — and a parenthesised list of zeroes would imply the
// work was free.
func TestCmdLogOverlay_SummaryOmitsZeroCPUVerbs(t *testing.T) {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{Argv: "git status", Start: time.Now()})

	o := NewCmdLogOverlay("")
	o.SetSize(100, 24)
	got := stripANSI(o.Render())

	if !strings.Contains(got, "1 commands") {
		t.Errorf("summary should still count the record:\n%s", got)
	}
	if strings.Contains(got, "(") {
		t.Errorf("summary should carry no attribution when no CPU was measured:\n%s", got)
	}
}

// The summary is a copy change, and a copy change is a width change: at the
// narrowest box SetSize allows it must still be ONE line.
//
// Height, not width, is the assertion that bites. lipgloss does not let an
// overlong line push the border out — it wraps it — so every rendered line stays
// inside the box either way and a width check passes on an unbounded summary. What
// actually breaks is the box growing a row, which shoves the hint line off the
// bottom of a height-constrained overlay.
//
// Both fixtures hold the SAME records and differ only in whether any CPU was
// measured, so the row block is byte-identical and the only thing that can change
// the height is the summary growing its per-verb attribution. (The rows themselves
// already wrap at this box size — renderRow's 8-cell argv floor overflows a
// 40-wide box — which is why the fixtures have to be matched rather than merely
// similar.)
func TestCmdLogOverlay_SummaryStaysOneLineInTheNarrowestBox(t *testing.T) {
	argvs := []string{
		"git diff --numstat 0123456789abcdef",
		"tmux -L atrium capture-pane -p -e -J",
		"gh pr view 12345 --json statusCheckRollup",
		"git rev-list --left-right --count main...HEAD",
	}
	render := func(cpu time.Duration) int {
		cmdlog.Reset()
		for _, argv := range argvs {
			cmdlog.Add(cmdlog.Record{Argv: argv, Start: time.Now(), CPU: cpu})
		}
		o := NewCmdLogOverlay("")
		o.SetSize(0, 0) // clamped to the minimum (40x8)
		return lipgloss.Height(o.Render())
	}

	bare := render(0)                             // no attribution: "N commands · 0s cpu"
	attributed := render(1234 * time.Millisecond) // three verbs and their totals appended

	if attributed != bare {
		t.Errorf("box height = %d with an attributed summary, %d without — the summary wrapped",
			attributed, bare)
	}
}
