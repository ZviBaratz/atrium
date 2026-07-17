package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"

	tea "github.com/charmbracelet/bubbletea"
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
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
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
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
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
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	// Failures -> (Session skipped) -> All.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	out := stripANSI(o.Render())
	if !strings.Contains(out, "Command Log — all") {
		t.Errorf("cycle with no session should land back on the all view:\n%s", out)
	}
}
