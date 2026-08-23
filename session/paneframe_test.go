package session

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// frameInstance returns a started instance whose tmux session runs on a fake
// executor, plus a counter of the argv verbs that executor saw. The counter is
// what proves CapturePaneFrame drops the has-session probe Preview() pays for:
// asserting on the frame text alone cannot tell one subprocess from two.
func frameInstance(t *testing.T, capture string, captureErr error) (*Instance, *[]string) {
	t.Helper()
	var seen []string
	exec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			seen = append(seen, verbOf(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			verb := verbOf(c)
			seen = append(seen, verb)
			switch verb {
			case "capture-pane":
				if captureErr != nil {
					return nil, captureErr
				}
				return []byte(capture), nil
			case "list-panes":
				return []byte("%1\n"), nil
			}
			return nil, nil
		},
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "frame", "claude", tmux.MakePtyFactory(), exec)
	return &Instance{ident: identity{title: "frame"}, status: Running, started: true, tmuxSession: ts}, &seen
}

// verbOf is the tmux subcommand of an argv, i.e. the first argument that is not
// the binary or one of tmux's own -L/-f prelude flags.
func verbOf(c *exec.Cmd) string {
	for i := 1; i < len(c.Args); i++ {
		a := c.Args[i]
		if strings.HasPrefix(a, "-") {
			i++ // skip the flag's value (-L <socket>, -f <conf>)
			continue
		}
		return a
	}
	return ""
}

func TestCapturePaneFrame_CapturesWithoutALivenessProbe(t *testing.T) {
	inst, seen := frameInstance(t, "hello from the agent\n", nil)

	text, err := inst.CapturePaneFrame()
	require.NoError(t, err)
	require.Equal(t, "hello from the agent\n", text)

	require.Contains(t, *seen, "capture-pane")
	require.NotContains(t, *seen, "has-session",
		"the capture IS the liveness signal — a probe would only cost a second subprocess")
}

func TestCapturePaneFrame_SkipsPausedAndAttachedSessions(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		inst, seen := frameInstance(t, "content", nil)
		inst.SetStatus(Paused)

		text, err := inst.CapturePaneFrame()
		require.NoError(t, err)
		require.Empty(t, text)
		require.NotContains(t, *seen, "capture-pane", "a paused session must not be captured at all")
	})

	t.Run("no tmux session", func(t *testing.T) {
		inst := &Instance{ident: identity{title: "bare"}, status: Running, started: true}
		text, err := inst.CapturePaneFrame()
		require.NoError(t, err)
		require.Empty(t, text)
	})
}

func TestPaneFrame_TracksTheLastGoodCapture(t *testing.T) {
	inst, _ := frameInstance(t, "frame one", nil)

	_, _, ok := inst.PaneFrame()
	require.False(t, ok, "a never-captured instance must report ok=false — that is what pins the setup splash")

	first := time.Now()
	inst.SetPaneFrame("frame one", first)
	text, at, ok := inst.PaneFrame()
	require.True(t, ok)
	require.Equal(t, "frame one", text)
	require.Equal(t, first, at)

	// A blank capture from a live pane is a real observation, not a miss: the
	// preview renders it blank rather than reverting to the splash.
	inst.SetPaneFrame("", first.Add(time.Second))
	text, _, ok = inst.PaneFrame()
	require.True(t, ok, "a captured-blank pane is still a successful capture")
	require.Empty(t, text)
}

func TestNotePaneFrameFailure_KeepsTheLastFrameAndItsStamp(t *testing.T) {
	inst, _ := frameInstance(t, "", errors.New("tmux server gone"))

	stamp := time.Now()
	inst.SetPaneFrame("last good frame", stamp)
	inst.NotePaneFrameFailure(errors.New("tmux server gone"), stamp.Add(3*time.Second))

	text, at, ok := inst.PaneFrame()
	require.True(t, ok)
	require.Equal(t, "last good frame", text, "a failed capture must never blank the pane")
	require.Equal(t, stamp, at,
		"the stamp must stay put so the frame's age keeps growing — that age IS the staleness marker")
}

// The cache is cleared through the Paused transition rather than a standalone drop —
// see TestPaneFrame_DroppedOnTheFlipToPaused, which asserts both the clearing and the
// placement that makes it stick.

// TestPaneFrame_DroppedOnTheFlipToPaused pins WHERE the cached frame is released.
//
// It used to be released at the top of pause(), before the commit and the worktree
// removal. For that whole window the session is not yet Paused, so resolveFrameTarget
// still targets it and the capture chain keeps applying frames — refilling the cache
// the drop had just cleared, and leaving a resumed pane free to flash its pre-pause
// picture. The status edge is the first moment after which no further capture is
// armed, so it is the only point where the drop sticks.
func TestPaneFrame_DroppedOnTheFlipToPaused(t *testing.T) {
	inst, _ := frameInstance(t, "pane contents", nil)

	inst.SetPaneFrame("pane contents", time.Now())
	_, _, ok := inst.PaneFrame()
	require.True(t, ok, "control: the cache holds a frame to begin with")

	// A frame landing while the pause is still doing its I/O — the session is not
	// Paused yet, so this is exactly what the capture chain is still allowed to do.
	inst.SetStatus(Running)
	inst.SetPaneFrame("mid-pause frame", time.Now())

	inst.SetStatus(Paused)

	text, at, ok := inst.PaneFrame()
	require.False(t, ok, "parking a session must leave no frame to paint")
	require.Empty(t, text)
	require.True(t, at.IsZero(), "and no stamp for the staleness marker to age")
}
