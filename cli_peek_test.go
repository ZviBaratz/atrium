package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTmux answers list-panes with one pane and capture-pane with content,
// recording every argv so a test can assert what tmux was actually asked.
type fakeTmux struct {
	content string
	calls   [][]string
}

func (f *fakeTmux) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error { return nil },
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			f.calls = append(f.calls, c.Args)
			for _, a := range c.Args {
				if a == "list-panes" {
					return []byte("%1\n"), nil
				}
			}
			return []byte(f.content), nil
		},
	}
}

func (f *fakeTmux) argvFor(sub string) []string {
	for _, args := range f.calls {
		for _, a := range args {
			if a == sub {
				return args
			}
		}
	}
	return nil
}

func peekTarget(t *testing.T, d session.InstanceData) *fakeTmux {
	t.Helper()
	sandboxDataDir(t)
	seedInstances(t, d)
	return &fakeTmux{content: "pane contents\n"}
}

// TestPeekWritesPaneContents is the happy path.
func TestPeekWritesPaneContents(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	f := peekTarget(t, d)

	var buf bytes.Buffer
	require.NoError(t, runPeek(context.Background(), &buf, f.exec(), "fix-auth", "", 0, false))
	assert.Equal(t, "pane contents\n", buf.String())
}

// TestPeekAddressesStoredTmuxName: the stored name is the handle, and it is
// repo-qualified so identical titles in two repos stay distinct on the shared
// tmux socket.
func TestPeekAddressesStoredTmuxName(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	f := peekTarget(t, d)

	var buf bytes.Buffer
	require.NoError(t, runPeek(context.Background(), &buf, f.exec(), "fix-auth", "", 0, false))
	assert.Contains(t, f.argvFor("list-panes"), "atrium_web_fix-auth")
}

// TestPeekFallsBackToDerivedNameForLegacyState covers instances stored before
// tmux_name existed: their live tmux session is under the older title-derived
// name, so an empty stored name must not become an empty tmux target.
func TestPeekFallsBackToDerivedNameForLegacyState(t *testing.T) {
	d := inst("legacy title", "/repo/web")
	d.TmuxName = ""
	f := peekTarget(t, d)

	var buf bytes.Buffer
	require.NoError(t, runPeek(context.Background(), &buf, f.exec(), "legacy title", "", 0, false))

	want := tmux.Prefix() + tmux.SanitizeNameSegment("legacy title")
	assert.Contains(t, f.argvFor("list-panes"), want)
}

// TestPeekRefusesPausedSession: pausing kills the tmux session, so peeking one
// would otherwise fail deep inside tmux with a far less useful message. No tmux
// command should run at all.
func TestPeekRefusesPausedSession(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.Status = session.Paused
	f := peekTarget(t, d)

	var buf bytes.Buffer
	err := runPeek(context.Background(), &buf, f.exec(), "fix-auth", "", 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paused")
	assert.Empty(t, f.calls, "a paused session must be rejected before tmux is touched")
	assert.Empty(t, buf.String())
}

// TestPeekUnknownSession reports the selector rather than an empty capture.
func TestPeekUnknownSession(t *testing.T) {
	f := peekTarget(t, inst("fix-auth", "/repo/web"))

	var buf bytes.Buffer
	err := runPeek(context.Background(), &buf, f.exec(), "nope", "", 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope"`)
	assert.Empty(t, f.calls)
}

// TestPeekAmbiguousSession surfaces the (Title, Path) ambiguity before capturing
// the wrong session's pane.
func TestPeekAmbiguousSession(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("api", "/repo/web"), inst("api", "/repo/svc"))
	f := &fakeTmux{content: "x\n"}

	var buf bytes.Buffer
	err := runPeek(context.Background(), &buf, f.exec(), "api", "", 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Empty(t, f.calls)
}

// TestPeekForwardsLinesAndColor checks the two flags reach tmux.
func TestPeekForwardsLinesAndColor(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	f := peekTarget(t, d)

	var buf bytes.Buffer
	require.NoError(t, runPeek(context.Background(), &buf, f.exec(), "fix-auth", "", 120, true))

	argv := f.argvFor("capture-pane")
	require.NotNil(t, argv)
	assert.Contains(t, argv, "-e", "--color must reach tmux as -e")
	assert.Contains(t, argv, "-120", "--lines must reach tmux as a negative start-line")
}

// TestPeekDefaultsToPlainVisiblePane: with no flags there is no -e and no -S, so
// scripts get clean text and only what is on screen.
func TestPeekDefaultsToPlainVisiblePane(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	f := peekTarget(t, d)

	var buf bytes.Buffer
	require.NoError(t, runPeek(context.Background(), &buf, f.exec(), "fix-auth", "", 0, false))

	argv := f.argvFor("capture-pane")
	require.NotNil(t, argv)
	assert.NotContains(t, argv, "-e")
	assert.NotContains(t, argv, "-S")
}
