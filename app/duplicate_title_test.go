package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addDirectInstance adds a (not-started) direct session titled title in dir, so its
// group key is the directory's basename — no git needed, keeping the test hermetic.
func addDirectInstance(t *testing.T, h *home, title, dir string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: dir, Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	return inst
}

// typeString feeds each rune through the app's key handler (the focused field
// receives it — the create form opened via `n` focuses the title).
func typeString(h *home, s string) {
	for _, r := range s {
		h.handleKeyPress(textMsg(string(r)))
	}
}

// Submitting a title already used in the target's group must keep the form open
// with an inline error at the title field — not close it, not add a row, and not
// let the session die later in the background.
func TestCreateForm_IntraGroupDuplicateBlocksSubmit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	addDirectInstance(t, h, "taken", dir)
	before := h.list.NumInstances()

	pressKey(h, 'n') // contextual target = the selected instance's dir; focus on title
	require.Equal(t, statePrompt, h.state)
	typeString(h, "taken")

	require.NotNil(t, h.textInputOverlay)
	assert.NotEmpty(t, h.textInputOverlay.TitleError(),
		"the inline error must appear live, while typing")

	h.handleKeyPress(keyMsg("ctrl+s"))

	assert.Equal(t, statePrompt, h.state, "a duplicate submit must keep the form open")
	require.NotNil(t, h.textInputOverlay)
	assert.Contains(t, h.textInputOverlay.TitleError(), "already used")
	assert.False(t, h.textInputOverlay.Submitted, "Submitted must be reset so the form stays interactive")
	assert.Equal(t, before, h.list.NumInstances(), "no row may be added for a blocked submit")
}

// The same title in a different repo group is the whole point of the feature:
// the conflict probe must scope to the form's target group.
func TestTitleConflict_ScopedToGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	addDirectInstance(t, h, "taken", t.TempDir())

	h.newSessionGroup = "some-other-group"
	assert.Empty(t, h.titleConflict("taken"),
		"a same-titled session in another group is not a conflict")
}

// Distinct titles that sanitize to the same derived names are still duplicates:
// raw-title comparison would let them collide at the tmux/git layer later.
func TestTitleConflict_DerivedVariantBlocked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	addDirectInstance(t, h, "Fix Bug", dir)

	h.newSessionGroup = filepath.Base(dir)
	assert.NotEmpty(t, h.titleConflict("fixbug"),
		"\"Fix Bug\" and \"fixbug\" share a tmux segment; must be treated as duplicates")
}

// Paused sessions still own their branch and tmux name; they must count.
func TestTitleConflict_IncludesPausedSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	data := session.InstanceData{
		Title: "parked", Path: "/nonexistent/grp", Status: session.Paused, Program: "echo",
		Worktree: session.GitWorktreeData{
			RepoPath: "/nonexistent/grp", WorktreePath: "/nonexistent/wt",
			SessionName: "parked", BranchName: "zvi/parked",
		},
	}
	inst, err := session.FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	h.list.AddInstance(inst)

	h.newSessionGroup = "grp"
	assert.NotEmpty(t, h.titleConflict("parked"))
}

// A title whose qualified tmux name would equal one of another session's DERIVED
// sibling sessions — the terminal shell (<name>_term) or the run command
// (<name>_run) — is reserved: the sibling would collide even though the agent
// sessions differ. QualifiedSessionName maps a dot to an underscore, which is how
// an innocuous title reaches another session's derived name.
func TestTitleConflict_DerivedSiblingReservation(t *testing.T) {
	for _, title := range []string{"foo.term", "foo.run"} {
		t.Run(title, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := newCreateFormHome(t)
			data := session.InstanceData{
				Title: "foo", Path: "/nonexistent/grp", Status: session.Paused, Program: "echo",
				TmuxName: tmux.QualifiedSessionName("grp", "foo"),
				Worktree: session.GitWorktreeData{
					RepoPath: "/nonexistent/grp", WorktreePath: "/nonexistent/wt",
					SessionName: "foo", BranchName: "zvi/foo",
				},
			}
			inst, err := session.FromInstanceData(context.Background(), data, "zvi/")
			require.NoError(t, err)
			h.list.AddInstance(inst)

			h.newSessionGroup = "grp"
			assert.NotEmpty(t, h.titleConflict(title),
				"sanitizes to one of session foo's derived sibling session names")
		})
	}
}

// And the other direction: an EXISTING session whose own tmux name is the derived
// sibling of the name a new title would mint. Here the candidate is the parent,
// which is the half a `cand+suffix == name` check alone would miss.
func TestTitleConflict_DerivedSiblingReservationBothDirections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	data := session.InstanceData{
		Title: "foo.run", Path: "/nonexistent/grp", Status: session.Paused, Program: "echo",
		TmuxName: tmux.QualifiedSessionName("grp", "foo.run"),
		Worktree: session.GitWorktreeData{
			RepoPath: "/nonexistent/grp", WorktreePath: "/nonexistent/wt",
			SessionName: "foo-run", BranchName: "zvi/foo-run",
		},
	}
	inst, err := session.FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	h.list.AddInstance(inst)

	h.newSessionGroup = "grp"
	assert.NotEmpty(t, h.titleConflict("foo"),
		"session foo would host its run command at foo_run, which is foo.run's own name")
}

// The async branch-existence verdict surfaces as the same inline error, and a
// stale result (title or path moved on) must be dropped, not applied.
func TestTitleCheckResult_AppliesAndDropsStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	// Pin a prefix that cannot equal the machine's username-derived default:
	// the verdict-matching below must hold for any prefix, not just the one
	// this test happens to run under.
	h.appConfig.BranchPrefix = "elsewhere/"
	dir := t.TempDir()
	addDirectInstance(t, h, "other", dir)

	pressKey(h, 'n')
	require.Equal(t, statePrompt, h.state)
	typeString(h, "mywork")

	// Derive the branch exactly as runTitleCheck does: titleConflict only honors
	// a verdict whose branch matches the current title's derived slug, so a
	// hardcoded literal would only pass on machines whose username matches it.
	branch := git.BranchNameForSession(h.appConfig.BranchPrefix, "mywork")
	h.Update(titleCheckResultMsg{title: "mywork", path: h.newSessionPath, branch: branch, exists: true})
	assert.Contains(t, h.textInputOverlay.TitleError(), "branch",
		"a fresh verdict for the current title must surface inline")

	h.textInputOverlay.SetTitleError("")
	h.Update(titleCheckResultMsg{title: "stale-title", path: h.newSessionPath, branch: branch, exists: true})
	assert.Empty(t, h.textInputOverlay.TitleError(), "a verdict for an abandoned title must be dropped")
}
