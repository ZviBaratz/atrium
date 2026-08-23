package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFanOutHome builds a create-form home with claude + codex profiles whose create
// form targets path (a git repo for real fan-out, or a plain dir to exercise the
// direct-target guard). The form is pre-built targeting path with the title focused;
// the returned home has no instances. Start is never run, so the flow stays hermetic.
func newFanOutHome(t *testing.T, path string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.appConfig.DefaultProgram = "claude"
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	h.program = "claude"
	h.newSessionPath = path
	h.state = statePrompt
	ov := overlay.NewSessionCreateOverlay(
		h.appConfig.GetProfiles(), h.appConfig.ClaudeAccounts, []string{path}, h.program, nil)
	h.textInputOverlay = ov
	ov.FocusTitle()
	return h
}

func tabKey(h *home)   { h.handleKeyPress(keyMsg("tab")) }
func rightKey(h *home) { h.handleKeyPress(keyMsg("right")) }
func downKey(h *home)  { h.handleKeyPress(keyMsg("down")) }
func plusKey(h *home)  { h.handleKeyPress(textMsg("+")) }
func ctrlS(h *home)    { h.handleKeyPress(keyMsg("ctrl+s")) }

func instanceByTitle(h *home, title string) *session.Instance {
	for _, inst := range h.list.GetInstances() {
		if inst.Title == title {
			return inst
		}
	}
	return nil
}

// TestPlanVariantTitlesUsesTheSharedScheme ties this package's suffix search to the
// owner of the <stem>-N spelling rather than to a literal, so the create form and
// `atrium new --variants` cannot derive different names for the same batch.
//
// It asserts the derived titles equal session.VariantTitle's output, not that they look
// like "race-1": the literal is pinned once, in session's own test, and a second copy
// here would be the drift this guard exists to catch.
func TestPlanVariantTitlesUsesTheSharedScheme(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	h.newSessionGroup = filepath.Base(dir)

	titles, conflict := h.planVariantTitles("race", 3, dir, true)
	require.Empty(t, conflict)
	require.Equal(t, []string{
		session.VariantTitle("race", 1),
		session.VariantTitle("race", 2),
		session.VariantTitle("race", 3),
	}, titles)

	// The fork path numbers from 2 (the bare stem is the 1) through the same helper.
	addDirectInstance(t, h, "race", dir)
	require.Equal(t, session.VariantTitle("race", 2), h.firstFreeTitle("race", dir, true))
}

// planVariantTitles must keep the bare title for a single session (the pre-#387
// contract), derive -1/-2/-N for a batch, skip suffixes already taken, and report a
// conflict when a bare single title collides.
func TestPlanVariantTitles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	dir := t.TempDir()
	h.newSessionGroup = filepath.Base(dir)

	titles, conflict := h.planVariantTitles("solo", 1, dir, true)
	assert.Empty(t, conflict)
	assert.Equal(t, []string{"solo"}, titles, "a single session keeps the bare title")

	titles, conflict = h.planVariantTitles("race", 3, dir, true)
	assert.Empty(t, conflict)
	assert.Equal(t, []string{"race-1", "race-2", "race-3"}, titles, "a batch derives -1/-2/-3")

	// An existing -1 is skipped; the next free suffixes are used instead.
	addDirectInstance(t, h, "race-1", dir)
	titles, conflict = h.planVariantTitles("race", 3, dir, true)
	assert.Empty(t, conflict)
	assert.Equal(t, []string{"race-2", "race-3", "race-4"}, titles, "a taken suffix is skipped")

	// A colliding bare single title reports the conflict (mirrors the old inline error).
	addDirectInstance(t, h, "solo", dir)
	_, conflict = h.planVariantTitles("solo", 1, dir, true)
	assert.NotEmpty(t, conflict, "a duplicate single title must conflict")
}

// One submit with claude ×3 spawns three sessions, each with a unique derived title
// and the entered boot prompt queued (AC1/AC2).
func TestCreateForm_FanOutSpawnsNVariants(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	before := h.list.NumInstances() // 0

	typeString(h, "race")
	h.textInputOverlay.SetPrompt("do the thing")

	h.textInputOverlay.FocusVariants()
	plusKey(h) // claude 1 -> 2
	plusKey(h) // claude 2 -> 3
	ctrlS(h)

	assert.Equal(t, stateDefault, h.state, "a valid batch closes the form")
	assert.Equal(t, before+3, h.list.NumInstances(), "claude x3 spawns three sessions")

	for _, title := range []string{"race-1", "race-2", "race-3"} {
		inst := instanceByTitle(h, title)
		require.NotNil(t, inst, "variant %s must exist", title)
		texts, _ := inst.QueueView()
		require.Len(t, texts, 1, "each variant gets the boot prompt queued")
		assert.Equal(t, "do the thing", texts[0])
		assert.Equal(t, agent.KeyClaude, agent.Resolve(inst.Program).Key)
	}
}

// The max_sessions cap is enforced against the whole batch up front: an over-cap
// batch is refused as a unit (no partial spawn, form stays open with an inline
// reason), and reducing the count to a batch that fits then succeeds (AC3).
func TestCreateForm_BatchCapRejectedWhole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	maxN := 2
	h.appConfig.MaxSessions = &maxN
	before := h.list.NumInstances() // 0

	typeString(h, "race")
	h.textInputOverlay.FocusVariants()
	plusKey(h) // claude 1 -> 2
	plusKey(h) // claude 2 -> 3  (batch 3 > cap 2)
	ctrlS(h)

	assert.Equal(t, statePrompt, h.state, "an over-cap batch keeps the form open")
	assert.Equal(t, before, h.list.NumInstances(), "never a partial spawn — nothing is created")
	require.NotNil(t, h.textInputOverlay)
	assert.Contains(t, h.textInputOverlay.VariantError(), "max_sessions")
	assert.False(t, h.textInputOverlay.Submitted)

	// Reduce claude 3 -> 1 (batch 1 <= cap 2): now it fits and spawns.
	downKey(h)
	downKey(h)
	ctrlS(h)
	assert.Equal(t, stateDefault, h.state, "a fitting batch spawns")
	assert.Equal(t, before+1, h.list.NumInstances())
}

// A mixed batch applies the claude-only overrides (here --model) to the claude
// variant alone; the codex variant's program carries no such flag (AC5).
func TestCreateForm_MixedProgramsGateClaudeFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))

	typeString(h, "race")

	// claude ×1 + codex ×1.
	h.textInputOverlay.FocusVariants()
	rightKey(h) // cursor -> codex
	plusKey(h)  // codex 0 -> 1
	// Set a model override; claude is still selected, so the field is enabled.
	tabKey(h) // variants -> model
	typeString(h, "opus")
	require.Equal(t, "opus", h.textInputOverlay.GetModel(), "the model field must have taken the input")
	ctrlS(h)

	claudeVar := instanceByTitle(h, "race-1")
	codexVar := instanceByTitle(h, "race-2")
	require.NotNil(t, claudeVar, "the claude variant must exist")
	require.NotNil(t, codexVar, "the codex variant must exist")
	assert.Equal(t, agent.KeyClaude, agent.Resolve(claudeVar.Program).Key)
	assert.True(t, strings.Contains(claudeVar.Program, "opus"),
		"the claude variant carries the --model override, got %q", claudeVar.Program)
	assert.False(t, strings.Contains(codexVar.Program, "opus"),
		"the codex variant must not carry the claude-only override, got %q", codexVar.Program)
}

// A fan-out (N>1) on a direct (non-git) target is refused — the variants would share
// one directory with no worktree isolation — but a single direct session still works.
func TestCreateForm_DirectTargetRefusesFanOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, t.TempDir()) // a plain dir → direct session
	before := h.list.NumInstances()

	typeString(h, "race")
	h.textInputOverlay.FocusVariants()
	plusKey(h) // claude 1 -> 2 (fan-out on a direct target)
	ctrlS(h)

	assert.Equal(t, statePrompt, h.state, "a fan-out on a direct target is refused")
	assert.Equal(t, before, h.list.NumInstances(), "nothing is spawned")
	assert.Contains(t, h.textInputOverlay.VariantError(), "git repo")

	// A single session on the same direct target is fine.
	downKey(h) // claude 2 -> 1
	ctrlS(h)
	assert.Equal(t, stateDefault, h.state, "a single direct session is allowed")
	assert.Equal(t, before+1, h.list.NumInstances())
}
