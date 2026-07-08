package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// menuVisible is the single source of truth for whether the hint bar claims a
// row. With the default config the bar is always on during plain navigation;
// inline interactions (new/filter, background name generation) always get it;
// self-documenting overlays (prompt/rename/confirm/help) never do. Turning
// hint_bar off restores the contextual-only behavior for plain navigation.
func TestMenuVisible_ByState(t *testing.T) {
	h := newCreateFormHome(t)

	cases := []struct {
		name       string
		state      state
		generating bool
		inFlight   bool
		want       bool
	}{
		{"default navigation shows the hint bar", stateDefault, false, false, true},
		{"default + background name gen shows progress", stateDefault, true, false, true},
		{"default + action in flight shows progress", stateDefault, false, true, true},
		{"inline filter", stateFilter, false, false, true},
		{"prompt overlay self-documents", statePrompt, false, false, false},
		{"rename overlay self-documents", stateRename, false, false, false},
		{"confirm overlay self-documents", stateConfirm, false, false, false},
		{"help overlay self-documents", stateHelp, false, false, false},
	}
	for _, c := range cases {
		h.state = c.state
		h.generatingName = c.generating
		h.actionInFlight = c.inFlight
		require.Equalf(t, c.want, h.menuVisible(), "%s", c.name)
	}
	h.actionInFlight = false

	// hint_bar off: plain navigation goes chrome-free again, but a background
	// name generation still claims the row, and inline interactions keep theirs.
	off := false
	h.appConfig.HintBar = &off
	h.state = stateDefault
	h.generatingName = false
	require.False(t, h.menuVisible(), "hint_bar=false restores clean navigation")
	h.generatingName = true
	require.True(t, h.menuVisible(), "name-gen progress still claims its row with the bar off")
	h.generatingName = false
	h.actionInFlight = true
	require.True(t, h.menuVisible(), "an in-flight action's progress row claims its row with the bar off")
	h.actionInFlight = false
	h.state = stateFilter
	require.True(t, h.menuVisible(), "the filter cue is independent of hint_bar")
}

// The composed View must carry the hint bar exactly when menuVisible says so.
// "kill" appears only in the bar's default hint line, so it keys the
// presence/absence assertions.
func TestView_HintBarContextual(t *testing.T) {
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "a", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	h.list.AddInstance(inst)()
	h.list.SelectInstance(inst)

	// Plain navigation with a selected session: the always-on bar is present.
	h.state = stateDefault
	h.menu.SetState(ui.StateDefault)
	h.menu.SetInstance(inst)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.Contains(t, h.View(), "kill", "default navigation renders the hint bar")

	// hint_bar off: plain navigation goes chrome-free.
	off := false
	h.appConfig.HintBar = &off
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.NotContains(t, h.View(), "kill", "hint_bar=false must not render the bar")
	on := true
	h.appConfig.HintBar = &on

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

// First-run guidance must come from exactly one surface: the bottom bar when it
// is on (the list's centered hint is suppressed), the centered in-list hint when
// the bar is off. "keys" appears only in the in-list hint; "quit" only in the
// bar's empty-state line. Both homes mirror newHome's SetShowEmptyHint wiring.
func TestView_EmptyStateGuidanceSingleSurface(t *testing.T) {
	h := newCreateFormHome(t) // no instances; default config (bar on)
	h.list.SetShowEmptyHint(!h.appConfig.GetHintBar())
	h.state = stateDefault
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := h.View()
	require.Contains(t, view, "quit", "the bar carries the empty-state keys")
	require.False(t, strings.Contains(view, "keys"), "the in-list hint is suppressed while the bar is on")

	off := false
	h2 := newCreateFormHome(t)
	h2.appConfig.HintBar = &off
	h2.list.SetShowEmptyHint(!h2.appConfig.GetHintBar())
	h2.state = stateDefault
	h2.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	view2 := h2.View()
	require.Contains(t, view2, "keys", "with the bar off, the in-list hint is the onboarding surface")
	require.False(t, strings.Contains(view2, "quit"), "no bottom bar with hint_bar=false")
}
