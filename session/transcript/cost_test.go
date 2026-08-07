package transcript

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// costLine builds one assistant JSONL entry carrying a full modern usage object.
// Tests state only the fields they are about; everything else is fixed here so a
// fixture cannot accidentally differ in a way the test does not mention.
//
// The 5m/1h split is explicit because the two tiers price differently, and
// cache_creation_input_tokens is written as their sum, which is what Claude Code
// does and what costTokens relies on when it prefers the split.
func costLine(id, requestID, model string, in, out, cacheRead, w5m, w1h int) string {
	return fmt.Sprintf(
		`{"type":"assistant","requestId":%q,"timestamp":"2026-08-07T12:00:00Z","message":{"id":%q,"model":%q,`+
			`"content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":%d,"output_tokens":%d,`+
			`"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,`+
			`"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d},`+
			`"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},`+
			`"service_tier":"standard","inference_geo":"not_available","speed":"standard"}}}`+"\n",
		requestID, id, model, in, out, cacheRead, w5m+w1h, w5m, w1h)
}

// costRoot builds a fake claude config root holding one main transcript for cwd.
func costRoot(t *testing.T, cwd, content string) (root, path string) {
	t.Helper()
	root = t.TempDir()
	path = filepath.Join(root, "projects", sanitizeCWD(cwd), "session.jsonl")
	writeFileWithMtime(t, path, content, time.Now())
	return root, path
}

// readCost is the one-shot form: a cold read with no cursor.
func readCost(t *testing.T, cwd, root string) Cost {
	t.Helper()
	cost, _, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost: %v", err)
	}
	return cost
}

// closeEnough compares two dollar figures. Every expectation in this file is
// hand-computed from published rates, so the tolerance is for float arithmetic
// only, not for disagreement about the answer.
func closeEnough(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// TestLatestCost_PricesEveryTokenCategory pins the whole formula on one entry
// whose counts are chosen so each term is a round number of dollars at Opus 5's
// published $5/$25: input 1M = $5, output 1M = $25, cache read 1M = $0.50, a 5m
// write 1M = $6.25, a 1h write 1M = $10.
//
// It is one assertion over six terms on purpose: a per-term test lives in
// session/agent/pricing_test.go, and what THIS test adds is that the reader
// routes each JSON field to the term that prices it. A transposed cache tier is
// invisible to the pricing package and visible here.
func TestLatestCost_PricesEveryTokenCategory(t *testing.T) {
	const cwd = "/home/zvi/work"
	const m = 1_000_000
	root, _ := costRoot(t, cwd, costLine("msg_1", "req_1", "claude-opus-5", m, m, m, m, m))

	got := readCost(t, cwd, root)
	want := 5.0 + 25.0 + 0.50 + 6.25 + 10.0
	if !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v", got.USD, want)
	}
	if got.Requests != 1 {
		t.Errorf("Requests = %d, want 1", got.Requests)
	}
	if got.Partial() {
		t.Error("Partial() = true, want false — every entry was priceable")
	}
}

// TestLatestCost_CountsOneRequestNotOneLine is the dedup guard, and it is the
// difference between a believable number and a useless one.
//
// Claude Code writes one line per content block of a single API response, each
// carrying a full copy of the same usage object. The fixture is that shape:
// three lines, one (message.id, requestId), one real request. Measured on real
// transcripts the naive sum overstates by 1.78x-2.46x depending on the category,
// so this is not a rounding concern.
//
// The second request follows immediately, which is also the case that proves the
// dedup is keyed rather than a blanket "skip repeats": it must be counted.
func TestLatestCost_CountsOneRequestNotOneLine(t *testing.T) {
	const cwd = "/home/zvi/work"
	dup := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	content := dup + dup + dup + costLine("msg_2", "req_2", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	root, _ := costRoot(t, cwd, content)

	got := readCost(t, cwd, root)
	if got.Requests != 2 {
		t.Errorf("Requests = %d, want 2 — three lines of one response are one request", got.Requests)
	}
	if want := 50.0; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v (naive line-summing would give %v)", got.USD, want, 100.0)
	}
}

// TestLatestCost_WalksTheSubagentTree covers the finding that reshaped this
// feature: sub-agent transcripts live in a directory newestTranscript never
// descends into, and on a measured session they carried 54% of the requests.
//
// The fixture reproduces all three levels Claude Code actually writes — the main
// transcript, <uuid>/subagents/, and the nested <uuid>/subagents/workflows/wf_*/
// that a non-recursive glob would miss — and asserts every one of them is in the
// total. It also gives a sub-agent entry isSidechain:true, because that is what
// they carry and because decodeUsage's rule for the same flag is the opposite
// one: occupancy must skip a sidechain, cost must bill it.
func TestLatestCost_WalksTheSubagentTree(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	dir := filepath.Join(root, "projects", sanitizeCWD(cwd))
	now := time.Now()

	writeFileWithMtime(t, filepath.Join(dir, "conv.jsonl"),
		costLine("msg_main", "req_main", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)

	sidechain := `{"type":"assistant","isSidechain":true,"requestId":"req_sub","timestamp":"2026-08-07T12:00:00Z",` +
		`"message":{"id":"msg_sub","model":"claude-opus-5","content":[],` +
		`"usage":{"input_tokens":0,"output_tokens":1000000,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0}}}` + "\n"
	writeFileWithMtime(t, filepath.Join(dir, "conv", "subagents", "agent-a1.jsonl"), sidechain, now)
	writeFileWithMtime(t, filepath.Join(dir, "conv", "subagents", "workflows", "wf_x", "agent-a2.jsonl"),
		costLine("msg_wf", "req_wf", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)

	got := readCost(t, cwd, root)
	if got.Requests != 3 {
		t.Errorf("Requests = %d, want 3 (main + subagents/ + subagents/workflows/wf_*/)", got.Requests)
	}
	if want := 75.0; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v — a sidechain entry is billed, unlike for context occupancy", got.USD, want)
	}
}

// TestLatestCost_ResumesFromTheCursorInsteadOfRereading is AC#3: a multi-hour
// session must cost no more per tick than a fresh one.
//
// Three properties, in one flow because they are one mechanism:
//
//  1. Appending and re-reading with the cursor gives the same total as reading
//     the grown file from scratch — so resuming is not merely cheap, it is right.
//  2. An UNCHANGED file is never opened. Asserted by making it unreadable
//     between the two calls: a reader that opened it would fail, and the mtime
//     and size gate is the only thing that can save it.
//  3. The dedup key survives the resume boundary. The appended lines repeat the
//     last request of the first pass, which only stays deduplicated if LastKey
//     was carried in the cursor.
func TestLatestCost_ResumesFromTheCursorInsteadOfRereading(t *testing.T) {
	const cwd = "/home/zvi/work"
	first := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	root, path := costRoot(t, cwd, first)

	cost, cursor, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err != nil {
		t.Fatalf("cold LatestCost: %v", err)
	}
	if cost.Requests != 1 {
		t.Fatalf("cold Requests = %d, want 1", cost.Requests)
	}

	// (2) An unchanged file must not be opened.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	unchanged, cursor, err := LatestCost(context.Background(), "claude", cwd, cursor, Options{Root: root})
	if err != nil {
		t.Fatalf("unchanged LatestCost opened a file it did not need to: %v", err)
	}
	if !closeEnough(unchanged.USD, cost.USD) {
		t.Errorf("unchanged read = %v, want the carried subtotal %v", unchanged.USD, cost.USD)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	// (1) and (3): append a duplicate of the last request plus a new one.
	dup := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	next := costLine("msg_2", "req_2", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	appendTo(t, path, dup+next)

	resumed, _, err := LatestCost(context.Background(), "claude", cwd, cursor, Options{Root: root})
	if err != nil {
		t.Fatalf("resumed LatestCost: %v", err)
	}
	if resumed.Requests != 2 {
		t.Errorf("resumed Requests = %d, want 2 — the appended duplicate must not "+
			"be counted, which needs LastKey to survive the resume", resumed.Requests)
	}

	scratch := readCost(t, cwd, root)
	if !closeEnough(resumed.USD, scratch.USD) || resumed.Requests != scratch.Requests {
		t.Errorf("resumed %+v != from-scratch %+v — an incremental read must agree "+
			"with a full one over the same bytes", resumed, scratch)
	}
}

// TestLatestCost_LeavesAPartialTrailingLineForTheNextPass is the correctness
// detail with no precedent elsewhere in this package.
//
// A transcript is appended to while it is being read, so its last line is
// routinely half an object. Consuming those bytes would advance the cursor past
// a line that was never decoded, and the request on it would be missing from the
// total permanently — a silent undercount that no later pass can repair. So the
// partial tail must be left alone and picked up whole once the writer finishes it.
func TestLatestCost_LeavesAPartialTrailingLineForTheNextPass(t *testing.T) {
	const cwd = "/home/zvi/work"
	complete := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	half := costLine("msg_2", "req_2", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	cut := len(half) / 2

	root, path := costRoot(t, cwd, complete+half[:cut])

	partial, cursor, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost: %v", err)
	}
	if partial.Requests != 1 {
		t.Fatalf("Requests = %d, want 1 — a half-written line is not a request", partial.Requests)
	}

	appendTo(t, path, half[cut:])
	whole, _, err := LatestCost(context.Background(), "claude", cwd, cursor, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost after completion: %v", err)
	}
	if whole.Requests != 2 {
		t.Errorf("Requests = %d, want 2 — the completed line must be read, which only "+
			"happens if its opening bytes were never consumed", whole.Requests)
	}
	if want := 50.0; !closeEnough(whole.USD, want) {
		t.Errorf("USD = %v, want %v", whole.USD, want)
	}
}

// TestLatestCost_RereadsAFileThatShrank guards the append-only assumption's
// escape hatch. A file shorter than the cursor's offset was rewritten, not
// appended to, so the subtotal describes bytes that no longer exist and keeping
// it would report spend the directory does not account for.
func TestLatestCost_RereadsAFileThatShrank(t *testing.T) {
	const cwd = "/home/zvi/work"
	long := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0) +
		costLine("msg_2", "req_2", "claude-opus-5", 0, 1_000_000, 0, 0, 0)
	root, path := costRoot(t, cwd, long)

	before, cursor, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost: %v", err)
	}
	if before.Requests != 2 {
		t.Fatalf("Requests = %d, want 2", before.Requests)
	}

	writeFileWithMtime(t, path,
		costLine("msg_9", "req_9", "claude-opus-5", 0, 1_000_000, 0, 0, 0), time.Now())

	after, _, err := LatestCost(context.Background(), "claude", cwd, cursor, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost after rewrite: %v", err)
	}
	if after.Requests != 1 {
		t.Errorf("Requests = %d, want 1 — a shrunken file must be re-read from zero, "+
			"not resumed from an offset past its end", after.Requests)
	}
	if want := 25.0; !closeEnough(after.USD, want) {
		t.Errorf("USD = %v, want %v", after.USD, want)
	}
}

// TestLatestCost_DropsAVanishedFilesSubtotal covers the other direction: a
// transcript deleted between passes must stop contributing, or a session's total
// would only ever ratchet upward regardless of what is on disk.
func TestLatestCost_DropsAVanishedFilesSubtotal(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	dir := filepath.Join(root, "projects", sanitizeCWD(cwd))
	now := time.Now()
	writeFileWithMtime(t, filepath.Join(dir, "a.jsonl"),
		costLine("msg_a", "req_a", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)
	writeFileWithMtime(t, filepath.Join(dir, "b.jsonl"),
		costLine("msg_b", "req_b", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)

	both, cursor, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost: %v", err)
	}
	if both.Requests != 2 {
		t.Fatalf("Requests = %d, want 2", both.Requests)
	}

	if err := os.Remove(filepath.Join(dir, "b.jsonl")); err != nil {
		t.Fatal(err)
	}
	one, _, err := LatestCost(context.Background(), "claude", cwd, cursor, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestCost after delete: %v", err)
	}
	if one.Requests != 1 || !closeEnough(one.USD, 25.0) {
		t.Errorf("got %+v, want 1 request / $25 — a deleted transcript must stop counting", one)
	}
}

// TestLatestCost_SkipsEntriesThatAreNotSpend enumerates every shape that must
// contribute nothing. Each is a separate way a naive reader inflates a total or
// crashes a price lookup.
func TestLatestCost_SkipsEntriesThatAreNotSpend(t *testing.T) {
	const cwd = "/home/zvi/work"
	priced := costLine("msg_ok", "req_ok", "claude-opus-5", 0, 1_000_000, 0, 0, 0)

	for _, tc := range []struct {
		name string
		line string
	}{
		{"a user entry", `{"type":"user","message":{"content":"hi","usage":{"output_tokens":9999}}}` + "\n"},
		{"an entry with no usage object", `{"type":"assistant","message":{"model":"claude-opus-5","content":[]}}` + "\n"},
		{
			// Claude Code's API-error placeholder: a full usage object with every
			// field at zero, no requestId, and a model id no table can price. Skipped
			// explicitly rather than left to the all-zero rule, so it can never reach
			// the price lookup and be miscounted as unpriced.
			"a <synthetic> API-error entry",
			`{"type":"assistant","message":{"id":"msg_e","model":"<synthetic>","content":[],` +
				`"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,` +
				`"cache_creation_input_tokens":0,"service_tier":null,"speed":null}}}` + "\n",
		},
		{"an all-zero usage object", costLine("msg_z", "req_z", "claude-opus-5", 0, 0, 0, 0, 0)},
		{"a malformed line", "{not json at all\n"},
		{"a blank line", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := costRoot(t, cwd, tc.line+priced)
			got := readCost(t, cwd, root)
			if got.Requests != 1 {
				t.Errorf("Requests = %d, want 1 — only the real entry counts", got.Requests)
			}
			if !closeEnough(got.USD, 25.0) {
				t.Errorf("USD = %v, want 25", got.USD)
			}
			if got.Partial() {
				t.Error("Partial() = true — a skipped entry is not an unpriced one")
			}
		})
	}
}

// TestLatestCost_UnpriceableEntriesMakeTheTotalALowerBound is the visible
// degradation the exact-match price table exists to produce.
//
// A model the table does not carry must not be guessed at and must not be
// silently dropped either: it is counted as unpriced, which makes Partial() true
// and turns the rendered figure from an estimate into a floor. The invented id
// is the point — claude-opus-99 is what next year's release looks like from here.
func TestLatestCost_UnpriceableEntriesMakeTheTotalALowerBound(t *testing.T) {
	const cwd = "/home/zvi/work"
	content := costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0) +
		costLine("msg_2", "req_2", "claude-opus-99", 0, 1_000_000, 0, 0, 0)
	root, _ := costRoot(t, cwd, content)

	got := readCost(t, cwd, root)
	if !got.Partial() {
		t.Error("Partial() = false — an unpriceable model must be visible, not absorbed")
	}
	if got.Unpriced != 1 {
		t.Errorf("Unpriced = %d, want 1", got.Unpriced)
	}
	if got.Requests != 1 {
		t.Errorf("Requests = %d, want 1 — an unpriced request is not a priced one", got.Requests)
	}
	if want := 25.0; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v — the unpriceable entry contributes nothing", got.USD, want)
	}
}

// TestLatestCost_OlderSchemaChargesTheAggregateCacheWriteAtTheCheaperRate covers
// the Claude Code builds (older than roughly 2.1.19x) that omit cache_creation
// entirely, leaving only the aggregate.
//
// The fallback deliberately picks the 5-minute rate, which is the CHEAPER of the
// two tiers, so an unsplittable entry under-states rather than over-states — the
// same direction every other unknown in this reader errs.
func TestLatestCost_OlderSchemaChargesTheAggregateCacheWriteAtTheCheaperRate(t *testing.T) {
	const cwd = "/home/zvi/work"
	old := `{"type":"assistant","requestId":"req_1","timestamp":"2026-08-07T12:00:00Z",` +
		`"message":{"id":"msg_1","model":"claude-opus-5","content":[],` +
		`"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":1000000,"service_tier":"standard"}}}` + "\n"
	root, _ := costRoot(t, cwd, old)

	got := readCost(t, cwd, root)
	if want := 6.25; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v (the 5m rate, $5 x 1.25 — not the 1h rate's $10)", got.USD, want)
	}
}

// TestLatestCost_PricesByTheEntrysOwnTimestamp is the reason the price table is
// a schedule. Two identical requests either side of Sonnet 5's 2026-09-01
// repricing must cost different amounts, and the only thing that can tell them
// apart is the timestamp on the line.
func TestLatestCost_PricesByTheEntrysOwnTimestamp(t *testing.T) {
	const cwd = "/home/zvi/work"
	line := func(id, stamp string) string {
		return fmt.Sprintf(
			`{"type":"assistant","requestId":%q,"timestamp":%q,"message":{"id":%q,`+
				`"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":0,"output_tokens":1000000,`+
				`"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`+"\n",
			"req_"+id, stamp, "msg_"+id)
	}
	root, _ := costRoot(t, cwd, line("a", "2026-08-31T23:59:59Z")+line("b", "2026-09-01T00:00:01Z"))

	got := readCost(t, cwd, root)
	// $10/MTok introductory, then $15/MTok.
	if want := 10.0 + 15.0; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v — each entry must be priced at the rate in effect "+
			"when it was made, not at one rate for the file", got.USD, want)
	}
}

// TestLatestCost_UnsupportedAndAbsent pins the two "no chip" paths, which must
// not look like failures to the caller.
func TestLatestCost_UnsupportedAndAbsent(t *testing.T) {
	t.Run("a non-claude program", func(t *testing.T) {
		_, _, err := LatestCost(context.Background(), "codex", "/home/zvi/work", CostCursor{}, Options{Root: t.TempDir()})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported", err)
		}
	})

	t.Run("no working directory", func(t *testing.T) {
		_, _, err := LatestCost(context.Background(), "claude", "", CostCursor{}, Options{Root: t.TempDir()})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported", err)
		}
	})

	t.Run("a session that has not talked to the model yet", func(t *testing.T) {
		// A missing project directory is not an error: the session has legitimately
		// spent nothing, and reporting a failure would make the caller keep whatever
		// it was holding instead of showing no chip.
		cost, _, err := LatestCost(context.Background(), "claude", "/home/zvi/never", CostCursor{}, Options{Root: t.TempDir()})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if cost != (Cost{}) {
			t.Errorf("cost = %+v, want the zero Cost", cost)
		}
	})
}

// TestLatestCost_HonorsContextCancellation matches the poll path's contract: an
// already-cancelled context is reported before any filesystem I/O.
func TestLatestCost_HonorsContextCancellation(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := costRoot(t, cwd, costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := LatestCost(ctx, "claude", cwd, CostCursor{}, Options{Root: root}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// appendTo opens path for append and writes more, the way Claude Code grows a
// transcript. It deliberately does not touch the mtime: the OS moves it, and a
// test that set it by hand would be asserting against its own fixture rather
// than against the change gate.
func appendTo(t *testing.T, path, more string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(more); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLatestCost_CountsEntriesThatCarryNoIdentifiers is the dedup guard's other
// direction, and it exists because the obvious implementation gets it backwards.
//
// The key is built as `message.id + "\x00" + requestId`. With both absent that is
// "\x00" — a perfectly good key that two consecutive id-less entries would SHARE,
// so the second would be discarded as a duplicate of the first and its spend
// would vanish. The rule is the opposite: an entry with nothing to identify it is
// counted, because over-counting one malformed entry beats losing a real request.
//
// Two entries, identical in every field including their absent ids. Both must
// count.
func TestLatestCost_CountsEntriesThatCarryNoIdentifiers(t *testing.T) {
	const cwd = "/home/zvi/work"
	anonymous := `{"type":"assistant","timestamp":"2026-08-07T12:00:00Z",` +
		`"message":{"model":"claude-opus-5","content":[],` +
		`"usage":{"input_tokens":0,"output_tokens":1000000,"cache_read_input_tokens":0,` +
		`"cache_creation_input_tokens":0}}}` + "\n"
	root, _ := costRoot(t, cwd, anonymous+anonymous)

	got := readCost(t, cwd, root)
	if got.Requests != 2 {
		t.Errorf("Requests = %d, want 2 — two unidentifiable entries are two requests, "+
			"not one deduplicated against an empty key", got.Requests)
	}
	if want := 50.0; !closeEnough(got.USD, want) {
		t.Errorf("USD = %v, want %v", got.USD, want)
	}
}

// TestLatestCost_AVanishedTranscriptDoesNotCostThePass covers the walk/Stat
// race: costFiles lists a transcript that is gone by the time it is Stat'ed.
//
// The reader skips it, and the assertion that matters is that the SURVIVING
// transcript's subtotal comes back on this pass rather than the next one —
// aborting would discard everything gathered so far and force a full cold re-read
// (up to 315ms on a large directory) because one file was deleted.
func TestLatestCost_AVanishedTranscriptDoesNotCostThePass(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	dir := filepath.Join(root, "projects", sanitizeCWD(cwd))
	now := time.Now()
	writeFileWithMtime(t, filepath.Join(dir, "keeper.jsonl"),
		costLine("msg_k", "req_k", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)

	ghost := filepath.Join(dir, "ghost.jsonl")
	writeFileWithMtime(t, ghost, costLine("msg_g", "req_g", "claude-opus-5", 0, 1_000_000, 0, 0, 0), now)
	if err := os.Remove(ghost); err != nil {
		t.Fatal(err)
	}

	got := readCost(t, cwd, root)
	if got.Requests != 1 || !closeEnough(got.USD, 25.0) {
		t.Errorf("got %+v, want 1 request / $25 — a vanished transcript must not "+
			"cost the pass every other transcript's subtotal", got)
	}
}

// TestLatestCost_AnUnreadableTranscriptFailsThePass is the deliberate opposite of
// the test above, and the pair is the whole policy: absent is skipped, unreadable
// is not.
//
// A file that is there but cannot be read — EACCES, EIO, a stale mount — has
// spend in it. Skipping it would return a total that is silently too low and
// indistinguishable from a correct one, which is the worst outcome available.
// Failing the pass instead leaves the caller holding its previous total (see
// Instance.ComputeCost's ok=false) and the next tick retries.
//
// Note what is NOT tested here: the third case, where the file vanishes between
// the Stat and the Open. LatestCost skips that one, matching the first test
// rather than this one, but it is a genuine race with no deterministic fixture —
// documented at its branch rather than covered.
func TestLatestCost_AnUnreadableTranscriptFailsThePass(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, path := costRoot(t, cwd, costLine("msg_1", "req_1", "claude-opus-5", 0, 1_000_000, 0, 0, 0))

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, _, err := LatestCost(context.Background(), "claude", cwd, CostCursor{}, Options{Root: root})
	if err == nil {
		t.Fatal("an unreadable transcript must fail the pass, not be silently omitted from the total")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want a permission error rather than a not-exist one", err)
	}
}
