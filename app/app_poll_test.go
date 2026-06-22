package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/require"
)

// TestPollTargets covers the metadata-poll throttle decision: a full sweep polls every
// active session, while a light tick polls only the selected session and any session
// with a queued prompt (so prompt delivery stays responsive). Throttling the rest is
// what keeps the shared tmux server from being hammered every 500ms.
func TestPollTargets(t *testing.T) {
	newInst := func(prompt string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.Prompt = prompt
		return inst
	}

	plain := newInst("")
	selected := newInst("")
	queued := newInst("do the thing")
	active := []*session.Instance{plain, selected, queued}

	t.Run("full sweep returns every active session", func(t *testing.T) {
		require.Equal(t, active, pollTargets(active, selected, true))
	})

	t.Run("light tick polls only selected and queued-prompt sessions", func(t *testing.T) {
		got := pollTargets(active, selected, false)
		require.ElementsMatch(t, []*session.Instance{selected, queued}, got)
		require.NotContains(t, got, plain, "a stable non-selected session is skipped on light ticks")
	})

	t.Run("light tick with no selection still polls queued prompts", func(t *testing.T) {
		require.Equal(t, []*session.Instance{queued}, pollTargets(active, nil, false))
	})
}
