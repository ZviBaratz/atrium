package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// splashTestHome builds the minimal home the splash tick needs: a tabbed
// window to push frames into and an empty list (the idle splash's condition).
func splashTestHome() *home {
	tw := ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background()))
	sp := spinner.New()
	return &home{tabbedWindow: tw, list: ui.NewList(&sp)}
}

// TestSplashTickFrozenWhileOverlayUp locks the "don't animate behind a
// dialog" behavior: outside the default state the loop neither arms nor
// re-arms, so the welcome dialog (and any other overlay) the user is reading
// has a still background, not churning motion.
func TestSplashTickFrozenWhileOverlayUp(t *testing.T) {
	m := splashTestHome()
	m.state = stateWelcome

	require.Nil(t, m.armSplashTick(), "the loop must not arm behind an overlay")

	// A live loop dies (and clears its flag) the moment an overlay owns the
	// screen, without advancing the frame.
	m.splashTicking = true
	_, cmd := m.handleSplashTick()
	require.Nil(t, cmd, "the loop must die behind an overlay")
	require.False(t, m.splashTicking, "a dead loop must clear its flag")
	require.Zero(t, m.splashFrame, "splash must not advance behind an overlay")
}

// TestSplashTickAnimatesWhenIdle locks the animation loop's contract in the
// idle empty state: arming is single-flight, and each tick advances exactly
// one frame and re-arms.
func TestSplashTickAnimatesWhenIdle(t *testing.T) {
	m := splashTestHome()
	m.state = stateDefault

	require.NotNil(t, m.armSplashTick(), "the idle empty state must arm the loop")
	require.True(t, m.splashTicking)
	require.Nil(t, m.armSplashTick(), "a live loop must not be armed twice")

	_, cmd := m.handleSplashTick()
	require.NotNil(t, cmd, "an animating loop must re-arm itself")
	require.Equal(t, 1, m.splashFrame, "each tick advances one frame")
	_, _ = m.handleSplashTick()
	require.Equal(t, 2, m.splashFrame)
}

// TestSplashTickNeverArmsWhenDisabled is the #316 opt-out at the tick, which is
// the half the render gate cannot deliver: with the animation off the panes draw
// a static wordmark, so a loop still running would push 60 identical frames a
// second at a screen that never changes — exactly the cost the setting exists to
// remove. The loop must not arm, and a live one must die.
func TestSplashTickNeverArmsWhenDisabled(t *testing.T) {
	m := splashTestHome()
	m.state = stateDefault
	m.appConfig = &config.Config{Splash: config.SplashOff}

	require.Nil(t, m.armSplashTick(), "a disabled splash must not arm the loop")
	require.False(t, m.splashTicking)

	// And a loop already running when the user turns it off dies on its next
	// tick without advancing, the same way one behind an overlay does.
	m.splashTicking = true
	_, cmd := m.handleSplashTick()
	require.Nil(t, cmd, "a disabled splash must stop the loop")
	require.False(t, m.splashTicking, "a dead loop must clear its flag")
	require.Zero(t, m.splashFrame, "a disabled splash must not advance")
}

// TestSplashTickAnimatesWhenEnabled is the positive control for the test above:
// the same home with the setting on still animates, so a failure there means the
// gate fired rather than that the fixture stopped reaching the idle branch.
func TestSplashTickAnimatesWhenEnabled(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"nil config": nil,
		"unset":      {},
		"random":     {Splash: config.SplashRandom},
		"pinned":     {Splash: config.SplashVariants()[0]},
	} {
		m := splashTestHome()
		m.state = stateDefault
		m.appConfig = cfg
		require.NotNilf(t, m.armSplashTick(), "%s: the idle empty state must arm the loop", name)
	}
}

// TestScreensaverAnimatesWithSplashDisabled pins the scope of the opt-out in app,
// mirroring ui's TestSplashDisabledLeavesTheScreensaverAnimating. The easter egg
// is an explicit keypress; folding it into the setting would leave the backtick
// a silently dead key, with no hint anywhere saying why.
func TestScreensaverAnimatesWithSplashDisabled(t *testing.T) {
	m := screensaverTestHome()
	m.state = stateDefault
	m.appConfig = &config.Config{Splash: config.SplashOff}

	_, cmd := m.handleKeyPress(textMsg("`"))
	require.Equal(t, stateScreensaver, m.state, "the easter egg must still enter")
	require.NotNil(t, cmd, "entering must arm the splash tick")
	require.True(t, m.splashTicking, "the screensaver must animate whatever the setting says")
}

// screensaverTestHome is splashTestHome at a window size the splash fits.
func screensaverTestHome() *home {
	m := splashTestHome()
	m.windowWidth, m.windowHeight = 80, 30
	return m
}

// TestScreensaverEntersAndAnyKeyExits locks the easter egg's core loop:
// backtick enters (arming the animation), and the next key wakes the screen
// while being consumed — a stray 'n' must not open the new-session form.
func TestScreensaverEntersAndAnyKeyExits(t *testing.T) {
	m := screensaverTestHome()
	m.state = stateDefault

	_, cmd := m.handleKeyPress(textMsg("`"))
	require.Equal(t, stateScreensaver, m.state)
	require.NotNil(t, cmd, "entering must arm the splash tick")
	require.True(t, m.splashTicking)

	_, _ = m.handleKeyPress(textMsg("n"))
	require.Equal(t, stateDefault, m.state)
	require.Nil(t, m.textInputOverlay, "the waking key must be consumed, not acted on")
}

// TestScreensaverConsumesQuitKeys guards against rage-quits: q / ctrl+c wake
// the screen instead of tearing the app down.
func TestScreensaverConsumesQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		textMsg("q"),
		keyMsg("ctrl+c"),
	} {
		m := screensaverTestHome()
		m.state = stateScreensaver
		_, cmd := m.handleKeyPress(k)
		require.Equal(t, stateDefault, m.state, "key %q must wake", k.String())
		require.Nil(t, cmd, "key %q must not quit", k.String())
	}
}

// TestScreensaverIgnoredBelowSplashFloor: with the window too small for the
// field to read there is nothing to show, so the key is silently inert.
func TestScreensaverIgnoredBelowSplashFloor(t *testing.T) {
	m := splashTestHome()
	m.windowWidth, m.windowHeight = 40, 10
	m.state = stateDefault

	_, _ = m.handleKeyPress(textMsg("`"))
	require.Equal(t, stateDefault, m.state)
}

// TestScreensaverAnimatesRegardlessOfSessions pins the short-circuit in
// splashAnimating: the screensaver animates whatever the session count — the
// nil list here would panic if the state ever fell through to the idle
// branch's NumInstances check.
func TestScreensaverAnimatesRegardlessOfSessions(t *testing.T) {
	m := &home{state: stateScreensaver}
	require.True(t, m.splashAnimating())
}

// TestScreensaverViewIsFullWindow: the view replaces the whole frame at the
// window size (the pane sizes here are zero — the screensaver must not use
// them).
func TestScreensaverViewIsFullWindow(t *testing.T) {
	m := screensaverTestHome()
	m.state = stateScreensaver
	m.splashFrame = 5

	lines := strings.Split(ansi.Strip(m.View()), "\n")
	require.Len(t, lines, m.windowHeight)
	for i, ln := range lines {
		require.LessOrEqual(t, lipgloss.Width(ln), m.windowWidth, "row %d overflows", i)
	}
}

// TestScreensaverMouse: a click wakes the screen; wheel and motion don't, so
// a nudged mouse doesn't tear it down.
func TestScreensaverMouse(t *testing.T) {
	m := screensaverTestHome()
	m.state = stateScreensaver

	_, _ = m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	require.Equal(t, stateScreensaver, m.state, "wheel must not wake")
	_, _ = m.handleMouse(tea.MouseMsg{Action: tea.MouseActionMotion})
	require.Equal(t, stateScreensaver, m.state, "motion must not wake")

	_, _ = m.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	require.Equal(t, stateDefault, m.state, "a click wakes")
}

// TestScreensaverExitsWhenResizedBelowFloor: shrinking the window under the
// splash floor mid-screensaver wakes rather than rendering a degenerate field.
func TestScreensaverExitsWhenResizedBelowFloor(t *testing.T) {
	h := newCreateFormHome(t)
	h.welcomeChecked = true // keep maybeShowWelcome out of the resize path
	h.state = stateScreensaver

	model, _ := h.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	h = model.(*home)
	require.Equal(t, stateScreensaver, h.state, "a comfortable resize keeps the screensaver")

	model, _ = h.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	h = model.(*home)
	require.Equal(t, stateDefault, h.state)
}
