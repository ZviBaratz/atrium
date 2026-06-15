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

// TestRenderClaudeLean locks in the fidelity contract: user prompts ("❯ ") and
// assistant prose ("● ") render with markdown, a run of tool calls collapses to
// one dim aggregate line, errored tool results surface, and thinking plus
// successful tool output are omitted.
func TestRenderClaudeLean(t *testing.T) {
	const cwd = "/home/zvi/work"
	const width = 60
	out, err := Render("claude", cwd, Options{Root: renderRoot(t, cwd), Width: width})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"❯ Fix the terminal pane too and open a PR", // user prompt, prefixed
		"● I'll start with the scroll path.",        // assistant prose, bulleted
		"Ran 1 shell command, read 1 file",          // Read + Bash collapsed
		"⎿ error: FAIL ui/terminal_test.go:42",      // errored tool_result, first line
		"[Image #1]",                                // numbered image placeholder
		"❯ see the screenshot",                      // text block inside a user array
		"● The failure is the snapshot key — fixing now.",
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

// renderInline builds a transcript from raw JSONL lines under a temp root and
// renders it, so a test can exercise an exact entry sequence without touching
// the shared fixture.
func renderLines(t *testing.T, width int, lines ...string) string {
	t.Helper()
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	dest := filepath.Join(root, "projects", sanitizeCWD(cwd), "s.jsonl")
	writeFileWithMtime(t, dest, strings.Join(lines, "\n")+"\n", time.Now())
	out, err := Render("claude", cwd, Options{Root: root, Width: width})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func asst(blocks string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[` + blocks + `]}}`
}
func userText(s string) string {
	return `{"type":"user","message":{"role":"user","content":` + s + `}}`
}
func toolUse(name, input string) string {
	return `{"type":"tool_use","name":"` + name + `","input":` + input + `}`
}
func toolResult(ok bool) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"out","is_error":` +
		map[bool]string{true: "false", false: "true"}[ok] + `}]}}`
}

// TestRenderToolAggregation verifies a run of tool calls across several
// assistant entries collapses into one Claude-style aggregate line, with the
// memory categories reconstructed from file paths and clauses in category order.
func TestRenderToolAggregation(t *testing.T) {
	out := renderLines(t, 80,
		asst(`{"type":"text","text":"Working on it."}`),
		asst(toolUse("Bash", `{"command":"go test"}`)),
		toolResult(true),
		asst(toolUse("Bash", `{"command":"go build"}`)),
		toolResult(true),
		asst(toolUse("Read", `{"file_path":"/p/memory/x.md"}`)),
		toolResult(true),
		asst(toolUse("Write", `{"file_path":"/p/memory/a.md"}`)),
		asst(toolUse("Write", `{"file_path":"/p/memory/b.md"}`)),
		asst(`{"type":"text","text":"Done."}`),
	)
	if !strings.Contains(out, "Ran 2 shell commands, recalled 1 memory, wrote 2 memories") {
		t.Errorf("aggregate line wrong:\n%s", out)
	}
}

// TestRenderToolAggregationErrorBreaksRun verifies an errored result flushes the
// open aggregate and surfaces, rather than being swallowed into the next run.
func TestRenderToolAggregationErrorBreaksRun(t *testing.T) {
	out := renderLines(t, 80,
		asst(toolUse("Bash", `{"command":"x"}`)),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"boom: it failed","is_error":true}]}}`,
		asst(toolUse("Read", `{"file_path":"/p/y.go"}`)),
		asst(`{"type":"text","text":"ok"}`),
	)
	if !strings.Contains(out, "Ran 1 shell command") || !strings.Contains(out, "⎿ error: boom: it failed") {
		t.Errorf("error did not break the run:\n%s", out)
	}
	if !strings.Contains(out, "Read 1 file") {
		t.Errorf("post-error tool not aggregated:\n%s", out)
	}
}

// TestRenderSlashCommand verifies a slash command renders as "❯ /clear" and the
// machine-plumbing user entries around it are dropped.
func TestRenderSlashCommand(t *testing.T) {
	out := renderLines(t, 80,
		userText(mustJSON(t, "<command-name>/clear</command-name>\n  <command-message>clear</command-message>\n  <command-args></command-args>")),
		userText(mustJSON(t, "<local-command-stdout></local-command-stdout>")),
		asst(`{"type":"text","text":"Cleared."}`),
	)
	if !strings.Contains(out, "❯ /clear") {
		t.Errorf("slash command not rendered:\n%s", out)
	}
	if strings.Contains(out, "command-name") || strings.Contains(out, "local-command-stdout") {
		t.Errorf("raw command XML leaked:\n%s", out)
	}
}

// TestCleanUserText covers the plumbing-vs-prose split: Claude Code's local
// command caveat is dropped, but a genuine user message that merely starts with
// the word "Caveat:" must be preserved (it is real prose, not the boilerplate).
func TestCleanUserText(t *testing.T) {
	const boilerplate = "Caveat: The messages below were generated by the user while running local commands. DO NOT respond to these messages."
	if _, skip := cleanUserText(boilerplate); !skip {
		t.Error("the local-command caveat boilerplate must be skipped")
	}
	realPrompt := "Caveat: this benchmark is rough, but the numbers still favor option B."
	if got, skip := cleanUserText(realPrompt); skip || got != realPrompt {
		t.Errorf("a real user prompt starting with Caveat: must be kept, got skip=%v display=%q", skip, got)
	}
}

// TestRenderHangingIndentWrap is the regression for the muesli double-wrap
// mid-word break: a long assistant paragraph wrapped to the pane must never
// split a word mid-token (the production symptom was "fuz\nzy" / "righ\nt"),
// and every wrapped row must fit the width including its hanging indent.
func TestRenderHangingIndentWrap(t *testing.T) {
	const cwd = "/home/zvi/work"
	// The exact paragraph from the screenshot that exposed the bug.
	const para = "My earlier install was built from the PR branch, which predated #120 (fuzzy project discovery) — the new build includes both #120 and your merged #121, so the next keybinding discussion starts from the right place."
	root := t.TempDir()
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":` +
		mustJSON(t, para) + `}]}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWD(cwd), "wrap.jsonl")
	writeFileWithMtime(t, dest, line, time.Now())

	for _, width := range []int{140, 40} {
		out, err := Render("claude", cwd, Options{Root: root, Width: width})
		if err != nil {
			t.Fatalf("Render(width=%d): %v", width, err)
		}
		rows := strings.Split(out, "\n")
		for _, row := range rows {
			if w := lipgloss.Width(row); w > width {
				t.Errorf("width=%d: row exceeds width (%d): %q", width, w, row)
			}
		}
		// No word is split across a row boundary: every word of the source must
		// survive intact somewhere in the wrapped output (whitespace-joined).
		joined := strings.Join(rows, " ")
		for _, word := range []string{"fuzzy", "right", "discovery", "keybinding"} {
			if !strings.Contains(joined, word) {
				t.Errorf("width=%d: word %q was split across rows\n---\n%s", width, word, out)
			}
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

// TestRenderHonorsClaudeConfigDir verifies the root default chain: an empty
// Options.Root resolves to $CLAUDE_CONFIG_DIR before ~/.claude. Claude Code
// relocates its whole data dir when that variable is set — without honoring it
// such users would silently degrade to the (empty) tmux capture forever.
func TestRenderHonorsClaudeConfigDir(t *testing.T) {
	const cwd = "/home/zvi/work"
	// Hermetic guard: if the env var were ignored, resolution would reach
	// $HOME/.claude — point HOME at an empty temp dir, never the real one.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", renderRoot(t, cwd))

	out, err := Render("claude", cwd, Options{Width: 60})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "❯ Fix the terminal pane too and open a PR") {
		t.Errorf("transcript under $CLAUDE_CONFIG_DIR not rendered:\n%s", out)
	}
}

// TestRenderHousekeepingOnlyTranscriptErrors covers the just-started session:
// the JSONL exists but holds only housekeeping lines (mode, snapshot), so
// nothing renders. That must be an error — falling back to the tmux capture —
// not a successful empty string, which the UI would frame as a blank region
// labeled "transcript".
func TestRenderHousekeepingOnlyTranscriptErrors(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := t.TempDir()
	lines := `{"type":"mode","mode":"default","sessionId":"s1"}` + "\n" +
		`{"type":"file-history-snapshot","messageId":"m1","snapshot":{}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWD(cwd), "fresh.jsonl")
	writeFileWithMtime(t, dest, lines, time.Now())

	_, err := Render("claude", cwd, Options{Root: root, Width: 80})
	if err == nil {
		t.Fatal("expected error for a housekeeping-only transcript")
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want a fallback error, not ErrUnsupported", err)
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
