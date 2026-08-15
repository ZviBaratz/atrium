package ui

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"
	"os/exec"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMockTmuxSession creates a mock tmux session backed by MockCmdExec.
// The returned session will report as existing and support capture-pane commands.
func newMockTmuxSession(t *testing.T, name string, cmdExec cmd_test.MockCmdExec) *tmux.Session {
	t.Helper()
	ptyFactory := &MockPtyFactory{
		t:       t,
		cmdExec: cmdExec,
	}
	return tmux.NewSessionWithDeps(context.Background(), name, "bash", ptyFactory, cmdExec)
}

// mockCmdExec returns a MockCmdExec that simulates a working tmux session.
// captureContent is returned for capture-pane commands.
func mockCmdExec(captureContent string, sessionExists bool) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()
			if strings.Contains(cmdStr, "has-session") {
				if sessionExists {
					return nil
				}
				return fmt.Errorf("session does not exist")
			}
			if strings.Contains(cmdStr, "new-session") {
				return nil
			}
			if strings.Contains(cmdStr, "kill-session") {
				return nil
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			cmdStr := cmd.String()
			if strings.Contains(cmdStr, "capture-pane") {
				return []byte(captureContent), nil
			}
			return []byte(""), nil
		},
	}
}

// makeStartedInstance creates a minimal instance that reports as started with the given title.
func makeStartedInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	workdir := t.TempDir()
	setupGitRepo(t, workdir)

	random := time.Now().UnixNano() % 10000000
	sessionName := fmt.Sprintf("test-terminal-%s-%d-%d", title, time.Now().UnixNano(), random)

	sessionCreated := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil
				}
				return fmt.Errorf("session does not exist")
			}
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
				return nil
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte(""), nil
		},
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   sessionName,
		Path:    workdir,
		Program: "bash",
	})
	require.NoError(t, err)

	ptyFactory := &MockPtyFactory{
		t:       t,
		cmdExec: cmdExec,
	}
	tmuxSession := tmux.NewSessionWithDeps(context.Background(), sessionName, "bash", ptyFactory, cmdExec)
	instance.SetTmuxSession(tmuxSession)

	err = instance.Start(true)
	require.NoError(t, err)

	return instance
}

// injectSession injects a mock tmux session into the TerminalPane's sessions map.
func injectSession(tp *TerminalPane, key string, ts *tmux.Session, cwd string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.sessions[key] = &terminalSession{
		tmuxSession: ts,
		cwd:         cwd,
	}
	tp.currentKey = key
}

// primeTerminalFrame runs one capture through the injected session's fake executor
// and applies it, standing in for the app's background capture chain
// (app/app_frames.go). The pane no longer captures for itself — that is the point
// of #380 — so a test that wants shell content must supply a frame first. The mock
// still serves the capture-pane call, so the real capture path stays exercised.
func primeTerminalFrame(t *testing.T, tp *TerminalPane, inst *session.Instance) {
	t.Helper()
	sess, key, ok := tp.CaptureTarget(inst)
	require.True(t, ok, "the shell session must be injected before priming a frame")
	content, err := sess.CapturePaneContent()
	require.NoError(t, err)
	tp.ApplyFrame(key, theme.SanitizeWidth(content), nil, time.Now())
}

// showTerminal is primeTerminalFrame followed by UpdateContent: the two steps the
// app performs per frame.
func showTerminal(t *testing.T, tp *TerminalPane, inst *session.Instance) {
	t.Helper()
	primeTerminalFrame(t, tp, inst)
	require.NoError(t, tp.UpdateContent(inst))
}

func TestTerminalUpdateContent(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	expectedContent := "$ whoami\nuser\n$ ls\nfile1.txt  file2.txt"

	cmdExec := mockCmdExec(expectedContent, true)

	instance := makeStartedInstance(t, "update-content")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	// Inject a mock session that returns expectedContent on capture-pane
	ts := newMockTmuxSession(t, "mock-update", cmdExec)
	// Start the session so DoesSessionExist returns true
	injectSession(tp, terminalKey(instance), ts, t.TempDir())

	// UpdateContent renders the frame the app's capture chain supplied.
	showTerminal(t, tp, instance)

	tp.mu.Lock()
	require.False(t, tp.fallback, "should not be in fallback mode after successful content update")
	require.Equal(t, expectedContent, tp.content, "content should match captured pane output")
	tp.mu.Unlock()

	// Verify String() output contains the content
	rendered := tp.String()
	require.Contains(t, rendered, "whoami", "rendered output should contain captured content")
}

func TestTerminalFallbackStates(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	t.Run("nil instance", func(t *testing.T) {
		err := tp.UpdateContent(nil)
		require.NoError(t, err)

		tp.mu.Lock()
		require.True(t, tp.fallback, "should be in fallback mode for nil instance")
		require.Contains(t, tp.fallbackMessage, "Select an instance", "fallback text should prompt to select instance")
		require.Empty(t, tp.content, "content should be empty in fallback mode")
		tp.mu.Unlock()
	})

	t.Run("paused instance", func(t *testing.T) {
		// Create an instance without starting it, then set status to Paused.
		// UpdateContent checks Paused status before Started(), so no need to start.
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "paused-inst",
			Path:    t.TempDir(),
			Program: "bash",
		})
		require.NoError(t, err)
		instance.SetStatus(session.Paused)

		err = tp.UpdateContent(instance)
		require.NoError(t, err)

		tp.mu.Lock()
		require.True(t, tp.fallback, "should be in fallback mode for paused instance")
		require.Contains(t, tp.fallbackMessage, "paused", "fallback text should mention paused")
		tp.mu.Unlock()
	})

	t.Run("not started instance", func(t *testing.T) {
		// Create an instance that hasn't been started
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "not-started",
			Path:    t.TempDir(),
			Program: "bash",
		})
		require.NoError(t, err)

		err = tp.UpdateContent(instance)
		require.NoError(t, err)

		tp.mu.Lock()
		require.True(t, tp.fallback, "should be in fallback mode for not-started instance")
		require.Contains(t, tp.fallbackMessage, "not started", "fallback text should indicate not started")
		tp.mu.Unlock()
	})
}

// TestTerminalFallbackCentered locks the fallback placeholder to the shared
// centerInBox contract: it must fill the pane's full height and sit at true
// vertical center, the same way the preview and diff panes center theirs. The
// prior hand-rolled path subtracted the tab/frame chrome a second time
// (height-3-4) even though TabbedWindow.SetSize had already removed it, so the
// banner rendered height-7 lines tall and sat high. This test fails on that old
// output and passes once the fallback centers via centerInBox.
func TestTerminalFallbackCentered(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	// Width below the splash floor (minSplashW) so the nil idle state renders the
	// plain centered fallback this test locks — the animated splash is covered by
	// the splash tests. Height stays full so the centering check is meaningful.
	const w, h = 48, 30
	tp := NewTerminalPane(context.Background())
	tp.SetSize(w, h)

	// A nil instance drops the pane into the fallback banner state.
	require.NoError(t, tp.UpdateContent(nil))

	lines := strings.Split(tp.String(), "\n")
	require.Len(t, lines, h, "fallback must occupy the pane's full height (was height-7 with the old double chrome subtraction)")

	first, last := -1, -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	require.NotEqual(t, -1, first, "fallback text should be present")
	top := first
	bottom := len(lines) - 1 - last
	if d := top - bottom; d < -1 || d > 1 {
		t.Fatalf("fallback not vertically centered: %d blank lines above, %d below", top, bottom)
	}
}

// The terminal's fallback shares the preview's composer and so shared its #355
// bug: its messages are short but still outrun a narrow pane ("Session is paused.
// Resume to use terminal." is 42 cols against a reachable 28), and the wordmark
// cannot render at all there. Fixing one pane and not the other would have left
// the same misrender one tab over on the same paused session.
func TestTerminalFallbackReadableOnNarrowPane(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	tp := NewTerminalPane(context.Background())
	tp.SetSize(28, 13)
	require.NoError(t, tp.UpdateContent(nil))

	out := tp.String()
	require.Contains(t, strings.Join(strings.Fields(ansi.Strip(out)), ""),
		strings.Join(strings.Fields("Select an instance to open a terminal"), ""),
		"the message must survive the narrow pane — wrapped, not chopped")
	require.False(t, strings.ContainsAny(ansi.Strip(out), bannerGlyphs),
		"a 48-col wordmark cannot render in 28 cols — it must be omitted, not sheared")
	for i, l := range strings.Split(out, "\n") {
		require.LessOrEqualf(t, lipgloss.Width(l), 28, "fallback line %d wider than the pane", i)
	}
}

// TestTerminalSplashParity locks the Terminal tab's idle empty state to the same
// animated field as the preview: at an adequate size String() renders the field
// (wordmark and prompt surviving, bounded), and below the floor it falls back to
// the plain placeholder with no field glyphs. The placeholder keeps the wordmark
// only where it fits — 48 cols affords it, narrower is the message alone (see
// fallbackBlock) — so the below-floor case asserts the field is gone rather than
// that the wordmark is present.
func TestTerminalSplashParity(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	tp.SetSplashFrame(6)
	require.NoError(t, tp.UpdateContent(nil))
	require.True(t, tp.splash, "nil instance must set the splash on the terminal pane")

	out := tp.String()
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 30, "splash must fill the pane height")
	for i, l := range lines {
		require.LessOrEqualf(t, lipgloss.Width(l), 80, "line %d width", i)
	}
	stripped := ansi.Strip(out)
	require.Contains(t, stripped, "Select an instance", "terminal prompt must survive")
	require.Contains(t, stripped, "█", "wordmark must survive")
	require.True(t, strings.ContainsAny(stripped, fieldGlyphs), "the field must render behind the wordmark")

	// Below the splash floor → plain fallback, no field glyphs.
	small := NewTerminalPane(context.Background())
	small.SetSize(48, 16)
	small.SetSplashFrame(6)
	require.NoError(t, small.UpdateContent(nil))
	require.False(t, strings.ContainsAny(ansi.Strip(small.String()), fieldGlyphs),
		"below the floor the terminal must render the plain placeholder, not the field")
}

// TestTerminalSplashDisabled is the #316 gate on the second idle pane. The
// SetSplashFrame fan-out feeds both panes, so a disable wired into only the
// preview would leave the terminal tab animating on the very same empty screen —
// and nothing in TestTerminalSplashParity would notice, since it never turns the
// setting off.
func TestTerminalSplashDisabled(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	disableSplash(t)

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	tp.SetSplashFrame(6)
	require.NoError(t, tp.UpdateContent(nil))

	stripped := ansi.Strip(tp.String())
	require.False(t, strings.ContainsAny(stripped, fieldGlyphs),
		"a disabled splash must render the plain placeholder, not the field")
	require.Contains(t, stripped, "Select an instance", "terminal prompt must survive")
	require.Contains(t, stripped, "█", "wordmark must survive")
}

func TestTerminalSessionCaching(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	content1 := "session-1-content"
	cmdExec1 := mockCmdExec(content1, true)
	ts1 := newMockTmuxSession(t, "cache-test-1", cmdExec1)

	content2 := "session-2-content"
	cmdExec2 := mockCmdExec(content2, true)
	ts2 := newMockTmuxSession(t, "cache-test-2", cmdExec2)

	instance1 := makeStartedInstance(t, "cache1")
	defer func() { _ = instance1.Kill() }()
	instance2 := makeStartedInstance(t, "cache2")
	defer func() { _ = instance2.Kill() }()

	// Inject two separate sessions
	injectSession(tp, terminalKey(instance1), ts1, t.TempDir())

	tp.mu.Lock()
	tp.sessions[terminalKey(instance2)] = &terminalSession{
		tmuxSession: ts2,
		cwd:         t.TempDir(),
	}
	tp.mu.Unlock()

	// Switch to instance1 and capture
	tp.mu.Lock()
	tp.currentKey = terminalKey(instance1)
	tp.mu.Unlock()

	showTerminal(t, tp, instance1)
	tp.mu.Lock()
	require.Equal(t, content1, tp.content)
	tp.mu.Unlock()

	// Switch to instance2 and capture
	tp.mu.Lock()
	tp.currentKey = terminalKey(instance2)
	tp.mu.Unlock()

	showTerminal(t, tp, instance2)
	tp.mu.Lock()
	require.Equal(t, content2, tp.content)
	tp.mu.Unlock()

	// Switch back to instance1 — session should still exist (cached)
	tp.mu.Lock()
	tp.currentKey = terminalKey(instance1)
	tp.mu.Unlock()

	require.NoError(t, tp.UpdateContent(instance1))
	tp.mu.Lock()
	require.Equal(t, content1, tp.content, "should get cached session content when switching back")
	// Verify both sessions are still in the map
	require.Len(t, tp.sessions, 2, "both sessions should be cached")
	tp.mu.Unlock()
}

func TestTerminalScrolling(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	// Create content with many lines for scrolling
	const numLines = 100
	lines := make([]string, numLines)
	for i := range numLines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	fullContent := strings.Join(lines, "\n")

	cmdExec := mockCmdExec(fullContent, true)
	instance := makeStartedInstance(t, "scroll")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	ts := newMockTmuxSession(t, "scroll-test", cmdExec)
	injectSession(tp, terminalKey(instance), ts, t.TempDir())

	// Initially not scrolling
	require.False(t, tp.IsScrolling(), "should not be scrolling initially")

	// ScrollUp should enter scroll mode
	err := tp.ScrollUp()
	require.NoError(t, err)
	require.True(t, tp.IsScrolling(), "should be in scroll mode after ScrollUp")

	// Viewport should contain the content
	viewContent := tp.viewport.View()
	require.NotEmpty(t, viewContent, "viewport should have content in scroll mode")

	// Move off the bottom first: entering lands at the bottom, where a
	// wheel-down would auto-exit back to the live view rather than scroll.
	err = tp.ScrollUp()
	require.NoError(t, err)

	// ScrollDown above the bottom should continue in scroll mode
	err = tp.ScrollDown()
	require.NoError(t, err)
	require.True(t, tp.IsScrolling(), "should still be in scroll mode after ScrollDown")

	// ResetToNormalMode should exit scroll mode
	tp.ResetToNormalMode()
	require.False(t, tp.IsScrolling(), "should not be scrolling after ResetToNormalMode")

	// Viewport content should be cleared
	tp.mu.Lock()
	require.False(t, tp.isScrolling, "isScrolling should be false")
	tp.mu.Unlock()
}

// TestTerminalScrollSnapshotUnpinsOnInstanceSwitch is the terminal-pane twin of the
// stuck-preview bug: the scroll snapshot was not keyed to an instance, and String()
// checks isScrolling before the fallbacks, so a latched snapshot pinned across
// selection changes until restart. Switching the displayed instance must drop the
// snapshot and capture the new instance's live shell.
// TestTerminalScrollDownAtBottomExitsToLive mirrors the preview pane's self-healing
// exit: a wheel-down while the snapshot is already at its bottom must leave scroll
// mode (the next UpdateContent tick repaints the live shell); a wheel-down anywhere
// above the bottom must scroll and stay in the mode.
func TestTerminalScrollDownAtBottomExitsToLive(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	// More lines than the 30-row viewport so "off the bottom" is reachable.
	const numLines = 100
	lines := make([]string, numLines)
	for i := range numLines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	fullContent := strings.Join(lines, "\n")

	instance := makeStartedInstance(t, "scroll-bottom-exit")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	injectSession(tp, terminalKey(instance), newMockTmuxSession(t, "mock-scroll-bottom", mockCmdExec(fullContent, true)), t.TempDir())

	// Enter scroll mode: the viewport starts at the bottom of the snapshot.
	require.NoError(t, tp.ScrollUp())
	require.True(t, tp.IsScrolling())
	tp.mu.Lock()
	require.True(t, tp.viewport.AtBottom(), "entering scroll mode must land at the bottom")
	tp.mu.Unlock()

	// Wheel-down while already at the bottom exits scroll mode.
	require.NoError(t, tp.ScrollDown())
	require.False(t, tp.IsScrolling(), "a wheel-down at the bottom must exit scroll mode")

	// A further wheel-down from the live view must not re-enter the snapshot —
	// otherwise a held wheel would toggle enter/exit forever.
	require.NoError(t, tp.ScrollDown())
	require.False(t, tp.IsScrolling(), "wheel-down from the live view must not enter scroll mode")

	// Off the bottom, a wheel-down scrolls — it must not exit.
	require.NoError(t, tp.ScrollUp()) // re-enter
	for range 5 {
		require.NoError(t, tp.ScrollUp())
	}
	tp.mu.Lock()
	require.False(t, tp.viewport.AtBottom())
	tp.mu.Unlock()
	require.NoError(t, tp.ScrollDown())
	require.True(t, tp.IsScrolling(), "scrolling down above the bottom must stay in scroll mode")

	// Reaching the bottom and wheeling down once more exits.
	for range 10 {
		require.NoError(t, tp.ScrollDown())
	}
	require.False(t, tp.IsScrolling(), "wheeling down past the bottom must exit scroll mode")
}

func TestTerminalScrollSnapshotUnpinsOnInstanceSwitch(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	contentA := "shell A scrollback"
	contentB := "shell B live output"

	instA := makeStartedInstance(t, "scroll-switch-a")
	defer func() { _ = instA.Kill() }()
	instB := makeStartedInstance(t, "scroll-switch-b")
	defer func() { _ = instB.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	injectSession(tp, terminalKey(instA), newMockTmuxSession(t, "mock-scroll-a", mockCmdExec(contentA, true)), t.TempDir())
	injectSession(tp, terminalKey(instB), newMockTmuxSession(t, "mock-scroll-b", mockCmdExec(contentB, true)), t.TempDir())

	// Show A live, then enter scroll mode (snapshot of A's shell history).
	showTerminal(t, tp, instA)
	require.NoError(t, tp.ScrollUp())
	require.True(t, tp.IsScrolling(), "ScrollUp must enter scroll mode")
	require.Contains(t, tp.String(), contentA)

	// Selecting another session must exit the snapshot and show B's live shell.
	showTerminal(t, tp, instB)
	require.False(t, tp.IsScrolling(), "switching instances must exit scroll mode")
	rendered := tp.String()
	require.Contains(t, rendered, contentB, "the newly selected session's live shell must be shown")
	require.NotContains(t, rendered, contentA, "the old session's snapshot must not pin")
}

// TestTerminalScrollSnapshotDropsWhenInstancePauses guards the fallback ranking:
// String() renders the scroll viewport before the fallbacks, so the snapshot must be
// dropped when the displayed instance can no longer back it (paused here) — otherwise
// the frozen capture outranks the "Session is paused" message.
func TestTerminalScrollSnapshotDropsWhenInstancePauses(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	content := "shell scrollback"
	instance := makeStartedInstance(t, "scroll-pause")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	injectSession(tp, terminalKey(instance), newMockTmuxSession(t, "mock-scroll-pause", mockCmdExec(content, true)), t.TempDir())

	require.NoError(t, tp.UpdateContent(instance))
	require.NoError(t, tp.ScrollUp())
	require.True(t, tp.IsScrolling())

	instance.SetStatus(session.Paused)
	require.NoError(t, tp.UpdateContent(instance))
	require.False(t, tp.IsScrolling(), "pausing the displayed instance must exit scroll mode")
	require.Contains(t, tp.String(), "paused", "the paused fallback must be visible, not the stale snapshot")
}

// Terminal shells were keyed term_<title> before tmux names became persisted
// state; the key change (<tmux name>_term) orphans those sessions on upgrade.
// Creating the new-keyed shell for an instance must reap its legacy-named one.
// Drives a real tmux server on the dedicated socket (self-skips without tmux).
func TestEnsureSessionReapsLegacyTermSession(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "legacy-reap")
	defer func() { _ = instance.Kill() }()

	// The shell session exactly as the pre-upgrade code minted it.
	legacy := tmux.NewSession(context.Background(), "term_"+instance.Title, "sleep 300")
	require.NoError(t, legacy.Start(t.TempDir()))
	t.Cleanup(func() { _ = legacy.Close() })

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	// EnsureSession is the create path — it now runs on the app's capture
	// goroutine rather than inside UpdateContent, so drive it directly.
	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)
	require.NotEmpty(t, key)

	tp.mu.Lock()
	_, created := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	require.True(t, created, "the new-keyed shell session must be created")
	require.False(t, legacy.DoesSessionExist(), "the orphaned legacy term_ session must be reaped")
}

// shellOnSocket reports whether the instance's <key>_term shell is alive on
// Atrium's tmux socket, and registers a cleanup that reaps it either way. The
// cache map cannot answer this: the whole of #701 is a shell that exists on the
// socket while no entry names it.
func shellOnSocket(t *testing.T, inst *session.Instance) bool {
	t.Helper()
	probe := tmux.NewSessionWithName(context.Background(), terminalKey(inst)+"_term", "probe", "/bin/sh")
	t.Cleanup(func() { _ = probe.Close() })
	return probe.DoesSessionExist()
}

// A pause or kill completing while EnsureSession is still in its tmux round trip
// used to orphan the shell: CloseForInstance can only reap what is already in the
// cache, and the install lands afterwards. Reproduced 10/10 on the unpatched tree
// by sleeping 5ms between the two; the beforeInstall seam makes it an assertion
// rather than a timing bet. Drives a real tmux server (self-skips without tmux).
func TestEnsureSessionDropsAShellReapedMidCreate(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "reaped-mid-create")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	// The done-handler's reap, landing after new-session and before the install —
	// the interleaving the issue draws. The cache is empty here, so CloseForInstance
	// closes nothing and only its generation bump records that it ran.
	tp.beforeInstall = func() { tp.CloseForInstance(instance) }

	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)

	// assert, not require, deliberately: the socket check below is the one that is
	// about the bug rather than about a map, and a fatal on either line above would
	// stop it from ever running — including under the mutations that prove it.
	tp.mu.Lock()
	_, cached := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	assert.Empty(t, key, "a shell reaped mid-create must not be reported as ready")
	assert.False(t, cached, "the reaped shell must not be installed after the reap")
	assert.False(t, shellOnSocket(t, instance),
		"the shell survived on the socket, in a worktree the teardown removed, with "+
			"nothing left to reap it but `atrium reset`")
}

// The other half of the same install check, and the reason it is two conditions.
// handlePauseDone returns before its reap when the pause reported an error, so no
// generation bump ever happens — while pause() has already removed the worktree
// and set Paused. Only the status re-read catches that one.
func TestEnsureSessionDropsAShellWhoseInstancePausedMidCreate(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "paused-mid-create")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	// Deliberately no CloseForInstance: the generation is untouched, so this test
	// fails if the status arm is dropped even though the reap arm survives.
	tp.beforeInstall = func() { instance.SetStatus(session.Paused) }

	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)

	tp.mu.Lock()
	_, cached := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	assert.Empty(t, key, "a shell whose instance paused mid-create must not be reported as ready")
	assert.False(t, cached, "a paused instance must not end up with a cached shell")
	assert.False(t, shellOnSocket(t, instance),
		"the shell survived in the worktree the pause removed")
}

// The positive control for both guards above: with nothing reaping and nothing
// pausing, the same call installs a shell and leaves it running. Without this, an
// EnsureSession that refused unconditionally would pass every assertion above.
func TestEnsureSessionInstallsAnUnraceedShell(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "unraced")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)
	require.Equal(t, terminalKey(instance), key)

	tp.mu.Lock()
	_, cached := tp.sessions[key]
	tp.mu.Unlock()
	require.True(t, cached, "an unraced create must install its shell")
	require.True(t, shellOnSocket(t, instance), "an unraced create must leave a live shell")
}

// The generation is per key because the abort path CLOSES the session it was about
// to install, and that session is not always one this call created: the branch above
// the install adopts a <key>_term already alive on the socket — left by a crashed or
// SIGKILLed run, or by a CloseForInstance whose own Close() errored and only dropped
// the map entry. Aborting that adoption for an unrelated instance's teardown does not
// cost a retry, it kills the user's shell and everything running in it.
func TestEnsureSessionKeepsAnAdoptedShellWhileAnotherInstanceIsReaped(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "adopted")
	defer func() { _ = instance.Kill() }()
	bystander := makeStartedInstance(t, "adopted-bystander")
	defer func() { _ = bystander.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	// Round one creates the shell. Dropping the cache entry without closing the
	// session reproduces what a crashed run leaves on the socket: alive, and named
	// by nothing in this process.
	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)
	require.NotEmpty(t, key)
	tp.mu.Lock()
	delete(tp.sessions, key)
	tp.mu.Unlock()

	// Round two adopts it, with an unrelated instance's teardown landing in the
	// same window the guards above use.
	tp.beforeInstall = func() { tp.CloseForInstance(bystander) }
	key, err = tp.EnsureSession(instance)
	require.NoError(t, err)

	// The socket first: it is the assertion about the consequence.
	assert.True(t, shellOnSocket(t, instance),
		"a bystander's teardown destroyed this instance's shell and everything running in it")
	assert.Equal(t, terminalKey(instance), key,
		"a bystander's teardown must not withhold this instance's shell")
	tp.mu.Lock()
	_, cached := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	assert.True(t, cached, "the adopted shell must be installed, not dropped")
}

// Close is the whole-pane teardown: it reaps every key at once, including keys no
// entry names yet. A create still in its round trip when that lands must not install
// into the emptied map, so the pane-wide epoch is checked alongside the per-key
// generation.
func TestEnsureSessionDropsAShellWhoseWholePaneClosedMidCreate(t *testing.T) {
	testutil.RequireTmux(t)
	t.Cleanup(log.Initialize(t.TempDir(), false))

	instance := makeStartedInstance(t, "pane-closed-mid-create")
	defer func() { _ = instance.Kill() }()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	tp.beforeInstall = tp.Close

	key, err := tp.EnsureSession(instance)
	require.NoError(t, err)

	tp.mu.Lock()
	_, cached := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	assert.False(t, shellOnSocket(t, instance),
		"the shell outlived the pane that owned it, with nothing left holding a handle to it")
	assert.Empty(t, key, "a shell whose pane closed mid-create must not be reported as ready")
	assert.False(t, cached, "the closed pane must not be repopulated by an in-flight create")
}

func TestTerminalCloseForInstance(t *testing.T) {
	t.Cleanup(log.Initialize(t.TempDir(), false))

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	content := "some content"
	cmdExec := mockCmdExec(content, true)

	instance1 := makeStartedInstance(t, "close1")
	defer func() { _ = instance1.Kill() }()
	instance2 := makeStartedInstance(t, "close2")
	defer func() { _ = instance2.Kill() }()

	ts1 := newMockTmuxSession(t, "close-test-1", cmdExec)
	ts2 := newMockTmuxSession(t, "close-test-2", cmdExec)

	injectSession(tp, terminalKey(instance1), ts1, t.TempDir())
	tp.mu.Lock()
	tp.sessions[terminalKey(instance2)] = &terminalSession{
		tmuxSession: ts2,
		cwd:         t.TempDir(),
	}
	tp.mu.Unlock()

	// Verify both sessions exist
	tp.mu.Lock()
	require.Len(t, tp.sessions, 2)
	tp.mu.Unlock()

	// Close instance1's session
	tp.CloseForInstance(instance1)

	// Only instance2 should remain
	tp.mu.Lock()
	require.Len(t, tp.sessions, 1, "should have only 1 session after closing instance1")
	_, exists := tp.sessions[terminalKey(instance1)]
	require.False(t, exists, "instance1 session should be removed")
	_, exists = tp.sessions[terminalKey(instance2)]
	require.True(t, exists, "instance2 session should still exist")
	require.Empty(t, tp.currentKey, "currentKey should be cleared when closing current instance")
	tp.mu.Unlock()

	// Closing an instance with no cached session (or nil) should not panic.
	uncached := makeStartedInstance(t, "uncached")
	defer func() { _ = uncached.Kill() }()
	tp.CloseForInstance(uncached)
	tp.CloseForInstance(nil)

	tp.mu.Lock()
	require.Len(t, tp.sessions, 1, "non-existent close should not affect existing sessions")
	tp.mu.Unlock()
}
