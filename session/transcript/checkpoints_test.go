package transcript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// checkpointSessionID is the uuid the fixture transcript is filed under; a
// session's transcript filename IS its Claude session id, which is what makes
// SessionID derivable without persisting anything.
const checkpointSessionID = "abcdabcd-1234-4123-8123-abcdabcdabcd"

// checkpointRoot writes content as <root>/projects/<sanitized cwd>/<sid>.jsonl.
// Unlike modelRoot it names the file after a session uuid, because these tests
// assert the id is read back off the path.
func checkpointRoot(t *testing.T, cwd, content string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "projects", sanitizeCWD(cwd), checkpointSessionID+".jsonl")
	writeFileWithMtime(t, path, content, time.Now())
	return root
}

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLoadCheckpoints_FixtureShapes walks every record shape the real corpus
// contains. The per-row expectations are the contract; the notes on each row say
// which real-world shape it stands in for.
func TestLoadCheckpoints_FixtureShapes(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if got.SessionID != checkpointSessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, checkpointSessionID)
	}
	if !strings.HasSuffix(got.Path, checkpointSessionID+".jsonl") {
		t.Errorf("Path = %q, want it to name the transcript", got.Path)
	}

	// Files/Outside accumulate down the list — each row is what the session had
	// touched by the end of that turn — so the expectations rise and never fall.
	want := []Checkpoint{
		// A session's first checkpoint legitimately tracks nothing yet, and has
		// nothing before it to fork from.
		{MessageID: "11111111-1111-4111-8111-111111111111", Label: "First prompt", Files: 0, Outside: 0, At: mustStamp(t, "2026-08-05T10:00:00.100Z"), ForkAtID: ""},
		// isSnapshotUpdate rewrites a snapshot in place, so the later record for this
		// messageId wins — 5 paths, 3 of them outside — and it applies at the row's
		// original position, not where the rewrite happens to sit in the file.
		// promptSource is "suggestion_accepted" — filtering on "typed" would drop it.
		// Its fork point is the assistant turn that answered the first prompt.
		{MessageID: "22222222-2222-4222-8222-222222222222", Label: "Second prompt with extra spaces", Files: 5, Outside: 3, At: mustStamp(t, "2026-08-05T10:05:01Z"), ForkAtID: "aaaa1111-1111-4111-8111-111111111111"},
		// A tool-result turn has no labelable text, and its payload quotes a snapshot
		// record back at the reader. +3 new paths from its map and +1 from its delta.
		{MessageID: "33333333-3333-4333-8333-333333333333", Label: "", Files: 9, Outside: 3, At: mustStamp(t, "2026-08-05T10:10:00Z"), ForkAtID: "22222222-2222-4222-8222-222222222222"},
		// A slash-command turn: markup stripped down to the command. Its map holds
		// only a path already counted, so the total holds.
		{MessageID: "44444444-4444-4444-8444-444444444444", Label: "/simplify", Files: 9, Outside: 3, Provisional: true, At: mustStamp(t, "2026-08-05T10:15:00Z"), ForkAtID: "33333333-3333-4333-8333-333333333333"},
		// Anchored to a sidechain entry, which is skipped — so the label is empty and
		// the time falls back to the snapshot's own. +1 path, and it is outside. Not
		// being on the main chain, it is also not forkable.
		{MessageID: "55555555-5555-4555-8555-555555555555", Label: "", Files: 10, Outside: 4, At: mustStamp(t, "2026-08-05T10:20:00.070Z"), ForkAtID: ""},
		// No anchor in the transcript at all, and an empty map: the running total is
		// what carries, which is the whole reason a later row cannot read as 0. With
		// no chain entry of its own there is no cut point either.
		{MessageID: "66666666-6666-4666-8666-666666666666", Label: "", Files: 10, Outside: 4, At: mustStamp(t, "2026-08-05T10:25:00Z"), ForkAtID: ""},
		// Anchor found, but its timestamp is unparseable: label applies, time
		// falls back. Its fork point steps back over the sidechain entry between
		// them to /simplify — the chain walk skips subagent turns, not just the
		// anchor lookup.
		{MessageID: "77777777-7777-4777-8777-777777777777", Label: "an anchor with an unparseable timestamp", Files: 10, Outside: 4, At: mustStamp(t, "2026-08-05T10:30:00Z"), ForkAtID: "44444444-4444-4444-8444-444444444444"},
	}
	if len(got.List) != len(want) {
		t.Fatalf("got %d checkpoints, want %d: %+v", len(got.List), len(want), got.List)
	}
	for i, w := range want {
		g := got.List[i]
		if g.MessageID != w.MessageID {
			t.Errorf("row %d: MessageID = %q, want %q (order must be oldest first)", i, g.MessageID, w.MessageID)
		}
		if g.Label != w.Label {
			t.Errorf("row %d (%s): Label = %q, want %q", i, w.MessageID, g.Label, w.Label)
		}
		if g.Files != w.Files {
			t.Errorf("row %d (%s): Files = %d, want %d", i, w.MessageID, g.Files, w.Files)
		}
		if g.Outside != w.Outside {
			t.Errorf("row %d (%s): Outside = %d, want %d", i, w.MessageID, g.Outside, w.Outside)
		}
		if g.Provisional != w.Provisional {
			t.Errorf("row %d (%s): Provisional = %v, want %v", i, w.MessageID, g.Provisional, w.Provisional)
		}
		if !g.At.Equal(w.At) {
			t.Errorf("row %d (%s): At = %s, want %s", i, w.MessageID, g.At, w.At)
		}
		if g.ForkAtID != w.ForkAtID {
			t.Errorf("row %d (%s): ForkAtID = %q, want %q", i, w.MessageID, g.ForkAtID, w.ForkAtID)
		}
	}
}

// TestLoadCheckpoints_ForkPointWalksBackToAChainEntry is the regression guard for
// the finding that cost #644 a probe run: a transcript ROW is not necessarily a
// chain entry. Between the assistant turn and the next prompt this fixture puts
// every uuid-less row type the real corpus produces — queue-operation, attachment,
// ai-title, last-prompt, mode, summary — then a sidechain assistant turn, which
// does carry a uuid but is not on the main chain, and finally a queue-operation
// row that carries a uuid of its own *and* nests a user entry inside itself.
//
// So four wrong implementations are separated from the right one. "The row before
// the prompt" lands on the queue row; "any uuid-bearing row" lands on it too (the
// byte prefilter admits it, because the NESTED entry carries the marker — only the
// decoded top-level type rejects it, the same trap decodeSnapshot documents);
// "the last row carrying any id-shaped field" lands on summary's leafUuid; and
// "MessageID itself" keeps the prompt it was meant to drop. Claude answers the
// first three with `No message found with message.uuid of` and exit 1, and the
// fourth *silently* — it is a valid entry, just the wrong one.
func TestLoadCheckpoints_ForkPointWalksBackToAChainEntry(t *testing.T) {
	const cwd = "/home/zvi/chain"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints-chain.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if len(got.List) != 2 {
		t.Fatalf("got %d checkpoints, want 2: %+v", len(got.List), got.List)
	}

	const (
		firstPrompt  = "c1c1c1c1-0001-4001-8001-000000000001"
		mainAssist   = "a5515747-0002-4002-8002-000000000002"
		sidechain    = "51de0000-0003-4003-8003-000000000003"
		summaryLeaf  = "deadbeef-0009-4009-8009-000000000009"
		queueRow     = "5751e400-0005-4005-8005-000000000005"
		secondPrompt = "c2c2c2c2-0004-4004-8004-000000000004"
	)
	if got.List[0].ForkAtID != "" {
		t.Errorf("oldest checkpoint: ForkAtID = %q, want empty — nothing precedes it", got.List[0].ForkAtID)
	}
	second := got.List[1]
	if second.MessageID != secondPrompt {
		t.Fatalf("row 1: MessageID = %q, want %q", second.MessageID, secondPrompt)
	}
	switch second.ForkAtID {
	case mainAssist: // right
	case "":
		t.Errorf("row 1: ForkAtID is empty — the walk-back gave up instead of stepping over the six uuid-less rows")
	case sidechain:
		t.Errorf("row 1: ForkAtID = the sidechain turn %q — the chain walk must skip subagent entries, not just the anchor lookup", sidechain)
	case queueRow:
		t.Errorf("row 1: ForkAtID = the queue-operation row %q — it nests a user entry, so the byte prefilter admits it and only the decoded TOP-LEVEL type can reject it", queueRow)
	case summaryLeaf:
		t.Errorf("row 1: ForkAtID = summary's leafUuid %q — only a `uuid` on a user/assistant row is a chain entry", summaryLeaf)
	case secondPrompt:
		t.Errorf("row 1: ForkAtID = MessageID — --resume-session-at keeps the entry it names, so this would keep the very prompt the fork drops")
	default:
		t.Errorf("row 1: ForkAtID = %q, want %q (the main-chain assistant turn)", second.ForkAtID, mainAssist)
	}
	if second.ForkAtID == firstPrompt {
		t.Errorf("row 1: ForkAtID = the first prompt — the fork would drop its answer too")
	}
}

// TestLoadCheckpoints_CountsRiseAndFoldInDeltas is the regression guard for the
// defect the first version of this reader shipped: counting only a snapshot's own
// map left the NEWEST row reading "no files" however much its turn had changed,
// because the map that absorbs a turn's work is the *next* snapshot's — and the
// newest checkpoint has no next. Folding each row's own deltas in fixes exactly
// that, and the running total must never fall.
func TestLoadCheckpoints_CountsRiseAndFoldInDeltas(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	prev := 0
	for i, cp := range got.List {
		if cp.Files < prev {
			t.Errorf("row %d: Files = %d after %d — the running total must not fall", i, cp.Files, prev)
		}
		prev = cp.Files
	}
	// src/late.go arrives ONLY as a delta on the 3333… checkpoint. Its own map holds
	// 4 paths and it inherits 5 from the row before, so counting the map alone would
	// leave this row at 8.
	for _, cp := range got.List {
		if cp.MessageID == "33333333-3333-4333-8333-333333333333" && cp.Files != 9 {
			t.Errorf("Files = %d, want 9 — the row's own delta must be counted", cp.Files)
		}
	}
}

// A checkpoint whose turn is the most recent one has no following snapshot to
// absorb it, so its deltas are the only record that anything happened. That is the
// shape every live session's newest row has — and the one that read as 0 before the
// deltas were folded in.
func TestLoadCheckpoints_NewestRowCountsItsOwnTurn(t *testing.T) {
	const cwd = "/home/zvi/work"
	lines := `{"type":"user","uuid":"aaaa","timestamp":"2026-08-05T10:00:00Z","isSidechain":false,"message":{"role":"user","content":"edit three files"}}` + "\n" +
		`{"type":"file-history-snapshot","messageId":"aaaa","snapshot":{"messageId":"aaaa","trackedFileBackups":{},"timestamp":"2026-08-05T10:00:00Z"}}` + "\n" +
		`{"type":"file-history-delta","messageId":"aaaa","snapshotMessageId":"aaaa","trackingPath":"a.go","backup":{"backupFileName":"1@v1","version":1},"timestamp":"2026-08-05T10:01:00Z"}` + "\n" +
		`{"type":"file-history-delta","messageId":"aaaa","snapshotMessageId":"aaaa","trackingPath":"b.go","backup":{"backupFileName":"2@v1","version":1},"timestamp":"2026-08-05T10:02:00Z"}` + "\n" +
		`{"type":"file-history-delta","messageId":"aaaa","snapshotMessageId":"aaaa","trackingPath":"/etc/hosts","backup":{"backupFileName":"3@v1","version":1},"timestamp":"2026-08-05T10:03:00Z"}` + "\n"
	root := checkpointRoot(t, cwd, lines)

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if len(got.List) != 1 {
		t.Fatalf("got %d checkpoints, want 1", len(got.List))
	}
	if got.List[0].Files != 3 {
		t.Errorf("Files = %d, want 3 — the latest turn's deltas are all there is", got.List[0].Files)
	}
	if got.List[0].Outside != 1 {
		t.Errorf("Outside = %d, want 1", got.List[0].Outside)
	}
}

// TestLoadCheckpoints_NoSnapshots covers both "this claude does not do file
// checkpointing" and "this session has not been checkpointed yet": an empty list
// and no error, so the UI shows an empty timeline rather than a failure.
func TestLoadCheckpoints_NoSnapshots(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints-empty.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if len(got.List) != 0 {
		t.Errorf("got %d checkpoints, want 0: %+v", len(got.List), got.List)
	}
	if got.SessionID != checkpointSessionID {
		t.Errorf("SessionID = %q, want it populated even with no checkpoints", got.SessionID)
	}
}

// TestLoadCheckpoints_BlobsReflectsFileHistoryDir verifies the retention signal:
// Claude sweeps <root>/file-history/<sid>/ on its own schedule while leaving the
// transcript records in place, so a listed checkpoint can outlive the copies it
// would restore from.
func TestLoadCheckpoints_BlobsReflectsFileHistoryDir(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if got.Blobs {
		t.Error("Blobs = true with no file-history dir, want false")
	}

	if err := os.MkdirAll(filepath.Join(root, "file-history", checkpointSessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if !got.Blobs {
		t.Error("Blobs = false with the file-history dir present, want true")
	}
}

// TestLoadCheckpoints_ReadsWholeFileByDefault is the regression guard for the
// one trap in this reader: every sibling reader caps its tail, and applyDefaults
// fills a zero MaxBytes with the render path's 512KB. Checkpoints sit throughout
// the transcript with the earliest near the top, so a defaulted cap would show a
// fraction of them with nothing failing.
func TestLoadCheckpoints_ReadsWholeFileByDefault(t *testing.T) {
	const cwd = "/home/zvi/work"
	var b strings.Builder
	b.WriteString(`{"type":"file-history-snapshot","messageId":"first-one","snapshot":{"messageId":"first-one","trackedFileBackups":{},"timestamp":"2026-08-05T10:00:00Z"}}` + "\n")
	// Push the first record well past defaultMaxBytes with filler turns.
	filler := fmt.Sprintf(`{"type":"assistant","uuid":"filler","isSidechain":false,"message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":%q}]}}`, strings.Repeat("x", 900))
	for written := 0; written < defaultMaxBytes+64*1024; written += len(filler) + 1 {
		b.WriteString(filler + "\n")
	}
	b.WriteString(`{"type":"file-history-snapshot","messageId":"last-one","snapshot":{"messageId":"last-one","trackedFileBackups":{},"timestamp":"2026-08-05T11:00:00Z"}}` + "\n")
	root := checkpointRoot(t, cwd, b.String())

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if len(got.List) != 2 {
		t.Fatalf("got %d checkpoints, want 2 — the head of the file was cut off", len(got.List))
	}
	if got.List[0].MessageID != "first-one" {
		t.Errorf("first row = %q, want first-one", got.List[0].MessageID)
	}
}

// TestLoadCheckpoints_MaxBytesHonoredWhenSet keeps the caller's cap working, so
// a test (or a future budget) can bound the read.
func TestLoadCheckpoints_MaxBytesHonoredWhenSet(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root, MaxBytes: 512})
	if err != nil {
		t.Fatalf("LoadCheckpoints: %v", err)
	}
	if len(got.List) >= 7 {
		t.Errorf("got %d checkpoints from a 512-byte window, want a truncated read", len(got.List))
	}
}

// TestLoadCheckpoints_Unsupported keeps the surface claude-only at the reader
// level too, matching Render/LatestModel.
func TestLoadCheckpoints_Unsupported(t *testing.T) {
	_, err := LoadCheckpoints(context.Background(), "codex", "/anywhere", Options{Root: t.TempDir()})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestLoadCheckpoints_MissingTranscript reports ErrNoTranscript rather than an
// empty list, so the caller can say "no transcript yet" — and, crucially, can tell
// that apart from a read that genuinely failed. Without the sentinel every failure
// looks like an absent file, and the UI tells the user a transcript it just failed
// to parse does not exist.
func TestLoadCheckpoints_MissingTranscript(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string // returns the root
	}{
		{"no project dir", func(t *testing.T) string { return t.TempDir() }},
		{"project dir with no jsonl", func(t *testing.T) string {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "projects", sanitizeCWD("/home/zvi/work")), 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}},
		{"empty transcript", func(t *testing.T) string { return checkpointRoot(t, "/home/zvi/work", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCheckpoints(context.Background(), "claude", "/home/zvi/work", Options{Root: tc.setup(t)})
			if !errors.Is(err, ErrNoTranscript) {
				t.Errorf("err = %v, want it to wrap ErrNoTranscript", err)
			}
		})
	}
}

// A line past the scanner's 4MB ceiling costs that line, not the transcript. This
// reader is the only one that scans the whole file rather than a bounded tail, so
// it is the only one the ceiling is reachable from — and bufio.Scanner's own answer
// (ErrTooLong, and stop) would discard every checkpoint already read, including the
// ones before the offending line.
func TestLoadCheckpoints_OversizedLineDoesNotDiscardTheEnumeration(t *testing.T) {
	const cwd = "/home/zvi/work"
	oversized := `{"type":"user","uuid":"aaaa","message":{"role":"user","content":"` +
		strings.Repeat("x", scannerBufMax+1) + `"}}` + "\n"
	lines := []string{
		`{"type":"file-history-snapshot","messageId":"m1","snapshot":{"trackedFileBackups":{"a.go":{}},"timestamp":"2026-08-05T10:00:00Z"}}`,
		`{"type":"user","uuid":"m1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"before the blob"}}`,
		strings.TrimSuffix(oversized, "\n"),
		`{"type":"file-history-snapshot","messageId":"m2","snapshot":{"trackedFileBackups":{"a.go":{},"b.go":{}},"timestamp":"2026-08-05T10:05:00Z"}}`,
		`{"type":"user","uuid":"m2","timestamp":"2026-08-05T10:05:00Z","message":{"role":"user","content":"after the blob"}}`,
	}
	root := checkpointRoot(t, cwd, strings.Join(lines, "\n")+"\n")

	got, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err != nil {
		t.Fatalf("err = %v, want the enumeration to survive an oversized line", err)
	}
	var labels []string
	for _, cp := range got.List {
		labels = append(labels, cp.Label)
	}
	want := []string{"before the blob", "after the blob"}
	if fmt.Sprint(labels) != fmt.Sprint(want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
}

// A read that fails for any other reason must NOT wrap ErrNoTranscript, or the UI
// reports an absent file over one that is plainly there — and `r` repeats the same
// wrong sentence. An unreadable project directory is the reachable case: os.ReadDir
// fails with EACCES, which is not an absence.
func TestLoadCheckpoints_ReadFailureIsNotMistakenForAbsence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))
	dir := filepath.Join(root, "projects", sanitizeCWD(cwd))
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := LoadCheckpoints(context.Background(), "claude", cwd, Options{Root: root})
	if err == nil {
		t.Fatal("err = nil for an unreadable project directory")
	}
	if errors.Is(err, ErrNoTranscript) {
		t.Errorf("err = %v wraps ErrNoTranscript — a read failure is not an absent transcript", err)
	}
}

// TestLoadCheckpoints_ContextCancelled matches LatestModel's contract: an
// already-cancelled context is reported before any filesystem I/O.
func TestLoadCheckpoints_ContextCancelled(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := checkpointRoot(t, cwd, loadFixture(t, "checkpoints.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := LoadCheckpoints(ctx, "claude", cwd, Options{Root: root}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestCleanLabel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "fix the parser", "fix the parser"},
		{"collapses runs of space", "fix   the\tparser", "fix the parser"},
		{"first non-empty line only", "\n\n  first line  \nsecond line", "first line"},
		{"strips command markup", "<command-name>/simplify</command-name>", "/simplify"},
		{"strips bash-input markup", "<bash-input>just ci</bash-input>", "just ci"},
		{"empty", "   \n\t ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanLabel(tc.in); got != tc.want {
				t.Errorf("cleanLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPathOutsideCWD(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/app.go", false},
		{"Makefile", false},
		{"..hidden", false},
		{"/etc/hosts", true},
		{"/home/zvi/.claude/plans/plan.md", true},
		{"../sibling/x.go", true},
		{"..", true},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := pathOutsideCWD(tc.path); got != tc.want {
				t.Errorf("pathOutsideCWD(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func mustStamp(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
