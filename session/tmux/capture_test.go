package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRecorder fakes tmux: it answers list-panes with the given pane ids and
// capture-pane with the given content, recording every argv it was handed.
type captureRecorder struct {
	panes   string
	content string
	listErr error
	capErr  error
	calls   [][]string
}

func (r *captureRecorder) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error { return nil },
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			r.calls = append(r.calls, c.Args)
			switch {
			case argvHas(c.Args, "list-panes"):
				return []byte(r.panes), r.listErr
			case argvHas(c.Args, "capture-pane"):
				return []byte(r.content), r.capErr
			}
			return nil, nil
		},
	}
}

func (r *captureRecorder) captureArgs(t *testing.T) []string {
	t.Helper()
	for _, args := range r.calls {
		if argvHas(args, "capture-pane") {
			return args
		}
	}
	t.Fatal("capture-pane was never invoked")
	return nil
}

func argvHas(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestCaptureTargetsPaneIDNotSessionName is the guard on this file's whole
// reason for existing. tmux resolves a session-name target to the session's
// *active* pane, so a split the user opened while attached would silently
// redirect the capture. Targeting the immutable pane id is what prevents that.
func TestCaptureTargetsPaneIDNotSessionName(t *testing.T) {
	r := &captureRecorder{panes: "%7\n%3\n%9\n", content: "hello\n"}
	got, err := CapturePaneForSession(context.Background(), r.exec(), "atrium_web_fix", CaptureOpts{})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", got)

	args := r.captureArgs(t)
	assert.Equal(t, "%3", argAfter(args, "-t"), "must target the smallest pane id, not the session name")
	assert.NotContains(t, args, "atrium_web_fix")
}

// TestCaptureFallsBackToSessionName keeps a capture working when list-panes
// cannot answer, matching Session.paneTarget's own graceful fallback.
func TestCaptureFallsBackToSessionName(t *testing.T) {
	r := &captureRecorder{listErr: errors.New("no server"), content: "hi\n"}
	_, err := CapturePaneForSession(context.Background(), r.exec(), "atrium_web_fix", CaptureOpts{})
	require.NoError(t, err)
	assert.Equal(t, "atrium_web_fix", argAfter(r.captureArgs(t), "-t"))
}

// TestCaptureFallsBackWhenPaneListIsUnparseable covers the other failure shape:
// list-panes succeeded but said nothing we can use.
func TestCaptureFallsBackWhenPaneListIsUnparseable(t *testing.T) {
	r := &captureRecorder{panes: "garbage\n", content: "hi\n"}
	_, err := CapturePaneForSession(context.Background(), r.exec(), "atrium_web_fix", CaptureOpts{})
	require.NoError(t, err)
	assert.Equal(t, "atrium_web_fix", argAfter(r.captureArgs(t), "-t"))
}

// TestCaptureUsesRuntimeSocket pins the identity rule from CLAUDE.md: the socket
// must be derived from config.RuntimeName(), never hardcoded, so a legacy install
// keeps talking to the tmux server its live sessions are on.
//
// The legacy HOME is the whole point. Under the package's normal sandbox
// RuntimeName() is always "atrium", so comparing against it would pass just as
// happily against a hardcoded literal — the assertion only bites where the two
// differ.
func TestCaptureUsesRuntimeSocket(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))
		t.Setenv("HOME", home)
		require.Equal(t, "atrium", config.RuntimeName())

		r := &captureRecorder{panes: "%1\n", content: "x\n"}
		_, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
		require.NoError(t, err)
		assert.Equal(t, "atrium", argAfter(r.captureArgs(t), "-L"))
	})

	t.Run("legacy install", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude-squad"), 0o755))
		t.Setenv("HOME", home)
		require.Equal(t, "claudesquad", config.RuntimeName(), "fixture must actually be a legacy dir")

		r := &captureRecorder{panes: "%1\n", content: "x\n"}
		_, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
		require.NoError(t, err)
		assert.Equal(t, "claudesquad", argAfter(r.captureArgs(t), "-L"),
			"a hardcoded socket name would send a legacy install to the wrong server")
	})
}

// TestCaptureAlwaysJoinsWrappedLines: -J reassembles a wrapped line into one,
// which is what makes the output greppable.
func TestCaptureAlwaysJoinsWrappedLines(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", content: "x\n"}
	_, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.Contains(t, r.captureArgs(t), "-J")
}

// TestCaptureColorIsOptIn: escape sequences are noise for the scripts and agents
// this exists for, so -e appears only when asked for.
func TestCaptureColorIsOptIn(t *testing.T) {
	plain := &captureRecorder{panes: "%1\n", content: "x\n"}
	_, err := CapturePaneForSession(context.Background(), plain.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.NotContains(t, plain.captureArgs(t), "-e", "plain output must not request escapes")

	colored := &captureRecorder{panes: "%1\n", content: "x\n"}
	_, err = CapturePaneForSession(context.Background(), colored.exec(), "s", CaptureOpts{Color: true})
	require.NoError(t, err)
	assert.Contains(t, colored.captureArgs(t), "-e")
}

// TestCaptureScrollbackFlag: a line count reaches into the history via a
// negative start-line, and is omitted entirely when the caller wants only the
// visible pane.
func TestCaptureScrollbackFlag(t *testing.T) {
	none := &captureRecorder{panes: "%1\n", content: "x\n"}
	_, err := CapturePaneForSession(context.Background(), none.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.NotContains(t, none.captureArgs(t), "-S")

	deep := &captureRecorder{panes: "%1\n", content: "x\n"}
	_, err = CapturePaneForSession(context.Background(), deep.exec(), "s", CaptureOpts{Lines: 200})
	require.NoError(t, err)
	assert.Equal(t, "-200", argAfter(deep.captureArgs(t), "-S"))
}

// TestCaptureReturnsExactlyLinesRequested is the other half of --lines. A
// negative start-line yields the requested history *plus* the visible pane, so
// without trimming "give me the last 5 lines" would return however tall the
// pane happens to be, plus five.
func TestCaptureReturnsExactlyLinesRequested(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	r := &captureRecorder{panes: "%1\n", content: b.String()}

	got, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{Lines: 5})
	require.NoError(t, err)
	assert.Equal(t, "line 36\nline 37\nline 38\nline 39\nline 40\n", got)
}

// TestCaptureKeepsShortOutputWhole: asking for more lines than exist is not an
// error and must not pad.
func TestCaptureKeepsShortOutputWhole(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", content: "only\ntwo\n"}
	got, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{Lines: 50})
	require.NoError(t, err)
	assert.Equal(t, "only\ntwo\n", got)
}

// TestCaptureTrimsPanePadding: tmux pads a partly-filled pane out to its full
// height with blank rows, which are an artifact of geometry rather than
// anything the agent printed.
func TestCaptureTrimsPanePadding(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", content: "real output\n\n   \n\n\n"}
	got, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.Equal(t, "real output\n", got)
}

// TestCaptureKeepsInteriorBlankLines: only trailing padding goes; a blank line
// between two outputs is content.
func TestCaptureKeepsInteriorBlankLines(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", content: "first\n\nsecond\n\n\n"}
	got, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.Equal(t, "first\n\nsecond\n", got)
}

// TestCaptureEmptyPane returns nothing rather than a lone newline.
func TestCaptureEmptyPane(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", content: "\n\n\n"}
	got, err := CapturePaneForSession(context.Background(), r.exec(), "s", CaptureOpts{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestCaptureErrorNamesSession so a failure says which session could not be read.
func TestCaptureErrorNamesSession(t *testing.T) {
	r := &captureRecorder{panes: "%1\n", capErr: errors.New("session not found")}
	_, err := CapturePaneForSession(context.Background(), r.exec(), "atrium_web_fix", CaptureOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "atrium_web_fix")
}

// TestCaptureRejectsEmptySessionName: an instance stored before tmux_name
// existed would otherwise send tmux a bare "-t".
func TestCaptureRejectsEmptySessionName(t *testing.T) {
	r := &captureRecorder{}
	_, err := CapturePaneForSession(context.Background(), r.exec(), "", CaptureOpts{})
	require.Error(t, err)
	assert.Empty(t, r.calls, "no tmux command should run without a target")
}

// TestSmallestPaneID documents the id-selection rule directly: ids are assigned
// in creation order, so the smallest belongs to the pane new-session made for
// the agent — a later split always has a larger one.
func TestSmallestPaneID(t *testing.T) {
	got, err := smallestPaneID([]byte("%12\n%4\n%7\n"))
	require.NoError(t, err)
	assert.Equal(t, "%4", got)

	// Lexicographic order would pick %12 here; numeric order is what matters.
	got, err = smallestPaneID([]byte("%12\n%9\n"))
	require.NoError(t, err)
	assert.Equal(t, "%9", got)

	_, err = smallestPaneID([]byte("nothing useful\n"))
	require.Error(t, err)
}

// TestPaneIDsForSessionListsEveryPane: the caller recognising its own pane may be a shell
// the user split off rather than the agent, so every pane in the session counts — and the
// blank line tmux's trailing newline leaves must not become an empty id that matches
// nothing (or, worse, an empty TMUX_PANE).
func TestPaneIDsForSessionListsEveryPane(t *testing.T) {
	r := &captureRecorder{panes: "%1\n%4\n%7\n"}

	ids, err := PaneIDsForSession(context.Background(), r.exec(), "atrium_web_fix-auth")

	require.NoError(t, err)
	assert.Equal(t, []string{"%1", "%4", "%7"}, ids)
	assert.Contains(t, r.calls[0], "atrium_web_fix-auth", "listed for the session it was asked about")
}

// TestPaneIDsForSessionRefusesAnEmptyName mirrors CapturePaneForSession's own guard: an
// empty target is a name tmux would resolve against the current session, which is not the
// session the caller asked about.
func TestPaneIDsForSessionRefusesAnEmptyName(t *testing.T) {
	r := &captureRecorder{panes: "%1\n"}

	_, err := PaneIDsForSession(context.Background(), r.exec(), "")

	require.Error(t, err)
	assert.Empty(t, r.calls, "and it must not ask tmux anything")
}

// TestSessionConfirmedAbsentOnlyTrustsTheServersAnswer is the whole point of the helper:
// it draws probeLiveness's distinction for a headless caller. Only tmux saying the session
// is not there counts. A timeout, a cancelled context or a diagnostic this package has not
// been taught is not an answer, and reading one as "gone" would clear a kill on a machine
// whose server was merely busy.
func TestSessionConfirmedAbsentOnlyTrustsTheServersAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  error
		want bool
	}{
		{"the session is there", nil, false},
		{"the server says it is not there", errors.New("exit status 1: can't find session: x"), true},
		{"no server is listening at all", errors.New("exit status 1: no server running on /tmp/x"), true},
		{"the probe was killed", errors.New("signal: killed"), false},
		{"the socket could not be opened", errors.New("error connecting to /tmp/x (No such file or directory)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := cmd_test.MockCmdExec{
				RunFunc:    func(*exec.Cmd) error { return tc.run },
				OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
			}

			assert.Equal(t, tc.want, SessionConfirmedAbsent(context.Background(), e, "atrium_web_fix-auth"))
		})
	}
}

// TestSessionConfirmedAbsentIgnoresACancelledContext: a cancelled context kills the probe
// process, which surfaces as an ExitError like any other. Read as an answer it would say
// every session is gone during shutdown.
func TestSessionConfirmedAbsentIgnoresACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return errors.New("exit status 1: can't find session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}

	assert.False(t, SessionConfirmedAbsent(ctx, e, "atrium_web_fix-auth"),
		"a probe the context killed answered nothing, whatever it printed")
}

// TestSessionConfirmedAbsentRefusesAnEmptyName: an empty name is not a session that is
// absent, it is no question at all — and `has-session -t=` would answer about whatever
// tmux considers current.
func TestSessionConfirmedAbsentRefusesAnEmptyName(t *testing.T) {
	asked := false
	e := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { asked = true; return errors.New("can't find session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}

	assert.False(t, SessionConfirmedAbsent(context.Background(), e, ""))
	assert.False(t, asked, "and tmux is not asked")
}
