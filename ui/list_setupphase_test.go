package ui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// runningSetupInstance returns an instance whose setup script is mid-run, plus the
// func that ends it. The script is a real, long-running `sleep` cancelled through the
// instance's own lifecycle context, so nothing here is stubbed: the phase only exists
// while a process is actually running, which is the state this row hint is for.
func runningSetupInstance(t *testing.T) (*session.Instance, func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))
	cfg := config.DefaultConfig()
	// The ordinary forking shape, not an `exec`-ed one: cancelling the context kills the
	// script's whole process group (session.isolateProcessGroup), so the shell and the
	// sleep it forked both end and the stop func below returns promptly.
	cfg.RepoScripts = []config.RepoScript{{Name: "any", SetupScript: "sleep 30; echo done"}}
	require.NoError(t, config.SaveConfig(cfg))

	dir := t.TempDir()
	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	inst.SetBaseContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.RunSetupScript(dir)
	}()
	require.Eventually(t, func() bool { return inst.SetupPhase() != "" }, 5*time.Second, 5*time.Millisecond,
		"the setup phase must be set before the process starts, not after it ends")

	return inst, func() {
		cancel()
		<-done
	}
}

// A session installing its dependencies has been Loading for two minutes with nothing
// on the row to say why. Line 2 is where that answer goes: it is the emptiest line on
// a not-yet-started row, and the alternative — a new session.Status — is a value
// persisted in state.json and read by a dozen-odd sites.
func TestRender_SetupPhaseReplacesLineTwo(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, stop := runningSetupInstance(t)
	defer stop()

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := ansi.Strip(r.Render(inst, 0, false, false))

	require.Contains(t, out, "setup script", "the row must say what it is waiting on")
}

// The hint is transient by construction: once the script is done the row goes back to
// its ordinary version-control line.
func TestRender_SetupPhaseGoesAwayWhenTheScriptFinishes(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, stop := runningSetupInstance(t)
	stop()

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := ansi.Strip(r.Render(inst, 0, false, false))

	require.NotContains(t, out, "setup script")
}

// The preview says "Setting up workspace..." for every pre-agent session alike, which
// is exactly when the setup script's minutes are spent — so the pane the user is
// staring at is where the phase matters most.
func TestPreview_SetupPhaseNamesWhatIsRunning(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, stop := runningSetupInstance(t)
	defer stop()

	p := NewPreviewPane()
	p.SetSize(60, 10)
	require.NoError(t, p.UpdateContent(inst))

	require.Contains(t, ansi.Strip(p.String()), "setup script")
}

// The same pane, on a resume. Resume re-materializes the worktree and runs the script
// while the instance is STILL Paused — SetStatus(Running) comes after — so the paused
// arm reached first and the pane spent the whole `npm ci` telling the user to press the
// key they had just pressed.
func TestPreview_SetupPhaseWinsOverThePausedHint(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, stop := runningSetupInstance(t)
	defer stop()
	inst.SetStatus(session.Paused)
	require.True(t, inst.Paused(), "the state a resume runs its script in")

	p := NewPreviewPane()
	p.SetSize(60, 10)
	require.NoError(t, p.UpdateContent(inst))

	out := ansi.Strip(p.String())
	require.Contains(t, out, "setup script")
	require.NotContains(t, out, "Press 'r' to resume")
}
