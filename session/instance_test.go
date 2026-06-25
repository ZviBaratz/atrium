package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusAccessorsAreRaceFree exercises the lifecycle-field accessors from two
// goroutines at once: a writer mutating the mu-guarded fields (status, tmuxSession)
// while the metadata-poll / UI readers query them. Before the RWMutex was added this
// raced (writer = Start's SetStatus(Running) + tmuxSession assignment; readers = the
// poll loop and the UI methods below), which under `go test -race` is a hard failure
// and at runtime could leave a session pinned at Loading. Every method exercised by the
// reader goroutine must read the guarded fields through the locked accessors, not the
// bare struct fields, so this also guards against a regression that reintroduces a
// direct read. It must pass under -race.
func TestStatusAccessorsAreRaceFree(t *testing.T) {
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	newSession := func() *tmux.Session {
		return tmux.NewSessionWithDeps(context.Background(), "race", "claude", tmux.MakePtyFactory(), mockExec)
	}
	inst := &Instance{Title: "race", status: Loading, started: true, tmuxSession: newSession()}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			inst.SetStatus(Running)
			inst.SetTmuxSession(newSession())
			inst.SetStatus(Ready)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = inst.GetStatus()
			_ = inst.Started()
			_ = inst.Paused()
			_ = inst.TmuxAlive()
			_ = inst.IsReadyForPrompt()
			_ = inst.SetPreviewSize(80, 24)
			_, _ = inst.PreviewFullHistory()
			_ = inst.SendKeys("x")
		}
	}()
	wg.Wait()
}

// TestPreviewSkipsCaptureWhenSessionDead asserts that previewing a started instance
// whose tmux session has died returns empty (not an error) without running
// capture-pane, so the preview refresh can't escalate the failure to the error box.
func TestPreviewSkipsCaptureWhenSessionDead(t *testing.T) {
	captured := false
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { captured = true; return nil, fmt.Errorf("capture fail") },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "dead", "claude", tmux.MakePtyFactory(), mockExec)
	inst := &Instance{Title: "dead", status: Running, started: true, tmuxSession: ts}

	content, err := inst.Preview()
	require.NoError(t, err)
	require.Equal(t, "", content)
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

// runGit runs git in dir and fails the test on error, discarding output. It is a
// thin wrapper over gitOutput (the sibling helper that returns the output) so the
// exec boilerplate lives in one place.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOutput(t, dir, args...)
}

// TestRecoverLostSessionTransitionsToPaused asserts that a started instance whose
// tmux session has died is moved to Paused (so it stops being polled and can be
// brought back with Resume), reusing the Pause path to preserve the branch.
func TestRecoverLostSessionTransitionsToPaused(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	wt, _, err := git.NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	deadExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("dead") },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", tmux.MakePtyFactory(), deadExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

	require.False(t, inst.TmuxAlive())
	require.NoError(t, inst.RecoverLostSession())
	require.True(t, inst.Paused(), "a lost session must transition to Paused")
}

// TestPause_ClearsCachedDirtyDiffStat asserts that pausing a session with
// uncommitted changes — which Pause commits before removing the worktree —
// clears the cached diffStats.Dirty flag. The metadata poll loop skips paused
// instances, so a stale Dirty=true would otherwise persist and surface a false
// "(has uncommitted changes)" in the kill dialog (and a stale list glyph).
func TestPause_ClearsCachedDirtyDiffStat(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	wt, _, err := git.NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	// Dirty the worktree so pause has uncommitted work to commit.
	require.NoError(t, os.WriteFile(filepath.Join(wt.GetWorktreePath(), "scratch.txt"),
		[]byte("uncommitted\n"), 0644))
	dirty, err := wt.IsDirty()
	require.NoError(t, err)
	require.True(t, dirty, "worktree should be dirty before pause")

	liveExec := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return nil },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(""), nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", tmux.MakePtyFactory(), liveExec)
	inst := &Instance{Title: "sess", status: Running, started: true, gitWorktree: wt, tmuxSession: ts}
	inst.diffStats = &git.DiffStats{Added: 1, FilesChanged: 1, Dirty: true}

	require.NoError(t, inst.pause())
	require.True(t, inst.Paused(), "instance must be paused")
	require.NotNil(t, inst.GetDiffStats())
	assert.False(t, inst.GetDiffStats().Dirty,
		"pause commits uncommitted work, so the cached dirty flag must be cleared")
}

func TestSetPath_ResolvesToAbsolute(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	require.NoError(t, inst.SetPath("/tmp/some/repo"))
	assert.Equal(t, "/tmp/some/repo", inst.Path)

	// A relative path is resolved to absolute, mirroring NewInstance.
	require.NoError(t, inst.SetPath("relative/dir"))
	want, _ := filepath.Abs("relative/dir")
	assert.Equal(t, want, inst.Path)
	assert.True(t, filepath.IsAbs(inst.Path))
}

func TestToInstanceData_PersistsGitContext(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// NewWorktreeFromStorage is a pure constructor (no git I/O), so we can use it
	// to stand up a worktree carrying a base ref without starting the instance.
	inst.gitWorktree = git.NewWorktreeFromStorage(
		context.Background(),
		"/repo", "/repo/wt", "t", "session/t", "abc123", "main", false, "session/")
	inst.diffStats = &git.DiffStats{
		Added: 12, Removed: 3, FilesChanged: 4, Commits: 2, Behind: 5, Dirty: true,
	}

	data := inst.ToInstanceData()

	assert.Equal(t, "main", data.Worktree.BaseRef, "base ref must survive persistence")
	assert.Equal(t, 4, data.DiffStats.FilesChanged)
	assert.Equal(t, 2, data.DiffStats.Commits)
	assert.Equal(t, 5, data.DiffStats.Behind)
	assert.True(t, data.DiffStats.Dirty)
}

// TestOperableGitSession_TruthTable pins the predicate behind the diff/PR guards:
// true only for a started, non-paused git session, false for unstarted, paused, and
// direct sessions. It also guards the key invariant the issue #188 premise got wrong —
// a paused git session keeps a non-nil worktree pointer (pause removes only the on-disk
// directory). NewWorktreeFromStorage is a pure constructor (no git I/O), so each state
// is stood up without starting tmux/git.
func TestOperableGitSession_TruthTable(t *testing.T) {
	newGitWt := func() *git.Worktree {
		return git.NewWorktreeFromStorage(
			context.Background(),
			"/repo", "/repo/wt", "t", "session/t", "abc123", "main", false, "session/")
	}

	t.Run("unstarted git session is not operable", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
		require.NoError(t, err)
		inst.gitWorktree = newGitWt() // worktree pointer set, but Start has not run
		assert.False(t, inst.IsDirect(), "an unstarted git session is not direct")
		assert.False(t, inst.operableGitSession())
	})

	t.Run("started git session is operable", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
		require.NoError(t, err)
		inst.started = true
		inst.gitWorktree = newGitWt()
		assert.True(t, inst.operableGitSession())
	})

	t.Run("paused git session is not operable but keeps its worktree", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
		require.NoError(t, err)
		inst.started = true
		inst.status = Paused
		inst.gitWorktree = newGitWt() // pause removes the on-disk dir, not this pointer
		assert.NotNil(t, inst.worktree(), "a paused git session must keep its worktree pointer")
		assert.False(t, inst.operableGitSession())
	})

	t.Run("started direct session is not operable", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo", Direct: true})
		require.NoError(t, err)
		inst.started = true
		assert.True(t, inst.IsDirect())
		assert.Nil(t, inst.worktree(), "a direct session has no worktree")
		assert.False(t, inst.operableGitSession())
	})
}

func TestSetPath_RejectedAfterStart(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// Simulate a started instance without spinning up tmux/git.
	inst.started = true
	err = inst.SetPath("/tmp/other")
	require.Error(t, err)
}

func TestDisplayName_FallsBackToTitle(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// With no label set, DisplayName mirrors Title.
	assert.Equal(t, "my-task", inst.DisplayName())

	inst.SetDisplayName("Nicer Label")
	assert.Equal(t, "Nicer Label", inst.DisplayName())
	// Title (the stable identifier) is untouched by the label.
	assert.Equal(t, "my-task", inst.Title)
}

func TestSetDisplayName_WorksAfterStart(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	// Unlike SetTitle, the cosmetic label can change after the instance has started.
	inst.started = true
	require.Error(t, inst.SetTitle("renamed"), "SetTitle must reject a started instance")

	inst.SetDisplayName("After Start")
	assert.Equal(t, "After Start", inst.DisplayName())
}

func TestSetDisplayName_TrimsAndClears(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)

	inst.SetDisplayName("  spaced label  ")
	assert.Equal(t, "spaced label", inst.DisplayName())

	// Empty/whitespace input clears the label, reverting to Title.
	inst.SetDisplayName("   ")
	assert.Equal(t, "my-task", inst.DisplayName())
}

func TestDisplayName_SerializedInInstanceData(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "my-task", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetDisplayName("Nicer Label")

	data := inst.ToInstanceData()
	assert.Equal(t, "Nicer Label", data.DisplayName)
	assert.Equal(t, "my-task", data.Title)
}

func TestInstanceData_MissingDisplayNameIsEmpty(t *testing.T) {
	// State files written before this feature have no display_name key; they must load with
	// an empty label so the name falls back to Title.
	var data InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"my-task"}`), &data))
	assert.Equal(t, "", data.DisplayName)
}

// approveRecorder builds a MockCmdExec that records every send-keys argv and
// resolves the agent pane as %7, so tests can assert exactly what reached tmux.
func approveRecorder(sendKeysArgs *[][]string) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			for i, arg := range cmd.Args {
				if arg == "send-keys" {
					*sendKeysArgs = append(*sendKeysArgs, cmd.Args[i+1:])
					break
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte("%7\n"), nil },
	}
}

// TestApplyPaneState pins the pane-state → status/prompt mapping that both the TUI
// metadata loop and the headless daemon route through. The tapped return is what lets the
// daemon refresh diff stats only after an auto-answer; it must be true for exactly one
// case (AutoYes prompt) so neither caller has to re-derive which states auto-answer.
func TestApplyPaneState(t *testing.T) {
	newInst := func(autoYes bool) *Instance {
		inst, err := NewInstance(InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.AutoYes = autoYes
		inst.SetStatus(Loading) // a recognizable prior state
		return inst
	}

	t.Run("working → Running", func(t *testing.T) {
		inst := newInst(false)
		require.False(t, inst.ApplyPaneState(tmux.PaneWorking))
		require.Equal(t, Running, inst.GetStatus())
	})

	t.Run("idle → Ready", func(t *testing.T) {
		inst := newInst(false)
		require.False(t, inst.ApplyPaneState(tmux.PaneIdle))
		require.Equal(t, Ready, inst.GetStatus())
	})

	t.Run("prompt with AutoYes off → NeedsInput, no tap", func(t *testing.T) {
		inst := newInst(false)
		require.False(t, inst.ApplyPaneState(tmux.PanePrompt))
		require.Equal(t, NeedsInput, inst.GetStatus())
	})

	t.Run("prompt with AutoYes on → tapped, not NeedsInput", func(t *testing.T) {
		inst := newInst(true)
		require.True(t, inst.ApplyPaneState(tmux.PanePrompt),
			"an auto-answered prompt must report tapped=true so the daemon refreshes its diff")
		require.NotEqual(t, NeedsInput, inst.GetStatus())
	})

	t.Run("manual prompt → NeedsInput even with AutoYes on, no tap", func(t *testing.T) {
		// The plan-approval dialog: auto-Enter would accept the plan and enable
		// auto-accept, so autoyes must surface it instead of answering.
		inst := newInst(true)
		require.False(t, inst.ApplyPaneState(tmux.PanePromptManual),
			"a destructive manual prompt must never tap Enter")
		require.Equal(t, NeedsInput, inst.GetStatus())
	})

	t.Run("unknown → status unchanged, no tap", func(t *testing.T) {
		inst := newInst(false)
		require.False(t, inst.ApplyPaneState(tmux.PaneUnknown))
		require.Equal(t, Loading, inst.GetStatus(), "an unreadable pane must not flip the status")
	})

	t.Run("dead → status unchanged, no tap", func(t *testing.T) {
		inst := newInst(false)
		require.False(t, inst.ApplyPaneState(tmux.PaneDead))
		require.Equal(t, Loading, inst.GetStatus(),
			"a dead session must not flip the status; recovery to Paused is handled separately")
	})
}

// ApprovePrompt is the user-initiated twin of the autoyes TapEnter: it must work
// with AutoYes off — that gate is what it deliberately bypasses.
func TestApprovePrompt_TapsEnterWithoutAutoYes(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve",
		status:      NeedsInput,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}
	require.False(t, inst.AutoYes, "the test must exercise the AutoYes-off path")

	require.NoError(t, inst.ApprovePrompt())

	require.Len(t, sent, 1, "exactly one keystroke batch must reach tmux")
	assert.Contains(t, sent[0], "Enter")
}

func TestApprovePrompt_NotStartedErrors(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve-unstarted",
		status:      Ready,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve-unstarted", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}

	require.Error(t, inst.ApprovePrompt())
	assert.Empty(t, sent, "an unstarted instance must never reach tmux")
}

// A started instance with no tmux session must error, not panic: ApprovePrompt
// follows the same nil guard as the other pane-touching methods (Poll,
// IsReadyForPrompt, CheckAndHandleTrustPrompt).
func TestApprovePrompt_NilTmuxSessionErrors(t *testing.T) {
	inst := &Instance{
		Title:   "approve-no-pane",
		status:  NeedsInput,
		started: true,
	}

	require.Error(t, inst.ApprovePrompt())
}

func TestApprovePrompt_PausedErrors(t *testing.T) {
	var sent [][]string
	inst := &Instance{
		Title:       "approve-paused",
		status:      Paused,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "approve-paused", "claude", tmux.MakePtyFactory(), approveRecorder(&sent)),
	}

	require.Error(t, inst.ApprovePrompt())
	assert.Empty(t, sent, "a paused instance has no live pane to tap")
}

// suggestionPane builds the raw (ANSI-bearing) capture of an idle claude pane
// whose input box holds boxLine. Bytes mirror the live fixture pinned in
// session/agent/suggestion_test.go (claude 2.1.17x, 2026-06-12).
func suggestionPane(boxLine string) string {
	return "transcript prose\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
		boxLine + "\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
		"  ? for shortcuts"
}

const ghostBoxLine = "\x1b[39m❯ \x1b[2mrun the failing test and fix it\x1b[0m"

// committedPane is the box after Right accepts a suggestion: the same text, now
// committed (non-dim) input rather than a dim ghost, so the detector reads it as
// "no suggestion". AcceptSuggestion waits for this transition before Enter.
const committedPane = "\x1b[39m❯ run the failing test and fix it"

// suggestionRecorder is approveRecorder plus raw panes served to capture-pane.
// The two recorders cannot share an OutputFunc: pane-id resolution (list-panes)
// and the AcceptSuggestion captures both go through Output, so the mock must
// branch on the argv. Successive capture-pane calls return successive captures
// (clamped to the last), so a test can feed the gate's ghost frame and then the
// post-Right committed frame the submit waits for.
func suggestionRecorder(sendKeysArgs *[][]string, captureErr error, captures ...string) cmd_test.MockCmdExec {
	i := 0
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			for j, arg := range cmd.Args {
				if arg == "send-keys" {
					*sendKeysArgs = append(*sendKeysArgs, cmd.Args[j+1:])
					break
				}
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			for _, arg := range cmd.Args {
				if arg == "capture-pane" {
					c := captures[len(captures)-1]
					if i < len(captures) {
						c = captures[i]
					}
					i++
					return []byte(c), captureErr
				}
			}
			return []byte("%7\n"), nil
		},
	}
}

// suggestionInstance builds a started, Ready instance running program with the
// given sequence of pane captures wired in (gate frame first, then any frames
// the post-Right wait observes).
func suggestionInstance(t *testing.T, program string, captureErr error, sent *[][]string, captures ...string) *Instance {
	t.Helper()
	return &Instance{
		Title:       "suggest",
		status:      Ready,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "suggest", program, tmux.MakePtyFactory(), suggestionRecorder(sent, captureErr, captures...)),
	}
}

// Right (accept) and Enter (submit) must go out as SEPARATE keystrokes with the
// accept committed in between — batching them sends Enter against claude's
// not-yet-updated empty input, where it is a no-op, leaving the suggestion
// inserted but unsent. The gate frame shows the ghost; the second frame shows
// the committed (non-dim) text the wait keys off before submitting.
func TestAcceptSuggestion_SendsRightThenEnter(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", nil, &sent, suggestionPane(ghostBoxLine), suggestionPane(committedPane))

	accepted, err := inst.AcceptSuggestion()
	require.NoError(t, err)
	require.True(t, accepted)

	require.Len(t, sent, 2, "Right and Enter must be separate sends, not one batch")
	// Each batch is "-t <pane> <key>"; assert the key (last arg) and that no
	// batch carries both keys.
	require.Equal(t, "Right", sent[0][len(sent[0])-1], "Right (accept) goes first, alone")
	require.Len(t, sent[0], 3, "the Right batch must carry only the one key")
	assert.Equal(t, "Enter", sent[1][len(sent[1])-1], "Enter (submit) follows once the accept committed")
}

func TestAcceptSuggestion_EmptyBoxSendsNothing(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", nil, &sent, suggestionPane("\x1b[39m❯ "))

	accepted, err := inst.AcceptSuggestion()
	require.NoError(t, err)
	assert.False(t, accepted)
	assert.Empty(t, sent)
}

// The safety-critical gate: non-dim text after the prompt char is a user-typed
// draft, and Enter would submit it — nothing may be sent.
func TestAcceptSuggestion_TypedDraftSendsNothing(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", nil, &sent, suggestionPane("\x1b[39m❯ half-written draft"))

	accepted, err := inst.AcceptSuggestion()
	require.NoError(t, err)
	assert.False(t, accepted)
	assert.Empty(t, sent)
}

// A non-claude agent has no suggestion UI (nil SuggestionVisible): the gate
// must answer before any capture, so even a pane that *looks* like a claude
// suggestion is never captured and never tapped.
func TestAcceptSuggestion_NonClaudeAdapterNoCaptureNoKeys(t *testing.T) {
	var sent [][]string
	captured := false
	rec := suggestionRecorder(&sent, nil, suggestionPane(ghostBoxLine))
	inner := rec.OutputFunc
	rec.OutputFunc = func(cmd *exec.Cmd) ([]byte, error) {
		for _, arg := range cmd.Args {
			if arg == "capture-pane" {
				captured = true
			}
		}
		return inner(cmd)
	}
	inst := &Instance{
		Title:       "suggest-codex",
		status:      Ready,
		started:     true,
		tmuxSession: tmux.NewSessionWithDeps(context.Background(), "suggest-codex", "codex", tmux.MakePtyFactory(), rec),
	}

	accepted, err := inst.AcceptSuggestion()
	require.NoError(t, err)
	assert.False(t, accepted)
	assert.Empty(t, sent)
	assert.False(t, captured, "the adapter gate must precede the capture")
}

func TestAcceptSuggestion_NotStartedErrors(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", nil, &sent, suggestionPane(ghostBoxLine))
	inst.started = false

	_, err := inst.AcceptSuggestion()
	require.Error(t, err)
	assert.Empty(t, sent)
}

func TestAcceptSuggestion_PausedErrors(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", nil, &sent, suggestionPane(ghostBoxLine))
	inst.status = Paused

	_, err := inst.AcceptSuggestion()
	require.Error(t, err)
	assert.Empty(t, sent)
}

// Same nil guard as ApprovePrompt: a started instance with no tmux session
// must error, not panic.
func TestAcceptSuggestion_NilTmuxSessionErrors(t *testing.T) {
	inst := &Instance{
		Title:   "suggest-no-pane",
		status:  Ready,
		started: true,
	}

	_, err := inst.AcceptSuggestion()
	require.Error(t, err)
}

func TestAcceptSuggestion_CaptureErrorSurfaces(t *testing.T) {
	var sent [][]string
	inst := suggestionInstance(t, "claude", fmt.Errorf("capture exploded"), &sent, "")

	_, err := inst.AcceptSuggestion()
	require.Error(t, err)
	assert.Empty(t, sent)
}
