package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// captureTargets runs CapturePaneContent against a fake executor whose
// list-panes answer is fixed, and records the -t value of every capture-pane
// and the number of list-panes resolutions.
type paneFake struct {
	listPanesOut string
	listPanesErr error

	resolutions    int
	captureTargets []string
}

func (f *paneFake) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil }, // has-session: alive
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			args := strings.Join(cmd.Args, " ")
			switch {
			case strings.Contains(args, "list-panes"):
				f.resolutions++
				return []byte(f.listPanesOut), f.listPanesErr
			case strings.Contains(args, "capture-pane"):
				for i, a := range cmd.Args {
					if a == "-t" && i+1 < len(cmd.Args) {
						f.captureTargets = append(f.captureTargets, cmd.Args[i+1])
					}
				}
				return []byte("pane content"), nil
			default:
				return nil, fmt.Errorf("unexpected Output command: %s", args)
			}
		},
	}
}

// Capture must target the resolved pane id, not the session name: a
// session-name target resolves to the *active* pane, so a user split inside
// an attached session would redirect status detection (and the autoyes
// daemon's Enter taps) to the wrong pane.
func TestCaptureTargetsAgentPaneID(t *testing.T) {
	fake := &paneFake{listPanesOut: "%3\n"}
	s := newSession(context.Background(), "pane-id", "claude", NewMockPtyFactory(t), fake.exec())

	_, err := s.CapturePaneContent()
	require.NoError(t, err)
	_, err = s.CapturePaneContentWithOptions("-", "-")
	require.NoError(t, err)

	require.Equal(t, []string{"%3", "%3"}, fake.captureTargets)
	require.Equal(t, 1, fake.resolutions, "pane id must be resolved once and cached")
}

// The agent pane is the one new-session created, i.e. the numerically
// smallest pane id in the session — regardless of list order or how later
// splits and windows shifted pane indexes.
func TestPaneResolutionPicksSmallestID(t *testing.T) {
	fake := &paneFake{listPanesOut: "%12\n%4\n%9\n"}
	s := newSession(context.Background(), "pane-min", "claude", NewMockPtyFactory(t), fake.exec())

	_, err := s.CapturePaneContent()
	require.NoError(t, err)
	require.Equal(t, []string{"%4"}, fake.captureTargets)
}

// A failed resolution must degrade to the historical behavior (session-name
// target), not break capture — and must not retry on every poll tick.
func TestPaneResolutionFailureFallsBackToSessionName(t *testing.T) {
	fake := &paneFake{listPanesErr: fmt.Errorf("no server running")}
	s := newSession(context.Background(), "pane-fallback", "claude", NewMockPtyFactory(t), fake.exec())

	for range 2 {
		content, err := s.CapturePaneContent()
		require.NoError(t, err)
		require.Equal(t, "pane content", content)
	}
	require.Equal(t, []string{Prefix() + "pane-fallback", Prefix() + "pane-fallback"}, fake.captureTargets)
	require.Equal(t, 1, fake.resolutions, "a failed resolution must not retry per capture")
}

// Garbage in the list-panes output (or none at all) is a resolution failure,
// not a bogus target.
func TestPaneResolutionRejectsMalformedOutput(t *testing.T) {
	fake := &paneFake{listPanesOut: "not-a-pane\n%abc\n\n"}
	s := newSession(context.Background(), "pane-garbage", "claude", NewMockPtyFactory(t), fake.exec())

	_, err := s.CapturePaneContent()
	require.NoError(t, err)
	require.Equal(t, []string{Prefix() + "pane-garbage"}, fake.captureTargets)
}

// Close kills the tmux session, so the cached pane id dies with it; the next
// capture (a resumed session) must re-resolve rather than reuse a stale id.
func TestPaneIDCacheResetOnClose(t *testing.T) {
	fake := &paneFake{listPanesOut: "%3\n"}
	s := newSession(context.Background(), "pane-reset", "claude", NewMockPtyFactory(t), fake.exec())

	_, err := s.CapturePaneContent()
	require.NoError(t, err)
	require.NoError(t, s.Close())

	fake.listPanesOut = "%8\n"
	_, err = s.CapturePaneContent()
	require.NoError(t, err)

	require.Equal(t, []string{"%3", "%8"}, fake.captureTargets)
	require.Equal(t, 2, fake.resolutions)
}
