package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
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
	loaded, _, err := storage.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, loaded, len(specs))
	for _, inst := range loaded {
		require.Truef(t, inst.Started(), "fixture %q must be started for the policy to see it", inst.Title)
	}
	return loaded
}

// allow is newUsagePolicy(an occupancy mode) + allowsContext, for the common
// "does this session get to read?" question.
func allow(instances []*session.Instance) []bool {
	p := newUsagePolicy(config.ContextIndicatorPercent, instances)
	out := make([]bool, len(instances))
	for i, inst := range instances {
		out[i] = p.allowsContext(inst)
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

	p := newUsagePolicy(config.ContextIndicatorPercent, []*session.Instance{started[0], unstarted})
	assert.True(t, p.allowsContext(started[0]))
	assert.False(t, p.allowsContext(unstarted), "an unstarted session has nothing to read")
}

// TestUsagePolicyOffReadsNothing is the efficiency half. UsageInfo has exactly
// one consumer, so with the chip switched off every reading is a directory walk
// per session per tick taken for a value nothing displays.
func TestUsagePolicyOffReadsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fleet := startedFixture(t, fixtureSpec{title: "a", path: t.TempDir()})
	assert.False(t, newUsagePolicy(config.ContextIndicatorOff, fleet).allowsContext(fleet[0]))
	assert.True(t, newUsagePolicy(config.ContextIndicatorPercent, fleet).allowsContext(fleet[0]),
		"…and the same session reads normally once the chip is on")
}

// TestUsagePolicyZeroValueAllowsNothing pins the safe default: a caller that
// forgot to build a policy reads nothing, rather than reading everything.
func TestUsagePolicyZeroValueAllowsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fleet := startedFixture(t, fixtureSpec{title: "a", path: t.TempDir()})
	assert.False(t, usagePolicy{}.allowsContext(fleet[0]))
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
	require.False(t, newUsagePolicy(config.ContextIndicatorPercent, pair).allowsContext(pair[0]))
	pair[0].ClearUsage()

	assert.Zero(t, pair[0].UsageInfo().ContextTokens,
		"a suppressed session must hold no reading, or killing the neighbour resurrects it")
}

// TestUsagePolicyCountsPausedNeighbours pins that a paused session still
// participates in the collision. Its worktree is gone, but WorkingDir() keeps
// returning that path across a pause, and — more to the point — the transcripts
// it wrote are still on disk and still as likely as not to be the newest-mtime
// file in the directory. Excluding it would restore the exact wrong reading the
// guard exists to prevent.
func TestUsagePolicyCountsPausedNeighbours(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	pair := startedFixture(t,
		fixtureSpec{title: "running", path: dir},
		fixtureSpec{title: "paused", path: dir})
	require.True(t, pair[1].Paused(), "the fixture must be paused for this test to mean anything")
	require.NotEmpty(t, pair[1].ContextSourceKey(),
		"a paused session still names a transcript directory")

	assert.False(t, newUsagePolicy(config.ContextIndicatorPercent, pair).allowsContext(pair[0]),
		"a paused neighbour still spoils the shared project dir")
}

// usageFleetInstance builds a STARTED direct session on `path` whose transcripts
// resolve under `root`, driven by a fake pane so Poll() answers without tmux.
// Same shape as newKeeperInstance, but the caller supplies the path and the root
// because the whole point of these tests is two sessions sharing them.
func usageFleetInstance(t *testing.T, name, path, root string) *session.Instance {
	t.Helper()
	fake := &fakeKeeperPane{}
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: name, Path: path, Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	inst.SetTmuxSession(tmux.NewSessionWithDeps(
		context.Background(), name, "claude", &keeperPtyFactory{t: t, exec: fake.exec()}, fake.exec()))
	require.NoError(t, inst.Start(true))
	inst.SetClaudeAccount("work", root, false)
	return inst
}

// writeUsageTranscript puts one usage-bearing assistant entry in the Claude
// project directory for workDir under root.
func writeUsageTranscript(t *testing.T, root, workDir string, tokens int) {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, workDir)
	dest := filepath.Join(root, "projects", sanitized, "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5",` +
		`"usage":{"input_tokens":` + strconv.Itoa(tokens) + `,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0},"content":[{"type":"text","text":"hi"}]}}` + "\n"
	require.NoError(t, os.WriteFile(dest, []byte(line), 0o644))
}

// TestMetadataTick_ReadsThenSuppressesTheContextChip is the end-to-end assertion
// the unit tests around it cannot make: the real tick path, collectMetadata
// off-thread into applyMetadataResults on the main thread, with a real transcript
// on disk and a real fleet in the list.
//
// It exists because every piece of this was individually correct in the first
// draft and the composition was still wrong — the suppression lived in the row
// renderer, so the reading was computed, stored, and merely hidden. Asserting
// newUsagePolicy in isolation, or calling ClearUsage by hand, would both have
// passed against that. Only driving the two halves together says whether the
// instance ends the tick holding a number.
//
// Both directions are asserted, and the first is load-bearing: a policy that
// suppressed everything would satisfy the second on its own.
func TestMetadataTick_ReadsThenSuppressesTheContextChip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}

	root, dir := t.TempDir(), t.TempDir()
	writeUsageTranscript(t, root, dir, 283_000)

	solo := usageFleetInstance(t, "solo", dir, root)
	h.list.AddInstance(solo)()

	// Alone on the directory: the tick reads, and the instance ends up holding it.
	results := collectMetadata(h.ctx, []*session.Instance{solo}, nil, false, h.usagePolicy())
	require.Len(t, results, 1)
	require.NotEqual(t, tmux.PaneDead, results[0].state, "precondition: the fake pane must be alive")
	require.True(t, results[0].usageOK, "a readable transcript must yield a result to apply")
	require.False(t, results[0].usageClear)
	h.applyMetadataResults(results, false)
	require.Equal(t, 283_000, solo.UsageInfo().ContextTokens,
		"the tick must store the reading on the instance, not merely compute it")

	// A second started session lands on the same directory. Same tick path, and
	// this time the survivor must come out of it holding nothing.
	neighbour := usageFleetInstance(t, "neighbour", dir, root)
	h.list.AddInstance(neighbour)()
	require.Equal(t, solo.ContextSourceKey(), neighbour.ContextSourceKey(),
		"the fixtures must actually collide for this test to mean anything")

	results = collectMetadata(h.ctx, []*session.Instance{solo, neighbour}, nil, false, h.usagePolicy())
	require.Len(t, results, 2)
	for i, r := range results {
		require.Truef(t, r.usageClear, "result %d must carry the clear verdict, not just a skipped read", i)
		require.Falsef(t, r.usageOK, "result %d must not also carry a reading", i)
	}
	h.applyMetadataResults(results, false)

	assert.Zero(t, solo.UsageInfo().ContextTokens,
		"an ambiguous source must leave the instance holding nothing — hiding it in the renderer "+
			"is what let a killed neighbour's number resurface")
	assert.Zero(t, neighbour.UsageInfo().ContextTokens)
}

// TestMetadataTick_ChipOffReadsNothing carries the efficiency half through the
// same real path: with the chip switched off the tick must not merely skip the
// render, it must not take the reading at all.
func TestMetadataTick_ChipOffReadsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	h.appConfig.ContextIndicator = config.ContextIndicatorOff

	root, dir := t.TempDir(), t.TempDir()
	writeUsageTranscript(t, root, dir, 283_000)
	inst := usageFleetInstance(t, "quiet", dir, root)
	h.list.AddInstance(inst)()

	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy())
	require.Len(t, results, 1)
	require.True(t, results[0].usageClear)
	require.False(t, results[0].usageOK, "a transcript that is readable must still go unread")

	h.applyMetadataResults(results, false)
	assert.Zero(t, inst.UsageInfo().ContextTokens)
}

// writeCostTranscript puts `requests` priceable assistant entries in the Claude
// project directory for workDir under root. Each is 1M Opus 5 output tokens, so
// the expected total is $25 per request.
func writeCostTranscript(t *testing.T, root, workDir string, requests int) {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, workDir)
	dest := filepath.Join(root, "projects", sanitized, "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	var content string
	for n := range requests {
		id := strconv.Itoa(n)
		content += `{"type":"assistant","requestId":"req_` + id + `","timestamp":"2026-08-07T12:00:00Z",` +
			`"message":{"id":"msg_` + id + `","model":"claude-opus-5","content":[],` +
			`"usage":{"input_tokens":0,"output_tokens":1000000,"cache_read_input_tokens":0,` +
			`"cache_creation_input_tokens":0}}}` + "\n"
	}
	require.NoError(t, os.WriteFile(dest, []byte(content), 0o644))
}

// TestMetadataTick_ReadsThenSuppressesTheCostChip is the cost mode's half of
// TestMetadataTick_ReadsThenSuppressesTheContextChip, and it exists for the same
// reason: every piece can be individually right while the composition stores
// nothing, or stores something it must not.
//
// The suppression direction matters more here than it does for occupancy. A
// wrong occupancy reading is a momentary misstatement that the next turn
// corrects; a cumulative total attributed to the wrong session is wrong for that
// session's whole life, because nothing later in the transcript contradicts it.
func TestMetadataTick_ReadsThenSuppressesTheCostChip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	h.appConfig.ContextIndicator = config.ContextIndicatorCost

	root, dir := t.TempDir(), t.TempDir()
	writeCostTranscript(t, root, dir, 2)

	solo := usageFleetInstance(t, "solo", dir, root)
	h.list.AddInstance(solo)()

	results := collectMetadata(h.ctx, []*session.Instance{solo}, nil, false, h.usagePolicy())
	require.Len(t, results, 1)
	require.NotEqual(t, tmux.PaneDead, results[0].state, "precondition: the fake pane must be alive")
	require.True(t, results[0].costOK, "a readable transcript must yield a result to apply")
	require.False(t, results[0].costClear)
	h.applyMetadataResults(results, false)
	require.InDelta(t, 50.0, solo.CostInfo().USD, 1e-9,
		"the tick must store the estimate on the instance, not merely compute it")

	neighbour := usageFleetInstance(t, "neighbour", dir, root)
	h.list.AddInstance(neighbour)()
	require.Equal(t, solo.ContextSourceKey(), neighbour.ContextSourceKey(),
		"the fixtures must actually collide for this test to mean anything")

	results = collectMetadata(h.ctx, []*session.Instance{solo, neighbour}, nil, false, h.usagePolicy())
	require.Len(t, results, 2)
	for i, r := range results {
		require.Truef(t, r.costClear, "result %d must carry the clear verdict, not just a skipped read", i)
		require.Falsef(t, r.costOK, "result %d must not also carry an estimate", i)
	}
	h.applyMetadataResults(results, false)

	assert.Zero(t, solo.CostInfo().USD,
		"an ambiguous source must leave the instance holding nothing")
	assert.Zero(t, neighbour.CostInfo().USD)
}

// TestMetadataTick_ChipModeReadsOneThingNotBoth is the efficiency claim the
// shared column rests on, driven through the real tick path.
//
// The two readings are separate walks over the same directory, so a policy that
// took both would double the per-tick I/O for a chip that can only display one
// of them — and nothing on screen would ever show it. Asserting the CLEAR
// verdict rather than merely a zero value is what distinguishes "was not read"
// from "was read and came back empty".
func TestMetadataTick_ChipModeReadsOneThingNotBoth(t *testing.T) {
	for _, tc := range []struct {
		mode                  string
		wantContext, wantCost bool
	}{
		{config.ContextIndicatorPercent, true, false},
		{config.ContextIndicatorCount, true, false},
		{config.ContextIndicatorBar, true, false},
		{config.ContextIndicatorCost, false, true},
		{config.ContextIndicatorOff, false, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := newCreateFormHome(t)
			h.lostStrikes = map[*session.Instance]int{}
			h.appConfig.ContextIndicator = tc.mode

			root, dir := t.TempDir(), t.TempDir()
			// Both readings are available on disk, so a mode that takes the wrong one
			// succeeds rather than failing for want of data — which is exactly the
			// bug this has to be able to see.
			writeCostTranscript(t, root, dir, 2)
			inst := usageFleetInstance(t, "one", dir, root)
			h.list.AddInstance(inst)()

			results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy())
			require.Len(t, results, 1)
			assert.Equal(t, tc.wantContext, !results[0].usageClear, "context read taken")
			assert.Equal(t, tc.wantCost, !results[0].costClear, "cost read taken")
		})
	}
}
