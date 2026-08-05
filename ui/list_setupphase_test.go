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
	// `exec` so the shell BECOMES sleep: without it a shell that forks leaves the
	// grandchild holding the output pipe, and cancelling the context kills the shell
	// while Run blocks for the full 30 seconds waiting on the pipe.
	cfg.RepoScripts = []config.RepoScript{{Name: "any", SetupScript: "exec sleep 30 >/dev/null 2>&1"}}
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
