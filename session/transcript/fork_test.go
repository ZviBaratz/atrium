package transcript

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The three uuids a fork verification turns on: the entry the cut keeps, the
// prompt the cut drops, and the assistant turn that answered the dropped prompt.
const (
	forkKeptID    = "a5515747-0002-4002-8002-000000000002"
	forkDroppedID = "c2c2c2c2-0004-4004-8004-000000000004"
	forkAnswerID  = "a5512222-0006-4006-8006-000000000006"
)

func forkRow(entryType, uuid, parent, text string) string {
	return fmt.Sprintf(
		`{"type":%q,"uuid":%q,"parentUuid":%q,"timestamp":"2026-08-06T09:00:00.000Z","isSidechain":false,"message":{"role":%q,"content":[{"type":"text","text":%q}]}}`,
		entryType, uuid, parent, entryType, text)
}

// truncatedFork is what a correct `--resume-session-at <forkKeptID>` produces:
// the prefix through the kept entry, then whatever the fork went on to do. The
// dropped prompt is gone, and so is every reference to it.
func truncatedFork() string {
	return strings.Join([]string{
		forkRow("user", "c1c1c1c1-0001-4001-8001-000000000001", "", "first prompt"),
		forkRow("assistant", forkKeptID, "c1c1c1c1-0001-4001-8001-000000000001", "done"),
		forkRow("user", "d1d1d1d1-0007-4007-8007-000000000007", forkKeptID, "a new direction"),
		forkRow("assistant", "a5513333-0008-4008-8008-000000000008", "d1d1d1d1-0007-4007-8007-000000000007", "on it"),
	}, "\n") + "\n"
}

// untruncatedFork is the CONTROL: the same fork with the truncation flag omitted
// or ignored. It is byte-identical up to the kept entry and then keeps going,
// which is exactly what an interactive `--fork-session --resume` produces — and
// exactly what a launch-only guard cannot tell apart from the arm above.
func untruncatedFork() string {
	return strings.Join([]string{
		forkRow("user", "c1c1c1c1-0001-4001-8001-000000000001", "", "first prompt"),
		forkRow("assistant", forkKeptID, "c1c1c1c1-0001-4001-8001-000000000001", "done"),
		forkRow("user", forkDroppedID, forkKeptID, "second prompt"),
		forkRow("assistant", forkAnswerID, forkDroppedID, "second answer"),
		forkRow("user", "d1d1d1d1-0007-4007-8007-000000000007", forkAnswerID, "a new direction"),
	}, "\n") + "\n"
}

func writeFork(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fork.jsonl")
	writeFileWithMtime(t, path, content, time.Now())
	return path
}

// TestContainsEntries_SeparatesATruncatedForkFromItsControl is the guard the whole
// of #644 rests on. `--resume-session-at` is ignored, not refused, outside print
// mode: the process starts, the agent answers, the row goes Ready, and the only
// thing wrong is that the conversation was seeded from HEAD. So the truncated arm
// passing proves nothing on its own — the control is what makes it evidence, by
// separating "the flag truncated" from "forking dropped it anyway".
//
// Deliberately not a line count. The driven probe gave 20 / 14 / 16 lines and
// those move with bookkeeping rows, so a count guard would be both brittle and
// weak. Identity of the dropped turn is neither.
func TestContainsEntries_SeparatesATruncatedForkFromItsControl(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		wantDropped bool
	}{
		{"truncated fork", truncatedFork(), false},
		{"untruncated control", untruncatedFork(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ContainsEntries(context.Background(), writeFork(t, tc.content), forkKeptID, forkDroppedID)
			if err != nil {
				t.Fatalf("ContainsEntries: %v", err)
			}
			if !got[forkKeptID] {
				t.Errorf("kept entry %s reported absent — the fork lost the prefix it was supposed to keep", forkKeptID)
			}
			if got[forkDroppedID] != tc.wantDropped {
				t.Errorf("dropped prompt %s: present = %v, want %v", forkDroppedID, got[forkDroppedID], tc.wantDropped)
			}
		})
	}
}

// TestContainsEntries_CatchesADroppedTurnByReferenceAlone guards the union in
// ContainsEntries' contract. A structural-only test — "is there a row whose own
// uuid is this?" — reports absent for a transcript that merely still points at
// the dropped turn, and that is a false clean bill of health: the reference can
// only exist because the turn was not dropped.
func TestContainsEntries_CatchesADroppedTurnByReferenceAlone(t *testing.T) {
	// The dropped prompt's own row is gone, but its answer still names it as parent.
	orphaned := strings.Join([]string{
		forkRow("user", "c1c1c1c1-0001-4001-8001-000000000001", "", "first prompt"),
		forkRow("assistant", forkKeptID, "c1c1c1c1-0001-4001-8001-000000000001", "done"),
		forkRow("assistant", forkAnswerID, forkDroppedID, "second answer"),
	}, "\n") + "\n"

	got, err := ContainsEntries(context.Background(), writeFork(t, orphaned), forkDroppedID)
	if err != nil {
		t.Fatalf("ContainsEntries: %v", err)
	}
	if !got[forkDroppedID] {
		t.Errorf("dropped prompt %s reported absent though a surviving row still names it as parentUuid", forkDroppedID)
	}
}

// TestContainsEntries_FindsAnIDOnAnOversizedLine covers this package's
// oversized-line handling, which is invisible to every other reader here because
// they only care that such a line fails to decode. A row past scannerBufMax — a
// huge paste, a large tool result — is delivered as pieces that decode as
// nothing, so a decode-based scan would report the dropped turn absent and wave
// an untruncated fork through.
func TestContainsEntries_FindsAnIDOnAnOversizedLine(t *testing.T) {
	oversized := forkRow("user", forkDroppedID, forkKeptID, strings.Repeat("x", scannerBufMax+(1<<16)))
	content := forkRow("assistant", forkKeptID, "", "done") + "\n" + oversized + "\n"

	got, err := ContainsEntries(context.Background(), writeFork(t, content), forkKeptID, forkDroppedID)
	if err != nil {
		t.Fatalf("ContainsEntries: %v", err)
	}
	if !got[forkKeptID] {
		t.Errorf("kept entry reported absent")
	}
	if !got[forkDroppedID] {
		t.Errorf("dropped prompt reported absent though it sits on an oversized line — an untruncated fork would pass verification")
	}
}

// TestContainsEntries_MatchesAnIDAcrossAPieceBoundary is the reason for the carry
// buffer. An oversized line arrives in buffer-sized pieces, and a 36-character
// uuid has no reason to respect those edges; without the carry, a dropped turn
// whose id straddles one is silently invisible.
func TestContainsEntries_MatchesAnIDAcrossAPieceBoundary(t *testing.T) {
	// Land the id so it spans the first piece boundary: pad to scannerBufMax minus
	// half an id, then write it.
	pad := strings.Repeat("x", scannerBufMax-len(forkDroppedID)/2)
	content := pad + forkDroppedID + strings.Repeat("y", 1<<16) + "\n"

	got, err := ContainsEntries(context.Background(), writeFork(t, content), forkDroppedID)
	if err != nil {
		t.Fatalf("ContainsEntries: %v", err)
	}
	if !got[forkDroppedID] {
		t.Errorf("id straddling a piece boundary reported absent — the carry-over is not joining the pieces")
	}
}

func TestForkPath(t *testing.T) {
	const (
		cwd  = "/home/zvi/work"
		sid  = "abcdabcd-1234-4123-8123-abcdabcdabcd"
		root = "/tmp/cfg"
	)
	want := filepath.Join(root, "projects", sanitizeCWD(cwd), sid+".jsonl")
	if got := ForkPath("claude", cwd, sid, Options{Root: root}); got != want {
		t.Errorf("ForkPath = %q, want %q", got, want)
	}
	// A fork is a claude-only mechanism, and an unknown working dir or session id
	// has no path — each must be "" rather than a plausible-looking join.
	for _, tc := range []struct{ name, program, cwd, sid string }{
		{"non-claude", "codex", cwd, sid},
		{"no working dir", "claude", "", sid},
		{"no session id", "claude", cwd, ""},
	} {
		if got := ForkPath(tc.program, tc.cwd, tc.sid, Options{Root: root}); got != "" {
			t.Errorf("%s: ForkPath = %q, want empty", tc.name, got)
		}
	}
}
