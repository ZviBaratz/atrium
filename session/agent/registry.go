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
		// Any interactive selection (AskUserQuestion, plan approval). The footer
		// requires its co-occurring tokens within a tight window, so prose merely
		// mentioning "Esc to cancel" higher in the chrome cannot trip it.
		{Name: "selection", Window: WindowFooter,
			All: []string{"Esc to cancel"},
			Any: []string{"to navigate", "to select"}},
	},

	Gates: []Gate{
		{Contains: []string{"Do you trust the files in this folder?", "new MCP server"},
			Dismiss: DismissEnter},
	},

	// tmux word-splits the trailing command string itself, so appending to the
	// single program argv element is sufficient — no shell wrapping.
	Resume:      func(program string) string { return program + " --continue" },
	HookSupport: true,
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
	ResumeProbe: "resume",
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

	Resume:      func(program string) string { return program + " --resume latest" },
	ResumeProbe: "--resume",
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
