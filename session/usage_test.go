package session

import (
	"os"
	"path/filepath"
	"strconv"
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

// TestSetUsageMetaClearsOnANewConversation is the other half of the "keep the
// last good reading" rule, and the one that decides whether the chip can lie.
//
// /clear starts a fresh transcript file, and so does resuming a paused session.
// The first tick that notices reads a file with no assistant entry yet, which is
// an empty reading — indistinguishable from the API-error case above, EXCEPT
// that the stamp names a different path. Without that distinction the row keeps
// painting the old conversation's number, in the old conversation's colour, over
// a conversation that has burned nothing.
func TestSetUsageMetaClearsOnANewConversation(t *testing.T) {
	inst := newUsageInstance(t)
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 920_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "before-clear.jsonl", Size: 10})

	// /clear: a new file, nothing said in it yet.
	inst.SetUsageMeta(transcript.Usage{}, transcript.Stamp{Path: "after-clear.jsonl", Size: 2})

	assert.Zero(t, inst.UsageInfo().ContextTokens,
		"a new transcript with no reading means a new conversation, not a stale one")
}

// TestClearUsageResetsTheMemoToo pins that clearing drops the stamp as well as
// the value. Leaving the stamp behind would leave the session memoized against
// bytes whose reading nobody kept, so the next legitimate tick would
// short-circuit and the chip would stay gone until the transcript changed again.
func TestClearUsageResetsTheMemoToo(t *testing.T) {
	inst := newUsageInstance(t)
	inst.SetUsageMeta(transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-5"},
		transcript.Stamp{Path: "t.jsonl", Size: 10})

	inst.ClearUsage()

	assert.Zero(t, inst.UsageInfo().ContextTokens)
	assert.Equal(t, transcript.Stamp{}, inst.usageStamp,
		"a cleared session must be able to re-read on the next allowed tick")
}

// usageTranscript writes one assistant entry carrying a usage object into the
// Claude project directory for workDir under root, and returns workDir.
func usageTranscript(t *testing.T, root, workDir string, tokens int) {
	t.Helper()
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5",` +
		`"usage":{"input_tokens":` + strconv.Itoa(tokens) + `,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0},"content":[{"type":"text","text":"hi"}]}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWDForTest(workDir), "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.WriteFile(dest, []byte(line), 0o644))
}

// TestComputeUsage_DirectSession runs the full extraction path without tmux, the
// way TestComputeModel_DirectSession does for the model: a started direct
// session's WorkingDir is its Path, and claudeConfigDir routes the transcript
// root — the same wiring the poll loop uses.
//
// Every negative case below is driven against a transcript that IS readable, so
// each one fails if its guard is deleted. Asserting them on a session with no
// transcript at all — the shape this test replaced — proves nothing: the
// extraction returns ok=false for the missing file either way, so the guards
// could all be removed and the suite would stay green.
func TestComputeUsage_DirectSession(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	usageTranscript(t, root, workDir, 283_000)

	inst, err := NewInstance(InstanceOptions{Title: "d", Path: workDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("work", root, false)

	usage, stamp, ok := inst.ComputeUsage()
	require.True(t, ok)
	assert.Equal(t, 283_000, usage.ContextTokens)
	assert.Equal(t, "claude-opus-5", usage.Model,
		"the denominator's model must come off the same entry as the count")

	inst.SetUsageMeta(usage, stamp)
	_, _, ok = inst.ComputeUsage()
	assert.False(t, ok, "unchanged transcript must short-circuit — this is the memo AC 5 rests on")

	// Move the transcript on before pausing, so the memo above cannot be what
	// makes the next call return false. Without this the paused assertion passes
	// against a ComputeUsage with no Paused check at all — the stamp gate answers
	// first and the guard is never reached.
	usageTranscript(t, root, workDir, 512_000)
	inst.status = Paused
	_, _, ok = inst.ComputeUsage()
	assert.False(t, ok, "a paused session is never extracted, readable transcript or not")

	inst.status = Running
	_, _, ok = inst.ComputeUsage()
	require.True(t, ok, "…and the same changed transcript IS read once it is not paused, "+
		"which is what makes the assertion above about the guard rather than the memo")
}

// TestComputeUsageSkipsUnstarted: an unstarted session has no transcript, and
// the gate must catch it before any filesystem work.
func TestComputeUsageSkipsUnstarted(t *testing.T) {
	inst := newUsageInstance(t)
	require.False(t, inst.Started())
	_, _, ok := inst.ComputeUsage()
	assert.False(t, ok)
}

// TestComputeUsageIsUnsupportedForNonClaude pins that the other agents degrade
// silently. codex, gemini and aider have no transcript adapter, so the extraction
// must report "nothing to apply" rather than an error the poll loop would log
// once per session per tick.
//
// Driven on a STARTED session over a directory that does hold a readable Claude
// transcript, so the only thing standing between codex and a reading is the
// adapter gate. A codex session pointed at a colleague's checkout would otherwise
// report that colleague's context as its own.
func TestComputeUsageIsUnsupportedForNonClaude(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	usageTranscript(t, root, workDir, 283_000)

	inst, err := NewInstance(InstanceOptions{Title: "codex", Path: workDir, Program: "codex", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("work", root, false)

	_, _, ok := inst.ComputeUsage()
	assert.False(t, ok)
	assert.Zero(t, inst.UsageInfo().ContextTokens, "a non-claude session never gets a reading")
	assert.Empty(t, inst.ContextSourceKey(),
		"…and claims no transcript directory, so it cannot suppress a claude neighbour either")
}

// TestContextSourceKeyCollapsesAliasedWorkingDirs pins the identity the fleet's
// ambiguity check keys on. Claude Code maps every non-alphanumeric rune of the
// cwd to '-', so /…/proj-a and /…/proj/a name one directory on disk — two
// sessions there read each other's transcripts while their WorkingDir() strings
// differ. The key has to be what the reader opens, not what the session calls
// itself.
func TestContextSourceKeyCollapsesAliasedWorkingDirs(t *testing.T) {
	root := t.TempDir()
	base := t.TempDir()
	dashed, nested := filepath.Join(base, "proj-a"), filepath.Join(base, "proj", "a")
	require.NoError(t, os.MkdirAll(dashed, 0o755))
	require.NoError(t, os.MkdirAll(nested, 0o755))

	key := func(path string) string {
		inst, err := NewInstance(InstanceOptions{Title: "k", Path: path, Program: "claude", Direct: true})
		require.NoError(t, err)
		inst.started = true
		inst.SetClaudeAccount("work", root, false)
		return inst.ContextSourceKey()
	}

	require.NotEqual(t, dashed, nested)
	assert.Equal(t, key(dashed), key(nested),
		"two working dirs that sanitize to one project dir must share a key")
}

// TestContextSourceKeySeparatesAccounts is the opposite direction: two sessions
// on the SAME working directory under different Claude config roots read
// different files entirely, so they must not suppress each other. A guard keyed
// on the working directory alone silently deletes both chips.
func TestContextSourceKeySeparatesAccounts(t *testing.T) {
	workDir := t.TempDir()

	key := func(root string) string {
		inst, err := NewInstance(InstanceOptions{Title: "k", Path: workDir, Program: "claude", Direct: true})
		require.NoError(t, err)
		inst.started = true
		inst.SetClaudeAccount("acct", root, false)
		return inst.ContextSourceKey()
	}

	assert.NotEqual(t, key(t.TempDir()), key(t.TempDir()),
		"same cwd, different account roots: different transcripts, no collision")
}

// TestContextSourceKeyIsEmptyForUnstarted: a session that never started has
// written nothing, so it can neither hold a reading nor spoil anyone else's.
func TestContextSourceKeyIsEmptyForUnstarted(t *testing.T) {
	assert.Empty(t, newUsageInstance(t).ContextSourceKey())
}
