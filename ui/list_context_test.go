package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memInstanceStore is a minimal in-memory config.InstanceStorage, so these tests
// can rehydrate instances without touching the user's state.json.
type memInstanceStore struct{ data json.RawMessage }

func (s *memInstanceStore) SaveInstances(b json.RawMessage) error {
	s.data = append([]byte(nil), b...)
	return nil
}
func (s *memInstanceStore) GetInstances() json.RawMessage {
	if s.data == nil {
		return []byte("[]")
	}
	return s.data
}
func (s *memInstanceStore) DeleteAllInstances() error {
	s.data = []byte("[]")
	return nil
}

// startedInstances rehydrates direct (non-git) instances on a shared path that
// report Started() == true — what the suppression guard keys on, and what
// session.NewInstance alone cannot produce, since a fresh instance has not
// started and starting one for real needs a git repo and a mocked PTY.
//
// Three fixture choices, each load-bearing:
//
//   - The production load path (Storage.LoadInstances → reattach) marks them
//     started, rather than reaching into unexported state — so they are started
//     for the same reason real restored sessions are.
//   - Paused is what makes that hermetic: reattach's paused branch marks the
//     instance started and returns without touching tmux. It is also faithful,
//     since a paused neighbour still occupies the shared project dir.
//   - Direct is what makes them collide. A direct session has no worktree, so
//     WorkingDir() is its Path — which is exactly the live fleet's collision
//     (two direct sessions on one qspace checkout). A worktree-backed fixture
//     would need a real worktree path and could not share one anyway.
func startedInstances(t *testing.T, path string, titles ...string) []*session.Instance {
	t.Helper()
	data := make([]session.InstanceData, len(titles))
	for i, title := range titles {
		data[i] = session.InstanceData{
			Title:   title,
			Path:    path,
			Program: "claude",
			Status:  session.Paused,
			Direct:  true,
		}
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)

	store, err := session.NewStorage(&memInstanceStore{data: raw})
	require.NoError(t, err)
	loaded, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, loaded, len(titles))
	for _, inst := range loaded {
		require.Truef(t, inst.Started(), "fixture %q must be started for the guard to apply", inst.Title)
		require.NotEmptyf(t, inst.WorkingDir(), "fixture %q must have a working directory to collide on", inst.Title)
		inst.SetUsageMeta(transcript.Usage{ContextTokens: 521_300, Model: "claude-opus-5"},
			transcript.Stamp{Path: "x", Size: 1})
	}
	return loaded
}

// contextRow renders one row at width 80 under the unicode theme with a context
// reading applied, and returns it ANSI-stripped.
func contextRow(t *testing.T, mode string, u transcript.Usage) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, contextIndicator: mode}
	r.setWidth(80)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "auth-refactor", Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetUsageMeta(u, transcript.Stamp{Path: "x", Size: 1})
	return ansi.Strip(r.Render(inst, 1, false, false))
}

// TestRender_ContextChipModes proves the chip reaches the row in each mode —
// the model/config plumbing can be right while the render call is not, and only
// this level sees that.
func TestRender_ContextChipModes(t *testing.T) {
	u := transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"}

	assert.Contains(t, contextRow(t, contextModePercent, u), "28%")
	assert.Contains(t, contextRow(t, contextModeCount, u), "283k")
	assert.Contains(t, contextRow(t, contextModeBar, u), "▃")

	off := contextRow(t, contextModeOff, u)
	assert.NotContains(t, off, "28%")
	assert.NotContains(t, off, "283k")

	// The zero value is what every directly-constructed renderer carries, so it
	// must mean the documented default rather than a silent fifth mode.
	assert.Contains(t, contextRow(t, "", u), "28%")
}

// TestRender_ContextChipAbsentWithoutAReading is acceptance criterion 2 at the
// row level: a session with nothing to report renders no chip, no zero, and the
// same two lines it always did.
func TestRender_ContextChipAbsentWithoutAReading(t *testing.T) {
	out := contextRow(t, contextModePercent, transcript.Usage{})
	assert.NotContains(t, out, "%")
	assert.NotContains(t, out, "0k")
	require.Len(t, strings.Split(out, "\n"), 2, "no reading must not add or remove a line")
}

// TestRender_ContextChipUnknownModelDegradesOnTheRow carries acceptance
// criterion 8 all the way to the rendered row, with an invented model id. The
// unit test on contextChip covers the same rule; this one covers the path a user
// actually sees, so a renderer that reached past contextChip for its own
// percentage could not pass both.
func TestRender_ContextChipUnknownModelDegradesOnTheRow(t *testing.T) {
	out := contextRow(t, contextModePercent, transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-99"})
	assert.Contains(t, out, "283k", "an unknown model must render a count on the row")
	assert.NotContains(t, out, "%", "…and never a percentage")
}

// TestList_ContextChipSuppressedForSharedWorkingDir is acceptance criterion 4.
//
// Two started sessions sharing a working directory resolve to the same Claude
// project dir, so newest-mtime picks arbitrarily among their transcripts and
// both rows would show one session's number. The live fleet has exactly this
// shape today (two direct sessions on /home/zvi/quantivly/qspace), which is why
// the guard is not hypothetical.
//
// Driven through List.String rather than the renderer, because String is where
// the fleet-wide set is computed and a renderer-only test would pass against a
// version that never computes it.
func TestList_ContextChipSuppressedForSharedWorkingDir(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	dir := t.TempDir()
	pair := startedInstances(t, dir, "solo", "neighbour")
	require.Equal(t, pair[0].WorkingDir(), pair[1].WorkingDir(),
		"the fixtures must actually collide for this test to mean anything")

	// One session on the directory: the chip shows.
	lone := NewList(newSpinner())
	lone.AddInstance(pair[0])
	lone.SetSize(80, 20)
	require.Contains(t, ansi.Strip(lone.String()), "52%",
		"a session alone on its working directory keeps its chip")

	// A second started session on the same directory: both chips go.
	shared := NewList(newSpinner())
	shared.AddInstance(pair[0])
	shared.AddInstance(pair[1])
	shared.SetSize(80, 20)
	out := ansi.Strip(shared.String())
	assert.NotContains(t, out, "52%",
		"two started sessions sharing a working directory must show no chip — an absent chip beats a confident wrong one")
	assert.NotContains(t, out, "521k", "…and must not fall back to a count either")
}

// TestSharedWorkingDirs_CountsPausedSessions pins that a paused session still
// participates in the collision. It has no worktree on disk, but its transcript
// is still in the shared project dir and is as likely as not to be the
// newest-mtime one — so excluding it would restore the exact wrong reading the
// guard exists to prevent.
func TestSharedWorkingDirs_CountsPausedSessions(t *testing.T) {
	pair := startedInstances(t, t.TempDir(), "running", "paused")
	require.True(t, pair[1].Paused(), "the fixture must be paused for this test to mean anything")

	shared := sharedWorkingDirs(pair)
	assert.True(t, shared[pair[0].WorkingDir()],
		"a paused neighbour still poisons the shared project dir")
}

// TestSharedWorkingDirs_IgnoresUnstartedSessions is the other direction: an
// unstarted session has never written a transcript, so it cannot collide with
// anything and must not suppress a real session's chip.
func TestSharedWorkingDirs_IgnoresUnstartedSessions(t *testing.T) {
	dir := t.TempDir()
	started := startedInstances(t, dir, "started")
	unstarted, err := session.NewInstance(session.InstanceOptions{
		Title: "unstarted", Path: dir, Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	require.False(t, unstarted.Started(), "the fixture must be unstarted for this test to mean anything")
	require.Equal(t, started[0].WorkingDir(), unstarted.WorkingDir(),
		"the two must share a directory, or this test would pass for the wrong reason")

	assert.Empty(t, sharedWorkingDirs([]*session.Instance{started[0], unstarted}),
		"an unstarted session has no transcript and cannot collide")
}

func newSpinner() *spinner.Model {
	s := spinner.New()
	return &s
}

// TestRender_ContextChipFitsTheEightyColumnRow is acceptance criterion 7, and the
// evidence behind the placement decision: line 1's right cluster already carries
// account, AUTO, model, effort and permission before this chip is added.
//
// It asserts three things a green suite would otherwise miss. Exact width, because
// composeLine pads to the column count and an overflow shows up as a wrap the
// height budget cannot see. A floor on the surviving name, because "fits" is
// trivially satisfiable by truncating the name to nothing — that is the failure
// the chip could actually cause, and the number is the point of the argument. And
// the chip's own presence, so a row that "fit" by silently dropping it fails.
//
// t.Log prints the row so `go test -v` shows what the arithmetic describes: the
// column math can be right while the chip lands in the wrong place, and only
// looking at the row catches that.
func TestRender_ContextChipFitsTheEightyColumnRow(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const (
		width        = 80
		name         = "context-window-chip"
		minNameCells = 19 // the whole of `name`; see the budget note below
	)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(width)

	inst, err := session.NewInstance(session.InstanceOptions{Title: name, Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetClaudeAccount("quantivly", "/home/x/.claude-quantivly", false)
	inst.AutoYes = true
	inst.SetModelMeta("claude-opus-5", transcript.Stamp{Path: "m", Size: 1})
	inst.SetEffortMeta("max")
	inst.SetModeMeta("acceptEdits")
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "u", Size: 1})

	raw := r.Render(inst, 1, false, false)
	plain := ansi.Strip(raw)
	t.Logf("AC-7 row at %d columns (ANSI stripped):\n%s", width, plain)

	for i, line := range strings.Split(plain, "\n") {
		require.Equalf(t, width, ansi.StringWidth(line), "line %d must be exactly %d cells", i, width)
	}

	line1 := strings.Split(plain, "\n")[0]
	for _, want := range []string{"quantivly", "AUTO", "opus 5", "max", "accept-edits", "28%"} {
		require.Containsf(t, line1, want, "line 1 must carry %q", want)
	}
	require.Containsf(t, line1, name,
		"the name must survive un-truncated: the chip is supposed to cost slack, not the name")
	require.GreaterOrEqual(t, len(name), minNameCells)

	// The chip rides just inside the agent icon, after the brand-coloured
	// model/effort/permission phrase — asserted by position, because "the chip is
	// somewhere on the row" would pass with it wedged between the model and the
	// effort, which is what the placement argument rejects.
	require.Greater(t, strings.Index(line1, "28%"), strings.Index(line1, "accept-edits"),
		"the context chip must follow the permission chip, not interrupt the brand-coloured phrase")
}
