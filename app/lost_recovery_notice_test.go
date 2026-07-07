package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSurfaceLostRecoveries pins the #270 visibility contract: a lost-session
// recovery is never silent, and the message shape matches its severity.
func TestSurfaceLostRecoveries(t *testing.T) {
	t.Run("ordinary terminal death → batched info toast", func(t *testing.T) {
		h := newCreateFormHome(t)
		cmd := h.surfaceLostRecoveries([]lostRecovery{{title: "alpha"}})
		require.NotNil(t, cmd)
		require.Equal(t, stateDefault, h.state, "an ordinary park must not pop a modal")
		require.Contains(t, h.menu.NoticeText(), "alpha")
		require.Contains(t, h.menu.NoticeText(), "resume")
	})

	t.Run("several deaths batch into one count", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.surfaceLostRecoveries([]lostRecovery{{title: "a"}, {title: "b"}})
		require.Contains(t, h.menu.NoticeText(), "2 sessions")
	})

	t.Run("crash at launch → persistent modal naming the command", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.surfaceLostRecoveries([]lostRecovery{{title: "boom", launchCmd: "claude --profile typo"}})
		require.Equal(t, stateInfo, h.state, "a launch crash must pop the persistent modal")
		require.Contains(t, h.textOverlay.Render(), "claude --profile typo",
			"the modal must name the launch command so a typo'd profile is diagnosable")
	})

	t.Run("crash at launch behind an overlay buffers instead of clobbering it", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.state = statePrompt // an overlay owns the screen

		cmd := h.surfaceLostRecoveries([]lostRecovery{{title: "boom", launchCmd: "claude --profile typo"}})

		require.Nil(t, cmd, "a buffered crash issues no command this tick")
		require.Equal(t, statePrompt, h.state, "a background recovery must never clobber an open overlay")
		require.NotNil(t, h.pendingLaunchCrash, "the crash must be buffered for later")

		// Once the overlay closes, the preview tick's flush pops the buffered modal.
		// (showInfo drives the modal via state, so the flush returns no command.)
		h.state = stateDefault
		h.flushPendingLaunchCrash()
		require.Equal(t, stateInfo, h.state, "the buffered crash pops once the screen is free")
		require.Contains(t, h.textOverlay.Render(), "claude --profile typo")
		require.Nil(t, h.pendingLaunchCrash, "flushing clears the buffer")
	})

	t.Run("a failed recovery surfaces an error naming the session", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.surfaceLostRecoveries([]lostRecovery{{title: "bad", err: fmt.Errorf("disk full")}})
		// The message is long, so from stateDefault handleError escalates it to the
		// persistent modal; either way the session name and cause must be visible.
		surfaced := h.menu.NoticeText() + h.errBox.String()
		if h.state == stateInfo {
			surfaced += h.textOverlay.Render()
		}
		require.Contains(t, surfaced, "bad")
		require.Contains(t, surfaced, "disk full")
	})
}
