package app

import (
	"context"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestHomeWithInstances(t *testing.T, paths ...string) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	for i, p := range paths {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   string(rune('a' + i)),
			Path:    p,
			Program: "echo",
		})
		require.NoError(t, err)
		l.AddInstance(inst)
	}
	return &home{ctx: context.Background(), list: l, appState: config.DefaultState()}
}

// newCreateFormHome builds a home wired enough to drive the `N` (create-form) flow.
func newCreateFormHome(t *testing.T) *home {
	t.Helper()
	s := spinner.New()
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         ui.NewList(&s),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		program:      "echo",
	}
}

// Pressing N opens the unified create form and, crucially, does NOT add a list row — the
// session is created only on submit, so nothing appears under a repo group while naming.
func TestKeyPrompt_OpensCreateFormWithoutAddingRow(t *testing.T) {
	h := newCreateFormHome(t)
	before := h.list.NumInstances()

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})

	assert.Equal(t, statePrompt, h.state)
	require.NotNil(t, h.textInputOverlay)
	assert.True(t, h.textInputOverlay.IsCreateForm(), "N should open the create form")
	assert.Equal(t, before, h.list.NumInstances(), "N must not add a list row before submit")
}

// N keeps its project-first focus: choosing a different repo is the reason to
// reach for it over n.
func TestKeyPrompt_FocusesProjectPicker(t *testing.T) {
	h := newCreateFormHome(t)

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})

	require.NotNil(t, h.textInputOverlay)
	assert.False(t, h.textInputOverlay.TitleFocused(), "N starts on the project picker, not the title")
}

// n opens the SAME create form (no inline naming flow, no premature list row),
// but focused on the title so "n → type a name → ⌃S" stays the fast path.
func TestKeyNew_OpensCreateFormFocusedOnTitle(t *testing.T) {
	h := newCreateFormHome(t)
	before := h.list.NumInstances()

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})

	assert.Equal(t, statePrompt, h.state)
	require.NotNil(t, h.textInputOverlay)
	assert.True(t, h.textInputOverlay.IsCreateForm(), "n should open the create form")
	assert.True(t, h.textInputOverlay.TitleFocused(), "n starts on the title field")
	assert.Equal(t, before, h.list.NumInstances(), "n must not add a list row before submit")
}

// Submitting the create form creates exactly one session carrying the typed title and
// prompt, and closes the overlay. (The returned Cmd would Start it in the background; we
// do not run it, so no tmux/worktree is spun up here.)
func TestCreateSessionFromForm_CreatesOneAndClearsOverlay(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.True(t, git.IsGitRepo(context.Background(), cwd), "test must run inside a git repository")

	h := newCreateFormHome(t)
	h.newSessionPath = cwd
	h.state = statePrompt
	ov := h.newSessionFormOverlay()
	h.textInputOverlay = ov
	// Focus starts on the project picker; Tab to the title field, then type the title.
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})

	before := h.list.NumInstances()
	cmd := h.createSessionFromForm("do the thing")
	require.NotNil(t, cmd)

	assert.Equal(t, before+1, h.list.NumInstances(), "submit must add exactly one row")
	assert.Nil(t, h.textInputOverlay, "overlay should close on submit")
	assert.Equal(t, stateDefault, h.state)

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "feature", inst.Title)
	assert.Equal(t, "do the thing", inst.Prompt)
}

// An empty title keeps the form open (cleared submit flag) and surfaces an error rather
// than creating a half-formed session.
func TestCreateSessionFromForm_EmptyTitleKeepsFormOpen(t *testing.T) {
	h := newCreateFormHome(t)
	h.newSessionPath, _ = os.Getwd()
	h.state = statePrompt
	ov := h.newSessionFormOverlay()
	ov.Submitted = true
	h.textInputOverlay = ov

	before := h.list.NumInstances()
	h.createSessionFromForm("") // no title typed

	assert.Equal(t, before, h.list.NumInstances(), "no session should be created")
	require.NotNil(t, h.textInputOverlay, "form stays open on validation error")
	assert.False(t, h.textInputOverlay.IsSubmitted(), "submitted flag cleared so the user can retry")
}

// Canceling the create form (Esc) creates nothing and returns to the default state.
func TestCreateForm_CancelCreatesNothing(t *testing.T) {
	h := newCreateFormHome(t)
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	require.Equal(t, statePrompt, h.state)
	before := h.list.NumInstances()

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.textInputOverlay)
	assert.Equal(t, before, h.list.NumInstances(), "cancel must not create a session")
}

func TestDefaultNewSessionPath_CwdFallback(t *testing.T) {
	h := newTestHomeWithInstances(t) // no instances → nothing highlighted
	cwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, cwd, h.defaultNewSessionPath())
}

func TestDefaultNewSessionPath_FromHighlightedInstance(t *testing.T) {
	h := newTestHomeWithInstances(t, "/tmp/repoA", "/tmp/repoB")
	h.list.SetSelectedInstance(1) // highlight repoB
	assert.Equal(t, "/tmp/repoB", h.defaultNewSessionPath())
}

func TestCandidateRepoPaths_CurrentFirstThenDeduped(t *testing.T) {
	h := newTestHomeWithInstances(t, "/tmp/repoA", "/tmp/repoB", "/tmp/repoA")
	h.newSessionPath = "/tmp/repoB" // current target

	got := h.candidateRepoPaths()

	// Current target comes first; duplicates are dropped; cwd is appended.
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, "/tmp/repoB", got[0])
	assert.Contains(t, got, "/tmp/repoA")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, got, cwd)

	// No duplicates overall.
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
		assert.Equal(t, 1, seen[p], "path %q duplicated", p)
	}
}

func TestCandidateRepoPaths_DropsStaleRecentPaths(t *testing.T) {
	h := newTestHomeWithInstances(t)
	existing := t.TempDir()
	missing := filepath.Join(t.TempDir(), "deleted-repo")
	require.NoError(t, h.appState.AddRecentPath(missing))
	require.NoError(t, h.appState.AddRecentPath(existing))

	got := h.candidateRepoPaths()

	assert.Contains(t, got, existing, "existing recent path should be offered")
	assert.NotContains(t, got, missing, "missing recent path should be pruned")
}
