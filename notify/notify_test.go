package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// fakeExec records the commands Run receives and optionally signals each call so a
// goroutine-dispatched Emit can be awaited deterministically.
type fakeExec struct {
	mu   sync.Mutex
	cmds []*exec.Cmd
	err  error
	done chan struct{}
}

func (f *fakeExec) Run(c *exec.Cmd) error {
	f.mu.Lock()
	f.cmds = append(f.cmds, c)
	f.mu.Unlock()
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.err
}

func (f *fakeExec) Output(*exec.Cmd) ([]byte, error) { return nil, nil }

func (f *fakeExec) calls() []*exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*exec.Cmd(nil), f.cmds...)
}

func TestEmitBellWritesBEL(t *testing.T) {
	var buf bytes.Buffer
	n := New(&buf, &fakeExec{})
	n.Emit(config.NotificationsBell, "", "sess", EventFinished)
	require.Equal(t, "\a", buf.String())
}

func TestEmitOSCWritesOSC9(t *testing.T) {
	var buf bytes.Buffer
	n := New(&buf, &fakeExec{})
	n.Emit(config.NotificationsOSC, "", "myagent", EventFinished)
	// OSC 9 is written to the TUI's own stdout (like the bell) with the session named
	// in the body, so it reaches the terminal over SSH and satisfies "names the session".
	require.Equal(t, ansi.Notify("myagent finished"), buf.String())
	require.True(t, strings.HasPrefix(buf.String(), "\x1b]9;"), "is an OSC 9 sequence")
	require.Contains(t, buf.String(), "myagent finished")
}

func TestEmitOSCFoldsControlCharsInBody(t *testing.T) {
	var buf bytes.Buffer
	n := New(&buf, &fakeExec{})
	// A display name with an embedded BEL would otherwise truncate the OSC sequence.
	n.Emit(config.NotificationsOSC, "", "my\aagent", EventNeedsInput)
	require.Equal(t, ansi.Notify("my agent needs input"), buf.String())
}

func TestEmitOffAndUnknownDoNothing(t *testing.T) {
	var buf bytes.Buffer
	fe := &fakeExec{}
	n := New(&buf, fe)
	n.Emit(config.NotificationsOff, "", "sess", EventFinished)
	n.Emit("bogus", "", "sess", EventNeedsInput)
	require.Empty(t, buf.String())
	require.Empty(t, fe.calls())
}

func TestDesktopCommandUserCommandCarriesEnv(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	c := n.desktopCommand(context.Background(), "notify-send \"$ATRIUM_SESSION\"", "my sess", EventNeedsInput)
	require.Equal(t, []string{"sh", "-c", "notify-send \"$ATRIUM_SESSION\""}, c.Args)
	require.Contains(t, c.Env, "ATRIUM_SESSION=my sess")
	require.Contains(t, c.Env, "ATRIUM_STATUS=NeedsInput")
	require.Contains(t, c.Env, "ATRIUM_EVENT=needs_input")
}

func TestDefaultCommandLinux(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	n.lookPath = func(name string) (string, error) {
		if name == "notify-send" {
			return "/usr/bin/notify-send", nil
		}
		return "", errors.New("not found")
	}
	c := n.defaultCommand(context.Background(), "linux", "sess", EventFinished)
	require.NotNil(t, c)
	require.Equal(t, []string{"/usr/bin/notify-send", "Atrium", "sess finished"}, c.Args)
}

func TestDefaultCommandDarwinPrefersTerminalNotifier(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	n.lookPath = func(name string) (string, error) {
		if name == "terminal-notifier" {
			return "/opt/tn", nil
		}
		return "", errors.New("not found")
	}
	c := n.defaultCommand(context.Background(), "darwin", "sess", EventNeedsInput)
	require.NotNil(t, c)
	require.Equal(t, []string{"/opt/tn", "-title", "Atrium", "-message", "sess needs input"}, c.Args)
}

func TestDefaultCommandDarwinFallsBackToOsascript(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	n.lookPath = func(name string) (string, error) {
		if name == "osascript" {
			return "/usr/bin/osascript", nil
		}
		return "", errors.New("not found")
	}
	c := n.defaultCommand(context.Background(), "darwin", "se\"ss", EventFinished)
	require.NotNil(t, c)
	require.Equal(t, "/usr/bin/osascript", c.Args[0])
	require.Equal(t, "-e", c.Args[1])
	// Body is AppleScript-quoted with the embedded quote escaped.
	require.Equal(t, `display notification "se\"ss finished" with title "Atrium"`, c.Args[2])
}

func TestDefaultCommandNoNotifierReturnsNil(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	n.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	require.Nil(t, n.defaultCommand(context.Background(), "linux", "sess", EventFinished))
}

func TestEmitDesktopRunsUserCommand(t *testing.T) {
	fe := &fakeExec{done: make(chan struct{}, 1)}
	n := New(&bytes.Buffer{}, fe)
	n.Emit(config.NotificationsDesktop, "true", "sess", EventFinished)
	select {
	case <-fe.done:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop command was not run")
	}
	calls := fe.calls()
	require.Len(t, calls, 1)
	require.Equal(t, []string{"sh", "-c", "true"}, calls[0].Args)
}

func TestEmitDesktopErrorIsNonFatal(t *testing.T) {
	fe := &fakeExec{err: errors.New("boom"), done: make(chan struct{}, 1)}
	n := New(&bytes.Buffer{}, fe)
	n.Emit(config.NotificationsDesktop, "false", "sess", EventNeedsInput)
	select {
	case <-fe.done:
	case <-time.After(2 * time.Second):
		t.Fatal("desktop command was not run")
	}
	// No panic, no bell written; the error is logged, not surfaced.
}

// TestEmitDesktopRunsRealCommandWithEnv exercises the whole desktop path end to end
// through the real cmd.Executor: Emit → goroutine → `sh -c` → env propagation → a
// process that writes what it received. This is the only test that proves the env
// actually reaches the spawned command (the fake-executor tests only inspect the
// built *exec.Cmd).
func TestEmitDesktopRunsRealCommandWithEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	out := filepath.Join(t.TempDir(), "out")
	n := New(io.Discard, cmd.MakeExecutor())
	script := "printf '%s|%s|%s' \"$ATRIUM_SESSION\" \"$ATRIUM_STATUS\" \"$ATRIUM_EVENT\" > " + out
	n.Emit(config.NotificationsDesktop, script, "sess one", EventNeedsInput)

	var data []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(out)
		if err != nil {
			return false
		}
		data = b
		return len(b) > 0
	}, 2*time.Second, 10*time.Millisecond, "the desktop command should run and write the file")
	require.Equal(t, "sess one|NeedsInput|needs_input", string(data))
}

func TestEmitDesktopNoNotifierDoesNotRun(t *testing.T) {
	fe := &fakeExec{}
	n := New(&bytes.Buffer{}, fe)
	n.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	n.Emit(config.NotificationsDesktop, "", "sess", EventFinished)
	// Desktop dispatch is async but returns nil before touching the runner; give it
	// a moment and confirm nothing ran.
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, fe.calls())
}

func TestOsaQuote(t *testing.T) {
	require.Equal(t, `"plain"`, osaQuote("plain"))
	require.Equal(t, `"a\\b"`, osaQuote(`a\b`), "backslash is escaped")
	require.Equal(t, `"a\"b"`, osaQuote(`a"b`), "double-quote is escaped")
	// Control characters an AppleScript string literal can't hold (a display name is
	// user-editable via rename) fold to spaces so the one-line script stays valid.
	require.Equal(t, `"a b"`, osaQuote("a\nb"), "newline folds to a space")
	require.Equal(t, `"a b c"`, osaQuote("a\tb\rc"), "tab and CR fold to spaces")
}

// TestEventVocabulary pins the three-way vocabulary handed to a notify command.
//
// ATRIUM_STATUS and ATRIUM_EVENT are deliberately different axes: status is what the row
// shows, so a question reports "Ready" — the turn DID end — and an existing user script
// keying on status keeps its meaning now that a third event exists. ATRIUM_EVENT is the
// axis that distinguishes them.
func TestEventVocabulary(t *testing.T) {
	cases := []struct {
		ev                  Event
		status, token, want string
	}{
		{EventFinished, "Ready", "finished", "sess finished"},
		{EventNeedsInput, "NeedsInput", "needs_input", "sess needs input"},
		{EventAsked, "Ready", "asked", "sess asked a question"},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			require.Equal(t, tc.status, tc.ev.status())
			require.Equal(t, tc.token, tc.ev.token())
			require.Equal(t, tc.want, tc.ev.headline("sess"))
		})
	}
}

// TestDesktopCommandAskedCarriesEnv pins the asked event through the same env-building
// path the other events use, so a user command can branch on ATRIUM_EVENT.
func TestDesktopCommandAskedCarriesEnv(t *testing.T) {
	n := New(&bytes.Buffer{}, &fakeExec{})
	c := n.desktopCommand(context.Background(), "notify-send \"$ATRIUM_EVENT\"", "my sess", EventAsked)
	require.Contains(t, c.Env, "ATRIUM_SESSION=my sess")
	require.Contains(t, c.Env, "ATRIUM_STATUS=Ready")
	require.Contains(t, c.Env, "ATRIUM_EVENT=asked")
}
