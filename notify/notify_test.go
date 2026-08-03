package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// moduleFile walks up from the test's working directory to the module root and reads the
// named file (see the identical helper in packages config, keys and ui/overlay).
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			b, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			return string(b)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir,
			"reached filesystem root without finding go.mod (looking for %s)", name)
		dir = parent
	}
}

// eventConstNames reads the Event const block out of notify.go rather than trusting a list
// written by hand. It is what makes the table in TestEveryEventTokenIsDocumented a claim
// the suite can falsify: a fourth event added to the enum shows up here immediately, and
// the table that does not mention it fails before any documentation is even consulted.
func eventConstNames(t *testing.T) []string {
	t.Helper()
	src := moduleFile(t, "notify/notify.go")
	start := strings.Index(src, "\tEventFinished Event = iota")
	require.GreaterOrEqual(t, start, 0, "notify.go must declare the Event enum with iota")
	end := strings.Index(src[start:], "\n)")
	require.Greater(t, end, 0, "the Event const block must be terminated")
	decl := regexp.MustCompile(`^\tEvent[A-Za-z]+`)
	var names []string
	for _, line := range strings.Split(src[start:start+end], "\n") {
		if m := decl.FindString(line); m != "" {
			names = append(names, strings.TrimSpace(m))
		}
	}
	return names
}

// TestEveryEventTokenIsDocumented pins the $ATRIUM_EVENT vocabulary to the prose that
// enumerates it, in both directions.
//
// This guard exists because that vocabulary drifted the moment it grew a third value:
// EventAsked shipped with README updated and Config.NotifyCommand's doc comment still
// promising a two-value world, three lines below a comment the same change had rewritten.
// Nothing caught it — `token()` is exercised by TestEventVocabulary, but no test had ever
// read the sentences that tell a user what to branch on, so a green suite proved only that
// the code was right about itself.
//
// The enum is read out of the source (eventConstNames), so a fourth event cannot satisfy
// this by being absent from the table too.
func TestEveryEventTokenIsDocumented(t *testing.T) {
	tokens := map[string]Event{
		"EventFinished":   EventFinished,
		"EventNeedsInput": EventNeedsInput,
		"EventAsked":      EventAsked,
	}
	named := make([]string, 0, len(tokens))
	for name := range tokens {
		named = append(named, name)
	}
	require.ElementsMatch(t, eventConstNames(t), named,
		"the Event enum and this test's table have diverged — a new event needs a token "+
			"here and a mention in every doc site below")

	// Each doc site enumerates the vocabulary in one sentence, so the window after the
	// first $ATRIUM_EVENT mention is where every token has to appear.
	for _, site := range []string{"README.md", "config/types.go"} {
		body := moduleFile(t, site)
		at := strings.Index(body, "ATRIUM_EVENT")
		require.GreaterOrEqualf(t, at, 0, "%s must document $ATRIUM_EVENT", site)
		window := body[at:min(at+220, len(body))]
		for name, ev := range tokens {
			require.Containsf(t, window, ev.token(),
				"%s does not list the %s token %q where it enumerates $ATRIUM_EVENT:\n%s",
				site, name, ev.token(), window)
		}
	}
}
