package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// TestWelcome_FitsNarrowTerminal guards that the first-run welcome, whose box is
// authored at a fixed 54 columns, is clamped to the terminal so it never spills
// off a narrow one (TestViewFitsTerminalBounds does not exercise stateWelcome).
func TestWelcome_FitsNarrowTerminal(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	const w = 40
	model, cmd := h.Update(tea.WindowSizeMsg{Width: w, Height: 30})
	h = model.(*home)
	require.Equal(t, stateWelcome, h.state, "first launch enters stateWelcome")
	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)

	// The overlay must be width-clamped at creation, not merely truncated by
	// PlaceOverlay on render: assert its own rendered box fits, which only holds
	// if maybeShowWelcome sized it from the cached terminal width up front (the
	// first WindowSizeMsg creates the overlay after the resize handler runs, so a
	// resize-only clamp would leave this first frame over-wide).
	require.LessOrEqual(t, lipgloss.Width(h.welcomeOverlay.Render()), w,
		"welcome overlay must be width-clamped at creation, not just truncated on render")

	for i, line := range strings.Split(h.View(), "\n") {
		require.LessOrEqualf(t, ansi.PrintableRuneWidth(line), w,
			"line %d exceeds terminal width %d in the welcome on a narrow terminal", i, w)
	}
}

// stubDetect swaps the package detection seam for the test's duration.
func stubDetect(t *testing.T, profiles []config.Profile) {
	t.Helper()
	orig := detectAgents
	detectAgents = func() []config.Profile { return profiles }
	t.Cleanup(func() { detectAgents = orig })
}

func TestWelcome_FirstRunConfirmPersistsProgram(t *testing.T) {
	stubDetect(t, []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	})
	h := newCreateFormHome(t)

	// First WindowSizeMsg opens the welcome and fires the detect cmd.
	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	require.Equal(t, stateWelcome, h.state, "first launch enters stateWelcome")
	require.NotNil(t, h.welcomeOverlay)
	require.NotNil(t, cmd, "detection cmd should be returned")

	// Run the detect cmd and feed its message back.
	msg := cmd()
	detected, ok := msg.(agentsDetectedMsg)
	require.True(t, ok, "cmd should yield agentsDetectedMsg")
	model, _ = h.Update(detected)
	h = model.(*home)

	// Move to codex and confirm.
	h.Update(tea.KeyMsg{Type: tea.KeyDown})
	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h = model.(*home)

	require.Equal(t, stateDefault, h.state, "confirm closes the welcome")
	require.Equal(t, "codex", h.appConfig.DefaultProgram, "confirm persists the picked program")
	require.Equal(t, "codex", h.program, "confirm applies the program to the run")
	require.NotZero(t, h.appState.GetHelpScreensSeen()&(helpTypeWelcome{}.mask()), "confirm sets the welcome seen-bit")
}

// Skipping the welcome with Esc now retires it (#381): the modal swallows keys
// until dismissed, so re-greeting a look-around-first user every launch is a
// recurring toll. The seen-bit is set on skip just as on confirm; recovery is a
// one-line notice pointing at settings, not the modal again.
func TestWelcome_SkipMarksSeen(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)

	model, skipCmd := h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	h = model.(*home)

	require.Equal(t, stateDefault, h.state, "esc closes the welcome")
	require.NotZero(t, h.appState.GetHelpScreensSeen()&(helpTypeWelcome{}.mask()),
		"skip must retire the welcome so a look-around-first user is not re-greeted")
	require.NotNil(t, skipCmd, "skip flashes a recovery notice alongside the resize")
}

// The retired seen-bit makes the next launch take the returning-user branch of
// maybeShowWelcome rather than re-opening the modal — the AC2 "relaunch does not
// re-open" guarantee, exercised through the real show path.
func TestWelcome_SkipThenRelaunchDoesNotReopen(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)
	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	h = model.(*home)
	require.NotZero(t, h.appState.GetHelpScreensSeen()&(helpTypeWelcome{}.mask()))

	// Simulate a relaunch: welcomeChecked guards once-per-process, so clear it and
	// re-run the show gate against the now-persisted seen-bit.
	h.welcomeChecked = false
	h.welcomeOverlay = nil
	h.maybeShowWelcome()
	require.NotEqual(t, stateWelcome, h.state, "a retired welcome must not re-open on relaunch")
	require.Nil(t, h.welcomeOverlay, "no welcome overlay is constructed on relaunch")
}

// While the welcome is up, menuVisible() is false and the panes are sized
// full-height by maybeShowWelcome's recomputeLayout(). Closing the welcome
// flips menuVisible back on (the hint bar is on by default), so unless the
// close path also recomputes, the hint bar's row lands on top of still
// full-height panes and the frame is one row taller than the terminal — on
// first-ever launch, before any other resize fixes it. TestViewFitsTerminalBounds
// (view_bounds_test.go) enforces the general "frame never exceeds the terminal"
// invariant across the app; these two mirror that same fit check specifically
// across a welcome close, on both the confirm and skip paths.
func TestWelcome_ConfirmFrameFitsAfterClose(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	require.Equal(t, stateWelcome, h.state, "first launch enters stateWelcome")

	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)

	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h = model.(*home)

	require.Equal(t, stateDefault, h.state, "confirm closes the welcome")
	require.Equal(t, 40, lipgloss.Height(h.View()),
		"frame must be exactly the terminal height after the welcome closes via confirm")
}

func TestWelcome_SkipFrameFitsAfterClose(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	require.Equal(t, stateWelcome, h.state, "first launch enters stateWelcome")

	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)

	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	h = model.(*home)

	require.Equal(t, stateDefault, h.state, "esc closes the welcome")
	require.Equal(t, 40, lipgloss.Height(h.View()),
		"frame must be exactly the terminal height after the welcome closes via skip")
}

// Confirming persists the picked profile's NAME as default_program (matching
// seededDefaultConfig), not its resolved Program, so GetProgram keeps resolving
// the default through the profile list on later launches.
func TestWelcome_ConfirmStoresProfileName(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "/opt/bin/claude"}})
	h := newCreateFormHome(t)

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)
	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h = model.(*home)

	require.Equal(t, "claude", h.appConfig.DefaultProgram, "confirm persists the profile name")
	require.Equal(t, "/opt/bin/claude", h.program, "confirm runs the resolved program")
}

// A user who customized a same-named profile but never started a session keeps
// their command on confirm: MergeDetectedProfiles leaves the custom profile
// intact, and persisting the NAME resolves the default through it rather than
// overwriting it with the detected program.
func TestWelcome_ConfirmPreservesCustomProfile(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "/opt/bin/claude"}})
	h := newCreateFormHome(t)
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude --model opus"}}

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	model, _ = h.Update(cmd().(agentsDetectedMsg))
	h = model.(*home)
	model, _ = h.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h = model.(*home)

	require.Equal(t, "claude", h.appConfig.DefaultProgram)
	require.Equal(t, "claude --model opus", h.program,
		"the user's customized same-named profile is preserved, not overwritten by detection")
}

// seedWelcomeSeen flips the welcome seen-bit so maybeShowWelcome takes the
// returning-user branch instead of showing the welcome.
func seedWelcomeSeen(t *testing.T, h *home) {
	t.Helper()
	seen := h.appState.GetHelpScreensSeen()
	require.NoError(t, h.appState.SetHelpScreensSeen(seen|(helpTypeWelcome{}.mask())))
}

func TestWarn_ReturningUserMissingProgram(t *testing.T) {
	h := newCreateFormHome(t)
	seedWelcomeSeen(t, h)
	h.program = "definitely-not-a-real-binary-xyzzy"

	// The startup path only schedules the check; the probe itself runs off the
	// main loop (checkProgramInstalledCmd), so nothing warns synchronously.
	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	require.Equal(t, stateDefault, h.state, "no welcome for a returning user")
	require.False(t, h.pathWarned, "the check is async — nothing warned synchronously")
	require.NotNil(t, cmd, "a returning user schedules the program check")

	// Deliver the check result: a missing program triggers the one-shot warning.
	msg, ok := cmd().(programCheckedMsg)
	require.True(t, ok, "cmd should yield programCheckedMsg")
	require.False(t, msg.installed)
	model, _ = h.Update(msg)
	h = model.(*home)
	require.True(t, h.pathWarned, "a missing program must trigger the one-shot warning")
}

func TestWarn_ReturningUserInstalledProgram(t *testing.T) {
	h := newCreateFormHome(t)
	seedWelcomeSeen(t, h)
	h.program = "sh" // present on PATH

	model, cmd := h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h = model.(*home)
	require.NotNil(t, cmd, "a returning user schedules the program check")

	msg := cmd().(programCheckedMsg)
	require.True(t, msg.installed, "sh is on PATH")
	model, _ = h.Update(msg)
	h = model.(*home)
	require.False(t, h.pathWarned, "an installed program must not warn")
}

func TestWarn_SuppressedWhenWelcomeShows(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)
	h.program = "definitely-not-a-real-binary-xyzzy" // would warn, but welcome shows

	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	require.Equal(t, stateWelcome, h.state, "first run shows the welcome")
	require.False(t, h.pathWarned, "the standalone warning is suppressed while the welcome shows")
}
