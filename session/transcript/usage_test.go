package transcript

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// usageLine builds one assistant JSONL entry with the given model and the
// three input-side usage counters, so each test below states only the fields it
// is actually about.
func usageLine(model string, input, cacheRead, cacheCreate int) string {
	return fmt.Sprintf(
		`{"type":"assistant","isSidechain":false,"message":{"model":%q,"content":[{"type":"text","text":"hi"}],`+
			`"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":999}}}`+"\n",
		model, input, cacheRead, cacheCreate)
}

// TestLatestUsage_SumsTheThreeInputFields pins the formula: context occupancy is
// input + cache_read + cache_creation. output_tokens is deliberately excluded —
// it is this turn's generation, not context the next turn carries — so the 999
// in the fixture must not appear in the total.
func TestLatestUsage_SumsTheThreeInputFields(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd, usageLine("claude-opus-5", 1, 280_000, 2_879), time.Now())

	got, _, err := LatestUsage(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if want := 1 + 280_000 + 2_879; got.ContextTokens != want {
		t.Errorf("ContextTokens = %d, want %d (output_tokens must not be counted)", got.ContextTokens, want)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", got.Model)
	}
}

// TestLatestUsage_NewestRealEntryWins locks in the selection rule against the
// three shapes that must not win: an older real entry (superseded), a sidechain
// entry (a sub-agent's context, not the main thread's), and a "<synthetic>"
// entry (Claude Code's API-error placeholder).
func TestLatestUsage_NewestRealEntryWins(t *testing.T) {
	const cwd = "/home/zvi/work"
	content := usageLine("claude-opus-5", 0, 100_000, 0) + // superseded
		usageLine("claude-opus-5", 0, 300_000, 0) + // the answer
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-5","content":[{"type":"text","text":"sub"}],` +
		`"usage":{"input_tokens":0,"cache_read_input_tokens":900000,"cache_creation_input_tokens":0}}}` + "\n" +
		usageLine(syntheticModel, 0, 0, 0)
	root, _ := modelRoot(t, cwd, content, time.Now())

	got, _, err := LatestUsage(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if got.ContextTokens != 300_000 {
		t.Errorf("ContextTokens = %d, want 300000 (sidechain and <synthetic> entries must be skipped)", got.ContextTokens)
	}
}

// TestLatestUsage_SyntheticErrorEntryYieldsNoReading is the #596 acceptance
// criterion that a turn ending in an API error keeps its previous value rather
// than dropping to 0.
//
// It is asserted at this layer because this is where it is decided: a real
// <synthetic> entry carries a usage object whose every field is zero, so a
// reader that merely skipped the *model* check would still sum it to 0 and hand
// the caller a confident, wrong "no context". Returning the zero Usage instead
// is what lets SetUsageMeta leave the last good reading standing.
func TestLatestUsage_SyntheticErrorEntryYieldsNoReading(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd, usageLine(syntheticModel, 0, 0, 0), time.Now())

	got, stamp, err := LatestUsage(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if got.ContextTokens != 0 {
		t.Errorf("ContextTokens = %d, want 0 (a synthetic entry is not a reading)", got.ContextTokens)
	}
	// The stamp still advances, so the caller does not re-parse the same bytes
	// every tick waiting for a reading that will never come from these lines.
	if stamp.Path == "" || stamp.Size == 0 {
		t.Errorf("stamp must still advance on an empty reading, got %+v", stamp)
	}
}

// TestLatestUsage_EntryWithoutUsageIsSkipped covers the older/other-shaped entry
// that carries a model but no usage object at all: it must not read as a zero
// reading that clobbers a newer real one.
func TestLatestUsage_EntryWithoutUsageIsSkipped(t *testing.T) {
	const cwd = "/home/zvi/work"
	content := usageLine("claude-opus-5", 0, 250_000, 0) +
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5","content":[{"type":"text","text":"no usage"}]}}` + "\n"
	root, _ := modelRoot(t, cwd, content, time.Now())

	got, _, err := LatestUsage(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestUsage: %v", err)
	}
	if got.ContextTokens != 250_000 {
		t.Errorf("ContextTokens = %d, want 250000 (a usage-less entry carries no reading)", got.ContextTokens)
	}
}

// TestLatestUsage_StampShortCircuit pins the change-detection contract that keeps
// an idle session to one ReadDir + Stat per tick: an unchanged transcript is
// answered without re-reading the file, and an appended entry yields the new
// reading under a fresh stamp.
func TestLatestUsage_StampShortCircuit(t *testing.T) {
	const cwd = "/home/zvi/work"
	base := usageLine("claude-opus-5", 0, 120_000, 0)
	mtime := time.Now().Add(-time.Minute)
	root, path := modelRoot(t, cwd, base, mtime)

	first, stamp, err := LatestUsage(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil || first.ContextTokens != 120_000 {
		t.Fatalf("first call: usage=%+v err=%v", first, err)
	}

	again, sameStamp, err := LatestUsage(context.Background(), "claude", cwd, stamp, Options{Root: root})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if again.ContextTokens != 0 {
		t.Errorf("unchanged transcript returned a reading (%d); it must return the zero Usage", again.ContextTokens)
	}
	if !sameStamp.Equal(stamp) {
		t.Errorf("unchanged transcript must echo prev, got %+v want %+v", sameStamp, stamp)
	}

	writeFileWithMtime(t, path, base+usageLine("claude-opus-5", 0, 480_000, 0), time.Now())
	grown, newStamp, err := LatestUsage(context.Background(), "claude", cwd, stamp, Options{Root: root})
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if grown.ContextTokens != 480_000 {
		t.Errorf("ContextTokens = %d, want 480000 after an appended turn", grown.ContextTokens)
	}
	if newStamp.Equal(stamp) {
		t.Error("stamp must advance after the transcript changed")
	}
}

// TestLatestUsage_Unsupported pins that non-claude programs are refused the same
// way LatestModel refuses them, so codex/gemini/aider sessions degrade silently
// to no chip rather than to an error in the log.
func TestLatestUsage_Unsupported(t *testing.T) {
	_, _, err := LatestUsage(context.Background(), "codex", "/home/zvi/work", Stamp{}, Options{Root: t.TempDir()})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestLatestUsageCanceledContextReturnsPromptly pins the lifecycle contract: an
// already-cancelled context is reported before any filesystem I/O, so app
// shutdown unwinds an in-flight extraction instead of running it to the end.
func TestLatestUsageCanceledContextReturnsPromptly(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd, usageLine("claude-opus-5", 0, 1000, 0), time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, _, err := LatestUsage(ctx, "claude", cwd, Stamp{}, Options{Root: root})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got.ContextTokens != 0 {
		t.Errorf("a cancelled read must yield no reading, got %d", got.ContextTokens)
	}
}
