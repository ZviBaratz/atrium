package app

import (
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// TestWelcomeShowsOnce verifies the first-launch welcome appears once and then
// never again within a process (the seen-bit handles persistence across runs).
func TestWelcomeShowsOnce(t *testing.T) {
	stubDetect(t, []config.Profile{{Name: "claude", Program: "claude"}})
	h := newCreateFormHome(t)

	cmd := h.maybeShowWelcome()
	require.Equal(t, stateWelcome, h.state, "welcome should appear on first check")
	require.NotNil(t, h.welcomeOverlay)
	require.True(t, h.welcomeChecked)
	require.NotNil(t, cmd, "detection cmd should be returned")

	// Simulate dismissing it, then check again: it must not reappear this process.
	h.state = stateDefault
	h.welcomeOverlay = nil
	cmd = h.maybeShowWelcome()
	require.Equal(t, stateDefault, h.state, "welcome must not reappear")
	require.Nil(t, h.welcomeOverlay)
	require.Nil(t, cmd)
}

// TestViewFitsTerminalBounds_ManyInstances guards the no-overflow invariant when
// the session list is longer than the screen (the scrolling viewport must keep
// the composed view within the terminal).
func TestViewFitsTerminalBounds_ManyInstances(t *testing.T) {
	sizes := [][2]int{{120, 30}, {80, 24}, {200, 50}}
	statuses := []session.Status{session.Running, session.Ready, session.NeedsInput, session.Paused}

	for _, dim := range sizes {
		w, h := dim[0], dim[1]
		home := newCreateFormHome(t)
		for i := 0; i < 40; i++ { // far more than fits
			inst, err := session.NewInstance(session.InstanceOptions{
				Title: fmt.Sprintf("sess-%02d", i), Path: t.TempDir(), Program: "echo",
			})
			require.NoError(t, err)
			inst.SetStatus(statuses[i%len(statuses)])
			home.list.AddInstance(inst)()
		}
		home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})

		lines := strings.Split(home.View().Content, "\n")
		require.LessOrEqualf(t, len(lines), h, "size=%dx%d: too many lines", w, h)
		for i, l := range lines {
			require.Equalf(t, w, ansi.PrintableRuneWidth(l), "size=%dx%d: line %d wrong width", w, h, i)
		}
	}
}
