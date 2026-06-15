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
// to registry_test.go pinning the new string against a captured pane.

// Claude Code. The reference adapter: every heuristic here predates this package
// and is pinned by the poll tests in session/tmux.
var claude = &Adapter{
	Key:         KeyClaude,
	DisplayName: "Claude Code",
	aliases:     []string{"claude"},

	// The footer renders e.g. "✻ Cogitating… (5s · esc to interrupt)" below the
	// input box for the whole turn, including silent tool calls.
	BusyMarkers:  []string{"esc to interrupt"},
	MarkerWindow: 0, // status hints render below the input box border

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
		{Name: "selection", Match: claudeSelectionFooterVisible},
	},

	// Ghost-text prompt suggestion in the idle input box (suggestion.go).
	// Pinned against a live 2.1.17x capture (suggestion_test.go fixture,
	// 2026-06-12). Version-sensitive like every heuristic here, but this one
	// fails closed: a rewording/restyling upstream makes `a` do nothing on an
	// idle claude — never sends a stray keystroke.
	SuggestionVisible: claudeSuggestionVisible,

	Gates: []Gate{
		{Contains: []string{"Do you trust the files in this folder?", "new MCP server", "New MCP server"},
			Dismiss: DismissEnter},
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
		{Contains: []string{"Do you trust the contents of this directory"},
			Dismiss: DismissEnter},
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
		{Contains: []string{"Do you trust this folder"}, Dismiss: DismissEnter},
	},

	Resume:        func(program string) string { return program + " --resume latest" },
	ResumeProbe:   "--resume",
	HeadlessNamer: true, // `gemini -p` prints bare text (session/naming.go)
}

// Aider. No stable busy marker is known, so it rides the poller's
// content-change fallback; its single confirmation shape and first-run
// documentation prompt carry over from the pre-adapter heuristics.
var aider = &Adapter{
	Key:         KeyAider,
	DisplayName: "Aider",
	aliases:     []string{"aider"},

	Prompts: []PromptMatcher{
		{Name: "confirm", Window: WindowPrompt,
			All: []string{"(Y)es/(N)o/(D)on't ask again"}},
	},

	Gates: []Gate{
		// First-run analytics/docs prompt; (D)on't ask again, then Enter.
		{Contains: []string{"Open documentation url for more info"},
			Dismiss: DismissDAndEnter},
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
