package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/session/transcript"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// costInstance builds a started, direct claude session rooted at workDir whose
// transcripts live under the fake config root — the same shape the
// ContextSourceKey tests use, because the cost reader keys on the same identity.
func costInstance(t *testing.T, workDir, root string) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: "cost", Path: workDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("acct", root, false)
	return inst
}

// writeCostTranscript drops one priceable assistant entry per requested request
// into the project directory workDir resolves to. Every entry is 1M Opus 5
// output tokens, so the expected total is $25 per request and the arithmetic
// stays checkable by eye.
func writeCostTranscript(t *testing.T, root, workDir, name string, requests int) {
	t.Helper()
	dir := transcript.ProjectDir("claude", workDir, transcript.Options{Root: root})
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	var content string
	for n := range requests {
		content += fmt.Sprintf(
			`{"type":"assistant","requestId":"req_%s_%d","timestamp":"2026-08-07T12:00:00Z",`+
				`"message":{"id":"msg_%s_%d","model":"claude-opus-5","content":[],`+
				`"usage":{"input_tokens":0,"output_tokens":1000000,"cache_read_input_tokens":0,`+
				`"cache_creation_input_tokens":0}}}`+"\n", name, n, name, n)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(content), 0o644))
}

// TestCostInfoStartsAbsent pins the zero state: a session that has never been
// polled reports nothing, which is what makes the chip absent rather than $0.00.
func TestCostInfoStartsAbsent(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "cost", Path: ".", Program: "claude"})
	require.NoError(t, err)
	assert.Zero(t, inst.CostInfo().USD)
	assert.False(t, inst.CostInfo().Partial())
}

// TestSetCostMetaStoresTheCursorSoTheNextReadResumes is the assertion that has
// to be behavioural, because the obvious version of it proves nothing.
//
// Checking that a second ComputeCost returns the same total would pass whether
// or not the cursor was stored — without one the reader simply re-reads the file
// and arrives at the same answer, which is the bug (AC#3's per-tick cost) rather
// than the fix. So the transcript is made unreadable between the two calls: a
// reader that resumed from the stored cursor never opens it and succeeds, and a
// reader that restarted fails outright.
//
// The zero-cost case is included because it is the one a "did the number stick?"
// assertion cannot see at all: a directory holding nothing priceable yet must
// still remember how far it got, or it re-reads every byte on every tick forever
// while reporting a perfectly correct $0.
func TestSetCostMetaStoresTheCursorSoTheNextReadResumes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requests int
		wantUSD  float64
	}{
		{"a priceable transcript", 2, 50.0},
		{"a transcript with nothing priceable in it", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, workDir := t.TempDir(), t.TempDir()
			inst := costInstance(t, workDir, root)
			writeCostTranscript(t, root, workDir, "conv", tc.requests)

			cost, cursor, ok := inst.ComputeCost()
			require.True(t, ok)
			require.Equal(t, tc.requests, cost.Requests)
			inst.SetCostMeta(cost, cursor)
			require.InDelta(t, tc.wantUSD, inst.CostInfo().USD, 1e-9)

			dir := transcript.ProjectDir("claude", workDir, transcript.Options{Root: root})
			path := filepath.Join(dir, "conv.jsonl")
			require.NoError(t, os.Chmod(path, 0o000))
			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

			again, cursor, ok := inst.ComputeCost()
			require.True(t, ok, "an unchanged transcript must be resumed past, not reopened")
			inst.SetCostMeta(again, cursor)
			assert.InDelta(t, tc.wantUSD, inst.CostInfo().USD, 1e-9)
			assert.Equal(t, tc.requests, inst.CostInfo().Requests)
		})
	}
}

// TestCostAccumulatesAcrossConversations is the scoping decision made visible: a
// session's cost covers its whole project directory, so /clear — which starts a
// new transcript file beside the old one — adds to the total instead of resetting
// it.
//
// This is the deliberate difference from SetUsageMeta, which CLEARS on a new
// transcript path. Occupancy is scoped to one conversation; spend is scoped to
// the session.
func TestCostAccumulatesAcrossConversations(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	inst := costInstance(t, workDir, root)

	writeCostTranscript(t, root, workDir, "first", 1)
	cost, cursor, ok := inst.ComputeCost()
	require.True(t, ok)
	inst.SetCostMeta(cost, cursor)
	require.InDelta(t, 25.0, inst.CostInfo().USD, 1e-9)

	// /clear: a second conversation in the same directory.
	writeCostTranscript(t, root, workDir, "second", 1)
	cost, cursor, ok = inst.ComputeCost()
	require.True(t, ok)
	inst.SetCostMeta(cost, cursor)

	assert.InDelta(t, 50.0, inst.CostInfo().USD, 1e-9,
		"a new conversation adds to the session's spend rather than replacing it")
}

// TestClearCostDropsTheCursorToo asserts the cursor field directly, and does so
// deliberately rather than for want of a behavioural test.
//
// The behavioural version does not exist, because dropping the cursor changes no
// observable answer: the cursor carries each file's dollar subtotal, so a
// retained one resumes to the SAME correct total that a discarded one rebuilds.
// What the drop buys is the invariant — a session told not to hold an estimate
// must not be holding one in a second field — and the only way to state that is
// to look at the field. An assertion on CostInfo() alone would pass over a
// ClearCost that cleared nothing but the visible half.
//
// The rebuild is asserted too, because "cleared" must not mean "broken".
func TestClearCostDropsTheCursorToo(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	inst := costInstance(t, workDir, root)

	writeCostTranscript(t, root, workDir, "conv", 2)
	cost, cursor, ok := inst.ComputeCost()
	require.True(t, ok)
	inst.SetCostMeta(cost, cursor)
	require.InDelta(t, 50.0, inst.CostInfo().USD, 1e-9)
	require.NotZero(t, inst.costCursor, "the fixture must leave a cursor to clear")

	inst.ClearCost()
	assert.Zero(t, inst.CostInfo().USD)
	assert.Zero(t, inst.costCursor,
		"the cursor carries per-file dollar subtotals, so leaving it behind leaves the "+
			"estimate behind — the session would still hold what it was told to drop")

	rebuilt, _, ok := inst.ComputeCost()
	require.True(t, ok)
	assert.InDelta(t, 50.0, rebuilt.USD, 1e-9,
		"a cleared session must be able to rebuild the total from scratch")
}

// TestComputeCostRefusesSessionsThatCannotHaveOne pins the three refusals. Each
// returns ok=false, which the poll layer treats as "nothing to apply" — the
// session keeps whatever it had rather than being handed a zero.
func TestComputeCostRefusesSessionsThatCannotHaveOne(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()

	t.Run("unstarted", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "c", Path: workDir, Program: "claude", Direct: true})
		require.NoError(t, err)
		_, _, ok := inst.ComputeCost()
		assert.False(t, ok)
	})

	t.Run("paused", func(t *testing.T) {
		inst := costInstance(t, workDir, root)
		inst.SetStatus(Paused)
		_, _, ok := inst.ComputeCost()
		assert.False(t, ok, "a paused session's poll goroutine never visits it anyway")
	})

	t.Run("a non-claude program", func(t *testing.T) {
		inst, err := NewInstance(InstanceOptions{Title: "c", Path: workDir, Program: "codex", Direct: true})
		require.NoError(t, err)
		inst.started = true
		_, _, ok := inst.ComputeCost()
		assert.False(t, ok, "codex/gemini/aider must degrade to no chip, not to a wrong one")
	})
}

// TestComputeCostSucceedsWithNothingSpentYet: a started claude session that has
// not talked to the model has no project directory at all, and that is a valid
// answer rather than a failure. Reporting it as an error would make the poll
// layer keep a stale total on a session that has been reset.
func TestComputeCostSucceedsWithNothingSpentYet(t *testing.T) {
	inst := costInstance(t, t.TempDir(), t.TempDir())

	cost, _, ok := inst.ComputeCost()
	assert.True(t, ok, "an absent project directory is zero spend, not a read failure")
	assert.Zero(t, cost.USD)
	assert.Zero(t, cost.Requests)
}
