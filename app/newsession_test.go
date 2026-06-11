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

// Opening the form on a non-git default target must not kick any open-time branch
// plumbing: there is nothing to fetch or search, the section is inert, and a later
// path change onto a git repo triggers its own verdict-driven fetch.
func TestOpenCreateForm_NonGitTargetSkipsBranchPlumbing(t *testing.T) {
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   "direct",
		Path:    t.TempDir(), // a plain directory, not a git repo
		Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})

	require.NotNil(t, h.textInputOverlay)
	assert.Empty(t, h.fetchedPaths, "a non-git target must not be seeded for fetching")
}

// A git default target keeps the open-time plumbing: it is seeded as fetched (once per
// form-session) so branches are current by the time the user reaches the branch field.
func TestOpenCreateForm_GitTargetSeedsFetch(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.True(t, git.IsGitRepo(context.Background(), cwd), "test must run inside a git repository")

	h := newCreateFormHome(t) // empty list → the default target is the cwd
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})

	assert.True(t, h.fetchedPaths[cwd], "a git target must be seeded as fetched at open")
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
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov
	// Focus starts on the project picker; Tab past the branch picker to the title field,
	// then type the title.
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
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

// TestCreateSessionFromForm_ExplicitPathOnlyAccountIsAccented pins the styling contract
// for a manually picked account: a path-only account (path_matches, no remote_matches) is
// a real route, so its badge is accented (isDefault=false) — not dimmed as a default. This
// is the IsCatchAll() delta: the old len(RemoteMatches)==0 test would have marked a
// path-only pick as the dim default. A hermetic temp dir keeps this non-git (direct).
func TestCreateSessionFromForm_ExplicitPathOnlyAccountIsAccented(t *testing.T) {
	dir := t.TempDir() // direct (non-git) target → no remote, hermetic

	h := newCreateFormHome(t)
	// A non-claude program keeps the model stop out of the form, so the Tab path
	// below (prompt → account) stays two hops; this test is about accounts only.
	h.appConfig.DefaultProgram = "echo"
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},                                // catch-all default
		{Name: "work", ConfigDir: "/w", PathMatches: []string{"/unmatched-xyz/"}}, // path-only; won't auto-route here
	}
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	// Type a title, then drive the picker to the path-only "work" account (index 1). A
	// navigation keypress marks the picker touched, so the choice is an explicit override
	// of the auto-route — which, since "work"'s path_matches misses dir, would land on the
	// "personal" default.
	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // title → prompt
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // prompt → account
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // personal → work, marks touched

	acct, ok := ov.GetSelectedAccount()
	require.True(t, ok, "driving the picker marks it an explicit override")
	require.Equal(t, "work", acct.Name, "the picker must have moved to the path-only account")

	require.NotNil(t, h.createSessionFromForm("do the thing"))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "work", inst.ClaudeAccountName())
	assert.False(t, inst.ClaudeAccountIsDefault(),
		"a manually picked path-only account is a real route (accented), not the dim default")
}

// A model typed into the form's Model field is composed into the persisted program
// string, so launch, pause/resume, and the daemon all see it with no extra plumbing.
func TestCreateSessionFromForm_ModelComposedIntoProgram(t *testing.T) {
	dir := t.TempDir() // direct (non-git) target, hermetic

	h := newCreateFormHome(t)
	h.program = "claude"
	h.appConfig.DefaultProgram = "claude" // GetProfiles synthesizes the claude profile
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	// Stops: [directory, branch, title, prompt, model, enter] (one profile → no
	// profile stop; claude → model stop present).
	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // title → prompt
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // prompt → model
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("opus")})
	require.Equal(t, "opus", ov.GetModel())

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude --model opus", inst.Program)
}

// A profile program that already pins --model is deduped, not double-flagged.
func TestCreateSessionFromForm_ModelOverridesProfilePin(t *testing.T) {
	dir := t.TempDir()

	h := newCreateFormHome(t)
	h.program = "claude --model haiku"
	h.appConfig.DefaultProgram = "claude --model haiku"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("opus")})

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude --model opus", inst.Program)
}

// A non-claude program gets no model plumbing at all: the field is absent from the
// form and the program string passes through untouched.
func TestCreateSessionFromForm_NonClaudeProgramUntouched(t *testing.T) {
	dir := t.TempDir()

	h := newCreateFormHome(t) // program "echo", DefaultProgram default
	h.appConfig.DefaultProgram = "echo"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	require.Equal(t, "", ov.GetModel(), "a non-claude form must expose no model override")

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "echo", inst.Program)
}

// An empty title keeps the form open (cleared submit flag) and surfaces an error rather
// than creating a half-formed session.
func TestCreateSessionFromForm_EmptyTitleKeepsFormOpen(t *testing.T) {
	h := newCreateFormHome(t)
	h.newSessionPath, _ = os.Getwd()
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
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

// A mode picked in the form's Permissions field is composed into the persisted
// program string, so launch, pause/resume, and the daemon all see it with no
// extra plumbing. Stops: [directory, branch, title, prompt, model, mode, enter].
func TestCreateSessionFromForm_PermissionModeComposedIntoProgram(t *testing.T) {
	dir := t.TempDir() // direct (non-git) target, hermetic

	h := newCreateFormHome(t)
	h.program = "claude"
	h.appConfig.DefaultProgram = "claude"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // title → prompt
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // prompt → model
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // model (empty → advance) → mode
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default → plan
	require.Equal(t, "plan", ov.GetPermissionMode())

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude --permission-mode plan", inst.Program)
}

// A profile program that already pins --permission-mode is deduped, not
// double-flagged.
func TestCreateSessionFromForm_PermissionModeOverridesProfilePin(t *testing.T) {
	dir := t.TempDir()

	h := newCreateFormHome(t)
	h.program = "claude --permission-mode acceptEdits"
	h.appConfig.DefaultProgram = "claude --permission-mode acceptEdits"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default → plan

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude --permission-mode plan", inst.Program)
}

// The default chip leaves the program untouched — including an existing
// profile pin, which "default" deliberately does not clear (it means
// "inherit": don't clobber deliberate config).
func TestCreateSessionFromForm_DefaultModeChipLeavesProgramUntouched(t *testing.T) {
	dir := t.TempDir()

	h := newCreateFormHome(t)
	h.program = "claude"
	h.appConfig.DefaultProgram = "claude"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	require.Equal(t, "", ov.GetPermissionMode())

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude", inst.Program)
}

// Model and mode compose together onto one program string.
func TestCreateSessionFromForm_ModelAndModeCompose(t *testing.T) {
	dir := t.TempDir()

	h := newCreateFormHome(t)
	h.program = "claude"
	h.appConfig.DefaultProgram = "claude"
	h.newSessionPath = dir
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov

	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // title → prompt
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // prompt → model
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("opus")})
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})   // "opus" is a complete alias → advance to mode
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default → plan

	require.NotNil(t, h.createSessionFromForm(""))

	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	assert.Equal(t, "claude --model opus --permission-mode plan", inst.Program)
}
