package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// menuVisible is the single source of truth for whether the contextual hint bar
// claims a row. It must be true only for the inline interactions the bar uniquely
// serves (new/filter, and a background name generation), and false during plain
// navigation and behind self-documenting overlays (prompt/rename/confirm/help).
func TestMenuVisible_ByState(t *testing.T) {
	h := newCreateFormHome(t)

	cases := []struct {
		name       string
		state      state
		generating bool
		want       bool
	}{
		{"default navigation is clean", stateDefault, false, false},
		{"default + background name gen shows progress", stateDefault, true, true},
		{"inline new session", stateNew, false, true},
		{"inline filter", stateFilter, false, true},
		{"prompt overlay self-documents", statePrompt, false, false},
		{"rename overlay self-documents", stateRename, false, false},
		{"confirm overlay self-documents", stateConfirm, false, false},
		{"help overlay self-documents", stateHelp, false, false},
	}
	for _, c := range cases {
		h.state = c.state
		h.generatingName = c.generating
		require.Equalf(t, c.want, h.menuVisible(), "%s", c.name)
	}
}

// The composed View must carry the hint bar exactly when menuVisible says so. We
// key the assertions on " │ " (the menu's group separator, present whenever the
// multi-option bar renders) and "submit name" (the bar's cue while naming inline).
func TestView_HintBarContextual(t *testing.T) {
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "a", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)()
	h.list.SelectInstance(inst)

	// Plain navigation with a selected session: no bottom bar.
	h.state = stateDefault
	h.menu.SetState(ui.StateDefault)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.NotContains(t, h.View(), " │ ", "default navigation must not render the hint bar")

	// Inline new-session: the bar appears with the submit cue.
	h.newInstance = inst
	h.state = stateNew
	h.menu.SetState(ui.StateNewInstance)
	h.menu.SetNewInstanceHint("myrepo")
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.Contains(t, h.View(), "submit name", "stateNew must render the inline hint bar")
}

// The welcome's seen-bit must NOT be set merely by rendering it — a stray keypress
// that dismisses the overlay should not burn the welcome forever. The bit is set
// only on the first successful session start (see TestWelcome_MarkedSeenOnStart).
func TestWelcome_NotMarkedSeenOnRender(t *testing.T) {
	h := newCreateFormHome(t)
	flag := helpTypeWelcome{}.mask()
	require.Zero(t, h.appState.GetHelpScreensSeen()&flag, "precondition: welcome unseen")

	h.showHelpScreen(helpTypeWelcome{}, nil)

	require.Equal(t, stateHelp, h.state, "welcome should render")
	require.NotNil(t, h.textOverlay)
	require.Zero(t, h.appState.GetHelpScreensSeen()&flag, "rendering the welcome must not mark it seen")
}

// The first successful session start retires the welcome. This is the single
// chokepoint every start funnels through, so creating a session is what makes the
// welcome stop re-appearing on subsequent launches.
func TestWelcome_MarkedSeenOnStart(t *testing.T) {
	h := newAutoNameHome(t, "a")
	inst := h.list.GetInstances()[0]
	flag := helpTypeWelcome{}.mask()
	require.Zero(t, h.appState.GetHelpScreensSeen()&flag, "precondition: welcome unseen")

	h.Update(instanceStartedMsg{instance: inst})

	require.NotZero(t, h.appState.GetHelpScreensSeen()&flag, "a successful start must mark the welcome seen")
}

// Sanity: the empty-state hint that replaces the removed always-on bar must not
// leak into the composed app view as bottom chrome — it lives inside the list panel.
func TestView_EmptyStateHasNoBottomBar(t *testing.T) {
	h := newCreateFormHome(t) // no instances
	h.state = stateDefault
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := h.View()
	require.Contains(t, view, "keys", "empty list surfaces the inline onboarding hint")
	require.False(t, strings.Contains(view, " │ "), "empty navigation must not render the hint bar")
}
