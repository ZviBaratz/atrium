package session

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/transcript"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUsageInstance(t *testing.T) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: "usage", Path: ".", Program: "claude"})
	require.NoError(t, err)
	return inst
}

// TestUsageInfoStartsAbsent pins the zero state: a session that has never been
// polled reports no reading, which is what makes the chip absent rather than 0.
func TestUsageInfoStartsAbsent(t *testing.T) {
	assert.Zero(t, newUsageInstance(t).UsageInfo().ContextTokens)
}

// TestSetUsageMetaKeepsTheLastGoodReading is the acceptance criterion that a turn
// ending in an API error keeps its number.
//
// The transcript layer refuses the all-zero <synthetic> entry and hands back an
// empty Usage under an advanced stamp; this is the half that turns that into
// "the row is unchanged". Overwriting unconditionally would drop a session that
// has burned 283k tokens to no chip at all the moment one request failed —
// visible, alarming, and wrong.
func TestSetUsageMetaKeepsTheLastGoodReading(t *testing.T) {
	inst := newUsageInstance(t)
	good := transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"}
	first := transcript.Stamp{Path: "t.jsonl", Size: 10}

	inst.SetUsageMeta(good, first)
	require.Equal(t, good, inst.UsageInfo())

	// The next tick parses a window whose only new entry is synthetic.
	advanced := transcript.Stamp{Path: "t.jsonl", Size: 20}
	inst.SetUsageMeta(transcript.Usage{}, advanced)

	assert.Equal(t, good, inst.UsageInfo(),
		"an empty reading must not clear the last known value")
	assert.Equal(t, advanced, inst.usageStamp,
		"…but the stamp must still advance, or the same bytes are re-parsed every tick")
}

// TestSetUsageMetaReplacesOnARealReading is the other half: a genuine new reading
// does overwrite, including one that went DOWN. The value is non-monotonic across
// a compaction — it drops sharply and that is the truth, not a stale high-water
// mark.
func TestSetUsageMetaReplacesOnARealReading(t *testing.T) {
	inst := newUsageInstance(t)
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 700_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "t.jsonl", Size: 10})

	compacted := transcript.Usage{ContextTokens: 40_000, Model: "claude-opus-5"}
	inst.SetUsageMeta(compacted, transcript.Stamp{Path: "t.jsonl", Size: 20})

	assert.Equal(t, compacted, inst.UsageInfo(),
		"a real reading wins even when it is lower — context genuinely drops across a compaction")
}

// TestComputeUsageSkipsUnstartedAndPaused pins the poll-path gate. Both cases
// return ok=false so applyMetadataResults writes nothing: an unstarted session
// has no transcript, and a paused one's is frozen, so re-reading either would be
// I/O spent to learn nothing.
func TestComputeUsageSkipsUnstartedAndPaused(t *testing.T) {
	inst := newUsageInstance(t)
	require.False(t, inst.Started())
	_, _, ok := inst.ComputeUsage()
	assert.False(t, ok, "an unstarted session has no transcript to read")

	inst.SetStatus(Paused)
	_, _, ok = inst.ComputeUsage()
	assert.False(t, ok, "a paused session's transcript is frozen")
}

// TestComputeUsageIsUnsupportedForNonClaude pins that the other agents degrade
// silently. codex, gemini and aider have no transcript adapter, so the extraction
// must report "nothing to apply" rather than an error the poll loop would log
// once per session per tick.
func TestComputeUsageIsUnsupportedForNonClaude(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "codex", Path: ".", Program: "codex"})
	require.NoError(t, err)
	_, _, ok := inst.ComputeUsage()
	assert.False(t, ok)
	assert.Zero(t, inst.UsageInfo().ContextTokens, "a non-claude session never gets a reading")
}
