package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolve pins the program-string → adapter mapping, including the
// wrapper-aware basename matching inherited from the old isClaude and the
// directory-name false positive it guards against.
func TestResolve(t *testing.T) {
	cases := []struct {
		program string
		want    Key
	}{
		{"claude", KeyClaude},
		{"/usr/local/bin/claude", KeyClaude},
		{"claude --continue", KeyClaude},
		{"launch-claude.sh", KeyClaude},
		{"CLAUDE", KeyClaude},
		{"codex", KeyCodex},
		{"codex --model o3", KeyCodex},
		{"gemini", KeyGemini},
		{"gemini --yolo", KeyGemini},
		{"/home/x/bin/gemini", KeyGemini},
		{"aider", KeyAider},
		{"aider --model ollama_chat/gemma3:1b", KeyAider},
		// A matching directory name must not resolve: only the basename counts.
		{"/home/user/.claude-squad/bin/otheragent", KeyGeneric},
		{"goose", KeyGeneric},
		{"", KeyGeneric},
	}
	for _, c := range cases {
		got := Resolve(c.program)
		require.NotNil(t, got, "Resolve must never return nil: %q", c.program)
		require.Equal(t, c.want, got.Key, "program %q", c.program)
	}
}

// --- Claude fixtures (mirroring the session/tmux poll tests, which remain the
// behavioral regression gate; these pin the same heuristics at the table level).

func TestClaudeBusyMarker(t *testing.T) {
	require.True(t, claude.HasBusyMarker("✻ Cogitating… (5s · esc to interrupt)"))

	// The marker is found in the footer below the input box even when a
	// variable-height team selector renders below it.
	working := strings.Join([]string{
		"⏺ Running the build…",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
		"  Running 2 agents…",
		"  ● main",
		"  ◯ general-purpose",
	}, "\n")
	require.True(t, claude.HasBusyMarker(working))

	// The same marker text above the input box (in the transcript) must not count.
	scrollback := strings.Join([]string{
		"  I will add the \"esc to interrupt\" marker check now.",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"  ● main",
	}, "\n")
	require.False(t, claude.HasBusyMarker(scrollback))
}

func TestClaudePrompts(t *testing.T) {
	name, ok := claude.DetectPrompt("Do this? \n  No, and tell Claude what to do differently")
	require.True(t, ok)
	require.Equal(t, "permission", name)

	_, ok = claude.DetectPrompt("How do you want to be notified?\n  1. Telegram\n  2. Email\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel")
	require.True(t, ok, "selection prompt")

	// Wrapped footer: "Esc to cancel" lands on a different physical line than
	// the nav/select tokens; flattening must reconstruct it.
	_, ok = claude.DetectPrompt("Server restart?\n  1. Relaunch\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes · Esc to cancel")
	require.True(t, ok, "wrapped selection footer")

	// A custom multi-line statusLine below the footer (drawing its own divider
	// rule) pushes the footer out of any fixed bottom window; the structural
	// segment scan must still see it. Mirrors the session/tmux statusLine poll
	// tests, which remain the behavioral gate.
	name, ok = claude.DetectPrompt(strings.Join([]string{
		"  6. Chat about this",
		"Enter to select · ↑/↓ to navigate · Esc to cancel",
		"────────────────────────────",
		"  main · opus · 12% ctx",
		"  3 files changed",
	}, "\n"))
	require.True(t, ok, "selection footer above a divider-drawing statusLine")
	require.Equal(t, "selection", name)

	// A footer quoted in the transcript sits above the input box; the scan stops
	// at the box interior, so the quote must not read as a live prompt.
	_, ok = claude.DetectPrompt(strings.Join([]string{
		"  The footer reads: Enter to select · ↑/↓ to navigate · Esc to cancel",
		"╭────────────────────────────╮",
		"│ >                          │",
		"╰────────────────────────────╯",
		"  ? for shortcuts",
	}, "\n"))
	require.False(t, ok, "a footer quote in the transcript must not match")

	// Live idle/working footers must not classify as prompts.
	for _, footer := range []string{
		"❯ \n⏵⏵ auto mode on · 1 shell · ctrl+t to hide tasks · ← for agents · ↓ to manage",
		"❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	} {
		_, ok := claude.DetectPrompt(footer)
		require.False(t, ok, "idle footer must not be a prompt: %q", footer)
	}
}

func TestClaudeGate(t *testing.T) {
	g, ok := claude.GateUp("Do you trust the files in this folder?\n  1. Yes, proceed")
	require.True(t, ok)
	require.Equal(t, DismissEnter, g.Dismiss)

	// Claude Code v2.1.162+ uses capital-N "New MCP server found in this project:"
	g, ok = claude.GateUp("New MCP server found in this project: nanoclaw\n  [Enter] to approve")
	require.True(t, ok, "capital-N singular MCP gate must fire")
	require.Equal(t, DismissEnter, g.Dismiss)

	_, ok = claude.GateUp("╭───╮\n│ > │  ? for shortcuts\n╰───╯")
	require.False(t, ok)
}

// --- Codex fixtures. Layout per openai/codex tui: the status row renders above
// the composer ("Working (0s • esc to interrupt)", pinned by the repo's own
// status_indicator_widget test), approval options per approval_overlay.rs.

func TestCodexBusyMarker(t *testing.T) {
	working := strings.Join([]string{
		"• I ran the build; now fixing the failing test.",
		"",
		"▌ Working (12s • esc to interrupt)",
		"",
		"› ",
		"",
		"  ? for shortcuts",
	}, "\n")
	require.True(t, codex.HasBusyMarker(working),
		"the status row above the composer must be inside the marker window")

	idle := "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	require.False(t, codex.HasBusyMarker(idle))

	// Marker text deep in the transcript (outside the window) must not count.
	scrollback := "We match the codex \"esc to interrupt\" status row.\n" +
		strings.Repeat("a normal line of build output\n", 10) +
		"› \n  ? for shortcuts"
	require.False(t, codex.HasBusyMarker(scrollback))
}

func TestCodexPrompts(t *testing.T) {
	approval := strings.Join([]string{
		"Would you like to run the following command?",
		"",
		"  rm -rf build/",
		"",
		"› 1. Yes, proceed",
		"  2. Yes, and don't ask again for this command in this session",
		"  3. No, and tell Codex what to do differently",
	}, "\n")
	name, ok := codex.DetectPrompt(approval)
	require.True(t, ok)
	require.Equal(t, "approval", name)

	permissions := "Codex needs your approval.\n› 1. Yes, grant these permissions for this turn\n" +
		"  2. No, continue without permissions"
	_, ok = codex.DetectPrompt(permissions)
	require.True(t, ok, "permission prompt variant")

	idle := "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	_, ok = codex.DetectPrompt(idle)
	require.False(t, ok)
}

func TestCodexGateAndResume(t *testing.T) {
	g, ok := codex.GateUp("Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit")
	require.True(t, ok)
	require.Equal(t, DismissEnter, g.Dismiss)

	require.Equal(t, "codex resume --last", codex.Resume("codex"))
	// A program carrying flags relaunches blank: the subcommand cannot be
	// safely spliced into an arbitrary argv.
	require.Equal(t, "codex --model o3", codex.Resume("codex --model o3"))
}

// --- Gemini fixtures. Strings verified against the installed 0.27 package
// source: LoadingIndicator.js, ToolConfirmationMessage.js, FolderTrustDialog.js.

func TestGeminiBusyMarker(t *testing.T) {
	working := strings.Join([]string{
		"✦ I am refactoring the parser module now.",
		"",
		"⠏ Reticulating splines... (esc to cancel, 12s)",
		"",
		"╭──────────────────────────────────────────╮",
		"│ >                                          │",
		"╰──────────────────────────────────────────╯",
		"~/project   no sandbox   gemini-2.5-pro",
	}, "\n")
	require.True(t, gemini.HasBusyMarker(working),
		"the loading row above the input box must be inside the marker window")

	idle := "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	require.False(t, gemini.HasBusyMarker(idle))
}

func TestGeminiPrompts(t *testing.T) {
	confirm := strings.Join([]string{
		"Apply this change?",
		"  1. Allow once",
		"  2. Allow always",
		"  3. No, suggest changes (esc)",
	}, "\n")
	name, ok := gemini.DetectPrompt(confirm)
	require.True(t, ok)
	require.Equal(t, "confirmation", name)

	// The pre-adapter matcher ("Yes, allow once") no longer exists in
	// gemini-cli; current panes must match on the decline option.
	_, ok = gemini.DetectPrompt("Do you want to proceed?\n  1. Yes, allow once")
	require.False(t, ok, "stale pre-0.2x option text alone must not match")

	idle := "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	_, ok = gemini.DetectPrompt(idle)
	require.False(t, ok)
}

func TestGeminiGateAndResume(t *testing.T) {
	g, ok := gemini.GateUp("Do you trust this folder?\n● 1. Trust folder\n  2. Trust parent folder")
	require.True(t, ok)
	require.Equal(t, DismissEnter, g.Dismiss)

	require.Equal(t, "gemini --resume latest", gemini.Resume("gemini"))
	require.Equal(t, "--resume", gemini.ResumeProbe)
}

// --- Aider fixtures.

func TestAider(t *testing.T) {
	require.False(t, aider.HasBusyMarker("anything at all"),
		"aider has no busy marker; it rides the content-change fallback")

	_, ok := aider.DetectPrompt("Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]:")
	require.True(t, ok)

	g, ok := aider.GateUp("Open documentation url for more info? (Y)es/(N)o/(D)on't ask again [Yes]:")
	require.True(t, ok)
	require.Equal(t, DismissDAndEnter, g.Dismiss)

	require.Nil(t, aider.Resume, "aider has no conversation resume")
}

// NamerKeys pins which agents claim headless auto-naming and their preference
// order — each entry must have a matching invocation branch in session/naming.go.
func TestNamerKeys(t *testing.T) {
	require.Equal(t, []Key{KeyClaude, KeyGemini}, NamerKeys())
}

// --- Generic: an unknown agent gets no heuristics — and, unlike the
// pre-adapter behavior, no aider documentation gate firing a stray 'D' at it.

func TestGeneric(t *testing.T) {
	g := Resolve("some-unknown-agent")
	require.Equal(t, KeyGeneric, g.Key)
	require.False(t, g.HasBusyMarker("esc to interrupt"))
	_, ok := g.DetectPrompt("Do you want to proceed? (Y)es/(N)o")
	require.False(t, ok)
	_, ok = g.GateUp("Open documentation url for more info")
	require.False(t, ok)
	require.Nil(t, g.Resume)
	require.False(t, g.HookSupport)
}
