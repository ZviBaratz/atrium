package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"

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

// startedFixture rehydrates direct (non-git) instances that report
// Started() == true — what ContextSourceKey gates on, and what
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
//     (several direct sessions on one qspace checkout). A worktree-backed
//     fixture would need a real worktree path and could not share one anyway.
//
// Each spec names a program and a path, so one call can build a fleet that
// mixes claude with codex and one checkout with another.
type fixtureSpec struct {
	title   string
	path    string
	program string
}

func startedFixture(t *testing.T, specs ...fixtureSpec) []*session.Instance {
	t.Helper()
	data := make([]session.InstanceData, len(specs))
	for i, spec := range specs {
		program := spec.program
		if program == "" {
			program = "claude"
		}
		data[i] = session.InstanceData{
			Title:   spec.title,
			Path:    spec.path,
			Program: program,
			Status:  session.Paused,
			Direct:  true,
		}
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)

	storage, err := session.NewStorage(&memInstanceStore{data: raw})
	require.NoError(t, err)
	loaded, err := storage.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, loaded, len(specs))
	for _, inst := range loaded {
		require.Truef(t, inst.Started(), "fixture %q must be started for the policy to see it", inst.Title)
	}
	return loaded
}

// allow is newUsagePolicy(enabled) + allows, for the common "does this session
// get to read?" question.
func allow(instances []*session.Instance) []bool {
	p := newUsagePolicy(true, instances)
	out := make([]bool, len(instances))
	for i, inst := range instances {
		out[i] = p.allows(inst)
	}
	return out
}

// TestUsagePolicySuppressesASharedTranscriptDir is acceptance criterion 4.
//
// Two started sessions on one working directory resolve to the same Claude
// project dir, so newest-mtime picks arbitrarily among their transcripts and
// both rows would show one session's number. The live fleet has exactly this
// shape today (several direct sessions on /home/zvi/quantivly/qspace), which is
// why the guard is not hypothetical.
func TestUsagePolicySuppressesASharedTranscriptDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	lone := startedFixture(t, fixtureSpec{title: "solo", path: dir})
	assert.Equal(t, []bool{true}, allow(lone),
		"a session alone on its working directory may read")

	pair := startedFixture(t,
		fixtureSpec{title: "solo", path: dir},
		fixtureSpec{title: "neighbour", path: dir})
	assert.Equal(t, []bool{false, false}, allow(pair),
		"two sessions on one transcript dir must both stop reading — an absent chip beats a confident wrong one")
}

// TestUsagePolicyCollapsesAliasedWorkingDirs is the case a working-directory
// comparison gets wrong, and the reason the key is the resolved project dir.
//
// Claude Code names a project directory by mapping every non-alphanumeric rune
// of the cwd to '-', so /…/proj-a and /…/proj/a are the SAME directory on disk.
// Two sessions in those two places share every transcript while their
// WorkingDir() strings differ, so a guard keyed on the raw path waves them both
// through and each row shows whichever conversation was written to last.
func TestUsagePolicyCollapsesAliasedWorkingDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	dashed := filepath.Join(base, "proj-a")
	nested := filepath.Join(base, "proj", "a")
	require.NoError(t, os.MkdirAll(dashed, 0o755))
	require.NoError(t, os.MkdirAll(nested, 0o755))

	pair := startedFixture(t,
		fixtureSpec{title: "dashed", path: dashed},
		fixtureSpec{title: "nested", path: nested})
	require.NotEqual(t, pair[0].WorkingDir(), pair[1].WorkingDir(),
		"the two working dirs must differ, or this test would pass for the wrong reason")
	require.Equal(t, pair[0].ContextSourceKey(), pair[1].ContextSourceKey(),
		"…while resolving to one transcript directory, which is the collision")

	assert.Equal(t, []bool{false, false}, allow(pair))
}

// TestUsagePolicyIgnoresSessionsThatReadNothing is the over-suppression
// direction, and it costs a correct chip rather than showing a wrong one — so it
// is the quieter half of the same bug.
//
// A codex session writes nothing under ~/.claude/projects: it has no transcript
// adapter, LatestUsage refuses it, and it can no more spoil a neighbour's
// reading than an empty directory can. A guard that counts every started session
// on the path deletes the claude session's chip permanently, with no way for the
// user to tell why.
func TestUsagePolicyIgnoresSessionsThatReadNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	mixed := startedFixture(t,
		fixtureSpec{title: "claude-side", path: dir},
		fixtureSpec{title: "codex-side", path: dir, program: "codex"})
	require.Empty(t, mixed[1].ContextSourceKey(), "a codex session reads no transcript")

	assert.Equal(t, []bool{true, false}, allow(mixed),
		"the claude session keeps its chip; the codex session never had one")
}

// TestUsagePolicyIgnoresUnstartedSessions: an unstarted session has never
// written a transcript, so it cannot collide with anything.
func TestUsagePolicyIgnoresUnstartedSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	started := startedFixture(t, fixtureSpec{title: "started", path: dir})
	unstarted, err := session.NewInstance(session.InstanceOptions{
		Title: "unstarted", Path: dir, Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	require.False(t, unstarted.Started(), "the fixture must be unstarted for this test to mean anything")
	require.Equal(t, started[0].WorkingDir(), unstarted.WorkingDir(),
		"the two must share a directory, or this test would pass for the wrong reason")

	p := newUsagePolicy(true, []*session.Instance{started[0], unstarted})
	assert.True(t, p.allows(started[0]))
	assert.False(t, p.allows(unstarted), "an unstarted session has nothing to read")
}

// TestUsagePolicyOffReadsNothing is the efficiency half. UsageInfo has exactly
// one consumer, so with the chip switched off every reading is a directory walk
// per session per tick taken for a value nothing displays.
func TestUsagePolicyOffReadsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fleet := startedFixture(t, fixtureSpec{title: "a", path: t.TempDir()})
	assert.False(t, newUsagePolicy(false, fleet).allows(fleet[0]))
	assert.True(t, newUsagePolicy(true, fleet).allows(fleet[0]),
		"…and the same session reads normally once the chip is on")
}

// TestUsagePolicyZeroValueAllowsNothing pins the safe default: a caller that
// forgot to build a policy reads nothing, rather than reading everything.
func TestUsagePolicyZeroValueAllowsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fleet := startedFixture(t, fixtureSpec{title: "a", path: t.TempDir()})
	assert.False(t, usagePolicy{}.allows(fleet[0]))
}

// TestSuppressedSessionLosesItsStoredReading is the difference between gating
// the read and hiding the chip, and it is the failure the render-layer guard
// this replaced could not prevent.
//
// A hidden reading is still in the instance. Kill the neighbour that caused the
// suppression and the survivor's row immediately paints the dead session's token
// count — worse, the stamp memo has already consumed that path/mtime/size, so
// the number stands until the survivor happens to take another turn. Clearing is
// what makes "suppressed" mean absent.
func TestSuppressedSessionLosesItsStoredReading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	pair := startedFixture(t,
		fixtureSpec{title: "survivor", path: dir},
		fixtureSpec{title: "neighbour", path: dir})

	// The survivor read a good number back when it was alone on the directory.
	good := transcript.Usage{ContextTokens: 521_300, Model: "claude-opus-5"}
	pair[0].SetUsageMeta(good, transcript.Stamp{Path: "old.jsonl", Size: 1})
	require.Equal(t, good, pair[0].UsageInfo())

	// The neighbour arrives; the tick that notices refuses the reading.
	require.False(t, newUsagePolicy(true, pair).allows(pair[0]))
	pair[0].ClearUsage()

	assert.Zero(t, pair[0].UsageInfo().ContextTokens,
		"a suppressed session must hold no reading, or killing the neighbour resurrects it")
}
