package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustJSON encodes s as a JSON string literal (with surrounding quotes) for
// inlining a text body into a hand-built JSONL line.
func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSanitizeCWD verifies the cwd → project-dir mapping used by Claude Code:
// every non-alphanumeric rune of the absolute path becomes '-'. The scheme was
// verified against real ~/.claude/projects entries; '.', '_', and '/' all map
// to '-' (see the worktree example, taken verbatim from disk).
func TestSanitizeCWD(t *testing.T) {
	cases := []struct {
		name string
		cwd  string
		want string
	}{
		{
			name: "plain path",
			cwd:  "/home/zvi/Projects/atrium",
			want: "-home-zvi-Projects-atrium",
		},
		{
			name: "dots underscores and digits",
			cwd:  "/home/zvi/.claude-squad/worktrees/zvi/stuck-preview_18b60954522fd076",
			want: "-home-zvi--claude-squad-worktrees-zvi-stuck-preview-18b60954522fd076",
		},
		{
			name: "empty",
			cwd:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeCWD(tc.cwd); got != tc.want {
				t.Errorf("sanitizeCWD(%q) = %q, want %q", tc.cwd, got, tc.want)
			}
		})
	}
}

// writeFileWithMtime creates path (and parents) with content and an explicit
// mtime — git does not preserve mtimes, so ordering must be set per-test.
func writeFileWithMtime(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestNewestTranscriptPicksNewestMtime verifies the live-session heuristic:
// the newest-mtime *.jsonl directly in the project dir wins (the same file
// `claude --continue` resumes), and <uuid>/ subdirs (subagent transcripts)
// are never considered.
func TestNewestTranscriptPicksNewestMtime(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeFileWithMtime(t, filepath.Join(dir, "old-session.jsonl"), "{}\n", base)
	writeFileWithMtime(t, filepath.Join(dir, "new-session.jsonl"), "{}\n", base.Add(time.Minute))
	// Subagent transcript nested under a session-uuid dir: newer than everything,
	// but must be ignored.
	writeFileWithMtime(t, filepath.Join(dir, "new-session", "subagents", "agent-1.jsonl"), "{}\n", base.Add(time.Hour))
	// Non-jsonl noise must be ignored too.
	writeFileWithMtime(t, filepath.Join(dir, "notes.txt"), "x", base.Add(time.Hour))

	got, err := newestTranscript(dir)
	if err != nil {
		t.Fatalf("newestTranscript: %v", err)
	}
	if want := filepath.Join(dir, "new-session.jsonl"); got != want {
		t.Errorf("newestTranscript = %q, want %q", got, want)
	}
}

// TestNewestTranscriptMissingDirAndEmpty verifies every "nothing usable here"
// shape returns an error so the caller falls back to the tmux capture.
func TestNewestTranscriptMissingDirAndEmpty(t *testing.T) {
	t.Run("missing dir", func(t *testing.T) {
		if _, err := newestTranscript(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Error("expected error for missing dir")
		}
	})
	t.Run("no jsonl files", func(t *testing.T) {
		dir := t.TempDir()
		writeFileWithMtime(t, filepath.Join(dir, "notes.txt"), "x", time.Now())
		if _, err := newestTranscript(dir); err == nil {
			t.Error("expected error for dir without .jsonl files")
		}
	})
	t.Run("only empty jsonl", func(t *testing.T) {
		dir := t.TempDir()
		writeFileWithMtime(t, filepath.Join(dir, "empty.jsonl"), "", time.Now())
		if _, err := newestTranscript(dir); err == nil {
			t.Error("expected error when the newest transcript is empty")
		}
	})
}

// TestHasResumable is the gate that keeps `claude --continue` from launching against a
// cwd with no conversation (which aborts with "No conversation found to continue!" and
// bounces the session to Paused). resumable tracks whether a usable transcript exists;
// supported tells the caller whether the program even has a native-transcript adapter —
// false for codex/gemini, which must keep deferring to their own resume probe.
func TestHasResumable(t *testing.T) {
	const cwd = "/home/zvi/.atrium/worktrees/zvi/improve_18bd02e657bdde19"

	t.Run("claude with a non-empty transcript", func(t *testing.T) {
		root := t.TempDir()
		proj := filepath.Join(root, "projects", sanitizeCWD(cwd))
		writeFileWithMtime(t, filepath.Join(proj, "session.jsonl"), "{}\n", time.Now())
		resumable, supported := HasResumable("claude", cwd, Options{Root: root})
		if !supported {
			t.Error("claude must be a supported (transcript-backed) program")
		}
		if !resumable {
			t.Error("a non-empty transcript must be reported resumable")
		}
	})

	t.Run("claude with no project dir", func(t *testing.T) {
		// The reported bug: a worktree the agent never wrote into.
		resumable, supported := HasResumable("claude --continue", cwd, Options{Root: t.TempDir()})
		if !supported {
			t.Error("claude must be supported regardless of history presence")
		}
		if resumable {
			t.Error("a missing project dir must not be reported resumable")
		}
	})

	t.Run("claude with only an empty transcript", func(t *testing.T) {
		root := t.TempDir()
		proj := filepath.Join(root, "projects", sanitizeCWD(cwd))
		writeFileWithMtime(t, filepath.Join(proj, "session.jsonl"), "", time.Now())
		if resumable, _ := HasResumable("claude", cwd, Options{Root: root}); resumable {
			t.Error("an empty transcript must not be reported resumable")
		}
	})

	t.Run("non-claude program is unsupported", func(t *testing.T) {
		resumable, supported := HasResumable("aider", cwd, Options{Root: t.TempDir()})
		if supported {
			t.Error("aider has no transcript adapter; supported must be false")
		}
		if resumable {
			t.Error("resumable must be false when no adapter handles the program")
		}
	})
}
