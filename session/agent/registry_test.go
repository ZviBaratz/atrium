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

// TestAutoTapRequiresAnAnchoredMatcher is #347's audit as an executable invariant, and it is
// the only thing in the tree that states the rule rather than checking one matcher that
// happens to follow it.
//
// Poll maps a matched prompt to PanePrompt unless the matcher sets NoAutoTap, and autoyes
// taps Enter on PanePrompt (session/tmux/poll.go, session/instance.go ApplyPaneState). A
// matcher with no Match reads a flat bottom-N window, which is a budget and not a liveness
// test (#342, #343): the literals such a matcher keys on live verbatim in registry.go, so an
// agent that greps, reads or discusses this file prints them into its own pane. Flat window
// AND auto-tap together is how a quote in a transcript came to approve a shell command.
//
// So the requirement is structural: a matcher autoyes answers must carry a Match that proves
// its dialog is live chrome AND that it is the dialog it claims — the two halves #350 found
// were both load-bearing. Anything less surfaces as needs-input, which is always safe.
func TestAutoTapRequiresAnAnchoredMatcher(t *testing.T) {
	var autoTapped, unanchored []string
	for _, a := range registry {
		for _, m := range a.Prompts {
			if m.NoAutoTap {
				continue
			}
			name := string(a.Key) + "/" + m.Name
			autoTapped = append(autoTapped, name)
			if m.Match == nil {
				unanchored = append(unanchored, name)
			}
		}
	}
	require.Empty(t, unanchored,
		"a flat-window matcher (Match == nil) must set NoAutoTap: its literals are quotable "+
			"from this file, and autoyes answers what it matches")

	// The allowlist is the other direction: dropping NoAutoTap from an anchored matcher is
	// just as much a decision as adding a flat one, and a diff that does it silently reads
	// like a cleanup. Both entries below earned it by being driven — claude's fetch dialog in
	// #350, aider's confirm shapes in #271.
	require.Equal(t, []string{"claude/permission", "aider/confirm"}, autoTapped,
		"the set of matchers autoyes may answer changed; each addition needs a captured "+
			"dialog and a positive identifier, not just an anchor")
}

// --- Claude fixtures (mirroring the session/tmux poll tests, which remain the
// behavioral regression gate; these pin the same heuristics at the table level).

// claudeBusyDefaultPane and claudeBusyRebindPane are the #354 A/B: the SAME live
// claude 2.1.220 pane (tmux capture-pane, width 100, 2026-07-28), streaming the same
// kind of turn, captured once with the default keybindings and once after rebinding
// chat:cancel to ctrl+q in <CLAUDE_CONFIG_DIR>/keybindings.json. Claude hot-reloads
// that file with no restart, so the two differ ONLY in the chord half of the interrupt
// hint — which is the whole point: "esc" is the user's, "to interrupt" is claude's.
//
// The trailing "/rc" is a custom statusLine sharing the footer row; kept because it is
// what the pane really rendered. The footer sits below the last horizontal rule, so
// footerRegion finds it without the no-border fallback.
const claudeBusyDefaultPane = `  10. The name outlived the paper. Ask it why

────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · esc to interrupt · ← for agents                                            /rc`

const claudeBusyRebindPane = `  60. the git status, and the thing that broke, as well.

────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · ctrl+q to interrupt · ← for agents                                         /rc`

func TestClaudeBusyMarker(t *testing.T) {
	require.True(t, claude.HasBusyMarker("✻ Cogitating… (5s · esc to interrupt)"))

	// #354: the chord is whatever the user bound chat:cancel to, so the marker must key
	// on the action half. Both captures are live panes, not variants of one string.
	require.True(t, claude.HasBusyMarker(claudeBusyDefaultPane), "default binding")
	require.True(t, claude.HasBusyMarker(claudeBusyRebindPane), "rebound chat:cancel")
	// Guards the fixture rather than the matcher: if this capture ever carried the old
	// literal, the assertion above would pass without exercising the broadening at all.
	require.NotContains(t, claudeBusyRebindPane, "esc to interrupt",
		"the rebound capture must not also carry the default chord")

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
	// This is the guard the #354 broadening had to leave intact: the box border, not the
	// chord, is what separates live chrome from a quote, so widening the literal does not
	// widen this. Phrased without the chord so it exercises the broadened matcher.
	scrollback := strings.Join([]string{
		"  I will add the \"to interrupt\" marker check now.",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"  ● main",
	}, "\n")
	require.False(t, claude.HasBusyMarker(scrollback))

	// The surface the broadening DOES buy, pinned so it is a known cost rather than a
	// later surprise. With no box border on the pane, MarkerWindow 0 falls back to the
	// last three non-empty lines (chrome.go footerRegion), and the marker carries no
	// animation gate, so a static match holds Working. Prose that names a different chord
	// now matches where "esc to interrupt" did not. Accepted, not desired: narrowing
	// footerRegion would also move permission-mode detection, which shares it, and a
	// borderless claude pane is one whose TUI is not up.
	borderless := "  Press ctrl+c to interrupt the running command."
	require.True(t, claude.HasBusyMarker(borderless),
		"borderless prose matches: the accepted cost of dropping the chord")
}

func TestClaudePrompts(t *testing.T) {
	m, ok := claude.DetectPrompt(claudeFetchPane)
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

// claudeFetchPane is the network-permission dialog, captured verbatim from live claude
// 2.1.210 at width 100 (tmux capture-pane, 2026-07-15) by prompting a session to WebFetch a
// fresh domain. It replaces the CONSTRUCTED fixture that stood here through #332 and #343 —
// the bundle's option labels under a guessed "Esc to cancel · Tab to amend" footer — and the
// guess was fiction: this dialog renders NO footer at all. That is not a detail. It means
// permission-local cannot see this family, so the "permission" matcher is the only thing
// between a live fetch dialog and a queued prompt being typed into it (session/tmux
// AwaitingInput), and a miss here is not the cheap failure it looks like.
//
// The shape is what the matcher keys on: the tool's own arguments (url, prompt) render
// INSIDE the dialog, below its top rule and ABOVE the question. That ordering is the whole
// discriminator — see claudeBashForgedPane for what it costs when a matcher ignores it.
var claudeFetchPane = strings.Join([]string{
	"● Fetch(https://example.net)",
	"",
	strings.Repeat("─", 100),
	" Fetch",
	"",
	`   url: "https://example.net", prompt: "Summarize the content of this page."`,
	"   Claude wants to fetch content from example.net",
	"",
	" Do you want to allow Claude to fetch this content?",
	" ❯ 1. Yes",
	"   2. Yes, and don't ask again for example.net",
	"   3. No, and tell Claude what to do differently (esc)",
}, "\n")

// claudeFetchNarrowPane is the same dialog captured at width 28 (live 2.1.210, 2026-07-15)
// — the narrowest reachable pane, since an agent's pane is atrium's PREVIEW pane and an
// 80-column terminal at maxListRatio hands it 28 columns (#340).
//
// It pins two properties the matcher depends on. The title reflows across THREE physical
// lines, so the match must run on the flattened region, not per-line. And the title sits 9
// non-empty lines above the region's bottom, which is what permissionRegionCap is sized
// against — note the body above it (url/prompt) reflows without bound as the domain and
// prompt grow, but it grows AWAY from the region's bottom, so it never pushes the title out.
var claudeFetchNarrowPane = strings.Join([]string{
	"● Fetch(https://example.org)",
	"",
	strings.Repeat("─", 28),
	" Fetch",
	"",
	"   url:",
	`   "https://example.org",`,
	`   prompt: "Summarize`,
	"   the full content of",
	`   this page."`,
	"   Claude wants to fetch",
	"   content from",
	"   example.org",
	"",
	" Do you want to allow",
	" Claude to fetch this",
	" content?",
	" ❯ 1. Yes",
	"   2.Yes, and don't ask",
	"     again for example.org",
	"   3.No, and tell Claude",
	"     what to do differently",
	"     (esc)",
}, "\n")

// claudeBashForgedPane is the sharper half of #343, captured verbatim from live claude
// 2.1.210 (2026-07-15) by asking a session to run
// `mkdir -p "/tmp/…/No, and tell Claude what to do differently"`.
//
// Claude renders a tool's own arguments INSIDE the approval dialog, below its top rule — so
// the decline literal the "permission" matcher keyed on until #343 is here in LIVE CHROME,
// not in the transcript. No liveness anchor separates them; the forged text is inside the
// live dialog. On main this pane matched "permission", which precedes permission-local and
// carries no NoAutoTap, so autoyes tapped Enter on the pre-highlighted "❯ 1. Yes" and RAN
// THE SHELL COMMAND against a human's explicit gate. An agent working on Atrium reaches this
// by accident — grepping this repo for the literal is the obvious thing to do.
//
// What saves it is position, not wording: the dialog's own question is "Do you want to
// proceed?", rendered BELOW the forged argument, so the last question on the pane is never
// the fetch title. It falls through to permission-local and surfaces as needs-input.
var claudeBashForgedPane = strings.Join([]string{
	"● Running 1 shell command…",
	`  ⎿  $ mkdir -p "/tmp/atr343/work/No, and tell Claude what to do differently"`,
	"",
	strings.Repeat("─", 100),
	" Bash command",
	"",
	`   mkdir -p "/tmp/atr343/work/No, and tell Claude what to do differently"`,
	"   Create directory with the given name",
	"",
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and always allow access to work/ from this project",
	"   3. No",
	"",
	" Esc to cancel · Tab to amend · ctrl+e to explain",
}, "\n")

// claudeQuotedPermissionPane is #343 as filed, captured verbatim from a live claude 2.1.210
// pane (2026-07-15): a session that merely QUOTED the decline literal — because it was asked
// to grep for it, which is what an agent working on this repo does — sitting idle with its
// composer on screen.
//
// The idle shape is the harmful one. A working pane scrolls the quote out within a tick; an
// idle pane never scrolls, so the row stays wrong until a human types. And the literal here
// lands on EXACTLY the 15th non-empty line from the bottom — inside the old flat window by
// one line — which is the measurement that makes the point: the window is a budget, not a
// liveness test, and no width for it is the right one.
//
// Note the composer holds claude's ghost-text suggestion ("retry the fetch on example.com").
// That is what autoyes tapped Enter on: not a harmless keystroke on an idle box, but a
// submit of text the user never wrote.
var claudeQuotedPermissionPane = strings.Join([]string{
	"✻ Baked for 51s",
	"",
	`❯ Run this exact bash command: grep -rn "No, and tell Claude what to do differently"`,
	"  /tmp/atr343/work || true",
	"",
	"● You declined the fetch — I've stopped. Running the grep you asked for:",
	"",
	"  Searched for 1 pattern",
	"",
	"● The grep found no matches — that string doesn't appear anywhere under /tmp/atr343/work (the ||",
	"  true means the exit status was suppressed, but empty output means zero hits either way).",
	"",
	"  Note that phrase is the label on the rejection option in Claude Code's own permission prompt, not",
	"  something that would live in your repo — so an empty result is expected here.",
	"",
	"  Where do you want to go from here? The example.com fetch is still un-run; say the word if you'd",
	"  like me to retry it, or let me know what you'd prefer instead.",
	"",
	"✻ Worked for 8s",
	"",
	strings.Repeat("─", 100),
	"❯ retry the fetch on example.com",
	strings.Repeat("─", 100),
	"  ⏸ manual mode on · ? for shortcuts · ← for agents",
}, "\n")

// TestClaudeFetchPermissionPrompt pins the one prompt autoyes still answers with Enter,
// against both captured widths. NoAutoTap must stay false: this is the matcher's whole
// purpose, and #343 must not be "fixed" by quietly making it manual.
func TestClaudeFetchPermissionPrompt(t *testing.T) {
	for _, tc := range []struct{ name, pane string }{
		{"width 100", claudeFetchPane},
		{"width 28", claudeFetchNarrowPane},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := claude.DetectPrompt(tc.pane)
			require.True(t, ok, "the live fetch dialog must be detected")
			require.Equal(t, "permission", m.Name)
			require.False(t, m.NoAutoTap, "the fetch dialog stays auto-tappable")
		})
	}

	// The captured dialog renders no footer, so nothing else in the adapter sees it: this
	// matcher is the only thing blocking prompt delivery into a live fetch dialog. Pinned
	// because it is the reason the matcher may not be made stricter by fusing it with a
	// footer pair — there is no footer to fuse with.
	require.NotContains(t, claudeFetchPane, "Esc to cancel",
		"the fixture is the real dialog: no footer (the pre-#343 fixture guessed one)")
	require.False(t, claudeLocalPermissionVisible(claudeFetchPane),
		"permission-local cannot back up the fetch dialog: it keys on a footer this dialog lacks")
	require.False(t, claudeSelectionFooterVisible(claudeFetchPane),
		"the selection matcher cannot see it either")
}

// TestClaudeForgedPermissionLiteral is the sharper half of #343: the decline literal
// rendered inside a live Bash dialog's own body, where no anchor can separate it from the
// dialog's real chrome. Against main this pane matches "permission" with NoAutoTap false —
// autoyes runs the shell command.
func TestClaudeForgedPermissionLiteral(t *testing.T) {
	require.Contains(t, claudeBashForgedPane, "No, and tell Claude what to do differently",
		"the fixture's point is that the forged literal IS in the live dialog region")
	region, ok := claudeLiveDialogRegion(claudeBashForgedPane)
	require.True(t, ok)
	require.Contains(t, region, "No, and tell Claude what to do differently",
		"and that it survives into the anchored region — the anchor cannot exclude it")

	m, ok := claude.DetectPrompt(claudeBashForgedPane)
	require.True(t, ok, "it is a real dialog: it must still surface as needs-input")
	require.Equal(t, "permission-local", m.Name,
		"a Bash approval whose command quotes the fetch dialog's decline option is still a Bash approval")
	require.True(t, m.NoAutoTap, "autoyes must never Enter-approve a shell command")

	// The discriminator, stated directly: the dialog's own question is the last one on the
	// pane, and it is not the fetch title.
	require.False(t, claudeFetchPermissionVisible(claudeBashForgedPane))
	require.Contains(t, region, "Do you want to proceed?")

	// The same forgery in a write dialog's diff body, where the quoted text sits between the
	// dialog's top rule and its question.
	forgedWrite := strings.Join([]string{
		"● Write(registry.go)",
		strings.Repeat("─", 56),
		" Create file",
		" registry.go",
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		`  1 All: []string{"No, and tell Claude what to do differently"}},`,
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		" Do you want to create registry.go?",
		" ❯ 1. Yes",
		"   2. Yes, allow all edits during this session (shift+tab)",
		"   3. No",
		"",
		" Esc to cancel · Tab to amend",
	}, "\n")
	m, ok = claude.DetectPrompt(forgedWrite)
	require.True(t, ok)
	require.Equal(t, "permission-local", m.Name, "an Edit diff quoting the literal is still a write approval")
	require.True(t, m.NoAutoTap, "autoyes must never Enter-approve a file write")
}

// TestClaudePermissionIgnoresTranscriptQuote is #343 as filed: the literals live verbatim in
// registry.go, so an agent working on Atrium prints them, and a flat bottom-N window read
// the quote as a live prompt — then tapped Enter into the composer.
func TestClaudePermissionIgnoresTranscriptQuote(t *testing.T) {
	_, ok := claude.DetectPrompt(claudeQuotedPermissionPane)
	require.False(t, ok, "a pane merely quoting the decline option must not read as a live prompt")

	// The measurement the captured pane encodes: the quote is inside the old window by one
	// line. Tuning the window is not available as a fix — this is what "the window is a
	// budget, not a liveness test" means concretely.
	require.Contains(t, flattenChrome(claudeQuotedPermissionPane, WindowPrompt),
		"No, and tell Claude what to do differently",
		"the quote sits inside the flat window the matcher used to trust")

	// Nothing above the composer counts, at any distance: walk the quote up line by line.
	// The named border is the shape an Atrium session actually shows (#332): claude renders
	// the branch name inside the box's top border, and only the bottom rule anchors then.
	for pad := 0; pad < WindowPrompt; pad++ {
		var b strings.Builder
		b.WriteString(`● The option reads "No, and tell Claude what to do differently" (esc)` + "\n")
		for i := 0; i < pad; i++ {
			b.WriteString("  filler transcript line\n")
		}
		b.WriteString(strings.Repeat("─", 40) + " my-branch ──\n❯ \n" + strings.Repeat("─", 52) + "\n")
		b.WriteString("  ⏸ manual mode on · ? for shortcuts · ← for agents\n")
		_, ok = claude.DetectPrompt(b.String())
		require.Falsef(t, ok, "quote %d line(s) above the composer must not read as a prompt", pad)
	}

	// The fetch dialog's TITLE quoted in the transcript must not fire either — it is in this
	// file now, so it is quotable exactly like the option it replaced.
	_, ok = claude.DetectPrompt(strings.Join([]string{
		`● The title is "Do you want to allow Claude to fetch this content?" and option 3 is`,
		`  "No, and tell Claude what to do differently (esc)".`,
		strings.Repeat("─", 60),
		"❯ ",
		strings.Repeat("─", 60),
		"  ⏸ manual mode on · ? for shortcuts",
	}, "\n"))
	require.False(t, ok, "quoting the title must not read as a live fetch dialog")
}

// TestClaudePermissionAnchorEdges pins the anchor's two edges. The gate answers these by
// falling back to the flat window (claudeGateVisible); these matchers must NOT, because a
// borderless pane is one where no dialog can be up, so the fallback has no miss to rescue
// and one real false positive to cause — and this matcher's false positive taps Enter.
func TestClaudePermissionAnchorEdges(t *testing.T) {
	// No anchor at all: a --continue replay quoting the literals before the box paints.
	_, ok := claude.DetectPrompt(strings.Join([]string{
		" Do you want to allow Claude to fetch this content?",
		" ❯ 1. Yes",
		"   3. No, and tell Claude what to do differently (esc)",
	}, "\n"))
	require.False(t, ok, "with no border there is no anchor, and no dialog can be up: never fire")

	// An anchor with nothing under it: footerBelowBox reports ok=true with an empty region.
	_, ok = claude.DetectPrompt(" Do you want to allow Claude to fetch this content?\n" +
		" 3. No, and tell Claude what to do differently\n" + strings.Repeat("─", 40))
	require.False(t, ok, "an empty region below the anchor must not fire")

	// The ceiling (permissionRegionCap). With no composer on screen the last rule can be one
	// the agent printed itself — a markdown rule, a table edge — and everything below it is
	// transcript. Unbounded, a quote far beneath such a rule fires.
	var b strings.Builder
	b.WriteString("● Here is a table:\n" + strings.Repeat("─", 40) + "\n")
	for i := 0; i < 60; i++ {
		b.WriteString("  a normal line of build output\n")
	}
	b.WriteString(" Do you want to allow Claude to fetch this content?\n")
	b.WriteString("   3. No, and tell Claude what to do differently (esc)\n")
	for i := 0; i < 30; i++ {
		b.WriteString("  more build output\n")
	}
	_, ok = claude.DetectPrompt(b.String())
	require.False(t, ok, "a quote far below a rule the agent printed must not fire")

	// The cap must not bite a real dialog: the title sits 9 non-empty lines above the
	// region's bottom in the tallest capture. claudeFetchNarrowPane firing is the positive
	// half; this asserts the budget it lives on is the reason, so shrinking the cap fails here.
	require.Greater(t, permissionRegionCap, 9,
		"permissionRegionCap must clear the fetch title's depth (claudeFetchNarrowPane, 9 lines)")
}

// TestClaudeNetworkPermissionNet pins the detection-only net for the fetch/network family's
// undriven sibling (the sandbox's "Do you want to allow this connection?", which needs
// sandbox mode to render). It exists because the fetch dialog carries no footer: without it,
// a shape in this family that is not the fetch dialog would be detected by NOTHING, and a
// queued prompt would be typed into it (session/tmux AwaitingInput — the "❯ 1. Yes" option
// pointer reads as an input box, so InputBoxVisible does not stop it).
func TestClaudeNetworkPermissionNet(t *testing.T) {
	// Shape constructed from the 2.1.210 bundle's title (~offset 159970960), which sits
	// beside this family's decline option; a live capture needs sandbox mode. The assertion
	// is about the NET, not the pane: any live dialog carrying this family's decline option
	// is surfaced, and never tapped.
	sandbox := strings.Join([]string{
		"● Bash(curl https://example.com)",
		strings.Repeat("─", 60),
		" Network access",
		" Do you want to allow this connection?",
		" ❯ 1. Yes",
		"   2. No, and tell Claude what to do differently (esc)",
	}, "\n")
	m, ok := claude.DetectPrompt(sandbox)
	require.True(t, ok, "an undriven member of the family must still surface as needs-input")
	require.Equal(t, "permission-network", m.Name)
	require.True(t, m.NoAutoTap, "only the driven fetch dialog is auto-answered")

	// The net is anchored too: quoting the option must not surface a prompt.
	_, ok = claude.DetectPrompt(`● It reads "No, and tell Claude what to do differently".` + "\n" +
		strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ⏸ manual mode on")
	require.False(t, ok, "the net must not fire on a transcript quote either")
}

// claudeWritePermissionPane is a live tool-permission dialog for a file write,
// captured verbatim from claude 2.1.210 (tmux capture-pane, 2026-07-15) and
// byte-identical on 2.1.207 — the version VerifiedVersion pinned while this
// shape went undetected (#332). The decline option is a bare "3. No": the
// "No, and tell Claude what to do differently" literal belongs only to the
// WebFetch/network dialogs, never to this one — though #343 later showed that a
// Bash/Write dialog can still RENDER that literal inside its own body when it is
// the tool's argument, which is why "permission" no longer keys on it at all
// (claudeBashForgedPane). The
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

	// Ordering guard. This pane is CONSTRUCTED, not captured — the live dialog
	// (claudeFetchPane) renders no footer at all, so the "Esc to cancel · Tab to
	// amend" line here is the adversarial worst case: if the fetch dialog ever does
	// grow that footer, "permission" must still win, or autoyes would silently stop
	// answering a prompt it answers today. The assertion is about matcher order, not
	// about the pane. Note the header and rule are not decoration: since #343 the
	// matcher requires its title BELOW the pane's last box border, so a bare option
	// list is no longer a fetch dialog to it.
	m, ok := claude.DetectPrompt(strings.Join([]string{
		"● Fetch(https://example.com)",
		strings.Repeat("─", 56),
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
// Width used to be load-bearing here, and #340's measurement is kept because it is what
// justified the rewrite rather than because it still binds: these titles sit ~8 lines up
// behind a prose paragraph that reflows, so narrowing the pane grew that paragraph and walked
// the title past GateUp's old bottom-15 budget. Driven live at 2.1.210 against real captures
// at each width (#340):
//
//	110 → fired    40 → fired    28 → MISSED    24 → MISSED
//
// At 28 the wrapped dialog runs 17 non-empty lines, so the title was the 16th from the bottom
// and fell outside the 15-line budget; the gate read nothing and an MCP-blocked session read
// Ready.
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
// #340 wrote the paragraph above to be retired, and this is where it is retired: the flat
// window was the wrong instrument to tune, not a budget to widen — it failed at the OTHER end
// too, an agent merely quoting these titles reading as gated (#342). claudeGateVisible
// anchors the match to live chrome instead of counting lines from the bottom, so no width
// walks the title out of the region, and claudeMCPNarrowPane pins the 28 that used to fail.
// The widths above are provenance now. claudeMCPWrappedPane stays the narrowest CAPTURE at
// 40 — never a measured boundary, since the widths between it and 28 were never driven.
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
// wrapped TITLE, because the wrap falls after it rather than inside it. See
// claudeMCPNarrowPane for the width this fixture used to be the floor of.
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

// claudeMCPNarrowPane is the single-server approval captured from a live 2.1.210 pane at
// width 28 (2026-07-15) — the width #340 measured as a genuine MISS and deliberately left
// unpinned, because under the old flat bottom-15 window a fixture here "would pin the
// limitation rather than the behavior": the reflowed dialog runs 17 non-empty lines, which
// walks the title off the top of that window.
//
// It pins the behavior now. claudeGateVisible anchors on the dialog's own top rule instead
// of counting lines from the bottom, so the region it matches in is the whole dialog however
// tall it reflows, and there is no longer a width at which the gate falls out of the window.
var claudeMCPNarrowPane = strings.Join([]string{
	strings.Repeat("─", 28),
	"  New MCP server found in",
	"  this project: nanoclaw",
	"",
	"  MCP servers may execute",
	"  code or access system",
	"  resources. All tool",
	"  calls require approval.",
	"  Learn more in the MCP",
	"  documentation.",
	"",
	"  ❯ 1. Use this MCP server",
	"    2.Use this and all",
	"      future MCP servers in",
	"      this project",
	"    3.Continue without using",
	"      this MCP server",
	"",
	"  Enter to confirm · Esc",
	"  to cancel",
}, "\n")

// claudeQuotedGatePane is the bug this gate's Match exists for, captured from a live 2.1.210
// pane (2026-07-15): a session that merely QUOTES the gate's title and footer, sitting idle
// with its composer on screen. Every gate literal check reads the same region here, so the
// idle shape is the one pinned — it is also the harmful one. A working pane scrolls the quote
// out of the window within a tick or two (the reported symptom flapped between "marker →
// working" and "gate → needs-input" in the atrium log), whereas an idle pane never scrolls:
// the row stays wrong at "waiting on setup screen" until a human types, and because PaneGate
// also gates prompt delivery (session/tmux AwaitingInput, whose caller's timeout never
// bypasses it) a prompt queued to this session is silently never sent.
//
// Note what defeats a cheaper fix: the quote is the title VERBATIM, beside the real footer
// wording. Tightening the literals, or requiring a title+footer pair, would still match here —
// the sessions that hit this are the ones editing this file.
var claudeQuotedGatePane = strings.Join([]string{
	"● The title is \"New MCP server found in this project: nanoclaw\" and the footer is \"Enter to confirm",
	"  . Esc to cancel\".",
	"",
	"  Ran 1 shell command",
	"",
	"● The sentence is above. The sleep 120 was blocked by this environment's harness — standalone sleeps",
	"  aren't permitted, and it suggests using Monitor with an until-loop or a background command",
	"  instead. Let me know if you want me to wait on something specific rather than just idle.",
	"",
	"✻ Baked for 5s",
	"",
	strings.Repeat("─", 100),
	"❯ run it in the background instead",
	strings.Repeat("─", 100),
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
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
		{"narrow", claudeMCPNarrowPane},
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

// TestClaudeGateIgnoresTranscriptQuote is the regression the anchored matcher exists for: the
// distance-based test above only ever pushed the quote WindowPrompt+5 lines up, which is not
// where an agent's own output lands. Its last message sits directly above the composer —
// inside any bottom-N window — so the quote has to be excluded structurally, not by distance.
func TestClaudeGateIgnoresTranscriptQuote(t *testing.T) {
	// The captured bug: a live pane quoting the title verbatim, composer on screen.
	_, ok := claude.GateUp(claudeQuotedGatePane)
	require.False(t, ok, "a pane merely quoting the gate's title must not read as gated")

	// The same quote directly above a live permission dialog. The dialog's segment opens
	// with its own title rather than the composer, so a scan that stops at the input box
	// walks straight past it into the transcript; anchoring on the border does not.
	_, ok = claude.GateUp("● I checked the \"New MCP server found in this project:\" title\n" + claudeWritePermissionPane)
	require.False(t, ok, "a quote above a live permission dialog must not read as gated")

	// Nothing above the composer counts, at any distance: walk the quote up line by line.
	for pad := 0; pad < WindowPrompt; pad++ {
		var b strings.Builder
		b.WriteString("● discussing the New MCP server found in this project: dialog\n")
		for i := 0; i < pad; i++ {
			b.WriteString("  filler transcript line\n")
		}
		b.WriteString(strings.Repeat("─", 40) + " my-branch ──\n❯ \n" + strings.Repeat("─", 52) + "\n")
		b.WriteString("  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt\n")
		_, ok = claude.GateUp(b.String())
		require.Falsef(t, ok, "quote %d line(s) above the composer must not fire the gate", pad)
	}
}

// The anchor answers "is there a rule?", which is not the same question as "is there a live
// dialog below it?". These pin the two gaps between those, both of which the border anchor
// opens and neither of which the flat window had.
func TestClaudeGateAnchorEdges(t *testing.T) {
	// A pane whose LAST line is the rule: footerBelowBox reports ok=true with an empty
	// region. Keying the fallback on ok alone matches "" and misses a real gate — the
	// fail-dangerous direction, since a queued prompt would then be typed into the screen.
	_, ok := claude.GateUp("Do you trust the files in this folder?\n  1. Yes, proceed\n" + strings.Repeat("─", 40))
	require.True(t, ok, "an empty region below the anchor must fall back, not read as ungated")

	// The ceiling (gateRegionCap). With no composer on screen the last rule can be one the
	// agent printed itself — a markdown rule, a table edge — and everything below it is
	// transcript. Unbounded, a quote far beneath such a rule fires the gate; the old
	// bottom-15 window did not, so this is a regression the cap has to hold.
	var b strings.Builder
	b.WriteString("● Here is a table:\n" + strings.Repeat("─", 40) + "\n")
	for i := 0; i < 60; i++ {
		b.WriteString("  a normal line of build output\n")
	}
	b.WriteString("● discussing the New MCP server dialog\n")
	for i := 0; i < 60; i++ {
		b.WriteString("  more build output\n")
	}
	_, ok = claude.GateUp(b.String())
	require.False(t, ok, "a quote far below a rule the agent printed must not fire the gate")

	// The cap must not bite a real dialog: the tallest capture is 17 non-empty lines, well
	// inside it. claudeMCPNarrowPane firing in TestClaudeGate is the positive half of this;
	// this asserts the budget it lives on is the reason, so shrinking the cap fails here.
	require.Greater(t, gateRegionCap, 17,
		"gateRegionCap must clear the tallest captured dialog (claudeMCPNarrowPane, 17 lines)")
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
	require.True(t, m.NoAutoTap, "an unanchored approval must never be Enter-approved (#347)")

	permissions := "Codex needs your approval.\n› 1. Yes, grant these permissions for this turn\n" +
		"  2. No, continue without permissions"
	m, ok = codex.DetectPrompt(permissions)
	require.True(t, ok, "permission prompt variant")
	require.True(t, m.NoAutoTap)

	idle := "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	_, ok = codex.DetectPrompt(idle)
	require.False(t, ok)

	// #347 as filed, and the half NoAutoTap does not fix: the decline literals live verbatim
	// in registry.go, so a session that greps or discusses this file prints them into its own
	// pane, and the flat bottom-15 window reads the quote as a live prompt. Composed rather
	// than captured — it measures the window's reach, not codex's overlay shape, which is
	// exactly why the fix for it is a captured anchor and not a tighter literal. Until then
	// NoAutoTap is what keeps this from tapping Enter into whatever is on screen.
	quoted := strings.Join([]string{
		"• I grepped the matcher table. The codex entry keys on",
		"  \"No, and tell Codex what to do differently\", which is the",
		"  decline option of the command-approval overlay.",
		"",
		"› ",
		"",
		"  ? for shortcuts",
	}, "\n")
	m, ok = codex.DetectPrompt(quoted)
	require.True(t, ok, "the flat window still reads a quoted literal as a live prompt")
	require.True(t, m.NoAutoTap, "…so the quote must surface as needs-input, never tap Enter")
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
	require.True(t, m.NoAutoTap, "an unanchored confirmation must never be Enter-approved (#347)")

	// The pre-adapter matcher ("Yes, allow once") no longer exists in
	// gemini-cli; current panes must match on the decline option.
	_, ok = gemini.DetectPrompt("Do you want to proceed?\n  1. Yes, allow once")
	require.False(t, ok, "stale pre-0.2x option text alone must not match")

	idle := "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	_, ok = gemini.DetectPrompt(idle)
	require.False(t, ok)

	// The #347 quote, composed the same way as codex's above and standing for the same
	// measurement. For gemini this is where it stops: the CLI is deprecated in favour of
	// Antigravity (docs/superpowers/specs/2026-07-23-antigravity-integration-design.md), so
	// the false positive is accepted and made harmless rather than anchored away.
	quoted := strings.Join([]string{
		"✦ The gemini entry keys on \"No, suggest changes (esc)\",",
		"  the decline label of the tool-confirmation dialog.",
		"",
		"╭───╮",
		"│ > │",
		"╰───╯",
		"~/project   no sandbox   gemini-2.5-pro",
	}, "\n")
	m, ok = gemini.DetectPrompt(quoted)
	require.True(t, ok, "the flat window still reads a quoted literal as a live prompt")
	require.True(t, m.NoAutoTap, "…so the quote must surface as needs-input, never tap Enter")
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

// --- Antigravity (agy) fixtures. Every pane below is a verbatim `tmux capture-pane -p`
// of a live agy 1.1.11 driven in an isolated tmux on 2026-08-09 (#512), at the width named
// in each const. Where a fixture starts mid-pane, the elided part is the startup splash —
// it carries the signed-in account's email address, and nothing above the live chrome is
// read by any matcher here.
//
// The narrow variants are not decoration. agy truncates its headline questions and wraps
// its option rows, so a fixture captured only at 120 columns would assert a matcher that
// the same dialog defeats at a width Atrium can actually render (ui/terminal.go SetSize
// resizes the detached session to the preview width; #340 puts the floor at 28).

// agyTrustGatePane is the startup folder-trust screen at width 120 — the whole pane.
const agyTrustGatePane = `Accessing workspace:

/tmp/agy512cap/repo

Do you trust the contents of this project?

Antigravity CLI requires permission to read, edit, and execute files here.

> Yes, I trust this folder
  No, exit

  ↑/↓ Navigate · enter Confirm
                                                                                                   Gemini 3.1 Pro · high`

// agyTrustGateNarrowPane is the SAME screen at width 28, and it is the reason the gate
// matcher keys on the option row. The question has been truncated to "Do you trust the
// contents of" — not wrapped, so no space-join recovers the rest — while the option row
// "> Yes, I trust this folder" is unchanged.
const agyTrustGateNarrowPane = `Accessing workspace:

/tmp/agy512cap/fresh28

Do you trust the contents of

Antigravity CLI requires per

> Yes, I trust this folder
  No, exit

  ↑/↓ Navigate · enter Confi
       Gemini 3.1 Pro · high`

// agyIdlePane is the settled composer at width 120: a ">" prompt between two rules, with
// "? for shortcuts" in the footer slot where a busy pane puts "esc to cancel".
const agyIdlePane = `────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
? for shortcuts                                                                                    Gemini 3.1 Pro · high`

// agyBusyPane is a working pane at width 120. Two things it pins: the composer box is
// still on screen while agy works (so the footer marker sits below the box's bottom
// border, which is what MarkerWindow 0 anchors to), and the spinner's verb is just one of
// several — this frame says "Generating...", others in the same turn said "Running...".
const agyBusyPane = `────────────────────────────────────────────────────────────
> Run the shell command: echo HELLO512 . Then explain in about five sentences what that command does and why echo is
  useful.
⣻  Generating...
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
esc to cancel                                                                                      Gemini 3.1 Pro · high`

// agyConfirmPane is the shell-execution confirmation at width 120.
const agyConfirmPane = `● Bash(rm -f /tmp/agy512cap/repo/hello.txt && echo DELETED) (ctrl+o to expand)

Command
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

Requesting permission for:
   rm -f /tmp/agy512cap/repo/hello.txt && echo DELETED

Do you want to proceed?
> 1. Yes
  2. Yes, and always allow in this conversation for commands that start with 'rm -f /tmp/agy512cap/repo/hello.txt'
  3. Yes, and always allow for commands that start with 'rm -f /tmp/agy512cap/repo/hello.txt' (Persist to settings.json)
  4. No

  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
esc to cancel                                                                                      Gemini 3.1 Pro · high`

// agyConfirmNarrowPane is the same dialog at width 40 for a LONG command, and it is the
// fixture that rules out keying on "Do you want to proceed?". The wrapped options push
// that question to 16 non-empty lines from the bottom — past WindowPrompt's 15 — while the
// nav hint it is matched on instead stays two lines up. Also note "Requesting permission
// for:" has wrapped mid-word here at 28 and is mangled at 40 too: another literal the
// wide capture would have made look safe.
const agyConfirmNarrowPane = `● Bash(rm -f /tmp/a...) (ctrl+o to
expand)

Command
────────────────────────────────────────

Requesting permission for:
   rm -f
/tmp/agy512cap/repo/very/deeply/ne
sted/directory/structure/a-file-wi
th-a-long-name.txt
&& echo
REMOVED_THE_DEEPLY_NESTED_FILE

Do you want to proceed?
> 1. Yes
  2. Yes, and always allow in this
conversation for commands that start
with 'rm -f
/tmp/agy512cap/repo/very/deeply/nested
/directory/structure/a-file-with-a-
l...'
  3. Yes, and always allow for commands
that start with 'rm -f
/tmp/agy512cap/repo/very/deeply/nested
/directory/structure/a-file-with-a-
l...' (Persist to settings.json)
  4. No

  ↑/↓ Navigate · tab Amend · ctrl+g edit
esc to cancel      Gemini 3.1 Pro · high`

func TestAgyBusyMarker(t *testing.T) {
	require.True(t, agy.HasBusyMarker(agyBusyPane),
		"the footer marker sits below the composer box's bottom border, where MarkerWindow 0 looks")
	require.False(t, agy.HasBusyMarker(agyIdlePane),
		"the idle pane puts \"? for shortcuts\" in the same footer slot")
	require.False(t, agy.HasBusyMarker(agyTrustGatePane))

	// The marker must not be read out of the scrolled-back transcript.
	scrollback := "We key agy's busy state on \"esc to cancel\".\n" +
		strings.Repeat("a normal line of build output\n", 10) + agyIdlePane
	require.False(t, agy.HasBusyMarker(scrollback),
		"the literal quoted deep in the transcript is not the live footer")
}

// The busy marker must survive a turn's whole arc, because that is what lets agy skip the
// LiveSpinner claude needs. Sampling a live turn once a second showed the footer marker up
// from the first frame after submit through the frames where the reply was streaming
// mid-word — so the marker, not the spinner glyph, is the signal, and it must not be keyed
// on the rotating verb.
func TestAgyBusyMarkerIsVerbIndependent(t *testing.T) {
	for _, verb := range []string{"Generating...", "Running...", "Working...", "Loading..."} {
		pane := strings.Replace(agyBusyPane, "Generating...", verb, 1)
		require.True(t, agy.HasBusyMarker(pane), "verb %q must not affect the marker", verb)
	}
	// A frame with no spinner glyph at all (the first frame after submit renders the verb
	// bare) is still busy, because the footer marker is already up.
	noGlyph := strings.Replace(agyBusyPane, "⣻  Generating...", "Generating...", 1)
	require.True(t, agy.HasBusyMarker(noGlyph))
}

// MarkerWindow 0 restricts the marker to the footer BELOW the input box's bottom border,
// and this is the case that makes that choice load-bearing rather than cosmetic: agy keeps
// its composer on screen while working, so a bottom-N window (the geometry codex and gemini
// need, because their status rows render above the box) would take the user's own typed
// text for the footer. Asking agy how to cancel a turn would pin the row Working forever.
func TestAgyBusyMarkerIgnoresTheComposerAndTranscript(t *testing.T) {
	typedIntoComposer := `────────────────────────────────────────
> how do I esc to cancel a turn?
────────────────────────────────────────
? for shortcuts                          Gemini 3.1 Pro · high`
	require.False(t, agy.HasBusyMarker(typedIntoComposer),
		"the marker literal typed into the composer is not the footer")

	echoedInTranscript := `> explain the footer
  The footer shows "esc to cancel" while a turn is running.
────────────────────────────────────────
>
────────────────────────────────────────
? for shortcuts                          Gemini 3.1 Pro · high`
	require.False(t, agy.HasBusyMarker(echoedInTranscript),
		"nor is the literal quoted in the reply just above the box")

	// The control: the same literal in the footer slot IS the marker.
	require.True(t, agy.HasBusyMarker(agyBusyPane))
}

func TestAgyConfirmationPrompt(t *testing.T) {
	for name, pane := range map[string]string{
		"width 120":                      agyConfirmPane,
		"width 40, wrapped long command": agyConfirmNarrowPane,
	} {
		t.Run(name, func(t *testing.T) {
			m, ok := agy.DetectPrompt(pane)
			require.True(t, ok, "the shell-execution confirmation must surface as needs-input")
			require.Equal(t, "confirmation", m.Name)
			require.True(t, m.NoAutoTap,
				"autoyes must never answer this: option 1 is Yes and it runs a shell command")
		})
	}

	_, ok := agy.DetectPrompt(agyIdlePane)
	require.False(t, ok, "an idle composer is not a prompt")
	_, ok = agy.DetectPrompt(agyBusyPane)
	require.False(t, ok, "a working pane is not a prompt")
	_, ok = agy.DetectPrompt(agyTrustGatePane)
	require.False(t, ok,
		"the trust gate is a gate, not a confirmation — its nav hint reads \"enter Confirm\"")
}

// The narrow fixture exists to falsify the obvious matcher, so state that in an assertion
// rather than only in a comment: at width 40 with a long command the dialog's headline
// question is outside the window the matcher reads, and a matcher keyed on it misses.
func TestAgyConfirmationQuestionFallsOutOfWindowWhenNarrow(t *testing.T) {
	require.Contains(t, agyConfirmNarrowPane, "Do you want to proceed?",
		"the question IS on the pane — it is the window, not the render, that loses it")

	keyedOnQuestion := PromptMatcher{Window: WindowPrompt, All: []string{"Do you want to proceed?"}}
	require.False(t, keyedOnQuestion.matches(agyConfirmNarrowPane),
		"a matcher keyed on the headline question misses this dialog, which is why the "+
			"shipped matcher keys on the bottom-anchored nav hint instead")
	require.True(t, keyedOnQuestion.matches(agyConfirmPane),
		"and it works at 120 — the width is the whole difference, so a wide-only fixture "+
			"would have passed this design straight through")
}

func TestAgyTrustGate(t *testing.T) {
	_, ok := agy.GateUp(agyTrustGatePane)
	require.True(t, ok, "the startup folder-trust screen must read as a gate")

	_, ok = agy.GateUp(agyTrustGateNarrowPane)
	require.True(t, ok, "and still at width 28, where the question has been truncated away")

	_, ok = agy.GateUp(agyIdlePane)
	require.False(t, ok)
	_, ok = agy.GateUp(agyConfirmPane)
	require.False(t, ok, "a tool confirmation is not the startup gate")
}

// The counterpart to the confirmation's window test: the gate's headline question is not
// merely pushed out of a window at 28 columns, it is truncated out of the PANE, so no
// window size and no space-join could recover it.
func TestAgyTrustGateQuestionIsTruncatedNotWrapped(t *testing.T) {
	require.NotContains(t, agyTrustGateNarrowPane, "Do you trust the contents of this project?")
	require.Contains(t, agyTrustGateNarrowPane, "Do you trust the contents of")

	keyedOnQuestion := Gate{Contains: []string{"Do you trust the contents of this project"}}
	require.False(t, keyedOnQuestion.matches(flattenChrome(agyTrustGateNarrowPane, WindowPrompt)),
		"the obvious gate literal is gone at 28 columns, which is why the gate keys on "+
			"its option row instead")
}

// NamerKeys pins which agents claim headless auto-naming and their preference
// order — each entry must have a matching invocation branch in session/naming.go.
func TestNamerKeys(t *testing.T) {
	require.Equal(t, []Key{KeyClaude, KeyGemini, KeyAgy}, NamerKeys())
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
