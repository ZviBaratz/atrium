package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderRoot builds a fake ~/.claude tree under a temp root containing the
// shared fixture transcript for cwd, and returns the root.
func renderRoot(t *testing.T, cwd string) string {
	t.Helper()
	root := t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "projects", sanitizeCWD(cwd), "session.jsonl")
	writeFileWithMtime(t, dest, string(data), time.Now())
	return root
}

// TestRenderClaudeLean locks in the Lean fidelity contract: user prompts and
// assistant prose render, tool calls compress to dim one-liners, errored tool
// results surface, and thinking plus successful tool output are omitted.
func TestRenderClaudeLean(t *testing.T) {
	const cwd = "/home/zvi/work"
	const width = 60
	out, err := Render("claude", cwd, Options{Root: renderRoot(t, cwd), Width: width})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"❯ Fix the terminal pane too and open a PR", // user prompt, prefixed
		"I'll start with the scroll path.",          // assistant prose
		"⏺ Read: ui/terminal.go",                    // tool_use one-liner via file_path
		"⏺ Bash: Run tests for the ui package",      // tool_use one-liner via description
		"⎿ error: FAIL ui/terminal_test.go:42",      // errored tool_result, first line
		"[image]",                                   // image placeholder
		"see the screenshot",                        // text block inside a user array
		"The failure is the snapshot key — fixing now.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	for _, reject := range []string{
		"package ui",                 // successful tool_result content is skipped
		"keys by title, not pointer", // thinking is skipped
		"sidechain prompt",           // sidechain entries never render
		"terminal pane fix",          // housekeeping (ai-title) never renders
	} {
		if strings.Contains(out, reject) {
			t.Errorf("output must not contain %q\n---\n%s", reject, out)
		}
	}

	// Every rendered line must fit the pane: lipgloss.Width is ANSI-aware, so
	// dim styling doesn't inflate the measurement.
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line exceeds width %d (%d): %q", width, w, line)
		}
	}
}

// TestRenderTruncationHeader verifies a tail-capped transcript announces the
// elision as its first line instead of silently dropping history.
func TestRenderTruncationHeader(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(`{"type":"user","message":{"role":"user","content":"prompt"}}` + "\n")
	}
	dest := filepath.Join(root, "projects", sanitizeCWD(cwd), "big.jsonl")
	writeFileWithMtime(t, dest, b.String(), time.Now())

	out, err := Render("claude", cwd, Options{Root: root, Width: 80, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	firstLine := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(firstLine, "transcript truncated") {
		t.Errorf("first line = %q, want truncation header", firstLine)
	}
}

// TestRenderUnsupportedProgram verifies the adapter boundary: only Claude
// (wrapper-aware) is handled; everything else signals ErrUnsupported so the
// caller falls back to the tmux capture.
func TestRenderUnsupportedProgram(t *testing.T) {
	for _, program := range []string{"aider", "codex", "gemini", "bash"} {
		if _, err := Render(program, "/anywhere", Options{Root: t.TempDir()}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Render(%q) error = %v, want ErrUnsupported", program, err)
		}
	}
	for _, program := range []string{"claude", "claude --continue", "/usr/local/bin/claude"} {
		_, err := Render(program, "/anywhere", Options{Root: t.TempDir()})
		if errors.Is(err, ErrUnsupported) {
			t.Errorf("Render(%q) must be supported, got ErrUnsupported", program)
		}
		if err == nil {
			t.Errorf("Render(%q) with no transcript on disk must error to trigger fallback", program)
		}
	}
}
