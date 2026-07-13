package agent

import (
	"path/filepath"
	"strings"
)

// The adapter table. Each entry records one agent CLI's heuristics with the
// provenance of every string, so a future "agent X shows as always idle" report
// can be fixed by re-checking the cited source and editing the one stale entry.
//
// Heuristic strings are version-sensitive by nature. When editing, add a fixture
// to registry_test.go pinning the new string against a captured pane, and bump
// the adapter's VerifiedVersion to the version you captured against (the drift
// guard in internal/doctor warns when an installed CLI moves past it).
//
// Remediation is ADDITIVE, never replace-in-place: when a CLI rewords a gating
// string, ADD the new variant alongside the old in the same matcher list and
// keep both through a deprecation window, e.g.
//   // claude >=2.1.180; "No, keep planning" kept for <2.1.180, remove after.
// A union match can't guess wrong (a pane shows only one variant), so matching
// never depends on the detected version. A plain re-verification (strings still
// valid at a newer release) is just a VerifiedVersion bump, no string edit.

// Claude Code. The reference adapter: every heuristic here predates this package
// and is pinned by the poll tests in session/tmux.
var claude = &Adapter{
	Key:         KeyClaude,
	DisplayName: "Claude Code",
	aliases:     []string{"claude"},

	// Heuristic strings confirmed against live claude 2.1.207 sessions (2026-07-12):
	// the busy marker "esc to interrupt" and the selection prompt matcher were seen
	// firing correctly across many real sessions in production (the #290 status
	// trace), and no structural reword has surfaced since the detailed 2.1.185
	// fixture capture (2026-06-22) — whose per-string provenance is carried forward:
	// the folder-trust dialog reworded since 2.1.170 and matched in both forms (see
	// the Gates comment + registry_test.go claudeTrustPane); the login-error "Please
	// run /login ·" separator (bundle render "Please run /login \xB7 …"); and the
	// permission / plan / model-error / MCP literals. Minor granularity (matching
	// gemini): claude ships patch releases every few days, so patch-level drift would
	// fire the warning almost constantly — alert fatigue, not signal. A patch reword
	// is already handled additively (both old and new variants kept in the same
	// matcher's union, so matching never depends on the version), and a missed reword
	// fails gracefully to "idle", never a wrong action. So only a minor/major bump —
	// where structural UI changes are likelier — counts as drift worth re-verifying.
	VerifiedVersion:  "2.1.207",
	DriftGranularity: GranularityMinor,

	// The below-box footer renders "esc to interrupt" while working — but only when the
	// 2.1.207 responsive hint area has room; contextual chips (a PR link, "ctrl+t to hide
	// tasks", background "N shell"/"N monitor", "↓ to manage") crowd it out on a busy
	// foreground turn. It stays a valid positive marker for the states that still show it.
	BusyMarkers:  []string{"esc to interrupt"},
	MarkerWindow: 0, // status hints render below the input box border

	// The above-box spinner status line ("<glyph> <Gerund>… (<elapsed> · …)") survives the
	// footer reflow and proves work even when "esc to interrupt" is crowded out (spinner.go).
	LiveSpinner: claudeSpinnerWorking,

	Prompts: []PromptMatcher{
		// The tool-permission dialog's decline option.
		{Name: "permission", Window: WindowPrompt,
			All: []string{"No, and tell Claude what to do differently"}},
		// The plan-approval dialog ("Would you like to proceed?" after plan mode).
		// Enter would accept the plan AND enable auto mode, so autoyes must not
		// answer it. Tokens pinned against a live 2.1.170 pane (registry_test.go
		// fixture): the rendered options are "Yes, and use auto mode" / "Yes,
		// manually approve edits" / "No, refine with Ultraplan…" / "Tell Claude
		// what to change" — and the dialog carries NO selection footer ("Esc to
		// cancel"), so without this matcher it reads as *idle*, not even as a
		// prompt. "No, keep planning" covers the binary's alternate label for the
		// feedback option. A future rewording fails open to that idle behavior.
		{Name: "plan", Window: WindowPrompt, NoAutoTap: true,
			Any: []string{
				"Yes, manually approve edits",
				"No, keep planning",
				"shift+tab to approve with this feedback",
			}},
		// The model-error notice: the API rejected --model X (404 model_not_found,
		// or the Pro-plan access restriction), strings pinned against the 2.1.170
		// binary's error mapping. The session stays alive with an idle input box,
		// so without this it reads as Ready. NoAutoTap: there is nothing to answer
		// — surface needs-input so the user attaches and fixes it via /model.
		// Unlike a dismissable dialog this is *transcript* content, so after the
		// fix it lingers in the bottom window into the start of the next turn
		// (prompt match precedes the busy marker in Poll); needs-input shows a few
		// extra seconds until output scrolls it away. Self-healing, nothing tapped.
		{Name: "model-error", Window: WindowPrompt, NoAutoTap: true,
			Any: []string{
				"issue with the selected model (",
				"is not available with the Claude Pro plan",
			}},
		// Auth expiry/revocation: those error messages start "Please run /login ·"
		// (same 2.1.170 provenance) and the session likewise sits idle-looking.
		// Same surfacing, nothing to auto-answer; same transcript-lingering note.
		{Name: "login-error", Window: WindowPrompt, NoAutoTap: true,
			All: []string{"Please run /login ·"}},
		// Any interactive selection (AskUserQuestion). A custom
		// multi-line statusLine can render *below* the key-hint footer — possibly
		// drawing its own ─── dividers — and push it out of any fixed bottom
		// window, so this matcher is structural: the rule-delimited segment scan
		// finds the footer wherever the statusLine displaced it, while the
		// input-box stop keeps a footer quoted in the transcript from counting.
		// NoAutoTap (#271, reversing the #103-era "generic selections stay
		// auto-tappable" pin): a selection is a judgment prompt — AskUserQuestion
		// renders even in bypass/auto permission modes, exactly where the agent
		// wants a human choice — and auto-Enter picks whatever option is
		// highlighted, chaining through multi-question flows on repeated ticks.
		// Permission/plan dialogs are unaffected: they match earlier in this
		// list, so they never reach here. Side effect, accepted: a stray open
		// selector (model/account picker) under autoyes surfaces needs-input
		// instead of being blindly Enter-ed.
		{Name: "selection", NoAutoTap: true, Match: claudeSelectionFooterVisible},
	},

	// Ghost-text prompt suggestion in the idle input box (suggestion.go).
	// Pinned against a live 2.1.17x capture (suggestion_test.go fixture,
	// 2026-06-12). Version-sensitive like every heuristic here, but this one
	// fails closed: a rewording/restyling upstream makes `a` do nothing on an
	// idle claude — never sends a stray keystroke.
	SuggestionVisible: claudeSuggestionVisible,

	// Collapsed-paste placeholder chip in the input box (claudePasteCollapsed). Claude
	// renders a ≥4-line bracketed paste as "[Pasted text #N +L lines]", so a queued multi-line
	// prompt never shows its first line for the delivery signature check — the chip is the
	// only landing signal. Verified live against claude 2.1.207 (2026-07-13).
	PasteCollapsed: claudePasteCollapsed,

	// Live permission mode from the footer's "⏵⏵ … on" / "⏸ plan mode on"
	// indicator, so the list chip tracks an in-session mode switch instead of
	// the stale launch-time flag. Pinned against a live 2.1.178 capture
	// (permissionmode_detect_test.go); version-sensitive like every heuristic
	// here, and fails safe — an unrecognized footer falls back to the flag.
	PermissionMode: claudePermissionMode,

	Gates: []Gate{
		// Folder-trust dialog. Claude reworded it after 2.1.170: the old title
		// "Do you trust the files in this folder?" is gone, replaced at 2.1.18x
		// by a "Quick safety check…" dialog whose confirm button reads "Yes, I
		// trust this folder" (pinned against a live 2.1.185 capture, see
		// registry_test.go claudeTrustPane). Both are matched so the gate fires
		// across the supported range; remove the old title once <2.1.18x is
		// unsupported. Plus the MCP-approval prompt (capital- and lowercase-N
		// variants).
		{Contains: []string{
			"Yes, I trust this folder",
			"Do you trust the files in this folder?",
			"new MCP server", "New MCP server"}},
	},

	// tmux word-splits the trailing command string itself, so appending to the
	// single program argv element is sufficient — no shell wrapping.
	Resume:        func(program string) string { return program + " --continue" },
	HookSupport:   true,
	HeadlessNamer: true, // `claude -p` with a JSON envelope (session/naming.go)
}

// selectionFooterTokens reports whether the flattened text carries claude's selection
// footer's co-occurring key hints: "Esc to cancel" plus a navigate/select token.
// Requiring the pair keeps prose that merely mentions one phrase from reading as a
// live prompt.
func selectionFooterTokens(s string) bool {
	return strings.Contains(s, "Esc to cancel") &&
		(strings.Contains(s, "to navigate") || strings.Contains(s, "to select"))
}

// claudeSelectionFooterVisible backs the claude "selection" matcher: the structural
// segment scan (see footerVisibleInSegments) applied to claude's footer tokens.
func claudeSelectionFooterVisible(content string) bool {
	return footerVisibleInSegments(content, selectionFooterTokens)
}

// claudePasteCollapsed backs the claude adapter's PasteCollapsed: it reports whether the input-box
// readback is a "[Pasted text +N lines]" placeholder chip (see pasteChipRegex), which claude shows
// in place of a ≥4-line bracketed paste.
func claudePasteCollapsed(boxText string) bool {
	return pasteChipRegex.MatchString(boxText)
}

// Codex CLI (openai/codex, Rust TUI). Strings verified against the repo at
// main (2026-06): the status row renders "Working (0s • esc to interrupt)"
// (status_indicator_widget.rs, pinned by its own test) *above* the composer,
// and every approval overlay carries a "No, …" option (approval_overlay.rs).
var codex = &Adapter{
	Key:         KeyCodex,
	DisplayName: "Codex",
	aliases:     []string{"codex"},

	BusyMarkers: []string{"esc to interrupt"},
	// The status row sits above the composer and its footer hints, outside the
	// below-the-box footer anchor; a window of 8 reaches over them.
	MarkerWindow: 8,

	Prompts: []PromptMatcher{
		// Decline options across the approval overlays: command/patch approvals
		// ("No, and tell Codex…"), permission and elicitation prompts ("No,
		// continue without…" / "No, but continue without it").
		{Name: "approval", Window: WindowPrompt,
			Any: []string{
				"No, and tell Codex what to do differently",
				"No, continue without",
				"No, but continue without",
			}},
	},

	Gates: []Gate{
		// onboarding/trust_directory.rs: "Do you trust the contents of this
		// directory?" with "Yes, continue" pre-highlighted.
		{Contains: []string{"Do you trust the contents of this directory"}},
	},

	// `codex resume --last` continues the most recent session. The subcommand
	// must follow the binary, so resume is only applied to a bare program; a
	// program carrying flags relaunches blank rather than risk an argv the
	// resume subcommand rejects.
	Resume: func(program string) string {
		if strings.ContainsRune(program, ' ') {
			return program
		}
		return program + " resume --last"
	},
	// The needle pins the clap subcommand listing line ("\n  resume  …"), not the
	// bare word: any old help text that merely *mentions* resuming would pass a
	// bare-word probe and relaunch an older codex into an argv it rejects. The
	// trade is deliberate — if clap's listing indent ever changes, the probe
	// fails closed and the session relaunches blank (the adapter's safe mode).
	ResumeProbe: "\n  resume ",

	// HeadlessNamer deliberately unset: `codex exec` output parsing is
	// unverified, so codex sessions auto-name through whichever capable agent
	// is installed (see session/naming.go).
}

// Gemini CLI (google-gemini/gemini-cli, React-Ink). Strings verified against
// the installed 0.27 package source: LoadingIndicator.js renders "(esc to
// cancel, 5s)" above the input box whenever streaming state is neither Idle nor
// WaitingForConfirmation, and ToolConfirmationMessage.js includes "No, suggest
// changes (esc)" in every confirmation variant. The pre-adapter matcher,
// "Yes, allow once", no longer appears anywhere in the package.
var gemini = &Adapter{
	Key:         KeyGemini,
	DisplayName: "Gemini CLI",
	aliases:     []string{"gemini"},

	// Heuristic strings verified against gemini 0.27. Minor granularity: the
	// confirmation wording tracks minor releases; pure patch bumps within a
	// minor don't warrant a warning.
	VerifiedVersion:  "0.27",
	DriftGranularity: GranularityMinor,

	BusyMarkers: []string{"esc to cancel"},
	// Like codex, the loading row renders above the input box.
	MarkerWindow: 8,

	Prompts: []PromptMatcher{
		{Name: "confirmation", Window: WindowPrompt,
			All: []string{"No, suggest changes (esc)"}},
	},

	Gates: []Gate{
		// FolderTrustDialog.js: "Do you trust this folder?" with "Trust folder"
		// pre-highlighted.
		{Contains: []string{"Do you trust this folder"}},
	},

	Resume:        func(program string) string { return program + " --resume latest" },
	ResumeProbe:   "--resume",
	HeadlessNamer: true, // `gemini -p` prints bare text (session/naming.go)
}

// aiderConfirmVisible backs the aider "confirm" matcher. Every confirm_ask
// (io.py at 0.86.2) opens its options with " (Y)es/(N)o", then appends
// "/(A)ll" (group, not explicit-yes), "/(S)kip all" (group), "/(D)on't ask
// again" (allow_never), and blocks at a trailing " [Yes]: "/" [No]: " default
// suffix. Two conditions, each doing one job:
//
//   - The "(Y)es"+"(N)o" pair anywhere in the flattened window covers every
//     option shape. Matching two tokens (not the contiguous "(Y)es/(N)o")
//     keeps a hard terminal wrap mid-run from defeating the match:
//     flattenChrome joins physical lines with a space.
//   - The last non-empty line must end with "]:" — the default suffix where
//     confirm_ask parks its cursor while blocked. This is the liveness
//     anchor: an answered confirm ("… [Yes]: y", or with output printed
//     below) and displayed content that merely mentions both tokens above
//     the "> " composer do not match, because something other than the
//     suffix ends the pane.
//
// The anchor is the bare "]:" rather than "[Yes]:"/"[No]:" to stay as
// wrap-tolerant as the token pair: of the possible wrap points inside the
// suffix, most leave a "]:"-tailed fragment as the last line, while the full
// bracket run survives none of them. Residual race, accepted: after an
// accept, the suffix line stays bottom-most until aider's next output lands,
// so a poll tick in that sub-second gap can still tap one extra Enter — with
// autoyes it accepts the next confirm's default, the intended semantics.
func aiderConfirmVisible(content string) bool {
	flat := flattenChrome(content, WindowPrompt)
	if !strings.Contains(flat, "(Y)es") || !strings.Contains(flat, "(N)o") {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(liveChromeLines(content, 1)), "]:")
}

// Aider. No stable busy marker is known, so it rides the poller's
// content-change fallback; the confirm matcher covers every confirm_ask
// option shape, and the first-run documentation gate carries over from the
// pre-adapter heuristics.
var aider = &Adapter{
	Key:         KeyAider,
	DisplayName: "Aider",
	aliases:     []string{"aider"},

	// Heuristic strings verified against a live aider 0.86.2 (2026-07-04),
	// one tmux capture per confirm shape (registry_test.go
	// TestAiderConfirmShapes). Minor granularity: aider ships 0.x minors
	// steadily while the confirm_ask format has been stable for years, so a
	// minor bump is the right re-verification cue and patch bumps are noise.
	VerifiedVersion:  "0.86.2",
	DriftGranularity: GranularityMinor,

	Prompts: []PromptMatcher{
		// See aiderConfirmVisible: the option-pair match covers every
		// confirm_ask shape — before #271 only the "/(D)on't ask again" shape
		// was matched, so the plain and group confirms read as *idle* (a
		// blocked session showed Ready and autoyes tapped nothing) — and the
		// trailing-"]:" liveness anchor keeps an answered confirm or
		// token-bearing displayed content from matching.
		{Name: "confirm", Match: aiderConfirmVisible},
	},

	Gates: []Gate{
		// First-run analytics/docs prompt.
		{Contains: []string{"Open documentation url for more info"}},
	},
}

// Generic is the adapter for programs no table entry recognizes: no markers
// (content-change fallback), no prompt or gate detection, no resume. Strictly
// the pre-adapter behavior for an unknown agent — except that unknown agents no
// longer match aider's documentation gate and receive its stray 'D' keystroke.
var Generic = &Adapter{
	Key:         KeyGeneric,
	DisplayName: "agent",
}

// registry is ordered; Resolve returns the first alias match. Aliases are
// disjoint today, so order is cosmetic.
var registry = []*Adapter{claude, codex, gemini, aider}

// Resolve maps a program string to its adapter, or Generic when no entry
// matches; it never returns nil. The program's first token is basenamed and
// lowercased before the contains match, so a direct invocation ("claude",
// "/usr/local/bin/claude", "claude --continue"), an argv with flags ("aider
// --model x"), and a launcher wrapper ("launch-claude.sh") all resolve, while a
// matching directory name ("/home/user/.claude-squad/bin/otheragent") does not.
func Resolve(program string) *Adapter {
	bin := program
	if i := strings.IndexByte(bin, ' '); i >= 0 {
		bin = bin[:i]
	}
	base := strings.ToLower(filepath.Base(bin))
	for _, a := range registry {
		for _, alias := range a.aliases {
			if strings.Contains(base, alias) {
				return a
			}
		}
	}
	return Generic
}
