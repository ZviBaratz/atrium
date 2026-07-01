package ui

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os/exec"
	"strings"
	"testing"
	"time"

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

func TestTerminalUpdateContent(t *testing.T) {
	log.Initialize(false)
	defer log.Close()

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

	// UpdateContent should set fallback=false and capture content
	err := tp.UpdateContent(instance)
	require.NoError(t, err)

	tp.mu.Lock()
	require.False(t, tp.fallback, "should not be in fallback mode after successful content update")
	require.Equal(t, expectedContent, tp.content, "content should match captured pane output")
	tp.mu.Unlock()

	// Verify String() output contains the content
	rendered := tp.String()
	require.Contains(t, rendered, "whoami", "rendered output should contain captured content")
}

func TestTerminalFallbackStates(t *testing.T) {
	log.Initialize(false)
	defer log.Close()

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)

	t.Run("nil instance", func(t *testing.T) {
		err := tp.UpdateContent(nil)
		require.NoError(t, err)

		tp.mu.Lock()
		require.True(t, tp.fallback, "should be in fallback mode for nil instance")
		require.Contains(t, tp.fallbackText, "Select an instance", "fallback text should prompt to select instance")
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
		require.Contains(t, tp.fallbackText, "paused", "fallback text should mention paused")
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
		require.Contains(t, tp.fallbackText, "not started", "fallback text should indicate not started")
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
	log.Initialize(false)
	defer log.Close()

	const w, h = 80, 30
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

func TestTerminalSessionCaching(t *testing.T) {
	log.Initialize(false)
	defer log.Close()

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

	err := tp.UpdateContent(instance1)
	require.NoError(t, err)
	tp.mu.Lock()
	require.Equal(t, content1, tp.content)
	tp.mu.Unlock()

	// Switch to instance2 and capture
	tp.mu.Lock()
	tp.currentKey = terminalKey(instance2)
	tp.mu.Unlock()

	err = tp.UpdateContent(instance2)
	require.NoError(t, err)
	tp.mu.Lock()
	require.Equal(t, content2, tp.content)
	tp.mu.Unlock()

	// Switch back to instance1 — session should still exist (cached)
	tp.mu.Lock()
	tp.currentKey = terminalKey(instance1)
	tp.mu.Unlock()

	err = tp.UpdateContent(instance1)
	require.NoError(t, err)
	tp.mu.Lock()
	require.Equal(t, content1, tp.content, "should get cached session content when switching back")
	// Verify both sessions are still in the map
	require.Len(t, tp.sessions, 2, "both sessions should be cached")
	tp.mu.Unlock()
}

func TestTerminalScrolling(t *testing.T) {
	log.Initialize(false)
	defer log.Close()

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
	log.Initialize(false)
	defer log.Close()

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
	log.Initialize(false)
	defer log.Close()

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
	require.NoError(t, tp.UpdateContent(instA))
	require.NoError(t, tp.ScrollUp())
	require.True(t, tp.IsScrolling(), "ScrollUp must enter scroll mode")
	require.Contains(t, tp.String(), contentA)

	// Selecting another session must exit the snapshot and show B's live shell.
	require.NoError(t, tp.UpdateContent(instB))
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
	log.Initialize(false)
	defer log.Close()

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
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	log.Initialize(false)
	defer log.Close()

	instance := makeStartedInstance(t, "legacy-reap")
	defer func() { _ = instance.Kill() }()

	// The shell session exactly as the pre-upgrade code minted it.
	legacy := tmux.NewSession(context.Background(), "term_"+instance.Title, "sleep 300")
	require.NoError(t, legacy.Start(t.TempDir()))
	t.Cleanup(func() { _ = legacy.Close() })

	tp := NewTerminalPane(context.Background())
	tp.SetSize(80, 30)
	t.Cleanup(tp.Close)

	require.NoError(t, tp.UpdateContent(instance))

	tp.mu.Lock()
	_, created := tp.sessions[terminalKey(instance)]
	tp.mu.Unlock()
	require.True(t, created, "the new-keyed shell session must be created")
	require.False(t, legacy.DoesSessionExist(), "the orphaned legacy term_ session must be reaped")
}

func TestTerminalCloseForInstance(t *testing.T) {
	log.Initialize(false)
	defer log.Close()

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
