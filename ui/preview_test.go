package ui

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// testSetup holds common test setup data
type testSetup struct {
	workdir     string
	instance    *session.Instance
	sessionName string
	cleanupFn   func()
}

// setupTestEnvironment creates a common test environment with git repo and instance
func setupTestEnvironment(t *testing.T, cmdExec cmd_test.MockCmdExec) *testSetup {
	t.Helper()
	return setupTestEnvironmentWithProgram(t, cmdExec, "bash")
}

// setupTestEnvironmentWithProgram is setupTestEnvironment with the instance's
// agent program made explicit — scroll-mode behavior branches on it (Claude
// sessions render their JSONL transcript; everything else captures tmux).
func setupTestEnvironmentWithProgram(t *testing.T, cmdExec cmd_test.MockCmdExec, program string) *testSetup {
	t.Helper()

	// Initialize logging
	log.Initialize(false)

	// Set up a temp working directory
	workdir := t.TempDir()

	// Initialize git repository
	setupGitRepo(t, workdir)

	// Create unique session name
	random := time.Now().UnixNano() % 10000000
	sessionName := fmt.Sprintf("test-preview-%s-%d-%d", t.Name(), time.Now().UnixNano(), random)

	// Clean up any existing tmux session (cs runs on a dedicated -L socket)
	cleanupCmd := exec.CommandContext(context.Background(), "tmux", "-L", "claudesquad", "kill-session", "-t", "claudesquad_"+sessionName)
	_ = cleanupCmd.Run() // Ignore errors if session doesn't exist

	// Create instance
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   sessionName,
		Path:    workdir,
		Program: program,
	})
	require.NoError(t, err)

	// Create MockPtyFactory
	ptyFactory := &MockPtyFactory{
		t:       t,
		cmdExec: cmdExec,
	}

	// Set up tmux session with mocks
	tmuxSession := tmux.NewSessionWithDeps(context.Background(), sessionName, program, ptyFactory, cmdExec)
	instance.SetTmuxSession(tmuxSession)

	// Start the tmux session
	err = instance.Start(true)
	require.NoError(t, err)

	// Create cleanup function
	cleanupFn := func() {
		if instance != nil {
			_ = instance.Kill() // Ignore errors during cleanup
		}
		log.Close()
	}

	return &testSetup{
		workdir:     workdir,
		instance:    instance,
		sessionName: sessionName,
		cleanupFn:   cleanupFn,
	}
}

// setupGitRepo initializes a git repository in the given directory
func setupGitRepo(t *testing.T, workdir string) {
	t.Helper()

	// Initialize git repository
	initCmd := exec.CommandContext(context.Background(), "git", "init")
	initCmd.Dir = workdir
	err := initCmd.Run()
	require.NoError(t, err)

	// Create basic git config (local to this repo only)
	configCmd := exec.CommandContext(context.Background(), "git", "config", "--local", "user.email", "test@example.com")
	configCmd.Dir = workdir
	err = configCmd.Run()
	require.NoError(t, err)

	configCmd = exec.CommandContext(context.Background(), "git", "config", "--local", "user.name", "Test User")
	configCmd.Dir = workdir
	err = configCmd.Run()
	require.NoError(t, err)

	// Create and commit a test file
	testFile := filepath.Join(workdir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	addCmd := exec.CommandContext(context.Background(), "git", "add", "test.txt")
	addCmd.Dir = workdir
	err = addCmd.Run()
	require.NoError(t, err)

	commitCmd := exec.CommandContext(context.Background(), "git", "commit", "-m", "initial commit")
	commitCmd.Dir = workdir
	err = commitCmd.Run()
	require.NoError(t, err)
}

// TestPreviewScrolling tests the scrolling functionality in the preview pane
func TestPreviewScrolling(t *testing.T) {
	// Track what commands were executed and their order
	var executedCommands []string
	inCopyMode := false
	scrollPosition := 0 // 0 = bottom, positive = scrolled up
	sessionCreated := false

	// Create test content with line numbers for scrolling
	const numLines = 100
	lines := make([]string, numLines+1)
	lines[0] = "$ seq 100" // Command that was run
	for i := 1; i <= numLines; i++ {
		lines[i] = fmt.Sprintf("%d", i)
	}
	fullContent := strings.Join(lines, "\n")

	// Mock command execution
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()
			executedCommands = append(executedCommands, cmdStr)

			// Handle tmux session creation and existence checking
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil // Session exists
				}
				return fmt.Errorf("session does not exist")
			}

			// Handle session creation
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
				return nil
			}

			// Handle attach-session
			if strings.Contains(cmdStr, "attach-session") {
				return nil
			}

			// Handle copy mode commands
			if strings.Contains(cmdStr, "copy-mode") {
				inCopyMode = true
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "q") {
				inCopyMode = false
				scrollPosition = 0 // Reset position when exiting copy mode
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "Up") {
				if inCopyMode {
					scrollPosition++
				}
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "Down") {
				if inCopyMode && scrollPosition > 0 {
					scrollPosition--
				}
			}

			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			cmdStr := cmd.String()

			// Handle capture-pane commands
			if strings.Contains(cmdStr, "capture-pane") {
				// Check if this is a request for cursor position
				if strings.Contains(cmdStr, "display-message") && strings.Contains(cmdStr, "copy_cursor_y") {
					var buf []byte
					buf = fmt.Appendf(buf, "%d", scrollPosition)
					return buf, nil
				}

				// Check if this is a copy mode capture with full history (-S -)
				if strings.Contains(cmdStr, "-S -") {
					// Always return the full content for PreviewFullHistory
					return []byte(fullContent), nil
				}

				// Regular capture for normal preview mode - show the last 20 lines
				const visibleLines = 20
				startLine := max(0, numLines+1-visibleLines)
				visibleContent := strings.Join(lines[startLine:], "\n")
				return []byte(visibleContent), nil
			}

			return []byte(""), nil
		},
	}

	// Setup test environment
	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Simulate running a command that produces lots of output
	err := setup.instance.SendKeys("seq 100")
	require.NoError(t, err)
	err = setup.instance.SendKeys("") // Simulate pressing Enter
	require.NoError(t, err)

	// Create the preview pane
	previewPane := NewPreviewPane()
	previewPane.SetSize(80, 30) // Set reasonable size for testing

	// Step 1: Check initial content - should show normal preview mode
	err = previewPane.UpdateContent(setup.instance)
	require.NoError(t, err)

	// Verify we're not in scrolling mode initially
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode initially")

	// Step 2: Check that PreviewFullHistory returns all content
	fullHistory, err := setup.instance.PreviewFullHistory()
	require.NoError(t, err)

	// Verify that the full history contains both the command and early output
	require.Contains(t, fullHistory, "$ seq 100", "Full history should contain the command")
	require.Contains(t, fullHistory, "1", "Full history should contain earliest output")

	// Step 3: Enter scroll mode
	err = previewPane.ScrollUp(setup.instance)
	require.NoError(t, err)

	// Verify we entered scrolling mode
	require.True(t, previewPane.isScrolling, "Should be in scrolling mode after ScrollUp")

	// Step 4: Get the content directly from the viewport
	viewportContent := previewPane.viewport.View()
	t.Logf("Viewport content: %q", viewportContent)

	// With proper implementation, the viewport should have the full history content
	// Note: The viewport will be positioned at the bottom initially, so we need to scroll up

	// Step 5: Scroll up multiple times to get to the top
	for range 50 {
		err = previewPane.ScrollUp(setup.instance)
		require.NoError(t, err)
	}

	// Now get the viewport content after scrolling up
	viewportAfterScrollUp := previewPane.viewport.View()
	t.Logf("Viewport after scrolling up: %q", viewportAfterScrollUp)

	// Step 6: Scroll down multiple times
	for range 25 {
		err = previewPane.ScrollDown(setup.instance)
		require.NoError(t, err)
	}

	// Get updated viewport content after scrolling down
	viewportAfterScrollDown := previewPane.viewport.View()
	t.Logf("Viewport after scrolling down: %q", viewportAfterScrollDown)

	// Step 7: Reset to normal mode
	err = previewPane.ResetToNormalMode(setup.instance)
	require.NoError(t, err)

	// Verify we exited scrolling mode
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode after reset")
}

// MockPtyFactory for testing tmux sessions
type MockPtyFactory struct {
	t       *testing.T
	cmdExec cmd_test.MockCmdExec

	// Array of commands and the corresponding file handles representing PTYs.
	cmds  []*exec.Cmd
	files []*os.File
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	filePath := filepath.Join(pt.t.TempDir(), fmt.Sprintf("pty-%s-%d", pt.t.Name(), len(pt.cmds)))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err == nil {
		pt.cmds = append(pt.cmds, cmd)
		pt.files = append(pt.files, f)

		// Execute the command through our mock to trigger session creation logic
		_ = pt.cmdExec.Run(cmd)
	}
	return f, err
}

func (pt *MockPtyFactory) Close() {}

// TestPreviewContentWithoutScrolling tests that the preview pane correctly displays content
// for a new instance without requiring scrolling
func TestPreviewContentWithoutScrolling(t *testing.T) {
	// Create test content
	expectedContent := "$ echo test\ntest"

	// Track session creation state
	sessionCreated := false

	// Mock command execution
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()

			// Handle tmux session creation and existence checking
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil // Session exists
				}
				return fmt.Errorf("session does not exist")
			}

			// Handle session creation
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
				return nil
			}

			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			cmdStr := cmd.String()

			// Handle capture-pane commands for normal preview
			if strings.Contains(cmdStr, "capture-pane") {
				// Return our test content for normal preview
				return []byte(expectedContent), nil
			}

			return []byte(""), nil
		},
	}

	// Setup test environment
	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Create the preview pane
	previewPane := NewPreviewPane()
	previewPane.SetSize(80, 30) // Set reasonable size for testing

	// Update the preview content (this should display the content without scrolling)
	err := previewPane.UpdateContent(setup.instance)
	require.NoError(t, err)

	// Verify we're not in scrolling mode
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode")

	// Verify that the preview state is not in fallback mode
	require.False(t, previewPane.previewState.fallback, "Preview should not be in fallback mode")

	// Verify that the preview state contains the expected content
	require.Equal(t, expectedContent, previewPane.previewState.text, "Preview state should contain the expected content")

	// Verify the rendered string contains the content
	renderedString := previewPane.String()
	require.Contains(t, renderedString, "test", "Rendered preview should contain the test content")
}

// TestPreviewDoesNotPinLoadingSplashForLiveSession reproduces the reported bug: a
// session whose status is (stale) Loading but whose tmux pane is alive and started
// must render the live pane, never the "Setting up workspace..." splash. This guards
// the preview defense-in-depth that complements the main-thread Running transition.
func TestPreviewDoesNotPinLoadingSplashForLiveSession(t *testing.T) {
	const expectedContent = "agent is working"
	sessionCreated := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil // session is alive
				}
				return fmt.Errorf("session does not exist")
			}
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if strings.Contains(cmd.String(), "capture-pane") {
				return []byte(expectedContent), nil
			}
			return []byte(""), nil
		},
	}

	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Pin the instance at Loading even though it is started with a live tmux pane —
	// the exact stuck state the user hit (list shows the session, preview was frozen).
	setup.instance.SetStatus(session.Loading)
	require.True(t, setup.instance.Started())
	require.True(t, setup.instance.TmuxAlive())

	pane := NewPreviewPane()
	pane.SetSize(80, 30)
	require.NoError(t, pane.UpdateContent(setup.instance))

	require.False(t, pane.previewState.fallback,
		"a started, tmux-alive session must not fall back to the setup splash")
	rendered := pane.String()
	require.NotContains(t, rendered, "Setting up workspace",
		"the Loading splash must never pin for a live session")
	require.Contains(t, rendered, expectedContent, "the live pane content must be shown")
}

// TestPreviewSplashClearsOnceContentArrives is the core self-heal guarantee: while a
// session is still coming up (empty pane) the splash is shown, but the instant the pane
// yields content the preview must switch to it — even if the status flag is still a stale
// Loading. This is the regression guard for "the splash isn't updated with the preview".
func TestPreviewSplashClearsOnceContentArrives(t *testing.T) {
	paneContent := "" // pane is blank during startup, then produces output
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
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if strings.Contains(cmd.String(), "capture-pane") {
				return []byte(paneContent), nil
			}
			return []byte(""), nil
		},
	}

	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Stuck-state precondition: status pinned at Loading with a blank pane.
	setup.instance.SetStatus(session.Loading)

	pane := NewPreviewPane()
	pane.SetSize(80, 30)

	// Tick 1: blank pane while still coming up → the setup splash.
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.True(t, pane.previewState.fallback, "a blank, still-loading pane shows the setup splash")
	require.Contains(t, pane.String(), "Setting up workspace")

	// Tick 2: the pane produces output. The status flag is still a stale Loading, but live
	// content must win and the splash must clear without any restart/reselect.
	paneContent = "agent is working"
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.False(t, pane.previewState.fallback,
		"live pane content must clear the splash even while status is still Loading")
	require.Contains(t, pane.String(), "agent is working")
}

// liveContentCmdExec returns a MockCmdExec that serves *content for every
// capture-pane call, with the usual has-session/new-session lifecycle handling.
// content is a pointer so tests can vary the served content between ticks.
func liveContentCmdExec(content *string) cmd_test.MockCmdExec {
	sessionCreated := false
	return cmd_test.MockCmdExec{
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
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if strings.Contains(cmd.String(), "capture-pane") {
				return []byte(*content), nil
			}
			return []byte(""), nil
		},
	}
}

// TestPreviewScrollSnapshotUnpinsOnInstanceSwitch reproduces the stuck-preview bug:
// entering scroll mode froze an instance-agnostic snapshot, and because nothing exited
// scroll mode on selection change, every session rendered the same stale capture until
// the app was restarted. Switching the displayed instance must drop the snapshot and
// resume the live view.
func TestPreviewScrollSnapshotUnpinsOnInstanceSwitch(t *testing.T) {
	contentA := "agent A scrollback"
	contentB := "agent B live output"

	setupA := setupTestEnvironment(t, liveContentCmdExec(&contentA))
	defer setupA.cleanupFn()
	setupB := setupTestEnvironment(t, liveContentCmdExec(&contentB))
	defer setupB.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)

	// Show A live, then enter scroll mode (snapshot of A's full history).
	require.NoError(t, pane.UpdateContent(setupA.instance))
	require.NoError(t, pane.ScrollUp(setupA.instance))
	require.True(t, pane.isScrolling, "ScrollUp must enter scroll mode")
	require.Contains(t, pane.String(), contentA)

	// Selecting another session must exit the snapshot and show B's live pane —
	// this is the exact "preview stuck on the same state for all sessions" symptom.
	require.NoError(t, pane.UpdateContent(setupB.instance))
	require.False(t, pane.isScrolling, "switching instances must exit scroll mode")
	rendered := pane.String()
	require.Contains(t, rendered, contentB, "the newly selected session's live pane must be shown")
	require.NotContains(t, rendered, contentA, "the old session's snapshot must not pin")
}

// TestPreviewScrollExitNeverRefuses guards the ESC dead end: ResetToNormalMode used to
// no-op for a nil or paused instance, leaving isScrolling latched with no exit besides
// restarting the app. Exiting scroll mode must succeed regardless of instance state;
// only the immediate live re-capture is conditional on a usable instance.
func TestPreviewScrollExitNeverRefuses(t *testing.T) {
	content := "agent output"
	setup := setupTestEnvironment(t, liveContentCmdExec(&content))
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)

	// Enter scroll mode, then pause the session while the snapshot is up.
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)
	setup.instance.SetStatus(session.Paused)

	// ESC on a paused selection must still leave scroll mode.
	require.NoError(t, pane.ResetToNormalMode(setup.instance))
	require.False(t, pane.isScrolling, "exiting scroll mode must work for a paused instance")

	// Same for a nil selection (e.g. the last session was killed while scrolling).
	setup.instance.SetStatus(session.Running)
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)
	require.NoError(t, pane.ResetToNormalMode(nil))
	require.False(t, pane.isScrolling, "exiting scroll mode must work with no selection")
}

// TestPreviewScrollSnapshotDropsWhenInstancePauses is the preview twin of the terminal
// pause test, via a different render ranking: String() draws the fallback before the
// scroll viewport, so the paused message displaces the snapshot — but if scroll mode
// survives the pause, UpdateContent keeps early-returning after resume and the stale
// "Session is paused" fallback pins on a running session. Pausing must exit scroll
// mode so resuming returns to the live view.
func TestPreviewScrollSnapshotDropsWhenInstancePauses(t *testing.T) {
	content := "agent scrollback"
	setup := setupTestEnvironment(t, liveContentCmdExec(&content))
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)

	require.NoError(t, pane.UpdateContent(setup.instance))
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)

	// Pausing the displayed instance must drop the snapshot, not just hide it.
	setup.instance.SetStatus(session.Paused)
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.False(t, pane.isScrolling, "pausing the displayed instance must exit scroll mode")
	require.Contains(t, pane.String(), "paused", "the paused fallback must be shown")

	// The latch this guards against: after resume, the live view must return —
	// not the stale paused fallback.
	setup.instance.SetStatus(session.Running)
	require.NoError(t, pane.UpdateContent(setup.instance))
	rendered := pane.String()
	require.Contains(t, rendered, content, "resuming must restore the live view")
	require.NotContains(t, rendered, "paused", "the paused fallback must not pin after resume")
}

// TestPreviewKeepsContentOnTransientCaptureError verifies a capture error never freezes a
// stale fallback or blanks the pane: the last good content is retained and the error is
// surfaced (not swallowed).
// TestPreviewScrollDownAtBottomExitsToLive covers the snapshot's self-healing exit:
// entering scroll mode lands at the bottom of the capture, so a wheel-down while
// already at the bottom must resume the live view (tmux copy-mode style) — the
// escape hatch for an accidental wheel flick. Scrolling down anywhere above the
// bottom must stay in scroll mode.
func TestPreviewScrollDownAtBottomExitsToLive(t *testing.T) {
	// More lines than the 30-row viewport so "off the bottom" is reachable.
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	content := strings.Join(lines, "\n")

	setup := setupTestEnvironment(t, liveContentCmdExec(&content))
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)
	require.NoError(t, pane.UpdateContent(setup.instance))

	// Enter scroll mode: the viewport starts at the bottom of the snapshot.
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)
	require.True(t, pane.viewport.AtBottom(), "entering scroll mode must land at the bottom")

	// Wheel-down while already at the bottom resumes the live view.
	require.NoError(t, pane.ScrollDown(setup.instance))
	require.False(t, pane.isScrolling, "a wheel-down at the bottom must exit scroll mode")
	require.Equal(t, content, pane.previewState.text, "the live pane content must be re-captured on exit")

	// A further wheel-down from the live view must not re-enter the snapshot —
	// otherwise a held wheel would toggle enter/exit forever.
	require.NoError(t, pane.ScrollDown(setup.instance))
	require.False(t, pane.isScrolling, "wheel-down from the live view must not enter scroll mode")

	// Off the bottom, a wheel-down scrolls — it must not exit.
	require.NoError(t, pane.ScrollUp(setup.instance)) // re-enter
	for range 5 {
		require.NoError(t, pane.ScrollUp(setup.instance))
	}
	require.False(t, pane.viewport.AtBottom())
	require.NoError(t, pane.ScrollDown(setup.instance))
	require.True(t, pane.isScrolling, "scrolling down above the bottom must stay in scroll mode")

	// Reaching the bottom and wheeling down once more exits.
	for range 10 {
		require.NoError(t, pane.ScrollDown(setup.instance))
	}
	require.False(t, pane.isScrolling, "wheeling down past the bottom must exit scroll mode")
}

func TestPreviewKeepsContentOnTransientCaptureError(t *testing.T) {
	const liveContent = "agent is working"
	sessionCreated := false
	captureFails := false
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
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if strings.Contains(cmd.String(), "capture-pane") {
				if captureFails {
					return nil, fmt.Errorf("error capturing pane content")
				}
				return []byte(liveContent), nil
			}
			return []byte(""), nil
		},
	}

	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)

	// Establish live content.
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.False(t, pane.previewState.fallback)
	require.Contains(t, pane.String(), liveContent)

	// A transient capture error must surface but leave the last good content intact.
	captureFails = true
	require.Error(t, pane.UpdateContent(setup.instance))
	require.False(t, pane.previewState.fallback, "a capture error must not flip to a stale fallback")
	require.Contains(t, pane.String(), liveContent, "the last good content must be retained")
}

// writeClaudeTranscript plants a Claude session JSONL for workingDir under the
// sandboxed $HOME/.claude tree, mirroring Claude Code's project-dir scheme
// (every non-alphanumeric rune of the cwd becomes '-'). The mapping is written
// out independently here on purpose: if the transcript package's sanitization
// drifted from the real on-disk scheme, this test would stop lining up.
func writeClaudeTranscript(t *testing.T, workingDir string, jsonlLines ...string) {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, workingDir)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir := filepath.Join(home, ".claude", "projects", sanitized)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := strings.Join(jsonlLines, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(content), 0o644))
}

// TestPreviewScrollUsesTranscriptForClaude verifies that scroll mode on a
// Claude session shows the session's own JSONL transcript as the history above
// a frozen capture of the current screen (the tmux history itself is
// structurally empty for in-place repainting agents). Anchoring the bottom on
// the current screen keeps entry seamless: the snapshot's tail is exactly what
// the live view showed, and the transcript continues above it past a divider.
func TestPreviewScrollUsesTranscriptForClaude(t *testing.T) {
	tmuxContent := "TMUX PANE CONTENT"
	setup := setupTestEnvironmentWithProgram(t, liveContentCmdExec(&tmuxContent), "claude")
	defer setup.cleanupFn()

	writeClaudeTranscript(t, setup.instance.WorkingDir(),
		`{"type":"user","message":{"role":"user","content":"transcribed user prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"transcribed assistant reply"}]}}`,
	)

	pane := NewPreviewPane()
	pane.SetSize(80, 30)
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)

	rendered := pane.String()
	require.Contains(t, rendered, "❯ transcribed user prompt")
	require.Contains(t, rendered, "transcribed assistant reply")
	require.Contains(t, rendered, "current screen", "a divider must separate transcript history from the screen capture")
	require.Contains(t, rendered, tmuxContent, "the frozen current screen must anchor the snapshot bottom")
	require.Contains(t, rendered, "transcript · ESC", "the footer must label the snapshot as a transcript")

	// Layout order: transcript history, divider, current screen, footer.
	idxTranscript := strings.Index(rendered, "transcribed assistant reply")
	idxDivider := strings.Index(rendered, "current screen")
	idxPane := strings.Index(rendered, tmuxContent)
	idxFooter := strings.Index(rendered, "transcript · ESC")
	require.True(t, idxTranscript < idxDivider, "transcript history must render above the divider")
	require.True(t, idxDivider < idxPane, "the divider must render above the screen capture")
	require.True(t, idxPane < idxFooter, "the screen capture must render above the footer")
}

// TestPreviewScrollFallsBackToTmuxForAider locks in the "never worse than
// today" guarantee: programs without a transcript adapter keep the existing
// tmux full-history snapshot and footer.
func TestPreviewScrollFallsBackToTmuxForAider(t *testing.T) {
	tmuxContent := "AIDER TMUX HISTORY"
	setup := setupTestEnvironmentWithProgram(t, liveContentCmdExec(&tmuxContent), "aider")
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)

	rendered := pane.String()
	require.Contains(t, rendered, tmuxContent)
	require.Contains(t, rendered, "snapshot · ESC", "non-transcript snapshots keep the snapshot footer")
	require.NotContains(t, rendered, "current screen", "tmux-sourced snapshots have no transcript divider")
}

// TestPreviewScrollClaudeWithoutTranscriptFallsBack covers the degraded path
// for a Claude session whose transcript is missing (e.g. the conversation has
// not started yet): scroll mode must silently fall back to the tmux capture.
func TestPreviewScrollClaudeWithoutTranscriptFallsBack(t *testing.T) {
	tmuxContent := "CLAUDE PANE WITHOUT TRANSCRIPT"
	setup := setupTestEnvironmentWithProgram(t, liveContentCmdExec(&tmuxContent), "claude")
	defer setup.cleanupFn()

	pane := NewPreviewPane()
	pane.SetSize(80, 30)
	require.NoError(t, pane.UpdateContent(setup.instance))
	require.NoError(t, pane.ScrollUp(setup.instance))
	require.True(t, pane.isScrolling)

	rendered := pane.String()
	require.Contains(t, rendered, tmuxContent)
	require.Contains(t, rendered, "snapshot · ESC", "a tmux-sourced snapshot keeps the snapshot footer")
}

// The empty-state fallback (banner + onboarding message) can be wider than a
// narrow pane. lipgloss.Place does not clip oversize content, so without the
// MaxWidth/MaxHeight clamp the pane — and with it the whole composed frame —
// grew past the terminal width, shoving every centered overlay off-center.
func TestPreviewFallbackClampedToPaneBox(t *testing.T) {
	pane := NewPreviewPane()
	// Narrower than the "No agents running yet..." message (69 cols).
	pane.SetSize(56, 13)
	require.NoError(t, pane.UpdateContent(nil))

	lines := strings.Split(pane.String(), "\n")
	require.LessOrEqual(t, len(lines), 13, "fallback must not exceed the pane height")
	for i, l := range lines {
		require.LessOrEqualf(t, ansi.PrintableRuneWidth(l), 56,
			"fallback line %d wider than the pane", i)
	}
}
