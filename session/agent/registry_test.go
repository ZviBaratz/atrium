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
	// at the box interior, so the quote must not read as a live prompt. The named
	// top border is the regression #332 fixed: the segment scan used to delimit on
	// the strict isHorizontalRule, which does not recognize a border carrying the
	// agent-context/branch name, so the box never opened a segment of its own and
	// the stop never fired. This matcher had the same latent false positive as
	// permission-local; the delimiter fix (chrome.go footerVisibleInSegments) closes
	// both, so pin it here rather than let it ride along untested.
	for name, box := range map[string][]string{
		"plain border": {"╭────────────────────────────╮", "│ >                          │", "╰────────────────────────────╯"},
		"named border": {"──── zvi/issue-332 ─────────", "❯ ", "────────────────────────────"},
	} {
		_, ok = claude.DetectPrompt(strings.Join(append([]string{
			"  The footer reads: Enter to select · ↑/↓ to navigate · Esc to cancel",
		}, append(box, "  ? for shortcuts")...), "\n"))
		require.False(t, ok, "a footer quote in the transcript (%s) must not match", name)
	}

	// Live idle/working footers must not classify as prompts.
	for _, footer := range []string{
		"❯ \n⏵⏵ auto mode on · 1 shell · ctrl+t to hide tasks · ← for agents · ↓ to manage",
		"❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	} {
		_, ok := claude.DetectPrompt(footer)
		require.False(t, ok, "idle footer must not be a prompt: %q", footer)
	}
}

// claudeWritePermissionPane is a live tool-permission dialog for a file write,
// captured verbatim from claude 2.1.210 (tmux capture-pane, 2026-07-15) and
// byte-identical on 2.1.207 — the version VerifiedVersion pinned while this
// shape went undetected (#332). The decline option is a bare "3. No": the
// "No, and tell Claude what to do differently" literal the "permission" matcher
// requires belongs only to the WebFetch/network dialogs, never to this one. The
// footer carries no "to navigate"/"to select" either, so the selection matcher
// misses it too — pre-fix the whole pane read as idle, showing a blocked session
// as Ready.
var claudeWritePermissionPane = strings.Join([]string{
	"● Write(hello.txt)",
	"────────────────────────────────────────────────────────",
	" Create file",
	" hello.txt",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"  1 hi",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	" Do you want to create hello.txt?",
	" ❯ 1. Yes",
	"   2. Yes, allow all edits during this session (shift+tab)",
	"   3. No",
	"",
	" Esc to cancel · Tab to amend",
}, "\n")

// claudeBashPermissionPane is the same dialog for a shell command, captured from
// live claude 2.1.210 (2026-07-15). Its options differ from the write dialog's
// ("Yes, and always allow access to <dir> from this project") and it carries an
// extra "ctrl+e to explain" hint, so the two shapes share only the question, the
// bare "No", and the "Esc to cancel · Tab to amend" footer pair — which is what
// the matcher keys on.
var claudeBashPermissionPane = strings.Join([]string{
	"● Running 1 shell command…",
	"  ⎿  $ mkdir probedir",
	"────────────────────────────────────────────────────────",
	" Bash command",
	"   mkdir probedir",
	"   Create probedir directory",
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and always allow access to atr-bash-UT3JTK/ from this project",
	"   3. No",
	" Esc to cancel · Tab to amend · ctrl+e to explain",
}, "\n")

// TestClaudeLocalPermissionPrompt pins the tool-permission matcher against both
// live shapes (#332). NoAutoTap: unlike the WebFetch dialog the "permission"
// matcher auto-answers, these gate local file writes and shell commands, so
// autoyes surfaces them as needs-input rather than Enter-approving them.
func TestClaudeLocalPermissionPrompt(t *testing.T) {
	for name, pane := range map[string]string{
		"write": claudeWritePermissionPane,
		"bash":  claudeBashPermissionPane,
	} {
		m, ok := claude.DetectPrompt(pane)
		require.True(t, ok, "the live %s permission dialog must be detected", name)
		require.Equal(t, "permission-local", m.Name)
		require.True(t, m.NoAutoTap, "autoyes must not auto-approve a local %s permission", name)
	}

	// Ordering guard. This pane is CONSTRUCTED, not captured: the option labels
	// are the bundle's fetch-dialog literals but its real footer was never
	// observed, so the "Esc to cancel · Tab to amend" line here is the adversarial
	// worst case — if the fetch dialog ever does carry that footer, "permission"
	// must still win, or autoyes would silently stop answering a prompt it
	// answers today. The assertion is about matcher order, not about the pane.
	m, ok := claude.DetectPrompt(strings.Join([]string{
		" Do you want to allow Claude to fetch this content?",
		" ❯ 1. Yes",
		"   2. Yes, and don't ask again for example.com",
		"   3. No, and tell Claude what to do differently (esc)",
		" Esc to cancel · Tab to amend",
	}, "\n"))
	require.True(t, ok)
	require.Equal(t, "permission", m.Name, "the fetch dialog must keep the auto-tappable matcher")
	require.False(t, m.NoAutoTap, "autoyes still answers the fetch dialog")

	// The trust gate and the /model picker both carry "Esc to cancel" but no
	// "Tab to amend"; requiring the pair keeps them out of this matcher.
	for name, footer := range map[string]string{
		"trust gate":   " ❯ 1. Yes, I trust this folder\n   2. No, exit\n Enter to confirm · Esc to cancel",
		"model picker": "   5. Haiku\n Enter to set as default · s to use this session only · Esc to cancel",
	} {
		if m, ok := claude.DetectPrompt(footer); ok {
			require.NotEqual(t, "permission-local", m.Name, "%s must not read as a tool permission", name)
		}
	}

	// The footer quoted in the transcript of an IDLE session, close enough to the
	// bottom to sit inside the matcher's window — the case that makes this matcher
	// structural rather than a flat bottom-N match. Atrium's own agents print this
	// exact string (it is in this file), and an idle pane never scrolls, so a flat
	// match would pin the row at needs-input until the user typed. The segment scan
	// stops at the input box, which a real dialog replaces while it is up.
	//
	// Both border forms must reject it. The NAMED one is the case that matters: claude
	// renders the agent-context/branch name inside the top border, and while the segment
	// scan delimited on the strict isHorizontalRule that border was invisible to it — the
	// bottom segment then spanned transcript AND box, so the input-box stop never fired
	// and this pane matched (#332). An Atrium session working on Atrium hits exactly this:
	// branch name in the border, this footer in the transcript.
	for name, top := range map[string]string{
		"plain border": strings.Repeat("─", 40),
		"named border": "──── zvi/issue-332 ───────────────────",
	} {
		_, ok = claude.DetectPrompt(strings.Join([]string{
			"● The dialog's footer reads: Esc to cancel · Tab to amend",
			"  so the matcher keys on that pair.",
			"",
			top,
			"❯ ",
			strings.Repeat("─", 40),
			"  ⏸ manual mode on · ? for shortcuts · ← for agents",
		}, "\n"))
		require.False(t, ok, "idle pane quoting the footer (%s) must not read as a live prompt", name)
	}

	// The same quote pushed far above the box must stay out too.
	_, ok = claude.DetectPrompt("  It said: Esc to cancel · Tab to amend\n" +
		strings.Repeat("a transcript line\n", WindowPrompt) +
		"╭───╮\n│ > │\n╰───╯\n  ⏸ manual mode on · ? for shortcuts")
	require.False(t, ok, "a transcript mention must not match")
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

// claudeMCPSinglePane is the one-server MCP approval, captured verbatim from live claude
// 2.1.210 launched in a fresh dir holding a project-scoped .mcp.json (2026-07-15). It
// replaces a composed fixture that ended "[Enter] to approve" — a line this dialog does not
// render (the string does exist elsewhere in the bundle, which is exactly why its presence
// there proved nothing). The title was always right, so the gate always fired; #332's
// permission bug was the same setup with the opposite outcome, so the shape is pinned from
// a real pane now rather than from a plausible guess.
var claudeMCPSinglePane = strings.Join([]string{
	strings.Repeat("─", 56),
	"  New MCP server found in this project: nanoclaw",
	"  MCP servers may execute code or access system resources. All tool calls require approval. Learn more in",
	"  the MCP documentation.",
	"  ❯ 1. Use this MCP server",
	"    2. Use this and all future MCP servers in this project",
	"    3. Continue without using this MCP server",
	"  Enter to confirm · Esc to cancel",
}, "\n")

// claudeMCPMultiPane is the multi-server MCP approval (live 2.1.210, three servers in one
// project-scoped .mcp.json). A distinct shape no fixture covered before: a checkbox
// multi-select whose title is PLURAL ("3 new MCP servers found in this project" — the
// lowercase gate literal matches it as a substring) and whose footer reads "Esc to reject
// all" rather than "Esc to cancel". The bundle's token table for this dialog reads
// "space select · enter confirm", which is not the rendered line — the standing reminder
// that the table enumerates, only a probe renders.
//
// Width is load-bearing here in a way it is not for the trust gate, and the limit is
// measured rather than reasoned: GateUp scans only the bottom WindowPrompt (15) non-empty
// lines, the trust gate's literal sits on an option line three lines off the bottom, and
// these titles sit ~8 lines up behind a prose paragraph that reflows. Narrowing the pane
// grows that paragraph and walks the title past the budget. Driven live at 2.1.210 against
// real captures at each width (#340):
//
//	110 → fires    40 → fires    28 → MISSES    24 → MISSES
//
// At 28 the wrapped dialog runs 17 non-empty lines, so the title is the 16th from the
// bottom and falls outside the 15-line budget; the gate reads nothing and an MCP-blocked
// session reads Ready.
//
// That width is REACHABLE, and the tempting reading — "no working terminal is 28 columns" —
// is a category error worth spelling out, because it is what kept this miss filed as
// theoretical. The pane is not the terminal. session/instance.go SetPreviewSize sizes each
// agent's detached tmux session to the PREVIEW pane, precisely so captured content wraps the
// way it renders, and that pane is the terminal minus the session list minus two 2-column
// frames. The split is user-adjustable (< / >, mouse drag) to config.maxListRatio = 0.60 and
// persisted in state.json, so it survives restarts. Measured by driving the real layout
// (ui.TabbedWindow SetSize → GetPreviewSize) rather than re-deriving its arithmetic:
//
//	term=80 ratio=0.60 → preview=28    term=100 ratio=0.60 → preview=36
//
// A plain 80-column terminal with the list dragged wide lands exactly on the miss.
//
// Recorded rather than fixed because the flat window is the wrong instrument to tune, not
// because the miss is rare: widening the budget buys it back with false-positive surface on
// every gate at every width, and the window already fails at the OTHER end too — an agent
// that merely quotes these titles reads as gated (#342). Only a liveness signal separates
// showing the dialog from discussing it; #344 anchors the match to live chrome instead of
// counting lines from the bottom, which dissolves the width limit and retires this
// paragraph. claudeMCPWrappedPane below is the narrowest CAPTURE that still fires — 40, not
// a measured boundary: the widths between it and 28 were never driven, and pinning an exact
// threshold would only pin how one prose paragraph happens to wrap today.
var claudeMCPMultiPane = strings.Join([]string{
	strings.Repeat("─", 56),
	"  3 new MCP servers found in this project",
	"  Select any you wish to enable.",
	"  MCP servers may execute code or access system resources. All tool calls require approval. Learn more in",
	"  the MCP documentation.",
	"  ❯ [✔] nanoclaw",
	"    [✔] picoclaw",
	"    [✔] femtoclaw",
	" Space to select · Enter to confirm · Esc to reject all",
}, "\n")

// claudeMCPWrappedPane is the multi-server approval captured from a live 2.1.210 pane at
// width 40 (2026-07-15), where the title itself reflows onto two lines:
//
//	3 new MCP servers found in this
//	project
//
// It pins the property the flattened match quietly depends on — the gate literal survives a
// wrapped TITLE, because the wrap falls after it rather than inside it. A narrower capture
// is not pinned here on purpose: at 28 the gate genuinely misses (see above), so a fixture
// there would pin the limitation rather than the behavior.
var claudeMCPWrappedPane = strings.Join([]string{
	strings.Repeat("─", 40),
	"  3 new MCP servers found in this",
	"  project",
	"  Select any you wish to enable.",
	"  MCP servers may execute code or",
	"  access system resources. All tool",
	"  calls require approval. Learn more",
	"  in the MCP documentation.",
	"  ❯ [✔] nanoclaw",
	"    [✔] picoclaw",
	"    [✔] femtoclaw",
	" Space to select · Enter to confirm ·",
	" Esc to reject all",
}, "\n")

func TestClaudeGate(t *testing.T) {
	_, ok := claude.GateUp("Do you trust the files in this folder?\n  1. Yes, proceed")
	require.True(t, ok)

	// Both MCP-approval shapes, captured verbatim from live claude 2.1.210 by putting a
	// project-scoped .mcp.json in a fresh dir (2026-07-15, #340). The gate fires on the
	// title in each: "New MCP server" (capital-N singular, v2.1.162+) and "new MCP server"
	// (the plural title's substring). Nothing else in the adapter sees either — the
	// singular's footer names no navigate/select token and the plural's says "Esc to
	// reject all", not "Esc to cancel" — so the gate is the only thing standing between
	// these and a session that reads Ready while blocked.
	//
	// Each literal is load-bearing on its own: removing the capital-N fails only the
	// singular case below, removing the lowercase fails both plural shapes. Case is what
	// separates them because the plural's count prefix ("3 new…") puts the word
	// mid-sentence, so the title lowercases it.
	//
	// Subtests over a slice, not require over a map: require aborts on the first failure and
	// map order is randomized, so a dropped literal would report one arbitrary shape and hide
	// the rest — leaving the claim above ("fails both plural shapes") unobservable in the
	// test that exists to demonstrate it.
	for _, tc := range []struct{ name, pane string }{
		{"singular", claudeMCPSinglePane},
		{"plural", claudeMCPMultiPane},
		{"wrapped", claudeMCPWrappedPane},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := claude.GateUp(tc.pane)
			require.True(t, ok, "the live %s MCP dialog must fire the gate", tc.name)
		})
	}

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
