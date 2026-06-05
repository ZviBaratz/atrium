package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSkipsMalformedAndHousekeeping verifies that only user/assistant
// message entries survive parsing: housekeeping types (mode, ai-title,
// file-history-snapshot, …), sidechain entries, and malformed lines are all
// dropped without failing the parse.
func TestParseSkipsMalformedAndHousekeeping(t *testing.T) {
	entries, truncated, err := parseTail(filepath.Join("testdata", "session.jsonl"), 1<<20)
	if err != nil {
		t.Fatalf("parseTail: %v", err)
	}
	if truncated {
		t.Error("small fixture must not be reported truncated")
	}

	// The fixture interleaves housekeeping, a malformed line, and a sidechain
	// entry between 9 real user/assistant message entries (see testdata).
	var got []string
	for _, e := range entries {
		kinds := make([]string, len(e.Blocks))
		for i, b := range e.Blocks {
			kinds[i] = b.Kind
		}
		got = append(got, e.Role+":"+strings.Join(kinds, ","))
	}
	want := []string{
		"user:text", // plain-string prompt normalized to one text block
		"assistant:thinking",
		"assistant:text",
		"assistant:tool_use", // Read
		"user:tool_result",   // ok result
		"assistant:tool_use", // Bash
		"user:tool_result",   // errored result
		"user:image,text",    // image + text blocks
		"assistant:text",
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Spot-check decoded content the renderer depends on.
	if entries[0].Blocks[0].Text != "Fix the terminal pane too and open a PR" {
		t.Errorf("prompt text = %q", entries[0].Blocks[0].Text)
	}
	if entries[3].Blocks[0].ToolName != "Read" {
		t.Errorf("tool name = %q, want Read", entries[3].Blocks[0].ToolName)
	}
	if entries[4].Blocks[0].IsError {
		t.Error("ok tool_result flagged as error")
	}
	errBlock := entries[6].Blocks[0]
	if !errBlock.IsError {
		t.Error("errored tool_result not flagged")
	}
	if !strings.Contains(errBlock.Text, "FAIL ui/terminal_test.go:42") {
		t.Errorf("errored tool_result text = %q, want the failure line", errBlock.Text)
	}
	if !strings.Contains(entries[5].Blocks[0].ToolInput, "Run tests for the ui package") {
		t.Errorf("Bash tool input not preserved: %q", entries[5].Blocks[0].ToolInput)
	}
}

// TestParseTailTruncates verifies the byte-tail cap: when the file exceeds
// maxBytes only the tail is parsed, the first (likely partial) line after the
// seek point is discarded, and the result is flagged truncated. A single line
// larger than bufio.Scanner's default 64KB buffer must still parse.
func TestParseTailTruncates(t *testing.T) {
	t.Run("tail cap discards partial first line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.jsonl")
		var b strings.Builder
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":"prompt number %03d"}}`+"\n", i)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		size := int64(b.Len())
		maxBytes := size / 2

		entries, truncated, err := parseTail(path, maxBytes)
		if err != nil {
			t.Fatalf("parseTail: %v", err)
		}
		if !truncated {
			t.Error("expected truncated=true for file larger than maxBytes")
		}
		if len(entries) == 0 || len(entries) >= 100 {
			t.Fatalf("expected a strict tail subset, got %d entries", len(entries))
		}
		// The earliest surviving entry must be intact (partial line discarded,
		// never half-parsed) and the final entry must be the file's last.
		if first := entries[0].Blocks[0].Text; !strings.HasPrefix(first, "prompt number ") {
			t.Errorf("first surviving entry corrupted: %q", first)
		}
		if last := entries[len(entries)-1].Blocks[0].Text; last != "prompt number 099" {
			t.Errorf("last entry = %q, want prompt number 099", last)
		}
	})

	t.Run("whole file under cap is not truncated", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "small.jsonl")
		content := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, truncated, err := parseTail(path, 1<<20)
		if err != nil {
			t.Fatalf("parseTail: %v", err)
		}
		if truncated {
			t.Error("expected truncated=false")
		}
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
	})

	t.Run("single line larger than default scanner buffer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "giant-line.jsonl")
		giant := strings.Repeat("x", 100*1024) // > bufio default 64KB token limit
		content := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"%s"}}`+"\n", giant)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		entries, _, err := parseTail(path, 1<<22)
		if err != nil {
			t.Fatalf("parseTail must handle giant lines: %v", err)
		}
		if len(entries) != 1 || len(entries[0].Blocks[0].Text) != 100*1024 {
			t.Fatalf("giant line not parsed intact")
		}
	})
}
