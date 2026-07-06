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
	m, ok := claude.DetectPrompt("Do this? \n  No, and tell Claude what to do differently")
	require.True(t, ok)
	require.Equal(t, "permission", m.Name)
	require.False(t, m.NoAutoTap, "permission prompts stay auto-tappable")

	m, ok = claude.DetectPrompt("How do you want to be notified?\n  1. Telegram\n  2. Email\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel")
	require.True(t, ok, "selection prompt")
	require.True(t, m.NoAutoTap, "selections are judgment prompts; autoyes must not answer them")

	// Wrapped footer: "Esc to cancel" lands on a different physical line than
	// the nav/select tokens; flattening must reconstruct it.
	m, ok = claude.DetectPrompt("Server restart?\n  1. Relaunch\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes · Esc to cancel")
	require.True(t, ok, "wrapped selection footer")
	require.True(t, m.NoAutoTap, "wrapped selection footer must stay manual-only")

	// A custom multi-line statusLine below the footer (drawing its own divider
	// rule) pushes the footer out of any fixed bottom window; the structural
	// segment scan must still see it. Mirrors the session/tmux statusLine poll
	// tests, which remain the behavioral gate.
	m, ok = claude.DetectPrompt(strings.Join([]string{
		"  6. Chat about this",
		"Enter to select · ↑/↓ to navigate · Esc to cancel",
		"────────────────────────────",
		"  main · opus · 12% ctx",
		"  3 files changed",
	}, "\n"))
	require.True(t, ok, "selection footer above a divider-drawing statusLine")
	require.Equal(t, "selection", m.Name)
	// Reversal (#271) of the #103-era pin ("generic selections stay
	// auto-tappable"): the selection footer is AskUserQuestion's surface — a
	// judgment prompt the agent renders even in bypass/auto permission modes,
	// exactly where it wants a human choice. Auto-Enter picks whatever option
	// is highlighted and chains through multi-question flows, so autoyes must
	// surface it as needs-input instead (the same carve-out #103 made for the
	// plan-approval dialog).
	require.True(t, m.NoAutoTap, "selections are manual-only; autoyes must not answer them")

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

// claudePlanPane is a live plan-approval dialog captured from claude 2.1.170
// (tmux capture-pane, 2026-06-10). Note the dialog carries no selection footer
// ("Esc to cancel" / "to navigate"), so the generic selection matcher does NOT
// see it — without the plan matcher this pane classifies as idle.
var claudePlanPane = strings.Join([]string{
	"   Ready to code?",
	"   Here is Claude's plan:",
	"  ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"   Plan",
	"   Write a file hello.txt in /tmp/demo containing the word \"hello\" using the Write tool.",
	"  ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"  ──────────────────────────────────────────────────────",
	"   Claude has written up a plan and is ready to execute. Would you like to proceed?",
	"",
	"   ❯ 1. Yes, and use auto mode",
	"     2. Yes, manually approve edits",
	"     3. No, refine with Ultraplan on Claude Code on the web",
	"     4. Tell Claude what to change",
	"        shift+tab to approve with this feedback",
	"",
	"   ctrl+g to edit in  VS Code  · ~/.claude/plans/make-a-plan-to-glimmering-wand.md",
}, "\n")

// TestClaudePlanPrompt pins the plan-approval matcher against the live pane: it
// must fire (the dialog has no selection footer, so nothing else detects it) and
// carry NoAutoTap, since Enter would accept the plan AND enable auto mode.
func TestClaudePlanPrompt(t *testing.T) {
	m, ok := claude.DetectPrompt(claudePlanPane)
	require.True(t, ok, "the live plan-approval pane must be detected")
	require.Equal(t, "plan", m.Name)
	require.True(t, m.NoAutoTap)

	// The binary carries an alternate label set for the same dialog ("Yes,
	// auto-accept edits" … "No, keep planning"); that variant must match too.
	variant := strings.Join([]string{
		"   Would you like to proceed?",
		"",
		"   ❯ 1. Yes, and auto-accept edits",
		"     2. Yes, and manually approve edits",
		"     3. No, keep planning",
	}, "\n")
	m, ok = claude.DetectPrompt(variant)
	require.True(t, ok, "the binary's alternate option labels must match")
	require.Equal(t, "plan", m.Name)
	require.True(t, m.NoAutoTap)

	// Plan-option text mentioned in prose above the input box must not read as a
	// live plan prompt (the windowed match only sees the bottom chrome).
	_, ok = claude.DetectPrompt("  I picked Yes, manually approve edits earlier.\n" +
		strings.Repeat("a transcript line\n", WindowPrompt) +
		"╭───╮\n│ > │\n╰───╯\n  ? for shortcuts")
	require.False(t, ok, "a transcript mention must not match")
}

// claudeModelErrorPane is a live bad-model launch captured from claude 2.1.170
// (tmux capture-pane after `claude --model atrium-bogus-model-check` + a first
// prompt, 2026-06-10). The session stays alive with an idle input box — without
// the model-error matcher this pane classifies as idle, hiding the failure.
var claudeModelErrorPane = strings.Join([]string{
	" ⚠ 1 setup issue: MCP · /doctor",
	"",
	"❯ say hi",
	"",
	"● There's an issue with the selected model (atrium-bogus-model-check). It may not exist or you may",
	"  not have access to it. Run /model to pick a different model.",
	"",
	"✻ Cogitated for 0s",
	"",
	"────────────────────────────────────────────────────────────────────────────────────────────────────",
	"❯ ",
	"────────────────────────────────────────────────────────────────────────────────────────────────────",
	"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents                         Remote Control active",
}, "\n")

// TestClaudeModelErrorPrompt pins the model-error matcher against the live pane
// (the launched session is the model-name validator — Atrium deliberately has
// no allowlist) plus the binary's Pro-plan access variant, and proves NoAutoTap:
// there is nothing for autoyes to answer.
func TestClaudeModelErrorPrompt(t *testing.T) {
	m, ok := claude.DetectPrompt(claudeModelErrorPane)
	require.True(t, ok, "the live bad-model pane must be detected")
	require.Equal(t, "model-error", m.Name)
	require.True(t, m.NoAutoTap)

	// The 2.1.170 binary's access-restriction variant (400 invalid model name on
	// a Pro plan) must match too.
	m, ok = claude.DetectPrompt("● Claude Opus is not available with the Claude Pro plan. " +
		"If you have updated your subscription plan recently, run /logout and /login " +
		"for the plan to take effect.\n\n❯ ")
	require.True(t, ok, "the Pro-plan variant must match")
	require.Equal(t, "model-error", m.Name)
	require.True(t, m.NoAutoTap)

	// The message hard-wrapped at a narrow width must survive flattening.
	m, ok = claude.DetectPrompt("● There's an issue with the selected\n" +
		"  model (bogus). It may not exist or\n  you may not have access to it.\n❯ ")
	require.True(t, ok, "narrow-pane wrap must still match")
	require.Equal(t, "model-error", m.Name)

	// The same text scrolled above WindowPrompt non-empty lines must not match.
	_, ok = claude.DetectPrompt("There's an issue with the selected model (bogus).\n" +
		strings.Repeat("a transcript line\n", WindowPrompt) +
		"❯ ")
	require.False(t, ok, "a scrolled-away error must not match")
}

// TestClaudeLoginErrorPrompt pins the auth-expiry matcher. Fixture constructed
// from the 2.1.170 binary's literal message prefix ("Please run /login · API
// Error: …" — mE() in its error mapping); a live capture would require a
// revoked token. NoAutoTap: tapping Enter cannot re-authenticate.
func TestClaudeLoginErrorPrompt(t *testing.T) {
	m, ok := claude.DetectPrompt(strings.Join([]string{
		"❯ continue",
		"",
		"● Please run /login · API Error: 401 OAuth token has expired",
		"",
		"────────────────────────────────────",
		"❯ ",
		"────────────────────────────────────",
		"  ⏵⏵ auto mode on (shift+tab to cycle)",
	}, "\n"))
	require.True(t, ok, "the auth-expiry pane must be detected")
	require.Equal(t, "login-error", m.Name)
	require.True(t, m.NoAutoTap)

	// Prose merely mentioning /login (no middle-dot prefix) must not match.
	_, ok = claude.DetectPrompt("  You could run /login to switch accounts.\n❯ ")
	require.False(t, ok, "a prose mention of /login must not match")
}

func TestClaudeGate(t *testing.T) {
	_, ok := claude.GateUp("Do you trust the files in this folder?\n  1. Yes, proceed")
	require.True(t, ok)

	// Claude Code v2.1.162+ uses capital-N "New MCP server found in this project:"
	_, ok = claude.GateUp("New MCP server found in this project: nanoclaw\n  [Enter] to approve")
	require.True(t, ok, "capital-N singular MCP gate must fire")

	_, ok = claude.GateUp("╭───╮\n│ > │  ? for shortcuts\n╰───╯")
	require.False(t, ok)

	// A gate literal quoted far above the live dialog region — the transcript body, or
	// the agent's own output (a claude session editing this very registry, or discussing
	// a "New MCP server") — must not fire the gate: detection is confined to the bottom
	// chrome, so a working/idle pane is never misclassified as blocked (#266 follow-up).
	var body strings.Builder
	body.WriteString("New MCP server found in this project: nanoclaw\n")
	body.WriteString("Do you trust the files in this folder?\n")
	for i := 0; i < WindowPrompt+5; i++ {
		body.WriteString("plain transcript line\n")
	}
	body.WriteString("╭───╮\n│ > │  ? for shortcuts\n╰───╯")
	_, ok = claude.GateUp(body.String())
	require.False(t, ok, "a gate string above the live dialog region must not fire the gate")
}

// claudeTrustPane is the folder-trust dialog captured verbatim from a live
// claude 2.1.185 launched in a fresh (untrusted) directory (2026-06-22). Claude
// reworded the dialog after 2.1.170: the old "Do you trust the files in this
// folder?" title is gone, replaced by the "Quick safety check…" copy below with
// a "Yes, I trust this folder" confirm button — the gate must still fire so the
// session surfaces as needs-input rather than a stale Ready.
const claudeTrustPane = `
────────────────────────────────────────────────────────────────────────────
 Accessing workspace:

 /tmp/atr-trust-XBG1IL

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source
 project, or work from your team). If not, take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`

func TestClaudeTrustGate_2_1_185(t *testing.T) {
	_, ok := claude.GateUp(claudeTrustPane)
	require.True(t, ok, "reworded 2.1.185 trust dialog must still fire the gate")
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
	m, ok := codex.DetectPrompt(approval)
	require.True(t, ok)
	require.Equal(t, "approval", m.Name)

	permissions := "Codex needs your approval.\n› 1. Yes, grant these permissions for this turn\n" +
		"  2. No, continue without permissions"
	_, ok = codex.DetectPrompt(permissions)
	require.True(t, ok, "permission prompt variant")

	idle := "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	_, ok = codex.DetectPrompt(idle)
	require.False(t, ok)
}

func TestCodexGateAndResume(t *testing.T) {
	_, ok := codex.GateUp("Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit")
	require.True(t, ok)

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
	m, ok := gemini.DetectPrompt(confirm)
	require.True(t, ok)
	require.Equal(t, "confirmation", m.Name)

	// The pre-adapter matcher ("Yes, allow once") no longer exists in
	// gemini-cli; current panes must match on the decline option.
	_, ok = gemini.DetectPrompt("Do you want to proceed?\n  1. Yes, allow once")
	require.False(t, ok, "stale pre-0.2x option text alone must not match")

	idle := "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	_, ok = gemini.DetectPrompt(idle)
	require.False(t, ok)
}

func TestGeminiGateAndResume(t *testing.T) {
	_, ok := gemini.GateUp("Do you trust this folder?\n● 1. Trust folder\n  2. Trust parent folder")
	require.True(t, ok)

	require.Equal(t, "gemini --resume latest", gemini.Resume("gemini"))
	require.Equal(t, "--resume", gemini.ResumeProbe)
}

// --- Aider fixtures.

func TestAider(t *testing.T) {
	require.False(t, aider.HasBusyMarker("anything at all"),
		"aider has no busy marker; it rides the content-change fallback")

	// The pre-#271 pinned shape must keep matching (the broadened matcher is a
	// strict superset — additive remediation, nothing replaced).
	_, ok := aider.DetectPrompt("Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]:")
	require.True(t, ok)

	_, ok = aider.GateUp("Open documentation url for more info? (Y)es/(N)o/(D)on't ask again [Yes]:")
	require.True(t, ok)

	require.Nil(t, aider.Resume, "aider has no conversation resume")
}

// TestAiderConfirmShapes pins every confirm_ask option shape aider 0.86.2
// renders, each against a pane captured live in tmux (2026-07-04; environment
// warning lines trimmed). confirm_ask (io.py) always opens the options with
// " (Y)es/(N)o", then appends "/(A)ll" (group, not explicit-yes), "/(S)kip
// all" (group), "/(D)on't ask again" (allow_never), then " [Yes]: "/" [No]: ".
// Before #271 only the "/(D)on't ask again" shape was matched, so the other
// confirms read as *idle* — a blocked session showed Ready and autoyes tapped
// nothing. The FP guards below pin the other half of the matcher
// (aiderConfirmVisible): only a pane still blocked at the trailing
// "[Yes]:"/"[No]:" default suffix is a live confirm.
func TestAiderConfirmShapes(t *testing.T) {
	cases := []struct {
		name string
		pane string
	}{
		// main.py:191 — plain shape, startup .gitignore recommendation.
		{"plain gitignore", strings.Join([]string{
			"Update git name with: git config user.name \"Your Name\"",
			"Update git email with: git config user.email \"you@example.com\"",
			"You can skip this check with --no-gitignore",
			"Add .aider* to .gitignore (recommended)? (Y)es/(N)o [Yes]:",
		}, "\n")},
		// commands.py:1019 — plain shape after /run.
		{"plain run output", strings.Join([]string{
			"hello-from-atrium",
			"Add 0.2k tokens of command output to the chat? (Y)es/(N)o [Yes]:",
		}, "\n")},
		// base_coder.py check_for_file_mentions — a single mention (group of 1
		// collapses, allow_never=True keeps the "(D)on't" option).
		{"single file mention", strings.Join([]string{
			"> please look at qux.py",
			"qux.py",
			"Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]:",
		}, "\n")},
		// base_coder.py check_for_file_mentions — a multi-file group.
		{"multi file mention", strings.Join([]string{
			"> please look at foo.py and bar.py",
			"bar.py",
			"Add file to the chat? (Y)es/(N)o/(A)ll/(S)kip all/(D)on't ask again [Yes]:",
		}, "\n")},
		// base_coder.py:2456 handle_shell_commands (explicit_yes_required drops
		// "(A)ll"). LLM-driven, so captured by driving the installed package's
		// InputOutput.confirm_ask in tmux with that caller's exact kwargs.
		{"run shell command", strings.Join([]string{
			"mkdir -p build",
			"Run shell command? (Y)es/(N)o/(S)kip all/(D)on't ask again [Yes]:",
		}, "\n")},
		// A hard terminal wrap can split the options run mid-token; flattening
		// joins the physical lines, so the pair match must survive it.
		{"wrapped options", "Add file to the chat? (Y)es/\n(N)o/(D)on't ask again [Yes]:"},
	}
	for _, c := range cases {
		m, ok := aider.DetectPrompt(c.pane)
		require.True(t, ok, "%s must classify as a prompt", c.name)
		require.Equal(t, "confirm", m.Name, c.name)
		require.False(t, m.NoAutoTap, "%s: aider confirms stay auto-tappable", c.name)
	}

	// FP guards: an idle aider pane (startup banner + bare composer, captured
	// from the same 0.86.2 session) and prose carrying only one of the tokens
	// must stay non-prompts.
	idle := strings.Join([]string{
		"Aider v0.86.2",
		"Main model: gpt-4o with diff edit format",
		"Git repo: .git with 3 files",
		"Repo-map: using 4096 tokens, auto refresh",
		">",
	}, "\n")
	_, ok := aider.DetectPrompt(idle)
	require.False(t, ok, "an idle aider pane must not read as a prompt")

	_, ok = aider.DetectPrompt("I answered (Y)es to the last prompt.\n>")
	require.False(t, ok, "one token alone must not read as a prompt")

	// Both tokens present but no live confirm: the pane must end at the
	// "[Yes]:"/"[No]:" default suffix where confirm_ask parks its cursor.
	// Displayed content that merely mentions both tokens above the composer
	// (e.g. aider showing this very matcher's source, or prose about Y/N
	// confirms) is not a prompt.
	sourceDisplay := strings.Join([]string{
		"Here is the matcher table entry:",
		"    All: []string{\"(Y)es\", \"(N)o\"},",
		">",
	}, "\n")
	_, ok = aider.DetectPrompt(sourceDisplay)
	require.False(t, ok, "both tokens in displayed content above the composer must not read as a prompt")

	// An answered confirm is no longer live: the echoed answer ("… [Yes]: y")
	// displaces the suffix from the line end…
	_, ok = aider.DetectPrompt("Add file to the chat? (Y)es/(N)o [Yes]: y")
	require.False(t, ok, "an answered confirm must not re-read as a live prompt")

	// …and once any output lands below it, the suffix line is no longer
	// bottom-most. Pre-fix, this lingering pane re-matched every poll tick
	// until 15 lines of output scrolled it away — autoyes tapped a stray
	// Enter per tick, and without autoyes the session pinned NeedsInput
	// while aider was actually working.
	answered := strings.Join([]string{
		"Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]: y",
		"Added qux.py to the chat",
		">",
	}, "\n")
	_, ok = aider.DetectPrompt(answered)
	require.False(t, ok, "an answered confirm above later output must not re-read as a live prompt")
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
