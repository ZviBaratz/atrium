package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/require"
)

// The surface gate for #701. Starting the terminal tab's shell runs tmux
// new-session in the instance's working directory, and a pause or kill in flight is
// in the middle of deleting that directory — on a kill, its branch and its instance
// record too, leaving a shell nothing but `atrium reset` sweeps.
//
// The gate is on resolveFrameTarget rather than on the keys because two of the
// three routes to the shell involve no keypress; the click route has its own test
// below, and the already-active-tab route is what every case here drives (the tab
// is set once, and each case resolves a fresh target as a tick would).
func TestTerminalShellIsNotStartedWhileATeardownIsInFlight(t *testing.T) {
	spy := newFrameSpy("shell prompt $")
	h, inst := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)

	// The positive control first: with nothing in flight the terminal tab resolves a
	// real target, so the refusals below are the gate talking and not the fixture.
	target := h.resolveFrameTarget()
	require.False(t, target.empty(), "an idle terminal tab must resolve a create target")
	require.Same(t, inst, target.termInstance)

	t.Run("an async action in flight", func(t *testing.T) {
		h.actionInFlight = true
		defer func() { h.actionInFlight = false }()
		require.True(t, h.resolveFrameTarget().empty(),
			"a pause/kill/resume in flight must not start a shell in the worktree it is removing")
	})

	t.Run("a teardown mark outliving the busy flag", func(t *testing.T) {
		// asyncActionDoneMsg clears actionInFlight one message BEFORE killDoneMsg
		// reaches applyKillDone's reap, and messages are processed in between. The
		// retiring mark is what covers that gap, so it is asserted with the busy flag
		// explicitly false.
		require.False(t, h.actionInFlight)
		h.retiring = map[*session.Instance]bool{inst: true}
		defer func() { h.retiring = nil }()
		require.True(t, h.resolveFrameTarget().empty(),
			"an instance whose teardown is still landing must not start a shell")
	})

	t.Run("another instance's teardown", func(t *testing.T) {
		other, err := session.NewInstance(session.InstanceOptions{
			Title: "bystander", Path: t.TempDir(), Program: "claude", Direct: true,
		})
		require.NoError(t, err)
		h.retiring = map[*session.Instance]bool{other: true}
		defer func() { h.retiring = nil }()
		require.False(t, h.resolveFrameTarget().empty(),
			"the mark is per-instance: a bystander's teardown must not withhold this shell")
	})

	// And the gate lets go again, rather than wedging the tab for the rest of the run.
	require.False(t, h.resolveFrameTarget().empty(),
		"the terminal tab must resolve a create target again once the action lands")
}

// The tab bar is clickable, and app_msgs.go's mouse branch never reaches the busy
// gate — TabAtZone resolves an index and SetActiveTab takes it. This drives that
// same call to prove the click route is covered by the surface gate rather than by
// keyAllowedWhileBusy, which it never consults.
func TestTerminalTabOpenedByClickWhileBusyStartsNoShell(t *testing.T) {
	spy := newFrameSpy("shell prompt $")
	h, _ := newCaptureHome(t, spy)

	h.actionInFlight = true
	// Exactly what the mouse handler does with TabAtZone's index.
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)
	require.True(t, h.tabbedWindow.IsInTerminalTab(), "precondition: the click landed on the terminal tab")

	require.True(t, h.resolveFrameTarget().empty(),
		"a click onto the terminal tab mid-teardown must not start a shell either")
}

// The gate withholds a shell; it must not freeze the tab. An existing shell is
// still captured while an action is in flight, which is what makes leaving the tab
// keys on the busy allowlist the right trade — the view stays navigable and live.
func TestBusyTerminalTabStillCapturesAnExistingShell(t *testing.T) {
	testutil.RequireTmux(t)
	spy := newFrameSpy("shell prompt $")
	h, inst := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)
	t.Cleanup(h.tabbedWindow.CloseTerminal)

	// Drive one round of the real chain so the shell exists and is cached.
	_, cmd := h.Update(paneFrameMsg{at: time.Now()})
	require.NotNil(t, cmd)
	cmd()
	require.True(t, h.tabbedWindow.HasTerminalSession(inst), "precondition: the shell exists")

	h.actionInFlight = true
	target := h.resolveFrameTarget()
	require.False(t, target.empty(), "an existing shell must keep being captured while busy")
	require.NotNil(t, target.terminal, "the capture must target the live shell, not a create")
}

// "Gate the surface" is only true while the surface has one door. EnsureTerminalSession
// is the pane's create path; armFrameCapture is the sole production caller and the one
// place shellStartRefused is consulted, so a second call site added elsewhere would
// bypass the gate silently — nothing else in the suite would notice.
// moduleRoot walks up from the test's working directory to the go.mod, the same
// idiom keys/readme_drift_test.go's moduleFile uses to read cross-package files.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached the filesystem root without finding go.mod")
		dir = parent
	}
}

func TestShellCreationHasOneProductionEntryPoint(t *testing.T) {
	root := moduleRoot(t)
	out, err := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "*.go").Output()
	require.NoError(t, err, "the guard needs the repo's Go files")

	callers := map[string]int{}
	call := regexp.MustCompile(`\bEnsureTerminalSession\b`)
	for _, path := range strings.Fields(string(out)) {
		if strings.HasSuffix(path, "_test.go") || path == "ui/tabbed_window.go" {
			continue // the definition's own file, and tests, are not production callers
		}
		body, readErr := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, readErr)
		for _, line := range strings.Split(string(body), "\n") {
			if call.MatchString(line) {
				callers[path]++
			}
		}
	}

	require.Equal(t, map[string]int{"app/app_frames.go": 1}, callers,
		"the terminal shell must have exactly one production entry point, and it must be the "+
			"gated one in armFrameCapture — a second caller would create shells behind "+
			"shellStartRefused's back (#701, and #522's read-only condition on the same predicate)")
}
