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
// — the narrowest pane this dialog was captured at, NOT a floor. An earlier draft called 28
// "the narrowest reachable pane" on the arithmetic that an 80-column terminal at
// maxListRatio hands the preview pane 28 columns (#340); that is one terminal size, not a
// bound. There is no floor — see registry.go's agy block, and the agy fixture ladder below
// (agyTrustGatePane down to agyConfirmFloorPane), cited by name because the line number
// this used to carry was never right: in the very commit that wrote it, those lines were
// already inside an aider test, several above where the agy block actually began. It did
// not rot; it was born wrong, which no amount of re-checking a diff would have caught.
// #512 falsified two literals at 24 and shipped a fixture at 20.
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

// The permission-local width ladder (#666 item 1), driven 2026-08-11 against a live claude
// 2.1.228 by scripts/drive-agent.sh (#647). One API turn per shape covers the whole ladder,
// because a dialog waits for input and the pane is resized under it. The write shape was
// driven at 120 60 40 34 32 31 30 29 28 26 24 20 and the bash shape at 120 60 40 34 30 29 28
// 26 24 20; DetectPrompt returned "permission-local" at every rung of both. The five fixtures
// below are the rungs that carry something the others do not.
//
// WHAT THIS SETTLES. #648 filed this matcher as the likeliest live defect in
// keysWithNoRecordedCaptureWidth — the debt list for captures whose width nobody wrote down,
// which is a different list from paneCoverageExempt and means close to the opposite thing
// (this matcher always had two captures; what it lacked was a width). The reasoning was that
// "Tab to amend" ends at column 29 in claudeWritePermissionPane
// and that registry.go already records claude truncating a footer on overflow ("at width 30 a
// busy 2.1.210 pane reads '⏸ manual mode on · esc to …'"), so the pair would die just under
// 30 — above the 28 claude's fetch dialog is already captured at. That is FALSIFIED. This
// footer WRAPS: at 30 it is one line, at 29 it breaks inside the discriminating literal
// itself (" Esc to cancel · Tab to" / " amend"), a flatten reconstructs the pair, and the
// matcher holds to 20 — the narrowest rung driven, and below the ~24 a 70-column terminal
// gives the preview pane.
//
// The inference that failed is worth naming, because it is the one this package keeps making:
// the busy-marker note describes the status line BELOW the input box, and a different region
// of the same CLI is a different renderer. Truncation somewhere is not truncation here.
//
// WHICH FLATTEN, though, is not one answer, and reading "footerVisibleInSegments flattens the
// segment" onto the whole ladder is the same shape of mistake one level down. That function
// segments on box borders inside the bottom-WindowPrompt window and only falls back to a flat
// workChromeLines window when it finds none. The 30 and 29 rungs still show a border there and
// take the segment scan; the 20 rungs do not — the splash has scrolled clear — and take the
// flat fallback, whose window is workChromeLines tall and which claudeBashPermissionFloorPane's
// three-piece footer fills EXACTLY.
// TestPermissionLocalFooterFlattenBudget below measures both, so neither the path nor that
// zero headroom is left as something to infer from the fixtures.
//
// WHAT IT DOES NOT SETTLE. A wrap is repaired only where the scan reaches, and which scan runs
// is decided by whether a border survives that window — not something to eyeball off a pane.
// These panes carry the startup splash and its box borders verbatim for that reason: eliding a
// region is the one edit that turns a capture back into a composition, and here it would also
// move a rung silently from one scan to the other.
//
// WHAT THESE FIXTURES ACTUALLY GUARD, measured by mutating the source rather than asserted
// (`go test ./session/agent/`, one mutation at a time):
//
//   - Replace footerVisibleInSegments' segment join with a per-LINE token test and only the
//     two width-29 rungs fail. Nothing else in the tree notices.
//   - De-flatten the other half — the no-rules fallback, tokens(flattenChrome(…)) — and the
//     two width-20 rungs fail, plus TestClaudePrompts, whose composed wrapped-footer case
//     covers that path for the SELECTION matcher, plus
//     TestProvenWidthFloorsAreComputedNotClaimed, because the permission-local floor rises
//     20 → 29 and stops matching its pinned rung list. Count all four before concluding a
//     refactor is clean: the last one names a pinned number, and "fix" it and the floor this
//     ladder drove is gone. There was no captured evidence for permission-local on either
//     path before this.
//   - Drop the "Tab to amend" half of the pair and neither of those groups fails: the kill
//     comes from TestClaudePrompts and from TestClaudeLocalPermissionPrompt's own trust-gate
//     and model-picker negatives. The pair's exclusion job was already guarded; what was not
//     guarded is that the pair survives being cut in half by a line break.
//
// So the flattening is the mechanism these captures exist to hold, and widening or shortening
// either literal is NOT what they catch — see localPermissionFooterTokens in registry.go.

// claudeWritePermissionNarrowPane is the write dialog at width 30: the last rung where the
// footer is a single line, and therefore the exact width #648 predicted the matcher would die
// just below. It fires.
//
// Said plainly, because the rest of this file is strict about it: this rung adds no matcher
// coverage. Every mutation that kills it kills the 2.1.210 wide captures above too — it is a
// RENDERING datum, the wide side of a boundary the next two fixtures cross, and it is here so
// that the prediction's own width has a firing capture at it rather than an argument. If
// claude ever does switch this footer to truncate-on-overflow, 30-versus-29 is the pair that
// localises it in one diff.
var claudeWritePermissionNarrowPane = strings.Join([]string{
	"",
	"╭─ Claude Code ──────────────╮",
	"│                            │",
	"│     Welcome back Zvi!      │",
	"│                            │",
	"│           ▐▛███▜▌          │",
	"│          ▝▜█████▛▘         │",
	"│            ▘▘ ▝▝           │",
	"│                            │",
	"│ Opus 5 (1M context) with   │",
	"│ medi…                      │",
	"│         Claude Max         │",
	"│       /…/claude/repo       │",
	"│                            │",
	"╰────────────────────────────╯",
	"",
	" ⚠ 1 MCP server needs",
	"   authentication · run /mcp",
	"",
	"❯ Use the Write tool to",
	"  create hello.txt containing",
	"  exactly one line: hi",
	"",
	"● Write(hello.txt)",
	"",
	"──────────────────────────────",
	" Create file",
	" hello.txt",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"  1 hi",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	" Do you want to create",
	" hello.txt?",
	" ❯ 1. Yes",
	"   2. Yes, allow all edits",
	"      during this session",
	"      (shift+tab)",
	"   3. No",
	"",
	" Esc to cancel · Tab to amend",
}, "\n")

// claudeWritePermissionWrappedFooterPane is the write dialog at width 29 — one column
// narrower, and the rung that answers the question. The footer breaks into the two physical
// lines " Esc to cancel · Tab to" and " amend" (quoted rather than shown as an indented block,
// because gofmt normalises a block's leading space away and the leading space is pane content),
// so "Tab to amend" is not on any single physical line, and a flat strings.Contains over the
// pane cannot see it. The matcher survives only because footerVisibleInSegments joins a
// segment before applying the token predicate. That is the property this fixture pins, and
// nothing in the tree pinned it against a real pane before.
var claudeWritePermissionWrappedFooterPane = strings.Join([]string{
	"│                           │",
	"│     Welcome back Zvi!     │",
	"│                           │",
	"│          ▐▛███▜▌          │",
	"│         ▝▜█████▛▘         │",
	"│           ▘▘ ▝▝           │",
	"│                           │",
	"│ Opus 5 (1M context) with  │",
	"│ medi…                     │",
	"│        Claude Max         │",
	"│      /…/claude/repo       │",
	"│                           │",
	"╰───────────────────────────╯",
	"",
	" ⚠ 1 MCP server needs",
	"   authentication · run /mcp",
	"",
	"❯ Use the Write tool to",
	"  create hello.txt",
	"  containing exactly one",
	"  line: hi",
	"",
	"● Write(hello.txt)",
	"",
	"─────────────────────────────",
	" Create file",
	" hello.txt",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"  1 hi",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	" Do you want to create",
	" hello.txt?",
	" ❯ 1. Yes",
	"   2. Yes, allow all edits",
	"      during this session",
	"      (shift+tab)",
	"   3. No",
	"",
	" Esc to cancel · Tab to",
	" amend",
}, "\n")

// claudeWritePermissionFloorPane is the write dialog at width 20, the narrowest rung driven
// and the floor this matcher is now proven at. The footer has wrapped again, this time after
// the separator (" Esc to cancel ·" / " Tab to amend"), and the splash has scrolled far enough
// that its top border is gone — so the pane opens mid-box, which is the segmentation the scan
// actually meets on a narrow preview pane.
var claudeWritePermissionFloorPane = strings.Join([]string{
	"│                  │",
	"│  Opus 5 (1M      │",
	"│  context) with   │",
	"│  medi…           │",
	"│    Claude Max    │",
	"│  /…/claude/repo  │",
	"│                  │",
	"╰──────────────────╯",
	"",
	" ⚠ 1 MCP server",
	"   needs",
	"   authentication ·",
	"   run /mcp",
	"",
	"❯ Use the Write",
	"  tool to create",
	"  hello.txt",
	"  containing",
	"  exactly one line:",
	"  hi",
	"",
	"● Write(hello.txt)",
	"",
	"────────────────────",
	" Create file",
	" hello.txt",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	"  1 hi",
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	" Do you want to",
	" create hello.txt?",
	" ❯ 1. Yes",
	"   2. Yes, allow all",
	"      edits during",
	"      this session",
	"      (shift+tab)",
	"   3. No",
	"",
	" Esc to cancel ·",
	" Tab to amend",
}, "\n")

// claudeBashPermissionWrappedFooterPane is the shell-command dialog at width 29. Its footer
// carries a third hint, and at THIS width that buys no extra piece: it breaks in the same
// place the write shape does — " Esc to cancel · Tab to" then " amend · ctrl+e to explain",
// two physical lines, exactly as many as claudeWritePermissionWrappedFooterPane — with the
// third hint TRAILING the split rather than the footer ending at it. The extra piece arrives
// further down, at the 20 rung, which is claudeBashPermissionFloorPane's job and not this
// one's. "Tab to amend" is again cut across the break, so this is the same property measured
// with a different amount of footer either side of it rather than a copy.
var claudeBashPermissionWrappedFooterPane = strings.Join([]string{
	"╰───────────────────────────╯",
	"",
	" ⚠ 1 MCP server needs",
	"   authentication · run /mcp",
	"",
	"❯ Use the Write tool to",
	"  create hello.txt",
	"  containing exactly one",
	"  line: hi",
	"",
	"● Write(hello.txt)",
	"  ⎿  User rejected    hello.t",
	"     write to         xt",
	"      1 hi",
	"",
	"✻ Crunched for 2m 39s",
	"",
	"❯ Run the shell command:",
	"  mkdir probedir",
	"",
	"  Creating probedir directory",
	"",
	"  ⎿  $ mkdir probedir",
	"",
	"─────────────────────────────",
	" Bash command",
	"",
	"   mkdir probedir",
	"   Create probedir",
	"   directory",
	"",
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and always allow",
	"      access to repo/ from",
	"      this project",
	"   3. No",
	"",
	" Esc to cancel · Tab to",
	" amend · ctrl+e to explain",
}, "\n")

// claudeBashPermissionFloorPane is the shell-command dialog at width 20, and it carries
// something no hand-written pane would have thought to include: claude's own repaint residue.
// Read the decline option — it is "   3. Nois project".
//
// When narrowing costs option 2 a wrapped line, claude draws the next row without erasing to
// end of line, so the tail of "this project" survives under "   3. No". It is deterministic,
// not a torn capture. Three things say so: it survives SETTLE raised to 4s; the write shape
// does the same thing at 32 and 24, there as "   3. Nohift+tab)"; and the width-24 case
// reproduces identically whether the pane was 120, 26 or 20 immediately before, so it is not a
// residue of any particular previous width.
//
// Atrium reaches this by dragging the split, since the preview pane resizes under a live
// dialog (session/instance.go SetPreviewSize). Pinned verbatim because it is what the poller
// actually sees, and because it makes the matcher's indifference to the option rows a measured
// fact rather than a design intention — the footer is untouched here, and the footer is all
// this matcher reads.
var claudeBashPermissionFloorPane = strings.Join([]string{
	"",
	"",
	"● Write(hello.txt)",
	"  ⎿  User     hello.",
	"     rejected txt",
	"     write to",
	"      1 hi",
	"",
	"✻ Crunched for 2m",
	"  39s",
	"",
	"❯ Run the shell",
	"  command: mkdir",
	"  probedir",
	"",
	"  Creating probedir",
	"  directory",
	"  ⎿  $ mkdir",
	"     probedir",
	"",
	"────────────────────",
	" Bash command",
	"",
	"   mkdir probedir",
	"   Create",
	"   probedir",
	"   directory",
	"",
	" Do you want to",
	" proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and",
	"      always allow",
	"      access to",
	"      repo/ from",
	"   3. Nois project",
	"",
	" Esc to cancel ·",
	" Tab to amend ·",
	" ctrl+e to explain",
}, "\n")

// borderInScannedWindow reports which of footerVisibleInSegments' two paths a pane takes. That
// function windows to the bottom WindowPrompt non-empty lines and segments on the box borders
// it finds THERE; with none it falls back to a flat workChromeLines window instead.
// liveChromeLines is the production helper for that same window — it keeps the non-empty lines
// where the matcher keeps the blanks between them too, which no border can be — so this asks
// the question off the same lines rather than off a count someone made by eye.
func borderInScannedWindow(pane string) bool {
	for _, line := range strings.Split(liveChromeLines(pane, WindowPrompt), "\n") {
		if isBoxBorderLine(line) {
			return true
		}
	}
	return false
}

// footerFlattenDepth is how many trailing non-empty lines have to be joined before the footer
// pair is readable — the budget a rung spends out of whatever window flattens it. 0 means the
// pair never reconstructs within the widest window this matcher ever sees.
func footerFlattenDepth(pane string) int {
	for n := 1; n <= WindowPrompt; n++ {
		if localPermissionFooterTokens(flattenChrome(pane, n)) {
			return n
		}
	}
	return 0
}

// TestPermissionLocalFooterFlattenBudget turns "the flatten carries it" into the two numbers
// that decide whether it still will: which scan each rung reaches, and how much of that scan's
// window its footer eats.
//
// The distinction matters because the two paths have very different room in them. A segment is
// as tall as the dialog, so the 29 rungs have slack to spare. The no-rules fallback is a flat
// window workChromeLines tall, and claudeBashPermissionFloorPane's footer is three pieces, so
// it fits with NOTHING left over. That is the narrowest thing about this matcher,
// and before this test it was visible only to someone who instrumented the code: the ladder
// records a floor of 20 and reads as structural evidence for it.
//
// What fails here, and should: a fourth hint in claude's footer (the shell shape is already at
// three), a rung driven below 20, or anyone trimming workChromeLines. The consequence of not
// failing here is a blocked session reading Ready while session/tmux AwaitingInput — which
// takes the dialog's own "❯ 1. Yes" for a composer — types the queued prompt into it.
func TestPermissionLocalFooterFlattenBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pane       string
		wantBorder bool
		wantDepth  int
	}{
		{"claudeWritePermissionNarrowPane", claudeWritePermissionNarrowPane, true, 1},
		{"claudeWritePermissionWrappedFooterPane", claudeWritePermissionWrappedFooterPane, true, 2},
		{"claudeBashPermissionWrappedFooterPane", claudeBashPermissionWrappedFooterPane, true, 2},
		{"claudeWritePermissionFloorPane", claudeWritePermissionFloorPane, false, 2},
		{"claudeBashPermissionFloorPane", claudeBashPermissionFloorPane, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantBorder, borderInScannedWindow(tc.pane),
				"this rung changed which of footerVisibleInSegments' paths it exercises, so "+
					"the ladder no longer covers the pair of them it is documented as covering")
			require.Equal(t, tc.wantDepth, footerFlattenDepth(tc.pane),
				"this rung's footer now needs a different number of lines joined before "+
					"\"Esc to cancel\" and \"Tab to amend\" co-occur")
			if !tc.wantBorder {
				require.LessOrEqualf(t, tc.wantDepth, workChromeLines,
					"%s takes the no-rules fallback, whose window is workChromeLines=%d "+
						"lines; a footer needing %d cannot be reconstructed there and the "+
						"matcher misses a live dialog",
					tc.name, workChromeLines, tc.wantDepth)
			}
		})
	}

	require.Equal(t, workChromeLines, footerFlattenDepth(claudeBashPermissionFloorPane),
		"the shell shape's floor rung fills the no-rules fallback window exactly — this is the "+
			"zero-headroom fact, pinned so that widening the footer or narrowing the window is "+
			"loud here rather than silent on a live pane")
	require.Less(t, footerFlattenDepth(claudeWritePermissionFloorPane), workChromeLines,
		"the write shape's floor rung is the one with a line to spare; if both shapes ever sit "+
			"flush against the budget there is no rung left showing what slack looks like")
}

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

// claudeMCPSinglePane is the one-server MCP approval, transcribed from live claude 2.1.210
// launched in a fresh dir holding a project-scoped .mcp.json (2026-07-15). Transcribed, not
// captured verbatim: #666 drove this shape again and showed the box rule here — a spliced
// strings.Repeat("─", 56) — is not what a real capture of this dialog draws, which is why the
// pane records no width (see paneCoverage in pane_width_test.go, and claudeMCPSingleWidePane
// below for the verbatim one). The CONTENT is a real pane's; it replaces a composed fixture
// that ended "[Enter] to approve", a line this dialog does not render (the string does exist
// elsewhere in the bundle, which is exactly why its presence there proved nothing). The title
// was always right, so the gate always fired; #332's permission bug was the same setup with
// the opposite outcome, so the shape is pinned from a real pane now rather than from a
// plausible guess.
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
// project-scoped .mcp.json), transcribed rather than captured verbatim — its spliced box rule
// is why, and why it records no width; see claudeMCPMultiWidePane below. A distinct shape no
// fixture covered before: a checkbox multi-select whose title is PLURAL ("3 new MCP servers found in this project" — the
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

// The MCP-approval width ladder, driven 2026-08-11 against a live claude 2.1.228 by
// scripts/drive-agent.sh (#647) at 110 40 28 24 20, both shapes, with NO api turn: the gate is
// a startup dialog, so a fresh workspace holding a project-scoped .mcp.json is the whole
// setup. The three servers are named nanoclaw/picoclaw/femtoclaw so the pane reproduces the
// 2.1.210 captures above rather than merely resembling them. claudeGateVisible fired at every
// rung of both shapes.
//
// This settles the width claim #648 flagged as disputed. claudeMCPMultiPane's comment carries
// a per-width result table headed 110, while the pane's widest line is 105 and the box rule
// spliced into it is strings.Repeat("─", 56). A real 110-column capture draws a 110-cell rule,
// as the two Wide panes below show — so the older fixtures are edited transcriptions, not
// verbatim 110-column captures, and their width stays unrecorded rather than inferred. #340's
// table is still provenance for what was measured; it is not provenance for a fixture's width.
//
// The rules here are written out rather than spliced with strings.Repeat for exactly that
// reason: splicing is what made the width unverifiable the first time.

// claudeMCPMultiWidePane is the multi-server approval driven at width 110 — the first capture
// of this shape to carry a 110 that is a datum rather than a claim. Not the first to carry a
// width at all: claudeMCPWrappedPane has recorded 40 since #665, and the single-server shape
// has claudeMCPNarrowPane at 28. What nothing recorded was the 110 claudeMCPMultiPane's
// comment asserts, which is the number this rung replaces. Its rule is 110 cells.
var claudeMCPMultiWidePane = strings.Join([]string{
	"",
	"──────────────────────────────────────────────────────────────────────────────────────────────────────────────",
	"  3 new MCP servers found in this project",
	"  Select any you wish to enable.",
	"",
	"  MCP servers may execute code or access system resources. All tool calls require approval. Learn more in",
	"  the MCP documentation.",
	"",
	"  ❯ [✔] nanoclaw",
	"    [✔] picoclaw",
	"    [✔] femtoclaw",
	" Space to select · Enter to confirm · Esc to reject all",
}, "\n")

// claudeMCPMultiFloorPane is the multi-server approval at width 20, and with its single-server
// sibling it takes claude/gate from proven-at-28 to proven-at-20 — under the ~24 a 70-column
// terminal leaves the preview pane. The title wraps across three lines here and the footer
// across three more; the gate reads the whole dialog region, so neither costs it anything.
var claudeMCPMultiFloorPane = strings.Join([]string{
	"",
	"────────────────────",
	"  3 new MCP",
	"  servers found in",
	"  this project",
	"  Select any you",
	"  wish to enable.",
	"",
	"  MCP servers may",
	"  execute code or",
	"  access system",
	"  resources. All",
	"  tool calls",
	"  require",
	"  approval. Learn",
	"  more in the MCP",
	"  documentation.",
	"",
	"  ❯ [✔] nanoclaw",
	"    [✔] picoclaw",
	"    [✔] femtoclaw",
	" Space to select ·",
	" Enter to confirm ·",
	" Esc to reject all",
}, "\n")

// claudeMCPSingleWidePane is the one-server approval at width 110. claudeMCPSinglePane, the
// fixture it stands beside, records no width at all; this one does, and its rule is 110 cells.
var claudeMCPSingleWidePane = strings.Join([]string{
	"",
	"──────────────────────────────────────────────────────────────────────────────────────────────────────────────",
	"  New MCP server found in this project: nanoclaw",
	"",
	"  MCP servers may execute code or access system resources. All tool calls require approval. Learn more in",
	"  the MCP documentation.",
	"",
	"  ❯ 1. Use this MCP server",
	"    2. Use this and all future MCP servers in this project",
	"    3. Continue without using this MCP server",
	"",
	"  Enter to confirm · Esc to cancel",
}, "\n")

// claudeMCPSingleFloorPane is the one-server approval at width 20.
//
// The ladder also caught a real reword between 2.1.210 and 2.1.228, and it is a reword rather
// than a reflow because the comparison is at ONE width: claudeMCPNarrowPane is this shape at
// 28, where 2.1.210 renders "    2.Use this and all" with the space after the number eaten and
// the continuation at column 6; the 28 rung driven here renders "    2. Use this and all" and
// hang-indents the continuation to column 7, under the label. A matcher keying on an option
// row would now have to carry both spellings. This gate keys on the title, which is the whole
// reason the change costs it nothing — and the reason the 28 rung is not pinned as a fixture,
// since claudeMCPNarrowPane already holds that width and the difference is in rows the gate
// does not read.
var claudeMCPSingleFloorPane = strings.Join([]string{
	"",
	"────────────────────",
	"  New MCP server",
	"  found in this",
	"  project:",
	"  nanoclaw",
	"",
	"  MCP servers may",
	"  execute code or",
	"  access system",
	"  resources. All",
	"  tool calls",
	"  require",
	"  approval. Learn",
	"  more in the MCP",
	"  documentation.",
	"",
	"  ❯ 1. Use this MCP",
	"       server",
	"    2. Use this and",
	"       all future",
	"       MCP servers",
	"       in this",
	"       project",
	"    3. Continue",
	"       without using",
	"       this MCP",
	"       server",
	"",
	"  Enter to confirm",
	"  · Esc to cancel",
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

	// Both MCP-approval shapes, transcribed from live claude 2.1.210 by putting a
	// project-scoped .mcp.json in a fresh dir (2026-07-15, #340) — transcribed rather than
	// verbatim, which is #666's finding and the reason neither records a width; the verbatim
	// pair are claudeMCPSingleWidePane / claudeMCPMultiWidePane. The gate fires on the
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

	// The cap must not bite a real dialog. Measured over every claude/gate capture rather than
	// pinned to whichever was tallest when this was written: #666 added a rung 10 lines taller
	// than the fixture the old assertion named, and a hardcoded number is exactly what cannot
	// notice that. Walking paneCoverage folds in the next ladder the day it lands.
	//
	// The height that binds is the region BELOW the anchoring rule, since that is what
	// claudeGateVisible hands to flattenChrome. It grows with the dialog — one line per MCP
	// server, and several per prose paragraph once the pane is narrow enough to reflow — so the
	// narrow rungs, not the wide ones, are where this gets close.
	tallest, tallestName := 0, ""
	for _, c := range paneCoverage["claude/gate/startup"] {
		region, hasRule := footerBelowBox(c.pane)
		if !hasRule {
			continue
		}
		lines := 0
		for _, line := range strings.Split(region, "\n") {
			if strings.TrimSpace(line) != "" {
				lines++
			}
		}
		if lines > tallest {
			tallest, tallestName = lines, c.name
		}
	}
	require.NotZero(t, tallest, "no claude/gate capture puts a region below a rule, so this "+
		"measures nothing — the anchor these captures exist to exercise is not being reached")
	require.Greaterf(t, gateRegionCap, tallest,
		"gateRegionCap is %d and the tallest captured gate region is %s at %d non-empty "+
			"lines. flattenChrome keeps the LAST gateRegionCap of them, and the title sits at "+
			"the TOP of the region, so a dialog past the cap loses the very literal the gate "+
			"reads: the row goes Ready while the session is blocked and PaneGate stops holding "+
			"its queued prompt (#340, #512). Raise the cap on evidence, or explain why this "+
			"capture is not one",
		gateRegionCap, tallestName, tallest)
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

// TestClaudeResumeLeavesAPinnedConversationAlone covers both directions of the
// claude adapter's resume rewrite. The ordinary session must still gain
// --continue on resurrection — without that arm, making the rewrite a no-op
// would pass — and a fork-from-checkpoint session, whose program pins the
// conversation it was seeded with, must come back untouched. `claude --resume X
// --continue` asks for two different conversations at once.
func TestClaudeResumeLeavesAPinnedConversationAlone(t *testing.T) {
	const forked = "claude --resume 5b1e9f6a-1111-4111-8111-111111111111"
	for _, tc := range []struct{ name, program, want string }{
		{"ordinary session", "claude", "claude --continue"},
		{"with other flags", "claude --model opus", "claude --model opus --continue"},
		{"forked session", forked, forked},
		{"forked, combined form", "claude --resume=abc", "claude --resume=abc"},
		{"forked, short form", "claude -r abc", "claude -r abc"},
		// Whole-field, so a lookalike is not read as a pin — the same rule hasFlag
		// applies to --model.
		{"lookalike flag", "claude --resume-session-at abc", "claude --resume-session-at abc --continue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claude.Resume(tc.program))
		})
	}
}

func TestCodexGateAndResume(t *testing.T) {
	_, ok := codex.GateUp("Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit")
	require.True(t, ok)

	require.Equal(t, "codex resume --last", codex.Resume("codex"))
	// A program carrying flags relaunches blank: the subcommand cannot be
	// safely spliced into an arbitrary argv.
	require.Equal(t, "codex --model o3", codex.Resume("codex --model o3"))
}

// geminiIdlePane is gemini's idle composer: a ">" prompt inside a "│"-bordered box.
// Composed from the installed 0.27 package source (InputPrompt.js), NOT captured from a
// live pane. Treated as evidence of the box SHAPE, which several tests already rely on;
// it is deliberately not the basis for any new matcher.
//
// The reason it stays composed is narrower than this comment used to claim. "gemini cannot
// be driven here (#347/#645)" was too broad, and #713 falsified it: the folder-trust gate
// renders on the STARTUP screen and costs no API turn, so it has now been driven live at
// 0.55.1 across a width ladder (gemini_pane_test.go) — which is how the gate literal's rot
// was found. What is still out of reach is everything past that dialog: the idle composer,
// the loading row and the tool-confirmation dialog all need an authenticated session and a
// real turn, which is what #347/#645 were about.
const geminiIdlePane = "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"

// aiderIdlePane is aider's idle pane, captured from the same live 0.86.2 session as the
// confirm fixtures below: a startup banner and a bare ">" composer with no box drawn.
var aiderIdlePane = strings.Join([]string{
	"Aider v0.86.2",
	"Main model: gpt-4o with diff edit format",
	"Git repo: .git with 3 files",
	"Repo-map: using 4096 tokens, auto refresh",
	">",
}, "\n")

// --- Gemini fixtures, all composed rather than captured. The busy and confirmation strings
// come from the 0.27 package source (LoadingIndicator.js, ToolConfirmationMessage.js) and
// are still present in the 0.55.1 bundle, but no pane has rendered either here. The trust
// gate is the exception and no longer belongs to that tier: it is pinned to verbatim 0.55.1
// captures in gemini_pane_test.go (#713).
//
// What changed at the vendor was the gate's HEADLINE, not its component. An earlier draft
// here said "FolderTrustDialog.js is gone from the 0.55.1 bundle along with the literal it
// carried", which is false twice over: the component ships in bundle/interactiveCli-*.js
// carrying its own source marker (packages/cli/src/ui/components/FolderTrustDialog.tsx), and
// only the standalone .js file is gone, because 0.55.1 ships bundled at all. A reader told
// the component was removed stops looking for it.

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

	idle := geminiIdlePane
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

	idle := geminiIdlePane
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
	// The shape, minimally: the gate keys on the accept ROW inside a live box, so this
	// composed pane carries both and not the headline. Verbatim 0.55.1 captures across a
	// width ladder — including the width where the headline is unreachable and the width
	// where the rows themselves truncate — are in gemini_pane_test.go; this stays here as
	// the adapter's own smoke test.
	_, ok := gemini.GateUp(strings.Join([]string{
		" ╭──────────────────────────────────────╮",
		" │ Do you trust the files in this folder│",
		" │ ● 1. Trust folder (repo)             │",
		" │   2. Trust parent folder (tmp)       │",
		" │   3. Don't trust                     │",
		" ╰──────────────────────────────────────╯",
	}, "\n"))
	require.True(t, ok)

	// The headline alone is not the anchor, and must not be mistaken for one: it is what
	// 0.55.1 renders and what a bundle grep hands you, and it dies on a narrow pane (#713).
	_, ok = gemini.GateUp("Do you trust the files in this folder?")
	require.False(t, ok, "the headline is not what the gate keys on — see gemini_pane_test.go")

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
	idle := aiderIdlePane
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
// the same dialog defeats at a width Atrium can actually render. There is no floor: the
// agent session is resized to the PREVIEW pane (app/app_layout.go GetPreviewSize →
// ui/list.go SetSessionPreviewSize → session/instance.go SetPreviewSize — NOT
// ui/terminal.go, which sizes the per-instance SHELL sessions), the list may take
// maxListRatio = 0.60 of the terminal (config/state.go), and the remainder is unclamped.
// Both literals were falsified at 24 columns, below the 28 an earlier draft called a floor.

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
//
// The three horizontal rules are deliberately not the same length, and that is verbatim:
// agy draws the separator above an echoed user message at 60 columns regardless of the
// pane, while the composer box's own rules span the full 120. Do not "fix" the short one
// into a 120-column rule — it would stop being a capture.
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
// nav hint it is matched on instead stays two lines up.
//
// "Requesting permission for:" is intact at this width; it is agyConfirmNarrowestPane
// below, at 28, that breaks it apart.
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

// agyConfirmNarrowestPane is the confirmation at width 28. It pins
// the two things the wider captures cannot: the matched nav hint survives here (as the
// prefix the matcher keys on, with the trailing "· ctrl+g …" cut off), and "Requesting
// permission for:" is torn into "Requesting permiss"/"ion"/"for:". That tear looks
// impossible from the string's own length — it is 26 columns in a 28-column pane — because
// agy wraps inside its own content box rather than at the terminal edge. Reasoning from
// the pane width alone would have called this literal safe.
const agyConfirmNarrowestPane = `● Bash(rm -f vict...)
(ctrl+o to expand)

Command
────────────────────────────

Requesting permiss
ion
for:
   rm -f victim.txt &&
echo GONE

Do you want to proceed?
> 1. Yes
  2. Yes, and always allow
in this conversation for
commands that start with
'rm -f victim.txt'
  3. Yes, and always allow
for commands that start
with 'rm -f victim.txt'
(Persist to settings.json)
  4. No

  ↑/↓ Navigate · tab Amend ·
esc to cancel`

// agyAnsweredConfirmPane is the pane right after the confirmation above was answered with
// Enter. agy REPLACES the dialog rather than leaving it in the scrollback, so neither the
// nav hint nor the question survives into the transcript. That is what lets the matcher be
// a bare flat-window substring with no liveness anchor of the kind aider's confirm needs
// (its trailing "]:" suffix): there is nothing left on screen to re-match. Captured at 40.
const agyAnsweredConfirmPane = `▸ Thought for 3s, 282 tokens
  Prioritizing Tool Usage
  The command has been successfully
  executed, and it output
  REMOVED_THE_DEEPLY_NESTED_FILE. Is
  there anything else you'd like me
  to run?

────────────────────────────────────────
>
────────────────────────────────────────
? for shortcuts    Gemini 3.1 Pro · high`

// agySlashMenuPane is the slash-command menu, open over a LIVE composer at width 120. It
// is the negative control that fixes how narrow the confirmation matcher has to be: this
// pane renders "↑/↓ Navigate · enter Select · tab Complete", so the generic "↑/↓ Navigate"
// prefix — the obvious way to cover every agy dialog at once — would make every user who
// types "/" read as blocked, and #512's own mechanism would then withhold their queued
// prompt. "tab Amend" is the half that belongs to the confirmation alone.
const agySlashMenuPane = `────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
> /
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
> /add-dir             Add a directory to the workspace
  /agents              List available custom agents
  /artifact            View and review artifacts
  /btw                 Ask a side question without interrupting the current task
  /changelog           Show release notes and changes
   ↓ 35 more

  ↑/↓ Navigate · enter Select · tab Complete
esc to cancel                                                                                      Gemini 3.1 Pro · high`

// agyTrustGateSubOptionPane is the trust gate at width 24, where the OPTION row itself
// truncates: "> Yes, I trust this fold". It is the fixture behind the gate literal being a
// prefix rather than the whole row, and the failure it prevents is the sharpest one in
// this adapter — the truncated row still opens with ">", so isInputBoxLine reports a
// composer and AwaitingInput goes true while GateUp is false. That is #512 exactly,
// reintroduced by a pane four columns narrower than the first capture went.
//
// 24 columns is reachable, not hypothetical: the agent session is resized to the preview
// pane (app/app_layout.go → list.SetSessionPreviewSize → session/instance.go
// SetPreviewSize), the list may take up to maxListRatio = 0.60 of the width
// (config/state.go), and nothing clamps the remainder to a minimum.
const agyTrustGateSubOptionPane = `Accessing workspace:

/tmp/agy512cap/fx24

Do you trust the content

Antigravity CLI requires

> Yes, I trust this fold
  No, exit

  ↑/↓ Navigate · enter C
   Gemini 3.1 Pro · high`

// agyConfirmSubHintPane is the confirmation at width 24, the prompt matcher's counterpart
// to agyTrustGateSubOptionPane: the nav hint itself truncates to "↑/↓ Navigate · tab Ame",
// so the full "tab Amend" no longer matches and only the shorter prefix does. Without this
// fixture the literal's length is an untested claim — reverting it to "tab Amend" passes
// every other test in this file, because the next-narrowest capture is 28 columns where
// the full hint still fits.
const agyConfirmSubHintPane = `● Bash(rm -f vict...)
(ctrl+o to expand)

Command
────────────────────────

Requesting
permission for:
   rm -f
victim.txt && echo
GONE

Do you want to proceed?
> 1. Yes
  2. Yes, and always
allow in this
conversation for
commands that start
with 'rm -f
victim.txt'
  3. Yes, and always
allow for commands
that start with 'rm -f
victim.txt' (Persist
to settings.json)
  4. No

  ↑/↓ Navigate · tab Ame
esc to cancel`

// agyConfirmFloorPane is the confirmation at width 20, where the shipped prompt literal
// fits with ZERO margin: the hint renders exactly "  ↑/↓ Navigate · tab". This is the
// adapter's real floor, and it cannot be pushed lower by shortening the literal — agy
// truncates from the right, so what binds is where "tab" ENDS in the line (18 cells of
// content plus the 2-space indent), not how long the matched substring is. "Navigate · tab"
// would need the same 20 columns. Going lower would mean abandoning the nav hint for an
// anchor earlier in the line, and the only one there is the generic "↑/↓ Navigate ·" that
// the slash-command menu also renders (see TestAgySlashMenuIsNotAPrompt).
//
// Note the question is gone at this width too — "Do you want to proceed?" has become
// "Do you want to proce", which is a third independent reason it was never a candidate.
const agyConfirmFloorPane = `● Bash(rm -f
vict...) (ctrl+o to
expand)

Command
────────────────────

Requesting
permission
for:
   rm -f
victim.txt &&
echo GONE

Do you want to proce
> 1. Yes
  2. Yes, and always
allow in this
conversation for
commands that
start with 'rm -f
victim.txt'
  3. Yes, and always
allow for commands
that start with
'rm -f victim.txt'
(Persist to
settings.json)
  4. No

  ↑/↓ Navigate · tab
esc to cancel`

// agyAcceptedGatePane is the pane immediately after "Yes, I trust this folder" is
// selected — the gate's counterpart to agyAnsweredConfirmPane. agy replaces the trust
// screen with its splash and composer rather than leaving it in the scrollback, so the
// gate cannot keep matching after acceptance and withhold the queued FIRST prompt (the
// moment a queued prompt is most likely to exist). The account line of the splash is
// redacted; nothing here reads it.
const agyAcceptedGatePane = `

      ▄▀▀▄        Antigravity CLI 1.1.11
     ▀▀▀▀▀▀       user@example.com (Google AI Pro)
    ▀▀▀▀▀▀▀▀      Gemini 3.1 Pro (High)
   ▄▀▀    ▀▀▄     /tmp/agy512cap/repo
  ▄▀▀      ▀▀▄

────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
>
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
? for shortcuts                                                                                    Gemini 3.1 Pro · high`

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

	// And the counterfactual that makes MarkerWindow 0 a choice rather than a default —
	// the same adapter with a bottom-N window DOES take the typed text for the footer.
	bottomN := &Adapter{BusyMarkers: agy.BusyMarkers, MarkerWindow: 8}
	require.True(t, bottomN.HasBusyMarker(typedIntoComposer),
		"a bottom-N window reads the composer, which is what the footer anchor avoids")
	require.True(t, bottomN.HasBusyMarker(echoedInTranscript))
}

func TestAgyConfirmationPrompt(t *testing.T) {
	for name, pane := range map[string]string{
		"width 120":                       agyConfirmPane,
		"width 40, wrapped long command":  agyConfirmNarrowPane,
		"width 28":                        agyConfirmNarrowestPane,
		"width 24, hint itself truncated": agyConfirmSubHintPane,
		"width 20, the floor":             agyConfirmFloorPane,
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
	_, ok = agy.DetectPrompt(agyAnsweredConfirmPane)
	require.False(t, ok,
		"an ANSWERED confirmation must not re-read as live: agy replaces the dialog rather "+
			"than leaving it in the transcript, so no liveness anchor is needed to tell them apart")
}

// The matcher must be narrow enough to exclude agy's other ↑/↓ selection UI. This is the
// guard that forbids the tempting generalisation — keying on "↑/↓ Navigate" so that any
// future agy dialog is covered — because the slash-command menu renders that prefix over a
// LIVE composer. A prompt match there would park the row on needs-input and withhold the
// queued prompt from a session that is merely showing an autocomplete.
func TestAgySlashMenuIsNotAPrompt(t *testing.T) {
	require.Contains(t, agySlashMenuPane, "↑/↓ Navigate",
		"the menu really does render the generic nav prefix — that is the whole hazard")

	_, ok := agy.DetectPrompt(agySlashMenuPane)
	require.False(t, ok, "the slash-command menu is not a blocking prompt")

	generic := PromptMatcher{Window: WindowPrompt, All: []string{"↑/↓ Navigate"}}
	require.True(t, generic.matches(agySlashMenuPane),
		"and a matcher keyed on the generic prefix WOULD fire on it, which is why the "+
			"shipped matcher keeps the confirmation-only \"tab\" half")
}

// The prompt literal is a prefix of the nav hint for the same reason the gate literal is a
// prefix of its option row: below 26 columns the hint truncates. Asserted separately from
// the width table above so the LENGTH of the literal — not merely the fact that it matches
// somewhere — is what fails when someone "restores" the fuller wording.
func TestAgyConfirmationHintTruncatesBelowTheFullWording(t *testing.T) {
	require.NotContains(t, agyConfirmSubHintPane, "tab Amend",
		"at 24 columns the hint is cut mid-word to \"tab Ame\"")

	fullHint := PromptMatcher{Window: WindowPrompt, All: []string{"↑/↓ Navigate · tab Amend"}}
	require.False(t, fullHint.matches(agyConfirmSubHintPane),
		"so the fuller literal misses this dialog entirely")
	require.True(t, fullHint.matches(agyConfirmNarrowestPane),
		"while still matching at 28 — four columns is the whole margin, which is why the "+
			"shipped literal stops at \"tab\"")
}

// 20 columns is the adapter's floor, and the reason it cannot be lowered is worth pinning
// because the obvious remedy does not work. agy truncates from the RIGHT, so what binds is
// where "tab" ends in the hint line, not how long the matched substring is — trimming the
// literal's leading "↑/↓ " buys exactly nothing.
func TestAgyConfirmationFloorCannotBeLoweredByShorteningTheLiteral(t *testing.T) {
	hint := ""
	for _, line := range strings.Split(agyConfirmFloorPane, "\n") {
		if strings.Contains(line, "Navigate") {
			hint = line
		}
	}
	require.Equal(t, "  ↑/↓ Navigate · tab", hint,
		"at 20 columns the hint ends exactly at the shipped literal — zero margin")

	// Both the shipped literal and a shorter one that drops the arrow prefix match here...
	for _, lit := range []string{"↑/↓ Navigate · tab", "Navigate · tab"} {
		m := PromptMatcher{Window: WindowPrompt, All: []string{lit}}
		require.True(t, m.matches(agyConfirmFloorPane), "%q matches at the floor", lit)
	}
	// ...and both end at the same column, so neither survives a narrower pane. Truncating
	// the fixture's hint by one cell is what a 19-column pane would render.
	narrower := strings.Replace(agyConfirmFloorPane, "  ↑/↓ Navigate · tab", "  ↑/↓ Navigate · ta", 1)
	for _, lit := range []string{"↑/↓ Navigate · tab", "Navigate · tab"} {
		m := PromptMatcher{Window: WindowPrompt, All: []string{lit}}
		require.False(t, m.matches(narrower),
			"%q is equally dead one column below the floor — shortening the literal is not "+
				"the lever, the hint's own layout is", lit)
	}
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

	_, ok = agy.GateUp(agyAcceptedGatePane)
	require.False(t, ok,
		"an ACCEPTED gate must not keep matching: it would hold the queued first prompt "+
			"forever, and PaneGate outranks everything else in Poll")
}

// The gate literal has to survive a pane narrower than the option row it comes from.
// Below 26 columns agy truncates that row, and a gate miss here is not a missed
// notification: the truncated row still starts with ">", so isInputBoxLine sees a
// composer, AwaitingInput goes true, and the queued first prompt is typed into the trust
// dialog. This is the width at which #512 would come back.
func TestAgyTrustGateNarrowerThanTheOptionRow(t *testing.T) {
	require.NotContains(t, agyTrustGateSubOptionPane, "Yes, I trust this folder",
		"at 24 columns the option row itself is cut short")

	_, ok := agy.GateUp(agyTrustGateSubOptionPane)
	require.True(t, ok, "the gate must still be detected on the truncated row")

	fullRow := Gate{Contains: []string{"Yes, I trust this folder"}}
	require.False(t, fullRow.matches(flattenChrome(agyTrustGateSubOptionPane, WindowPrompt)),
		"whereas the untruncated option row does NOT match here — that is why the shipped "+
			"literal is a prefix of it")
	require.True(t, fullRow.matches(flattenChrome(agyTrustGatePane, WindowPrompt)),
		"and it matches fine at 120, so a wide-only fixture would have passed this through")

	// The pointer half of the hazard: the truncated row still reads as an input box, so
	// nothing but GateUp stands between a queued prompt and this dialog.
	require.True(t, isInputBoxLine("> Yes, I trust this fold", defaultPrompts),
		"the truncated option row still looks like a composer to the box check")
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
