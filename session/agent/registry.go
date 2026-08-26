package agent

import (
	"path/filepath"
	"regexp"
	"strings"
)

// The adapter table. Each entry records one agent CLI's heuristics with the
// provenance of every string, so a future "agent X shows as always idle" report
// can be fixed by re-checking the cited source and editing the one stale entry.
//
// Heuristic strings are version-sensitive by nature. When editing, add a fixture
// to registry_test.go pinning the new string against a captured pane, and bump
// the adapter's VerifiedVersion to the version you captured against.
//
// Read VerifiedVersion as a RECORD of what was last driven against a live pane,
// not as a tripwire. The drift guard in internal/doctor only warns once an
// installed CLI passes the pin at the adapter's DriftGranularity, so for the
// minor-granularity adapters here every patch release inside the pinned minor
// series reports "ok" no matter how far it has moved. #332 was filed on the
// premise that `atrium doctor` was flagging installed 2.1.209 against a 2.1.207
// pin; it was not, and could not — both truncate to 2.1.0. Nothing tells you a
// heuristic went stale. Only driving it does.
//
// A version pin also can't express everything that moves. Claude picks between two
// footer implementations in one binary on a server-resolved feature gate, so the
// rendered UI can change with NO version change at all — and because Atrium routes
// sessions to a per-account CLAUDE_CONFIG_DIR, two sessions on the same claude
// version can resolve that gate differently and render differently. VerifiedGates
// records which branch a capture came from; `atrium doctor` reads the value claude
// last resolved per account and reports a mismatch. So provenance here names a
// version AND a gate state: every capture in this file is an UNGATED capture.
//
// Nor can a version pin express what the USER changed. Claude composes most of its
// key hints at render time as "<chord> to <action>" (the Be/Sn components), and Sn
// resolves the chord through the keybinding registry — so a hint's leading half is
// whatever the user bound in <CLAUDE_CONFIG_DIR>/keybindings.json, hot-reloaded with
// no restart. Match the action half, never the chord. Two consequences for sweeps:
//
//   - Bundle ABSENCE is not evidence, the converse of the rule below. A composed hint
//     has zero contiguous hits in the binary while rendering perfectly: at 2.1.220
//     "esc to interrupt", "manual mode on", "bypass permissions on" and "Tab to amend"
//     all grep to 0, and all four are in captured panes in registry_test.go. A sweep
//     that greps to ask whether a marker still renders learns nothing in either
//     direction.
//   - Composed is not the same as variable. Only a registry-fed chord moves.
//     claudePermissionModeMarkers is composed from constants (the label words, with
//     the "(shift+tab to cycle)" chord deliberately excluded), and the #354 A/B found
//     every chord in the permission and selection footers hardcoded. Prove which is
//     which by REBINDING and re-capturing, not by reading the bundle.
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

	// Every heuristic below was driven against a live claude 2.1.210 pane in the #332
	// sweep (2026-07-15) — busy marker (at widths 200/60/56/30), live spinner, plan
	// approval, model-error, AskUserQuestion selection, folder-trust gate, all six
	// permission-mode footers, the "? for shortcuts" fall-through, the collapsed-paste
	// chip, the dim ghost-text suggestion, the --settings capability probe, and both
	// MCP-approval shapes. The one string a pane cannot show is the login-error separator
	// (reaching it means revoking auth); it was confirmed present in the 2.1.210 bundle
	// instead. #332 claimed the MCP titles were unreachable too — they are not, a
	// project-scoped .mcp.json renders them on demand, and #340 drove them. The
	// fetch/network dialog was the last shape never driven; #343 drove it (prompt a
	// session to WebFetch a fresh domain) at widths 100 and 28.
	//
	// The sweep exists because the pin is a claim about the WHOLE surface, and three times
	// now that claim was false at the version it named. #333 found the default footer
	// ("manual mode on") rendering on a live 2.1.207 pane the marker table did not know.
	// #332 then found the tool-permission matcher keyed on a literal that belongs only to
	// the fetch/network dialogs, so every Write/Edit/Bash approval read as idle — also
	// reproducing at 2.1.207. #343 then drove the fetch dialog that literal came from and
	// found the fixture describing it was invented: it renders NO footer, and it renders
	// the tool's own arguments inside itself, which is what made the literal forgeable.
	// None was newer-CLI drift; all were wrong at the pin. So re-verify by DRIVING each
	// heuristic, and treat "the string is still in the bundle" as necessary but not
	// sufficient — a literal can survive while nothing renders it, and the bundle cannot
	// tell you what surrounds it on screen.
	//
	// #354 (2026-07-28) drove a TARGETED set against a live 2.1.220 pane — the busy
	// marker under a rebound and restored chat:cancel, the Write-approval footer, the
	// AskUserQuestion selection footer, the folder-trust gate and the default
	// permission-mode footer — and the VerifiedVersion below is deliberately NOT bumped
	// for it. The pin is a claim about the WHOLE surface; the fetch and MCP dialogs, the
	// plan and model-error prompts, the live spinner, the suggestion and paste heuristics
	// were not re-driven. Bumping on a partial drive is how a pin starts lying, which is
	// the failure the three misses above all share.
	//
	// The background-work chips (background.go) were driven against a live 2.1.228 pane on
	// 2026-08-12 — a real Bash(run_in_background) and a real Monitor, singly, doubled and
	// together — and the pin is deliberately NOT bumped for that either, for the same
	// reason: one surface is not the whole surface.
	//
	// Minor granularity (matching gemini): claude ships patch releases every few days, so
	// patch-level drift would fire the warning almost constantly — alert fatigue, not
	// signal. A patch reword is already handled additively (both old and new variants kept
	// in the same matcher's union, so matching never depends on the version), and a missed
	// reword fails gracefully to "idle", never a wrong action. So only a minor/major bump —
	// where structural UI changes are likelier — counts as drift worth re-verifying. Note
	// the corollary the two misses above make concrete: within a minor series this pin
	// warns about nothing, so it is a record of what was checked, not a tripwire that will
	// tell you when to check again.
	VerifiedVersion:  "2.1.210",
	DriftGranularity: GranularityMinor,

	// The footer's implementation is chosen by this gate, not by the version. The
	// ungated branch (false) builds a hint LIST — hints concatenate, and "? for
	// shortcuts" is pushed only when the list is otherwise empty. The gated branch is
	// the single mutually-exclusive slot #333 described and mistook for ours. Live
	// 2.1.210 resolves false, proven by a co-occurrence the slot branch cannot produce:
	// "⏸ manual mode on · ? for shortcuts · ← for agents" carries the shortcuts hint
	// and the agents hint at once. Every capture pinned in this package is therefore an
	// UNGATED capture, and this field is what says so.
	//
	// What a flip would do to detection is NOT known, and cannot be found out here.
	// The gated branch is the one heuristic-relevant surface in this file that cannot
	// be driven at 2.1.210: the CLAUDE_INTERNAL_FC_OVERRIDES parse is dead code (A1r
	// returns before it reads the env var) and the in-memory payload beats the on-disk
	// map, so there is no supported way to make a pane render it. #337 read the gated
	// source and expects "<label> on" and "esc to interrupt" to survive, which would
	// leave BusyMarkers and the mode table spanning both branches — but that is a
	// bundle reading of a branch nobody has rendered, and this file's own rule is that
	// bundle presence is necessary and not sufficient. It is not a reason to skip
	// re-verifying. (Sharpening the point: "esc to interrupt" is not even a contiguous
	// literal in the binary — 0 hits at 2.1.210 and again at 2.1.220 — so grepping for
	// it proves nothing in either direction, on either branch. See the header on
	// composed hints.)
	//
	// Detecting a flip is therefore what we get instead of auditing for one, and why
	// this is a pin rather than a comment — see internal/doctor/gates.go.
	VerifiedGates: []VerifiedGate{{Name: "tengu_copper_thistle", Value: false}},

	// The below-box footer renders "<chord> to interrupt" while working, and the chord
	// half is the USER'S: claude builds the hint as Be({chord: t, action: "interrupt"}),
	// where action is a hardcoded literal but t = pc("chat:cancel", "Chat", "esc") — the
	// display text of whatever the user bound chat:cancel to. So the marker is keyed on
	// the invariant half. Driven at 2.1.220 (#354, 2026-07-28): rebinding chat:cancel to
	// ctrl+q in <CLAUDE_CONFIG_DIR>/keybindings.json hot-reloaded the SAME live pane's
	// footer from "esc to interrupt" to "ctrl+q to interrupt" and back on restore, so the
	// old marker missed and a working session read Ready.
	//
	// "to interrupt" is a strict superset of the shape it replaces, so this is the #271
	// broadening, not the two-variant union: keeping "esc to interrupt" beside it would
	// be dead string, and TestClaudeBusyMarker still pins the default-binding footer.
	//
	// It does not cover UNBINDING. Be returns null on an empty chord, so chat:cancel set
	// to null renders no hint at all — nothing is left to match and the live spinner
	// below carries alone. That is the same fail-safe as the two causes listed further
	// down, not a new hole.
	//
	// The widening it does buy: MarkerWindow 0 falls back to the last three non-empty
	// lines when the pane shows no box border, and unlike LiveSpinner the marker has no
	// animation gate, so a static match holds Working. Prose ending in "… to interrupt"
	// on a borderless pane now matches where "esc to interrupt" did not. Pinned in
	// TestClaudeBusyMarker as a known cost rather than fixed here: narrowing footerRegion
	// would also move permission-mode detection, which shares it, and a claude pane with
	// no border is one whose TUI is not up.
	//
	// #308 read the marker's absence on a busy pane as a *responsive* hint area crowding
	// it out at narrow widths; that was wrong, and the sweep in #332 corrected it. The
	// hint list is built by plain concatenation with no width term and no priority — the
	// interrupt hint and the "ctrl+t to hide tasks" chip render together, so a chip never
	// displaces the marker. Confirmed live at 2.1.210: a busy pane keeps the interrupt
	// hint intact at widths 200, 60 and 56.
	//
	// Two real reasons the marker can still go missing on a working pane:
	//   - The footer gates it on the CLI's narrowest notion of busy. The bundle tracks
	//     isLoading / isExternalLoading / betweenCalls separately and only isLoading
	//     lights the hint, so a turn can be underway with no marker at all. That is the
	//     shape the #308 bug pane actually captured (session/tmux/spinner_poll_test.go).
	//   - The whole footer line is rendered with truncate-on-overflow, so a *narrow
	//     enough* pane cuts the tail off mid-word — at width 30 a busy 2.1.210 pane
	//     reads "⏸ manual mode on · esc to …", losing the marker. This is one composed
	//     line overflowing, not hint selection: the hint is present, just clipped.
	// Both fail safe — a missing marker reads idle, never a wrong action — and the live
	// spinner below covers them, so the marker stays a valid positive signal.
	BusyMarkers:  []string{"to interrupt"},
	MarkerWindow: 0, // status hints render below the input box border

	// The above-box spinner status line ("<glyph> <Gerund>… (<elapsed> · …)") proves work
	// when the footer marker is absent (spinner.go). It survives both causes above: it
	// tracks a broader notion of busy than the interrupt hint, and its signature sits at
	// the head of its own line, where truncation reaches last.
	LiveSpinner: claudeSpinnerWorking,

	// The footer's shell/monitor count chips, which outlive the turn that started them
	// (background.go). Distinct from both signals above: those say the turn is running,
	// this says the turn ended and left something behind.
	BackgroundWork: claudeBackgroundWorkVisible,

	Prompts: []PromptMatcher{
		// The fetch/network permission dialog — the ONE prompt in this list autoyes
		// still answers with Enter, so it is the only heuristic here whose failure
		// performs an action rather than mislabeling a row. Keyed on the dialog's own
		// title, positioned as the live question (claudeFetchPermissionVisible), and
		// pinned against a live 2.1.210 capture at two widths (registry_test.go
		// claudeFetchPane / claudeFetchNarrowPane).
		//
		// Until #343 it keyed on the decline option "No, and tell Claude what to do
		// differently" in a flat bottom-15 window. That was wrong twice over, both
		// captured live:
		//   - The literal lives verbatim in this file, so a session merely reading or
		//     grepping this repo printed it and read as a live prompt — on an idle pane,
		//     which never scrolls, autoyes tapped Enter into the composer
		//     (claudeQuotedPermissionPane).
		//   - Worse, claude renders a tool's own arguments INSIDE the approval dialog,
		//     below its top rule. So `grep "No, and tell Claude what to do differently"`
		//     put the literal in LIVE CHROME, not the transcript: the Bash dialog matched
		//     here, this matcher precedes permission-local, and autoyes Enter-approved
		//     the shell command against a human's explicit gate (claudeBashForgedPane).
		//     No liveness anchor can fix that one — the forged text is inside the live
		//     dialog — which is why the title, not the option, is what this keys on.
		{Name: "permission", Match: claudeFetchPermissionVisible},
		// Local tool approvals: the Write/Edit/Bash dialogs. Their decline option
		// is a bare "3. No" and their footer names no navigate/select token, so
		// before #332 neither the matcher above nor the selection matcher below
		// saw them and a blocked session read as *idle* — Ready, with autoyes
		// walking past it. Keyed on the footer pair rather than the options,
		// which vary per tool ("Yes, allow all edits during this session" for a
		// write, "Yes, and always allow access to <dir> from this project" for a
		// command); "Tab to amend" is the discriminator, since "Esc to cancel"
		// alone also appears under the trust gate and the /model picker.
		// Structural, not a flat window: this footer is the most quotable string
		// in the adapter — an agent working on Atrium itself prints it — and a
		// flat bottom-N match reads that quote as a live prompt. Unlike the
		// model-error notice, which scrolls away on the next turn, the quote sits
		// on an IDLE pane that never scrolls, so it would stick at needs-input
		// until the user typed. The segment scan stops at the input box, and the
		// dialog replaces that box while it is up, so the live shape matches and
		// a quote above the box cannot.
		// NoAutoTap: Enter here approves a file write or a shell command against
		// a human's explicit gate. The fetch dialog above stays auto-tappable —
		// this matcher sits after it, so that behavior is unchanged.
		// Pinned against live 2.1.210 captures, byte-identical on 2.1.207
		// (registry_test.go claudeWritePermissionPane / claudeBashPermissionPane).
		{Name: "permission-local", NoAutoTap: true, Match: claudeLocalPermissionVisible},
		// The rest of the fetch/network family: its decline option in live chrome,
		// surfaced as needs-input but never tapped. The bundle carries that option
		// under two titles — the fetch dialog above, and the sandbox's "Do you want
		// to allow this connection?" — and only the first can be driven here (the
		// second needs sandbox mode), so only the first is auto-answered. This net
		// keeps the undriven sibling DETECTED, which is not cosmetic: the fetch
		// dialog renders NO footer, so permission-local cannot see this family, and
		// DetectPrompt is the only thing standing between a queued prompt and a live
		// dialog (session/tmux AwaitingInput — claude's dialog "❯ 1. Yes" reads as an
		// input box, so InputBoxVisible does not stop it). Undetected, a queued
		// prompt would be typed into the dialog and retried every cycle.
		//
		// It sits after permission-local only so the log names the right dialog:
		// both are NoAutoTap, so a pane matching both behaves identically either way,
		// and a forged Bash dialog (see above) is a Bash dialog, not a network one.
		{Name: "permission-network", NoAutoTap: true, Match: claudeNetworkPermissionVisible},
		// The plan-approval dialog ("Would you like to proceed?" after plan mode).
		// Enter would accept the plan AND enable auto mode, so autoyes must not
		// answer it. Tokens pinned against a live 2.1.170 pane (registry_test.go
		// fixture) and re-confirmed verbatim on a live 2.1.210 dialog (#332): the
		// rendered options are "Yes, and use auto mode" / "Yes,
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
		// binary's error mapping and re-confirmed on a live 2.1.210 pane (#332:
		// `claude --model __atrium_probe__` then a prompt). The session stays alive with an idle input box,
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
		// (same 2.1.170 provenance; a pane cannot be driven into it without revoking
		// auth, so #332 re-confirmed the literal in the 2.1.210 bundle instead) and the session likewise sits idle-looking.
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
		// list, so they never reach here. Scope, measured live at 2.1.210 (#332):
		// this fires on AskUserQuestion, whose footer reads "Enter to select ·
		// ↑/↓ to navigate · Esc to cancel". It does NOT fire on the /model
		// picker, whose footer names no navigate/select token ("Enter to set as
		// default · s to use this session only · Esc to cancel") — an earlier
		// comment here claimed that picker surfaced needs-input; it does not,
		// and reads as idle instead. Harmless (a stray picker is a rare,
		// self-inflicted state) but not something to rely on.
		{Name: "selection", NoAutoTap: true, Match: claudeSelectionFooterVisible},
	},

	// Ghost-text prompt suggestion in the idle input box (suggestion.go).
	// Pinned against a live 2.1.17x capture (suggestion_test.go fixture,
	// 2026-06-12); re-confirmed at 2.1.210 (#332), where an idle box still reads
	// "❯" + U+00A0 + SGR dim + the suggested text. Version-sensitive like every heuristic here, but this one
	// fails closed: a rewording/restyling upstream makes `a` do nothing on an
	// idle claude — never sends a stray keystroke.
	SuggestionVisible: claudeSuggestionVisible,

	// Collapsed-paste placeholder chip in the input box (claudePasteCollapsed). Claude
	// renders a ≥4-line bracketed paste as "[Pasted text #N +L lines]", so a queued multi-line
	// prompt never shows its first line for the delivery signature check — the chip is the
	// only landing signal. Verified live against claude 2.1.207 (2026-07-13); re-confirmed
	// against 2.1.210 (2026-07-15, #332).
	PasteCollapsed: claudePasteCollapsed,

	// Live permission mode from the footer's "⏵⏵ … on" / "⏸ manual mode on"
	// indicator, so the list chip tracks an in-session mode switch instead of
	// the stale launch-time flag. Every marker in the table is pinned against a
	// live capture (permissionmode_detect_test.go): the shift+tab cycle and
	// dontAsk at 2.1.209 (#333), re-confirmed at 2.1.210 along with
	// bypassPermissions — which #333 could only read off the bundle — in #332.
	// Version-sensitive like every heuristic here, and fails safe: an
	// unrecognized footer falls back to the flag.
	PermissionMode: claudePermissionMode,

	Gates: []Gate{
		// Folder-trust dialog. Claude reworded it after 2.1.170: the old title
		// "Do you trust the files in this folder?" is gone, replaced at 2.1.18x
		// by a "Quick safety check…" dialog whose confirm button reads "Yes, I
		// trust this folder" (pinned against a live 2.1.185 capture, see
		// registry_test.go claudeTrustPane; re-confirmed verbatim on a live 2.1.210
		// launch in a fresh dir, #332). Both are matched so the gate fires
		// across the supported range; remove the old title once <2.1.18x is
		// unsupported.
		//
		// Plus the MCP-approval prompt, whose two literals are not a
		// capital/lowercase spelling hedge but the titles of two DIFFERENT
		// dialogs (both observed on live 2.1.210 dialogs, #340). The fixtures
		// beside them, registry_test.go claudeMCPSinglePane / claudeMCPMultiPane,
		// are transcriptions of those panes rather than verbatim captures — #666
		// settled that and re-drove both shapes, so the verbatim ones are
		// claudeMCPSingleWidePane / claudeMCPMultiWidePane:
		//   "New MCP server found in this project: <name>"   → one server
		//   "3 new MCP servers found in this project"        → many, matched
		//                                                      as a substring
		// Neither literal is redundant, and the fixtures prove it one at a
		// time: drop the capital-N and only the singular fixture fails, drop
		// the lowercase and only the plural shapes do. Case is what separates
		// them because the plural's count prefix ("3 new…") lowercases the title.
		//
		// The gate is the ONLY thing that sees either. The singular's footer
		// ("Enter to confirm · Esc to cancel") names no navigate/select token,
		// and the plural's says "Esc to reject all" — so neither reaches the
		// selection matcher, and a missing gate would read as Ready while the
		// session sits blocked. Keyed on the titles, which is what makes it
		// sound: unlike #332's permission literal, these ARE this dialog's own
		// text rather than another family's option label.
		//
		// Structural, not a flat window (claudeGateVisible): these titles are the
		// most quotable strings in the adapter — "new MCP server" is a bare noun
		// phrase, and an agent working on Atrium prints all four verbatim — so a
		// bottom-N match read those quotes as a live gate. #340's width note is
		// obsolete with it: the anchored region is the dialog however tall it
		// reflows, so the 15-line budget no longer bounds the gate and the
		// width-28 miss it recorded is fixed (registry_test.go
		// claudeMCPNarrowPane).
		{Name: "startup", Match: claudeGateVisible},
	},

	// tmux word-splits the trailing command string itself, so appending to the
	// single program argv element is sufficient — no shell wrapping.
	//
	// A program that already pins the conversation to resume is left alone. A
	// fork-from-checkpoint session carries `--resume <id>` for its whole life
	// (resuming an id keeps that id, so the pin never goes stale — see
	// session/fork.go), and appending --continue beside it would hand claude two
	// different answers to "which conversation?" on every resurrection.
	//
	// DRIVEN 2026-08-18 on claude 2.1.234, in a directory with no conversation:
	// `claude --continue` prints "No conversation found to continue" and exits 1,
	// killing the pane. That is why Instance.startResuming asks the transcript adapter
	// FIRST for claude — and why this row is the control in drive-agent.sh's
	// RESUME_TABLE: an arm that could not observe a death would report the other three
	// as survivors just as it does now. Re-check with `just drive-agent resume claude`.
	// Note the death lands only once the folder is TRUSTED; a fresh workspace sits at
	// the trust gate holding the flag.
	Resume: func(program string) string {
		if hasFlag(program, "--resume") || hasFlag(program, "-r") {
			return program
		}
		return program + " --continue"
	},
	HookSupport:   true,
	HeadlessNamer: true, // `claude -p` with a JSON envelope (session/naming.go)
}

// claudeGateTitles are the literals claude's gate is keyed on. A package-level var rather
// than an inline literal because claudeGateVisible — the Gate's own Match — reads them, and a
// Gate literal referencing a func that read that same Gate back would be an initialization
// cycle. The Gate deliberately carries no Contains: Match replaces that scan entirely
// (GateUp), so a Contains beside it would never be read, and a reader could not tell that the
// no-border fallback lives inside claudeGateVisible rather than in the declarative field.
var claudeGateTitles = []string{
	"Yes, I trust this folder",
	"Do you trust the files in this folder?",
	"new MCP server", "New MCP server",
}

// claudeGateVisible backs claude's Gate.Match: its titles, matched only inside the region a
// box border proves is live chrome (footerBelowBox), never the transcript above it.
//
// Claude's gates are shaped "one rule across the top, dialog below it, no bottom rule" —
// pinned by every captured shape, which is a set to look up rather than a list to keep here:
// they are the claude/gate entries of pane_width_test.go's paneCoverage, and both ladders
// driven so far added to them (#340, #666). So the last border on a gated pane is the dialog's
// own top rule and everything below it IS the dialog, while on a
// running session the last border is the composer's bottom edge and everything below it is
// just the footer. That asymmetry is the whole signal, and it is the one footerBelowBox was
// written for: "a caller that must not false-match a phrase quoted in the conversation".
//
// Why not the flat window it replaces: only ~5 lines of live chrome sit below the composer,
// so a bottom-15 window always also holds the tail of the transcript, and a session merely
// discussing these titles read as blocked — with the row stuck on "waiting on setup screen"
// and, because PaneGate also gates prompt delivery (session/tmux AwaitingInput), its queued
// prompt silently never sent. Tightening the literals cannot fix that: the sessions that hit
// it quote the titles verbatim, being about this file.
//
// Why not the segment scan the prompt matchers use (footerVisibleInSegments): its input-box
// stop only fires on a segment whose FIRST line is the composer, so a live permission dialog
// — whose segment opens with its own title — lets the scan walk on into the transcript. The
// border anchor does not walk, and it puts no floor under the region, so a title reconstructs
// however tall the dialog reflows (claudeMCPWrappedPane, claudeMCPNarrowPane).
//
// What the anchor does NOT prove, bounded here rather than assumed away:
//
//   - That anything sits below the rule at all. footerBelowBox reports ok=true for a pane
//     whose LAST line is the rule, handing back an empty region: ok means "an anchor exists",
//     not "the region is meaningful". Keying the fallback on ok alone would match "" and go
//     silent — a MISSED gate — so an empty region falls back too.
//   - That the rule is live chrome. Removing the floor must not remove the ceiling with it,
//     or transcript below a rule the agent printed itself matches instead: see gateRegionCap.
//
// Either fallback lands on the flat window, which is today's behavior, kept because ITS
// failure is a false positive (needs-input on a live session), never a missed gate — and it
// is unreachable for the bug above, which needs a composer on screen, which is itself drawn
// with borders.
//
// Known limit, accepted: a rule rendered BELOW a live dialog steals the anchor, and the gate
// is missed. A custom statusLine drawing its own ─── is the shape chrome.go names (it is why
// footerVisibleInSegments exists). Reaching it needs claude to paint REPL chrome around a
// startup screen, which no captured gate does — the dialogs replace the composer rather than
// sit above it — so there is no pane to pin it from; revisit if one is ever captured.
func claudeGateVisible(content string) bool {
	region, ok := footerBelowBox(content)
	if !ok || strings.TrimSpace(region) == "" {
		return containsAny(flattenChrome(content, WindowPrompt), claudeGateTitles)
	}
	return containsAny(flattenChrome(region, gateRegionCap), claudeGateTitles)
}

// gateRegionCap bounds how many non-empty lines below the anchoring rule claudeGateVisible
// matches in. The anchor is the pane's LAST rule, which is the dialog's own top rule only
// while a dialog IS the live chrome. On a frame with no composer — startup, or a --continue
// transcript replay — the last rule can instead be one the agent printed in its own output (a
// markdown rule, a table edge, a diff header), and then everything below it is transcript,
// unbounded. Dropping the flat window's budget dropped that ceiling along with the floor
// #340 measured as the bug: without this, a title quoted 60 lines under such a rule fires the
// gate where the bottom-15 window did not — a false positive, which is the reported bug's own
// direction (a row stuck on "waiting on setup screen", its queued prompt never sent).
//
// The cap restores the ceiling without restoring the floor, so it bites only when the anchor
// turns out not to be live chrome. That it still clears every real gate is MEASURED, not
// asserted here: TestClaudeGateAnchorEdges walks every claude/gate capture, counts what each
// puts below its anchoring rule, and fails if the cap does not exceed the tallest. Read the
// margin off that test rather than from a number in this comment — the tallest region went
// from 16 lines to 26 when #666 drove these shapes down to 20, and a figure written down here
// is exactly what would not have noticed. When it does bite, the title is at
// the TOP of the region and so the first thing flattenChrome drops, which is a MISSED gate.
// Same role aboveBoxBlockCap plays for the upward scan (chrome.go).
const gateRegionCap = 40

// claudeFetchTitles are the fetch/network dialog's own question text, captured live at
// 2.1.210 (registry_test.go claudeFetchPane). This is the dialog's OWN chrome, which is
// what makes it a sound key — unlike the decline option it replaces, which is a label
// shared with the sandbox dialog and, fatally, appears inside other dialogs' bodies.
var claudeFetchTitles = []string{"Do you want to allow Claude to fetch this content?"}

// claudeQuestionPrefix opens every claude tool-approval question: "Do you want to allow
// Claude to fetch this content?" (fetch), "Do you want to proceed?" (bash), "Do you want
// to create hello.txt?" (write) — all captured live at 2.1.210. It is the pivot
// claudeFetchPermissionVisible uses to find the dialog's question rather than its body.
const claudeQuestionPrefix = "Do you want to "

// claudeNetworkDeclineOptions is the fetch/network family's decline option. The bundle
// carries it only under the fetch title and the sandbox's "Do you want to allow this
// connection?"; local tool approvals use a bare "No" (#332). It backs permission-network
// — detection only. It must never gate an auto-tap again: it is this file's own text, and
// it renders inside other dialogs' bodies (#343).
var claudeNetworkDeclineOptions = []string{"No, and tell Claude what to do differently"}

// permissionRegionCap bounds how many non-empty lines below the anchoring rule the
// permission matchers match in. It plays the same role gateRegionCap does for the gate —
// restoring a ceiling once the flat window's floor is gone, so transcript below a rule the
// agent printed itself on a composer-less frame cannot match — but it is deliberately its
// own constant at a much tighter value, because the two measure different things: the
// gate's literal is a dialog TITLE at the top of its dialog (hence 40, clearing the tallest
// capture), while these key on the question and options at the BOTTOM, which flattenChrome
// reaches first. Measured on the live 2.1.210 captures: the fetch title sits 9 non-empty
// lines above the region's bottom at width 28 (claudeFetchNarrowPane), so 20 clears every
// captured shape with better than 2x margin while exposing half the surface 40 would.
//
// 28 is the narrowest width this dialog was CAPTURED at, not a floor — there is none (see
// the agy block below, and #512's captures at 24 and 20). The margin above is therefore
// measured only down to 28; a narrower pane reflows the body further and the headroom there
// is unmeasured. That is a known gap, not a claim.
const permissionRegionCap = 20

// claudeLiveDialogRegion returns claude's live dialog region — the lines below the pane's
// last box border, flattened — and whether the pane has that anchor at all. It is
// footerBelowBox's contract ("the border proves everything below it is live chrome, never
// scrolled-back transcript") applied to the permission matchers: on a pane with a composer
// the last rule is the composer's own bottom edge, so the region is just the footer and a
// phrase QUOTED in the transcript above can never reach it; on a dialog pane the last rule
// is the dialog's own top rule and the region is the dialog itself. Every captured claude
// dialog and gate leads with that rule (registry_test.go), and every captured composer ends
// with one, which is what makes the anchor's absence meaningful.
//
// !ok is returned for no anchor AND for an anchor with nothing under it (footerBelowBox
// reports ok=true for a pane whose last line is the rule — ok means "an anchor exists", not
// "the region is meaningful"). Both are hard false at the callers, with NO fallback to the
// flat window — the opposite of claudeGateVisible, deliberately:
//
//   - For the gate a miss is the dangerous direction (a queued prompt typed into a trust
//     screen), so its fallback's false positives are the cheaper failure and worth keeping.
//   - Here the fallback could only ever hurt. Every borderless claude pane is one where no
//     dialog can be up — a pre-box boot frame, a --continue replay before the box paints, a
//     degenerate capture — because every captured dialog carries its own top rule. So the
//     flat window has no miss to rescue on such a pane, and one real false positive to
//     cause: a --continue replay of an Atrium session's transcript quotes these literals
//     while no box has painted yet.
//
// The known limit inherited from the anchor — a rule rendered BELOW a live dialog steals it
// (a custom statusLine's own ───, the shape chrome.go names) — costs a missed dialog. The
// choice above does not bear on it either way: a stolen anchor reports ok=true with the wrong
// region, so a fallback keyed on !ok would never fire for it. The cost is not symmetric:
//
//   - For permission a miss is one Enter autoyes does not send. The human taps it: safe.
//   - For permission-network/permission-local a miss reads as idle, and idle is what lets a
//     queued prompt be typed into the live dialog (permission-network above; session/tmux
//     AwaitingInput takes the dialog's "❯ 1. Yes" for a composer). That is the gate's
//     dangerous direction, not a safe one.
//
// Accepted on the same ground claudeGateVisible accepts it, not on fail-safety: no captured
// dialog renders a rule below itself — they replace the composer rather than sit above it — so
// there is no pane to pin it from. Revisit if one is ever captured. The segment scan is not
// the escape hatch here, for the reason claudeGateVisible gives: its input-box stop never
// fires on a dialog's own segment, so it walks on into the transcript.
func claudeLiveDialogRegion(content string) (string, bool) {
	region, ok := footerBelowBox(content)
	if !ok || strings.TrimSpace(region) == "" {
		return "", false
	}
	return flattenChrome(region, permissionRegionCap), true
}

// claudeFetchPermissionVisible backs the claude "permission" matcher — the only prompt
// autoyes answers with Enter, so it is written to fire on the fetch dialog and nothing else.
//
// Two conditions, each doing one job, because #343 proved one is not enough:
//
//   - The region must be live chrome (claudeLiveDialogRegion). This is what stops the
//     reported bug: the literals live verbatim in this file, so an agent working on Atrium
//     prints them, and on an idle pane — which never scrolls — a flat bottom-N match stuck
//     at needs-input until a human typed, with autoyes tapping Enter into the composer.
//
//   - The region's LAST question must be the fetch title. The anchor alone cannot do this,
//     and that is the sharper half of #343: claude renders a tool's own arguments inside the
//     approval dialog, BELOW its top rule, so `grep "No, and tell Claude what to do
//     differently"` forges the old key inside live chrome and autoyes approved the shell
//     command (claudeBashForgedPane, captured live). Body text is not transcript; no anchor
//     separates it. Position does: every captured dialog renders its body ABOVE its
//     question, so the LAST "Do you want to …" on the pane is always the dialog's own, and a
//     forged title in a Bash command or an Edit diff is never it. The Bash dialog's real
//     question is "Do you want to proceed?", so it falls through to permission-local and
//     surfaces as needs-input instead of being tapped.
//
// LastIndex over the FLATTENED region, not a per-line scan: at width 28 the title reflows
// across three physical lines ("Do you want to allow" / "Claude to fetch this" / "content?"
// — claudeFetchNarrowPane), and only flattening reconstructs it.
//
// Residual, accepted and unpinnable: text rendered BELOW a dialog's own question would
// forge the title. No captured dialog does that — the question always sits last, directly
// above the options — so there is no pane to pin it from. It also needs a filename or an
// argument crafted to end in this exact sentence, which is an adversarial agent, not the
// accidental quoting this fix is about.
func claudeFetchPermissionVisible(content string) bool {
	flat, ok := claudeLiveDialogRegion(content)
	if !ok {
		return false
	}
	i := strings.LastIndex(flat, claudeQuestionPrefix)
	if i < 0 {
		return false
	}
	return hasAnyPrefix(flat[i:], claudeFetchTitles)
}

// claudeNetworkPermissionVisible backs the claude "permission-network" matcher: the
// fetch/network family's decline option anywhere in the live dialog region. Detection only
// (NoAutoTap), which is the whole point — it is deliberately looser than the fetch matcher
// above so the family's undriven sibling (the sandbox's connection dialog) still blocks
// prompt delivery and surfaces needs-input, while nothing it matches is ever tapped.
func claudeNetworkPermissionVisible(content string) bool {
	flat, ok := claudeLiveDialogRegion(content)
	if !ok {
		return false
	}
	return containsAny(flat, claudeNetworkDeclineOptions)
}

// hasAnyPrefix reports whether s begins with any of prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
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

// localPermissionFooterTokens reports whether the flattened text carries the local
// tool-permission dialog's footer pair: "Esc to cancel" plus "Tab to amend". The
// pair is what separates it from the trust gate and the /model picker, which show
// "Esc to cancel" beside a different second hint.
//
// FLATTENED is load-bearing, and #666 is where that stopped being an assumption. Both
// dialog shapes were driven against a live 2.1.228 at 120 60 40 34 32 31 30 29 28 26 24 20
// (the write shape) and 120 60 40 34 30 29 28 26 24 20 (the shell one). This footer WRAPS on
// overflow — it does not truncate the way the below-box status line does — and at 29 the
// break falls INSIDE "Tab to amend": the pane carries " Esc to cancel · Tab to" and " amend"
// on separate physical lines. A flat per-line match would miss every rung below 30. The
// captures are claudeWritePermission{Narrow,WrappedFooter,Floor}Pane and
// claudeBashPermission{WrappedFooter,Floor}Pane, filed with their widths in
// pane_width_test.go, which is what proves the pair holds to 20 rather than asserting it.
//
// WHICH flatten is not one answer, and the ladder straddles both. footerVisibleInSegments
// segments on box borders inside the bottom-WindowPrompt window; the 30 and 29 rungs still
// show one there, so they take the segment scan, whose segment is as tall as the dialog. By 20
// the splash has scrolled clear and NO border survives that window, so the 20 rungs take the
// no-rules fallback instead — a flat window workChromeLines tall, which
// claudeBashPermissionFloorPane's three-piece footer fills EXACTLY. Zero headroom is the
// narrowest thing about this matcher, and it is a measured value rather than a remark:
// TestPermissionLocalFooterFlattenBudget computes the depth each rung needs and reddens when
// the shell shape stops fitting. A fourth hint in claude's footer, or a rung below 20, lands
// there first — and lands as a MISSED dialog, which session/tmux AwaitingInput then types the
// queued prompt into.
//
// So do not "simplify" this to a match over lines, and do not shorten either half to buy
// width: neither half is length-bound here. What the pair costs is the two decoys it exists
// to exclude, which is a different budget entirely.
func localPermissionFooterTokens(s string) bool {
	return strings.Contains(s, "Esc to cancel") && strings.Contains(s, "Tab to amend")
}

// claudeLocalPermissionVisible backs the claude "permission-local" matcher: the same
// structural segment scan the selection matcher uses, so a footer quoted in the
// transcript above the input box cannot read as a live prompt.
func claudeLocalPermissionVisible(content string) bool {
	return footerVisibleInSegments(content, localPermissionFooterTokens)
}

// claudePasteCollapsed backs the claude adapter's PasteCollapsed: it reports whether the input-box
// readback is a "[Pasted text +N lines]" placeholder chip (see pasteChipRegex), which claude shows
// in place of a ≥4-line bracketed paste.
func claudePasteCollapsed(boxText string) bool {
	return pasteChipRegex.MatchString(boxText)
}

// Codex CLI (openai/codex, Rust TUI). Originally read off the repo at main (2026-06); every
// matcher below has since been driven against a live codex-cli 0.147.0 in an isolated tmux
// on 2026-08-09 (#510), at widths 120/60/40/28/24/20:
//
//   - BusyMarkers/MarkerWindow — "• Working (1s • esc to interrupt)" renders above the
//     composer as expected. Worth stating because a strings(1) probe of the 0.147.0 binary
//     does NOT contain "esc to interrupt" (the key label is interpolated at runtime), so the
//     binary reads as drifted and is not: absence in a binary is not evidence.
//   - InputBoxPrompts — "›" (U+203A), byte-verified with cat -A.
//   - Gates/GateWindow — the trust dialog, at every rung (codexTrustGateLadder).
//   - Prompts — the command-approval overlay, at every rung (codexApprovalLadder).
//   - ResumeProbe — `codex --help` lists "  resume          Resume a previous interactive
//     session", so the clap-listing needle still matches.
//   - No PasteCollapsed: codex renders a bracketed paste verbatim rather than collapsing it
//     into a chip the way claude does (codexNumberedListComposerPane120).
//
// That is the whole adapter, which is what VerifiedVersion claims — it was left empty
// through 0.147.0 only because no one had driven it.
var codex = &Adapter{
	Key:         KeyCodex,
	DisplayName: "Codex",
	aliases:     []string{"codex"},

	VerifiedVersion:  "0.147.0",
	DriftGranularity: GranularityMinor,

	BusyMarkers: []string{"esc to interrupt"},
	// The status row sits above the composer and its footer hints, outside the
	// below-the-box footer anchor; a window of 8 reaches over them.
	MarkerWindow: 8,

	// Codex draws its composer with "›" (U+203A), byte-verified with cat -A against a
	// live 0.147.0 pane. The default set never accepted it, so InputBoxVisible — and
	// therefore AwaitingInput, and therefore prompt delivery — was dead for every codex
	// session (#510). Replacing rather than extending the default is load-bearing here:
	// codex's own banner ("│ >_ OpenAI Codex (v0.147.0)") and its header ("> You are in
	// <dir>") both open with ">", and under the default set the banner is what the
	// readback actually returned on a 120-column pane — the composer's contents were
	// never read at all, they were the startup banner's.
	InputBoxPrompts: []string{"›"},

	Prompts: []PromptMatcher{
		// Decline options across the approval overlays: command/patch approvals
		// ("No, and tell Codex…"), permission and elicitation prompts ("No,
		// continue without…" / "No, but continue without it").
		//
		// NoAutoTap, pending a captured overlay (#347). Two independent reasons,
		// either of which is enough:
		//
		//   - The window is flat, so this matches the literals above wherever they
		//     land in the bottom 15 lines — including this file quoted in a
		//     session's own transcript, which is #343 verbatim with codex's name in
		//     it. Enter on a real overlay approves a shell command or a patch.
		//   - Position cannot rescue it the way it rescued claude. The overlay is
		//     built by approval_overlay.rs as title → body → options → footer hint,
		//     so the agent-controlled text (the command, the diff) renders BELOW the
		//     title rather than above it — the inverse of claude, where "the last
		//     question wins" made the dialog's own question unforgeable (#350). And
		//     codex echoes the command INTO an option label ("Yes, and don't ask
		//     again for commands that start with `…`"), so option text is
		//     agent-controlled too.
		//
		// Anchoring it needs a new primitive as well: the overlay draws no box
		// border at all, so footerBelowBox finds no anchor (and on a pane where the
		// agent printed its own rule, finds the wrong one). All of that has to be
		// designed off a captured pane, not off these labels.
		{Name: "approval", Window: WindowPrompt, NoAutoTap: true,
			Any: []string{
				"No, and tell Codex what to do differently",
				"No, continue without",
				"No, but continue without",
			}},
	},

	Gates: []Gate{
		// onboarding/trust_directory.rs: "Do you trust the contents of this
		// directory?" with "Yes, continue" pre-highlighted.
		{Name: "trust", Contains: []string{"Do you trust the contents of this directory"}},
	},
	// Codex WRAPS that body instead of truncating it (the opposite of agy, #512), so the
	// dialog's line count grows as the pane narrows while its text stays intact: 9
	// non-empty lines at 120 columns, 18 at 20 — past the default 15-line budget, which
	// drops the headline the gate is keyed on and makes GateUp miss a gate that is fully
	// on screen. 24 clears the widest driven rung with headroom; every rung of the
	// captured ladder is asserted, so a future codex that wraps wider fails loudly rather
	// than silently reopening the miss. This gate is one of the two guards keeping a
	// queued prompt off the trust screen (DetectPrompt is the other), and it has to be,
	// because the screen's "› 1. Yes, continue" selector is indistinguishable from a
	// composer holding a numbered-list prompt — see promptSet.
	GateWindow: 24,

	// `codex resume --last` continues the most recent session. The subcommand
	// must follow the binary, so resume is only applied to a bare program; a
	// program carrying flags relaunches blank rather than risk an argv the
	// resume subcommand rejects.
	//
	// DRIVEN 2026-08-18 on codex 0.147.0, in a directory with no conversation: it
	// starts normally into an empty composer and the pane survives. Its picker is
	// cwd-filtered — `codex resume --help` describes --all as the flag that "disables
	// cwd filtering" — so a fresh directory really does leave it nothing to resume.
	// Nothing in Atrium asks that question for codex (only claude has a transcript
	// adapter, and ResumeProbe below is a capability check), so this is a record of the
	// vendor's tolerance rather than of our own guard: re-check with
	// `just drive-agent resume codex` (#712).
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

// Gemini CLI (google-gemini/gemini-cli, React-Ink). The heuristic surfaces below sit at two
// different evidence tiers, and #713 is what the difference costs — say which is which
// rather than let one word cover both.
//
// DRIVEN at 0.55.1 — six surfaces, which is not the same claim as "every surface this adapter
// declares" and an earlier draft ran the two together. drift_fields_test.go's comment asked for
// four by name; #717 added the second Gate after that sentence was written, and #712 drove
// Resume after that. The one surface still undriven is below.
//
//   - the folder-trust Gate — width ladder in gemini_pane_test.go (#713), re-confirmed byte
//     for byte in #736's run;
//   - the IDE-integration Gate — gemini_ide_nudge_pane_test.go (#717);
//   - the tool-confirmation PromptMatcher — gemini_confirm_pane_test.go (#736);
//   - the busy marker — same file, same authenticated sessions, and what it found moved
//     MarkerWindow;
//   - generateNameGemini's `gemini -p` output contract (session/naming.go), probed in the
//     same run: bare text on stdout, warnings on stderr, no JSON envelope;
//   - Resume, in a directory with no conversation (#712) — the DRIVEN note beside the field
//     itself carries what was observed, and drive-agent.sh's RESUME_TABLE re-drives it, held
//     to the field by drive_agent_drift_test.go.
//
// NOT DRIVEN, and the reason this list stops short of "the whole adapter" — the phrase the
// codex adapter's header uses, counting its own ResumeProbe among the surfaces it claims.
// ResumeProbe is the one left, and it is a live heuristic: tmux runs
// binHelpContains(bin, a.ResumeProbe) and silently disables resume, with only an InfoLog line,
// when the needle is absent from `<bin> --help`. Neither #736 nor #712 probed it — #712 drove
// the flag Resume writes, which is a different question from whether the needle is still in
// `--help`. It was checked by hand at 0.55.1 — `gemini --help` still lists "-r, --resume" with
// "latest" — so nothing is broken today; what is wrong is a pin certifying a surface nobody
// drove, in the direction this header calls self-concealing. Splitting it out is #721's job.
//
// FIVE of those six are why VerifiedVersion moved to 0.55.1 below — every one but Resume,
// which was driven at the same version afterwards and moved no pin. internal/doctor's
// check_test.go cites that five by name, so the two counts differ on purpose rather than one
// of them being stale. They took a real turn per rung plus an isolated config dir to buy.
//
// ONE OF THOSE FIVE WAS VERIFIED AND BROKEN, and moving the pin is what forced fixing it here
// rather than later. generateNameGemini's output CONTRACT holds exactly as documented; its
// INVOCATION did not, because runGeminiHeadless runs `gemini -p` from a fresh os.MkdirTemp and
// 0.55.x refuses an untrusted cwd, so session naming was broken for every 0.55.x install
// (#744). The pin is why that could not be left standing: doctor reports drift only when
// installed > verified, so moving to 0.55.1 turns a 0.55.x user's "drifted" into "ok". The pin
// was never a naming check and its old amber said nothing about titles, but it was the only
// amber those users saw — taking it away while the break was live would have been a silent
// downgrade. runGeminiHeadless now sets GEMINI_CLI_TRUST_WORKSPACE=true, measured at 0.55.1 in
// the same drive.
//
// WHAT THE DRIVE COST AND WHY IT HAD NOT BEEN PAID. Reaching anything past the trust dialog
// needs an authenticated session, and drive-agent.sh drove against the real ~/.gemini, so a
// capture run could not answer a dialog without editing the developer's config.
// ATR_CAP_ENV (#736) is the opt-in that fixed it. Then oauth-personal turned out to be
// unusable: at 0.55.1 it returns IneligibleTierError ("This client is no longer supported for
// Gemini Code Assist for individuals"), so these panes were driven under gemini-api-key. An
// individual Google account cannot reach ANY of them, which is a fact about who this adapter
// still serves rather than about the captures.
//
// WHAT THE DRIVE FOUND, since "presence is necessary and NOT sufficient" was the standing
// worry and both halves of it landed:
//
//   - "No, suggest changes (esc)" is present in the bundle and UNREACHABLE below a 34-column
//     pane, because the option label truncates. The old flat matcher missed real dialogs at
//     every width Atrium's preview pane produces. See geminiConfirmationVisible.
//   - "esc to cancel" is present in the bundle and was reachable at only three of the seven
//     driven widths. The header used to reason that locating the render site in the bundle
//     "still does not say what a live pane puts on screen around it"; this is what it puts
//     there, and the two halves have different remedies. At 34 and 33 the row renders in full
//     and sat outside MarkerWindow, which is fixed: the constant moved 8 -> 9, the depth
//     measured at those two rungs. At 24 and 20 the row is ON SCREEN — deeper still, at 10 and
//     11 — but gemini has truncated the phrase itself to "(esc to c" and "(esc", so no window
//     reaches it and none would. TestGeminiBusyMarkerSitsAtTheEdgeOfItsWindow holds all seven
//     depths; geminiBusyTruncatedRungs holds the truncation.
//
// The asymmetry that kept the pin at 0.27 through six review rounds still holds and is worth
// keeping written down: a wrong "drifted" is noise, dismissed in a keystroke; a wrong "ok" is
// silent and self-concealing. What changes here is not the principle but the evidence — every
// surface the drive could reach now has a live capture. NOT every surface: ResumeProbe is
// still ungraded, exactly as the NOT DRIVEN list above says, so the pin does cover one thing
// nobody drove. An earlier draft wrote "there is no longer an ungraded surface", which reads
// as the opposite of that list two paragraphs up. Splitting the pin per-surface is what would
// actually close it, and that is still #721.
//
// The OLDER direction is silent by construction, and that is not specific to gemini: doctor
// reports drift only when installed > verified (internal/doctor/compare.go, driftExceeds), so
// every install below the pin reads "ok" whatever its panes render. Moving a pin forward
// therefore never warns the users it just stopped covering. The gate's shape makes that
// cheaper than it could be — it reads ONE literal, "Trust folder", which the 0.27 dialog
// rendered too, which is why the two-literal version of this fix was withdrawn — but it does
// NOT make an older install covered, and an earlier draft of this paragraph implied it did.
// The gate also requires a box, and whether 0.27 drew one is unknown: it was never driven, and
// the tree's only artifact of that shape is unboxed and does not gate. See
// geminiTrustGateVisible.
var gemini = &Adapter{
	Key:         KeyGemini,
	DisplayName: "Gemini CLI",
	aliases:     []string{"gemini"},

	// Moved 0.27 -> 0.55.1 by #736, which is the first change entitled to move it: the pin
	// is a claim about the WHOLE adapter, and #736 drove the surfaces #713 and #717 had left
	// ungraded (confirmation, busy) plus generateNameGemini's contract, in the same
	// authenticated sessions. Every HEURISTIC surface below now has a live 0.55.1 capture
	// behind it — not every surface: ResumeProbe has none, and the header's NOT DRIVEN list
	// is where that is argued. "Every surface below" was the earlier spelling and it made
	// this comment contradict that list. Nor is a capture per surface the same as one per
	// rendered variant: the confirmation was driven in its `exec` branch only, and
	// geminiConfirmationVisible says what that leaves grepped.
	// Minor granularity: the confirmation wording tracks minor releases; pure patch bumps
	// within a minor don't warrant a warning.
	VerifiedVersion:  "0.55.1",
	DriftGranularity: GranularityMinor,

	BusyMarkers: []string{"esc to cancel"},
	// Like codex, the loading row renders above the input box, so what decides whether the
	// window reaches it is how many rows everything below it occupies. TWO things grow, which
	// is the correction: an earlier draft credited the extra row to a footer wrap in five
	// places, and its replacement credited it entirely to the composer placeholder — both
	// under-count, and the second is false at 20.
	//
	// The workspace/branch footer really is two rows at every driven width, dropping columns
	// with an ellipsis rather than wrapping. The two that grow are the " Shift+Tab to accept
	// edits" hint and the " >   Type your message or @path/to/file" placeholder, and they grow
	// at different widths. The counts are read off the bytes rather than restated here, and
	// TestGeminiBusyDepthIsTheSumOfItsTwoGrowthSites computes the depth from them — so a new
	// rung cannot be added with a note that disagrees with its own bytes, which is how this
	// paragraph was wrong twice.
	//
	// 9 is the maximum across the rungs this can COVER, not across the driven set, and the
	// distinction is the whole reason 9 is safe to stop at. 24 and 20 sit deeper — 10 and 11 —
	// and are unreachable for an unrelated reason: gemini truncates the phrase off the row, so
	// widening to 11 would buy nothing. Every rung whose marker text survives is at 9 or less.
	// TestGeminiBusyMarkerSitsAtTheEdgeOfItsWindow holds all seven depths and the two
	// truncations, so a rung deeper than 9 that still renders the phrase reddens it.
	//
	// The cost side, which a review round deleted rather than replaced: MarkerWindow is a
	// live-detection budget for every gemini pane, not just a dialog's. Each row it grows is
	// one more row of transcript in which a quoted "esc to cancel" reads as streaming — Running
	// with no completion ding, and promptDeliveryReady holding the queued prompt until its
	// timeout. geminiIdlePane055's composer chrome is exactly 8 non-empty rows, so 9 is chrome
	// plus one transcript row: the budget is already spent.
	MarkerWindow: 9,

	Prompts: []PromptMatcher{
		// NoAutoTap: Enter on a real confirmation runs the shell command or writes the
		// file (#347). Autoyes users who want the taps have gemini's own --yolo /
		// --approval-mode.
		//
		// WHICH row Enter lands on is not fixed, and the variable case is the worse one.
		// initialIndex is 0 — "Allow once" — unless the folder is trusted, permanent
		// approval is allowed, the type is info/edit/mcp and security.autoAddToPolicyByDefault
		// is set, in which case gemini pre-highlights the row that writes the approval to
		// ~/.gemini/policies/auto-saved.toml. A tap there does not approve once; it makes
		// the approval permanent. Anywhere below that says "the highlighted default is
		// Allow once" is describing the default configuration only.
		//
		// Anchored on the dialog's box rather than on a bottom-N window (#736). This
		// entry used to be a flat All match on "No, suggest changes (esc)", excused on
		// the grounds that anchoring it "would take a primitive nothing else needs …
		// the confirmation renders INSIDE a rounded box with the app footer below it".
		// Driving it falsified the excuse in BOTH directions, which is why the fix is
		// not the one #736 proposes:
		//
		//   - There is no footer below it. gemini's Composer returns null while a tool
		//     confirmation is pending (ui.collapseDrawerDuringApproval, default true)
		//     and the Footer renders INSIDE that Composer, so the box's bottom border
		//     is the last non-empty line on all eight driven rungs. bottomBoxBlock
		//     anchors it with trailingBelowBoxCap unspent. (The clause that clause
		//     replaced — "aboveBoxBlock anchors on a composer the confirmation has
		//     replaced" — was correct, and #715 retracted it along with the wrong half.)
		//   - The literal is UNREACHABLE at the widths that matter. The option label
		//     renders wrap:"truncate" behind a 5-column row prefix, inside a box costing
		//     4 columns, at a width that is the pane's — so the label column is
		//     paneWidth-9 and a 25-cell literal needs a 34-column pane. Driven: present
		//     at 34, gone at 33, gone at 24 and 20. Atrium's agent pane is the PREVIEW
		//     pane, ~45 columns at a plain 70x24 terminal and narrower when the list is
		//     dragged wide, so keying on it misses real dialogs.
		//
		// That second half makes the old matcher a false NEGATIVE as well as the false
		// positive #736 reported, and the negative is the worse one: a missed
		// confirmation has no gate and no busy marker either — HasBusyMarker is false
		// here because showLoadingIndicator requires !hasPendingActionRequired — so the
		// row lands on PaneIdle → Ready → a completion ding, on a session blocked at a
		// shell approval. geminiConfirmationVisible has the evidence and the guards.
		{Name: "confirmation", NoAutoTap: true, Match: geminiConfirmationVisible},
	},

	Gates: []Gate{
		// The folder-trust dialog, keyed on its accept row inside a LIVE box — see
		// geminiTrustGateVisible for why neither a bare literal nor a conjunction of two
		// was enough. Driven live at 0.55.1 (#713); the ladder is geminiTrustGateLadder,
		// proven at widths 80/40/24 with a disclosed miss at 20.
		{Name: "trust", Match: geminiTrustGateVisible},
		// The IDE-integration nudge (#717). A second Gate rather than a second literal
		// because this one draws a composer glyph inside its own box, which is the exact
		// shape geminiTrustGateVisible rejects on — see geminiIdeNudgeVisible. Driven live
		// at 0.55.1; the ladder is geminiIdeNudgeLadder, and unlike the trust gate every
		// rung of it fires, 20 included.
		//
		// Both are Match, and that is load-bearing beyond either matcher: a Gate carrying
		// Contains would flatten at gateWindow() on every gemini poll, which nothing here
		// does today. GateWindow is inert only while that stays true.
		{Name: "ide-nudge", Match: geminiIdeNudgeVisible},
	},

	// DRIVEN 2026-08-18 on gemini 0.55.1, in a directory with no conversation: it
	// prints "No previous sessions found for this project." and carries on, pane alive.
	// The lookup is per-project by gemini's own account (--list-sessions is documented
	// as listing sessions "for the current project"), so the flag really is being
	// applied with nothing to resume. Nothing in Atrium asks that question for gemini —
	// ResumeProbe is a capability check, not an existence one — so this records the
	// vendor's tolerance: re-check with `just drive-agent resume gemini` (#712).
	Resume:        func(program string) string { return program + " --resume latest" },
	ResumeProbe:   "--resume",
	HeadlessNamer: true, // `gemini -p` prints bare text (session/naming.go)
}

// geminiTrustGateVisible reports gemini's folder-trust dialog. It requires the accept row to
// appear inside a box whose bottom border ends the pane or sits within trailingBelowBoxCap
// lines of the end, and both halves of that are load-bearing: the literal says which dialog,
// the box says it is live.
//
// What the dialog renders, read off the bundle that draws it (bundle/interactiveCli-*.js at
// 0.55.1): a title Text of "Do you trust the files in this folder?", the discovery blurb,
// then a RadioButtonSelect whose three labels are `Trust folder (${dirName})`, `Trust parent
// folder (${parentFolder})` and "Don't trust" — options LAST, which is half of why they are
// the anchor. The component was NOT removed: a draft of this comment said the
// FolderTrustDialog.js it used to name "is not in the package at all any more", which is
// false and sends a reader away from the code. 0.55.1 ships bundled, so the standalone .js
// file is gone, but the component is in the bundle and carries its own source marker,
// packages/cli/src/ui/components/FolderTrustDialog.tsx. The HEADLINE was reworded; nothing
// was deleted.
//
// The bundle-file COUNTS an earlier draft carried here ("4 files", "four of them") are gone
// on purpose. Nothing in the suite can recount them — the tests are hermetic and the package
// need not be installed — so they were prose that could only rot, and how many chunks a
// vendor splits its bundle into says nothing about Atrium. Presence is the claim; the version
// it was checked at is the datum, and that is VerifiedVersion.
//
// Why "Trust folder" alone identifies the dialog. The claim is a NEGATIVE, and stated as one
// so it can be rechecked with a single grep: no other string the 0.55.1 TUI renders as a label
// contains "Trust folder". In particular it does NOT match `Trust this folder (${dirName})`,
// which is /permissions' modify-trust dialog — a mid-session settings screen that must not
// report as a startup gate — because "this " sits between the two words.
//
// An earlier draft made that point by ENUMERATING every `Trust `-prefixed label in the package
// and asserting the list was complete. It was not: the package also ships "Trust the server
// (bypass all tool call confirmation prompts)", "Trust all provided hooks for a project",
// "Trust Level", "Trust Services", "Trust Tokens", "Trust CMP" and more. None of them changes
// the conclusion, which is the point — the enumeration was doing no work and could only rot,
// exactly like the bundle-file counts removed two paragraphs above.
//
// The one true exception is worth naming because it looks like a counter-example and is not:
// bundle/docs/cli/trusted-folders.md documents the option as `- **Trust folder**: Grants full
// trust to the current folder`. It is shipped documentation, never drawn in the TUI, and it
// could only reach this matcher by an agent printing the file into its own pane — the quoted-
// prose class the box requirement exists for. So the literal needs no second literal to
// disambiguate it, and asking for one costs more than it buys (below).
//
// Why the box, and why the box must END the pane. A literal alone cannot say the dialog is
// on screen, and this gate has three distinct ways to be wrong about that. All three were
// measured, and each has a named guard:
//
//   - PROSE. A working pane whose transcript quotes the row — this repo's own source, a
//     summary of this file, a review comment — matched when the gate was a flat bottom-N
//     window. Measured: GateUp true AND HasBusyMarker true on the same pane, and poll.go
//     ranks the gate above the busy marker on purpose ("a false gate beats positive proof
//     of work"), so the row reported needs-input while the agent streamed and its queued
//     prompt was withheld. That is #342's direction, and it is strictly worse than the
//     rotted literal #713 replaced, which was FALSE on that pane.
//     TestGeminiTrustGateIgnoresItsOwnRowsInProse. This class is NARROWED, not closed: prose
//     cannot reach the block, but quoted BOX ART carries walls and a corner border like any
//     real box, so a transcript that quotes a dialog and ends at the quoted border does gate.
//     Measured, and pinned as behaviour rather than left in a sentence —
//     TestGeminiTrustGateFiresOnQuotedBoxArtEndingThePane. The residue is transient (one more
//     rendered line drops it) where the window form was persistent.
//   - WRAP SYNTHESIS. flattenChrome joins lines with a space, so a transcript reading
//     "Never Trust" / "folder contents from an untrusted source" flattens to a string
//     containing "Trust folder" and fired the gate — on a pane where no line renders the
//     phrase at all. bottomBoxBlock returns its interior UNFLATTENED for this reason.
//     TestGeminiTrustGateIsNotSynthesizedAcrossAWrap.
//   - THE DISMISSED DIALOG. A window-only gate keeps matching a dialog that is still on
//     screen but no longer live, and while a gate is up AwaitingInput (tmux.go) is false, so
//     the queued FIRST prompt — the moment a queued prompt is most likely to exist — is never
//     delivered. Requiring the border to be at the bottom drops the gate once anything taller
//     than trailingBelowBoxCap renders beneath it — a composer, being a box, always is.
//     TestGeminiTrustGateDropsOnceSomethingRendersBelowIt. Note
//     what is and is not established here: that gemini LEAVES the answered dialog on screen is
//     NOT verified — driving it costs an Enter at a dialog whose acceptance writes
//     ~/.gemini/trustedFolders.json, and the capture harness deliberately does not isolate the
//     agent's config dir. The anchor is worth having either way, because it costs nothing and
//     does not depend on the answer; agy pins the same property with agyAcceptedGatePane and
//     earns it from a captured post-acceptance pane, which is the stronger evidence tier.
//
// The bottom border is the last non-empty line on all four WIDTH rungs (each ends "╰──…──╯"),
// including the width-20 miss — but that is a property of those captures, not of the dialog,
// and an earlier draft of this paragraph mistook the one for the other. It said the fact to
// re-measure "if this ever goes quiet" was "a future gemini that renders a footer beneath the
// dialog", and offered the resulting missed gate as the fail-safe direction.
//
// Both halves were wrong. gemini 0.55.1 ALREADY renders a footer beneath the dialog: whenever
// the box overflows its pane it draws a one-line <ShowMoreLines> hint under the bottom border.
// And a missed gate is not the fail-safe direction here — it is #713 itself, the bug this
// matcher exists for. Driven at twelve geometries, the border-must-end-the-pane form missed
// seven, including the 45x19 pane a plain 70x24 terminal produces at the default split. So the
// anchor allows trailingBelowBoxCap trailing lines, and the rungs that prove it are
// geminiTrustGateOverflowLadder. What no fixture can re-measure is the vendor: these are frozen
// strings, so only re-driving the harness sees the next render change, which is what
// VerifiedVersion and doctor's drift check are for.
//
// What the block is bounded BY matters as much as what anchors it, and the first draft of
// this fix got it wrong in both directions at once: it scanned upward for a matching top
// border. That demands the WHOLE dialog be on screen, and the agent's pane is the preview
// pane rather than the terminal (session/instance.go SetPreviewSize), so on a pane shorter
// than the dialog BOX — 28 rows at width 24, 33 at width 20 (geminiDialogRows; this sentence
// used to give 37, which is the width-24 capture's height and overstates the threshold by nine
// rows, and those figures are themselves height-40 measurements, since the box shrinks with the
// pane) — the top border has scrolled off and the gate went down. #713's own symptom on the
// height axis, at ordinary terminal sizes. It was also
// unbounded the other way: any two rules with transcript between them made that span
// "interior". bottomBoxBlock now takes the run of side-walled rows above the border instead,
// which is height-independent and self-bounding; see its doc for the measurements, and
// TestGeminiTrustGateSurvivesADialogTallerThanThePane /
// TestGeminiTrustGateIgnoresTranscriptBetweenTwoRules for the guards.
//
// Capture form is a real exposure here, and it is not the form the panes below are in. They
// were taken with `capture-pane -p`; production reads `-p -e -J` (session/tmux/tmux.go) and
// strips CSI with ansiRegex (session/tmux/poll.go), which does not strip OSC. isHorizontalRule
// rejects a line holding any character outside the box set, so one surviving escape ON THE
// BORDER takes the whole gate down, where the old literal-in-a-window form would at worst have
// lost the lines it landed on. That is a sharper failure than the anchor it replaced, so it
// was measured rather than assumed: a prod-form pane driven at 0.55.1 carries 152 escape
// sequences, every one CSI SGR and none OSC, with SGR mid-line on both load-bearing rows —
// and cleanForDetection hands the matcher something it gates on.
// session/tmux's TestGeminiTrustGateSurvivesProductionCaptureForm holds the capture and
// asserts both directions, because the two halves live in different packages: the fixture
// production reads here, the cleaner that feeds it there.
//
// What was tried in between, recorded because the trade is not obvious. Round 1 of #713 keyed
// on Contains{"Trust folder", "Don't trust"}; Contains is an ALTERNATION, and "Don't trust" is
// ordinary English, so it fired on prose containing only the second. Round 2 made it a Match
// requiring BOTH — which narrowed the prose window without closing it (a pane quoting both
// rows within fifteen lines still matched) and left the other two classes untouched, because
// all three are liveness failures and a conjunction of literals is not a liveness test. It
// also cost coverage no one asked for: the dialog gemini shipped at 0.27 had "Trust folder"
// but the tree's only fixture of it carried no "Don't trust" row, so requiring both would
// have taken the gate away from installs older than the pin while doctor stayed silent about
// it (driftExceeds only reports installed > verified).
//
// That artifact is geminiPre055TrustGatePane, and it is named here because for one round it
// was not in the tree at all: #713 replaced the fixture these two paragraphs cite with a
// 0.55.1-shaped boxed pane, leaving the reasoning pointing at nothing. What it is worth is
// bounded — it is hand-composed, 0.27 has never been driven, and it is evidence of what this
// repo once claimed the dialog looked like, not of what gemini rendered.
//
// The argument above is about the LITERAL only, and the older-install case is not thereby
// covered — an earlier draft of this paragraph closed by claiming one literal plus a structural
// anchor "makes the gate work on both dialog shapes at once", which nothing here establishes.
// The anchor is a second requirement, and whether 0.27 met it is unknown: fed to this matcher
// the recorded shape returns FALSE. So what dropping the second literal buys is the removal of
// one of two ways to miss on an older install, not coverage of it. If a pre-0.55 user reports
// the #713 symptom, that is the thing to drive, and doctor will not have said a word (same
// direction: installed < verified is not drift, and no adapter carries a version FLOOR the way
// internal/doctor's dependency specs do — #722).
//
// Keeping the 0.27 headline as a SECOND Gate is the obvious mitigation and it does not work.
// Anchored, it is redundant: if 0.27 drew a box then "Trust folder" is already inside it and
// the shipped matcher covers that install unaided; if it did not, no anchored literal reaches
// it either — both miss for the same reason. So the second Gate helps only UNANCHORED, and
// unanchored it reopens the PROSE class above for every 0.55+ user. Both halves are measured
// in TestGeminiPre055ShapeIsUncoveredAndTheSecondLiteralWouldNotCoverIt, the second on a
// working pane (busy marker present) that merely quotes #713's own text — which is the
// literal's likeliest appearance on a current user's screen, an agent reading this tracker.
// geminiProsePane could not have shown it: that fixture quotes the option ROWS.
//
// Still uncovered, and disclosed:
//
//   - The startup AUTHENTICATION dialog. gemini ships an AuthDialog asking "How would you like
//     to authenticate for this project?" and a "Do you want to continue?" confirmation, both
//     boxed RadioButtonSelects like the trust dialog. Gates holds exactly one matcher, keyed on
//     the trust dialog's accept row, and no Prompt matches these — so nothing here covers a
//     second keystroke-consuming startup screen. Whether it lands on Ready (the #713 chain) is
//     NOT established: it turns on the busy marker, and "esc to cancel" is matched
//     case-sensitively while the bundle ships both that spelling and "Esc to cancel". Unlike
//     /permissions below, this one is cheap to settle — it IS the auth screen, so reaching it
//     needs no authenticated session, only a driven run whose HOME is isolated (answering the
//     trust dialog to get past it writes ~/.gemini/trustedFolders.json, which is why
//     drive-agent.sh, which does not isolate the agent's config dir, has not been pointed at it).
//   - /permissions' modify-trust dialog, which does need an authenticated session.
//
// IdeIntegrationNudge was the third entry on this list and is now COVERED, by a second Gate of
// its own (geminiIdeNudgeVisible, #717). It is not covered by this matcher and could not be:
// it renders its headline behind a "> " Text inside the same rounded box, so the composer
// rejection two lines below returns false on it by design. That glyph is also why it was the
// worse of the two — InputBoxVisible is TRUE on it, so AwaitingInput was true and a queued
// prompt was typed into a RadioButtonSelect whose highlighted default is "Yes", the #512 class
// rather than this gate's milder one.
//
// Width. The headline is UNREPAIRABLE as an anchor once it wraps: gemini draws the dialog in
// a rounded box, so a wrapped headline has the box's own "│" between its halves, and
// flattenChrome joins on whitespace only. Measured on residue-free captures, it is
// unreachable at 40, 24 and 20 at every GateWindow up to 200 — more lines than any of those
// panes has. Codex wraps WITHOUT a box, which is why widening its window worked there and why
// #713's guess that the same "would also work at width 40" does not hold here.
// TestGeminiTrustGateHeadlineIsUnreachableOnceItWraps pins it.
//
// The option rows truncate from the right rather than wrapping, so "Trust folder" survives as
// a left-anchored prefix however long the directory name is — until the row itself is cut,
// which happens between 24 and 20. 24 is therefore a MEASURED floor: at 20 the rows read
// "● 1. Trust fo…" and "3. Don't tr…" and the gate misses. That is not parity with agy, whose
// gate also stops at 24 — agy's 24 is an evidence gap (its gate has never been driven
// narrower, though its busy and confirmation ladders reach 20), not a measured cliff. And the
// miss is not free: at width 20 the user loses the "waiting on setup screen" hint and gains a
// false completion ding, which is #713 itself surviving at one width.
// TestGeminiTrustGateOptionRowsAreTruncatedAtWidth20 holds it so it stays disclosed.
//
// The composer is a box too, which is the one hole "bottom-most box" does not close on its
// own. Measured: a pane ending at a composer border whose typed text reads "Trust folder"
// raises the gate — and InputBoxVisible is true on it, so the effect is the #342 direction
// again (AwaitingInput false, queued prompt withheld) with the user's own keystrokes as the
// trigger. This paragraph used to add that "gemini's real render puts a footer line below the
// composer (geminiIdlePane), which is why this is narrow rather than routine" — retracted on
// both counts. geminiIdlePane is hand-composed from 0.27 package source and its own doc says it
// is not evidence for a matcher, so it never established what gemini renders; and a lone footer
// line is inside trailingBelowBoxCap now, so it would not put the composer out of reach even if
// it were real. The glyph check below is the only thing that closes it, and it closes it only
// while the glyph is ON SCREEN — which is a bound, not a quibble, because the pane here is the
// PREVIEW pane and it is short: 19 rows at a plain 70x24 terminal, per geminiOverflowPaneHeights.
// A composer taller than that scrolls its "> " row off the top, leaving walled continuation rows
// under a bottom border, and isInputBoxLine has nothing left to match. Measured: a two-row
// continuation whose text carries "Trust folder", a border, and a footer row gives GateUp true
// with InputBoxVisible FALSE — needs-input suppressed, the queued prompt withheld, the row
// reporting "waiting on setup screen" while the user is typing. That is the #342 direction with
// the user's own keystrokes as the trigger, and it is DISCLOSED rather than closed:
// TestGeminiTrustGateFiresOnAComposerTallerThanThePane pins it so it cannot rot into a surprise.
//
// Closing it means demanding the MENU shape — "Trust folder" preceded by a list index, which
// BaseSelectionList renders as "N." — and that was rejected on #713's own evidence: the rows
// gemini renumbers or restyles are exactly the ones a narrow pane truncates, and every literal
// this gate has lost was lost by being more specific than it needed to be. The trigger needs a
// paste taller than the pane that quotes "Trust folder"; the miss it would risk needs only a
// vendor bump. So a
// block that reads as a composer is rejected: the trust dialog is a MENU, its rows open with
// "●" and a number, and no rung renders a composer glyph anywhere inside the dialog —
// TestGeminiCapturesRenderNoComposerGlyphInsideTheDialog, which scans the whole block. It
// cannot be TestGeminiTrustGateIsNeitherComposerNorPrompt, which this comment used to cite:
// that one calls InputBoxVisible, and InputBoxVisible reads the last WindowPrompt (15)
// non-empty lines, while the block it must speak for is deeper than that on the narrow rungs
// — the guard measures how much deeper rather than saying so here. It would stay green with a
// glyph in the upper half of the dialog while the gate went down.
//
// defaultPrompts rather than the adapter's own set, because gemini declares no InputBoxPrompts
// and a package-level func cannot reference `gemini` without an initialization cycle. That
// substitution is pinned by TestGeminiUsesTheDefaultComposerGlyphs, so an adapter that later
// gains a custom glyph fails there instead of silently reopening this hole.
//
// No GateWindow is consulted at all, and the reason is narrower than an earlier draft claimed.
// It said "GateUp short-circuits on Match before it flattens anything, so an adapter-level
// GateWindow would not reach this and setting one would be inert" — false as a general
// statement about GateUp. A Match returning FALSE falls through to `continue` (agent.go), and
// the next Gate carrying Contains flattens at gateWindow() as usual; measured with a
// two-gate adapter whose first Match always returns false, which flattens and gates. What is
// actually true is that no gemini Gate carries Contains, so nothing ever reaches the flatten.
// That used to be phrased as "gemini declares exactly ONE Gate today", which #717 made false —
// the IDE nudge is a second — while leaving the conclusion intact, because it is Match too. A
// Gate carrying Contains, whenever one is added (the startup auth dialog disclosed above is the
// obvious candidate), silently reinstates a flatten on every gemini poll, and GateWindow stops
// being inert with it. The window this gate has is the box.
func geminiTrustGateVisible(content string) bool {
	block, ok := bottomBoxBlock(content)
	if !ok {
		return false
	}
	found := false
	for _, line := range block {
		if isInputBoxLine(line, defaultPrompts) {
			return false // a composer, not the dialog
		}
		if strings.Contains(line, "Trust folder") {
			found = true
		}
	}
	return found
}

// geminiAllowRow and geminiCancelRow are the two option labels geminiConfirmationVisible keys
// on. They are constants rather than literals in the predicate so the guards can measure
// against the symbol the matcher reads: the floor is a property of geminiAllowRow's LENGTH — a
// 19-column pane, per the header below — and a test restating that number instead of reading
// this symbol is constant-vs-constant and cannot fail.
//
// THE TWO ARE NOT GUARDED ALIKE, and an earlier draft of this comment claimed they were.
// Shortening geminiAllowRow moves the floor and reddens
// TestGeminiConfirmationFloorIsSetByTheAllowRow. Shortening geminiCancelRow reddens NOTHING:
// a conjunction's floor is its widest term, so the cancel prefix does not bind it, and a
// shorter prefix still finds its row at every driven rung. Set it to "No," and the package
// stays green while the conjunction widens to fire on any bottom-most box containing "No,".
//
// So its length is a judgement, not a measurement, and it is the one thing here a guard cannot
// hold. What is asserted instead is that it stays a PREFIX of shippedCancelLabel, which catches
// the other drift — a term gemini never renders.
const (
	geminiAllowRow  = "Allow once"
	geminiCancelRow = "No, sug"
)

// geminiConfirmationVisible reports gemini's tool-confirmation dialog (#736): the first and
// last option rows of ToolConfirmationQueue's RadioButtonSelect, both inside a box whose
// bottom border ends the pane. Two clauses — the box says the dialog is LIVE, the pair says
// WHICH dialog — and both carry every driven rung. There used to be a third, a composer veto;
// what it cost and why it went is spelled out further down, along with the two things these
// two clauses do not close.
//
// WHY NOT "No, suggest changes (esc)", the literal #736 proposes keeping. It is 25 cells and
// the label column is paneWidth-9, so it needs a 34-column pane; driven, it is present at 34
// and gone at 33, 24 and 20. "Allow once" is 10 cells and survives every driven width.
// TestGeminiConfirmationCancelRowTruncatesBelowWidth34 holds the measurement so the issue's
// proposal cannot be re-adopted by re-reading the issue.
//
// A CONJUNCTION, through Match rather than All. "Allow once" alone is ordinary English and
// "No, sug" alone is a fragment; either would fire on prose. Every literal this adapter has
// lost, it lost by being more specific than it needed to be.
//
// WHICH HALF BINDS, since a conjunction's floor is its widest term and an earlier draft of
// this comment reasoned the headroom off the wrong one. The cancel prefix needs 7 of the label
// column's cells and clears the narrowest driven rung with room — true, and irrelevant.
// "Allow once" needs 10, so the pair needs a label column of 10 and the matcher's floor is a
// 19-COLUMN PANE. At the narrowest rung driven, width 20, the label column is 11 and that row
// renders "Allow once" with one cell spare while the cancel row is already elided to
// "No, sugges…". One column, not four, and pane_width_test.go's header says the preview width
// is not clamped to any minimum. TestGeminiConfirmationFloorIsSetByTheAllowRow pins it.
//
// ONE BRANCH DRIVEN, four grepped, and that gap is the one #713 charges for. Every rung of
// geminiConfirmLadder is an `exec` confirmation (`rm -f README.md`). "Allow once" leads the
// option list in all five option-bearing branches of getOptions — edit, sandbox_expansion,
// exec, info, mcp — and so does the cancel row: getOptions ends each of those five branches
// with the same options2.push carrying the identical "No, suggest changes (esc)" label, so
// BOTH halves of the conjunction are branch-invariant, not just the one that is easier to
// reason about — with one exception inside edit that the NOT COVERED note below carries, since
// edit's pushes are the only conditional ones. That is read off the 0.55.1 bundle, and this
// file's standing rule is that bundle presence is necessary and NOT sufficient. If edit
// (which renders a diff above its options) or any of the other three renumbers, rewords or
// re-nests EITHER row — a conjunction is only as reachable as its rarer term — this misses
// there and nothing here would notice.
//
// The block is read LINE-WISE and unflattened, so two adjacent wrapped rows cannot synthesise
// a phrase neither renders — the trap flattenChrome sprang on the trust gate (#713).
//
// THE CONFIGURATION THIS CANNOT SEE. ui.collapseDrawerDuringApproval defaults true, which is
// what makes the box clause work at all: with the drawer collapsed, a live dialog's bottom
// border ends the pane. It is a documented settings.json key that the in-app settings dialog
// does not offer, so setting it false is a hand edit — and then gemini renders the composer
// AND footer below a LIVE dialog. bottomBoxBlock anchors on the composer, so this returns
// false, and InputBoxVisible answers TRUE on that same composer.
//
// Session.AwaitingInput is `!GateUp && !DetectPrompt && InputBoxVisible` over a live capture
// — three terms, and HasBusyMarker is not one of them. That matters for whoever picks #746
// up: the busy state does gate delivery, but downstream in promptDeliveryReady's state
// argument, behind a timeout that expires. Fixing this in BusyMarkers or MarkerWindow would
// redden nothing and close nothing. All three of AwaitingInput's terms fall the wrong way
// here, so Atrium sends the queued first prompt at a pane holding an unanswered approval.
//
// What gemini then does with those keystrokes is NOT stated here, because it was not driven.
// The bundle has the composer subscribed and isInputActive true during
// "waiting_for_confirmation", which points at the composer absorbing them; the flat literal
// this replaced matched this pane, so #746 is a regression either way. Reaching the answer
// costs a hand-edited setting and an API turn, and a guess would be the third wrong sentence
// this file has written about a consequence it could not measure.
//
// FIXABLE, and not fixed here — the difference matters, because an earlier draft of this
// paragraph said no predicate could fix it and that was false. Measured: a flat conjunction of
// both labels over flattenChrome(content, WindowPrompt) is TRUE on this pane and FALSE on
// every driven negative in the tree — the dismissed capture, the 0.55.1 idle composer and the
// quoting pane. The premise that ruled it out was wrong too: an answered dialog does not
// linger looking live. geminiConfirmDismissedPane, a driven capture, holds ZERO occurrences of
// either label — gemini replaces the whole dialog with a tool-RESULT box.
//
// What it costs is why it is #746's call and not this change's: a flat clause fires on any
// pane carrying both labels within WindowPrompt lines, and both are verbatim in this file. It
// re-opens a narrowed form of the very class #736 reported, trading it for a configuration
// nobody has driven. The measurement is recorded on the issue so that trade is made on
// numbers. TestGeminiConfirmationMissesWhenTheDrawerStaysOpen pins the miss meanwhile.
//
// WHAT THE BOX CLAUSE NARROWS AND DOES NOT CLOSE. It is not a guarantee that the block is a
// dialog, only that it is the pane's last box: any bottom-most box carrying both rows fires,
// including one gemini draws itself around a tool RESULT. That is the residual of the very
// class #736 reported, and it is reachable in this repo specifically — both literals are now
// verbatim in this file and in gemini_confirm_pane_test.go, so an agent that cats either into
// a tool-output box ending the pane reproduces it. Same disclosure bottomBoxBlock already
// carries for quoted box art on the trust gate, and pinned the same way, by
// TestGeminiConfirmationStillFiresOnAQuotedDialogEndingThePane. Narrowing it further would
// take a header literal, and the headers differ per branch — four of which are undriven.
//
// THERE IS NO COMPOSER VETO HERE, and its removal is the point rather than an omission. The
// loop used to reject any block line isInputBoxLine matched, to keep the matcher off a 0.27
// composer — which WAS a walled box. 0.55.1 draws the composer unwalled (block glyphs, not box
// rules), so it can never enter a bottomBoxBlock and the clause could not fire on a 0.55.1
// pane at all. What it could still do was MISS: one row inside the dialog's own outer box that
// survives the single-"│" trim and starts with "> " took the whole match down.
//
// That miss is worse than a miss. isInputBoxLine with defaultPrompts is the SAME predicate
// InputBoxVisible anchors on, and gemini declares no InputBoxPrompts of its own, so the one
// row that vetoed the match also answered InputBoxVisible TRUE. WITH THE VETO IN PLACE that
// pane measured DetectPrompt false, GateUp false, InputBoxVisible true — Session.AwaitingInput
// exactly, on a live dialog. This file already calls that mechanism the worse of the two for
// the IDE nudge, and made it #717.
//
// Read that as the counterfactual it is. TestGeminiConfirmationFiresOnADialogRowThatLooksLikeAComposer
// pins the state AFTER the removal — it asserts DetectPrompt TRUE and the conjunction FALSE,
// and restoring the veto is what reddens it. An earlier draft cited it for the three verdicts
// above without marking the tense, so a reader following the citation found the named test
// disproving two of them.
//
// WHAT GETS TYPED THERE IS NOT DESCRIBED, in the default configuration any more than in the
// drawer-open one. "No composer is rendered" is not "nothing absorbs the keystrokes", and an
// earlier draft slid between the two: the bundle has isInputActive true during
// "waiting_for_confirmation", which is a fact about the state machine rather than about
// whether the drawer is drawn, so it applies here as well and points the other way. That is
// the third attempt at this one sentence, so per CLAUDE.md the claim is deleted rather than
// restated a third time. What is measured is the AwaitingInput conjunction, and that is all
// this argues from.
//
// AND IT IS STILL THERE NEXT DOOR. geminiTrustGateVisible carries the identical clause, and
// the argument above transfers to it verbatim — with a worse consequence, since Enter on that
// dialog writes ~/.gemini/trustedFolders.json permanently, granting the trust runGeminiHeadless
// spends twenty lines describing. It is not deleted here because reachability differs by what
// each dialog renders, and that is MEASURED rather than reasoned about:
// TestGeminiTrustGateCapturesRenderNoComposerGlyphInsideTheDialog walks every driven trust
// capture — four widths plus both overflow rungs — and the veto fires on none of them. So
// GateUp is true on every trust dialog this repo has bytes for, and AwaitingInput is false;
// the unattended-approval path is not reachable there today. It is reachable on a confirmation
// because that dialog renders tool ARGUMENTS, where a row beginning "> " is ordinary.
//
// That guard is what makes deferring this safe rather than merely stated: the day a trust
// capture carries such a row, it reddens. #757 carries the fix, including the option neither matcher has taken: vetoing only at
// block index 0 keeps the 0.27 rejection and fixes the miss, because the walled composer's box
// has its input-box row at index 0 while a dialog's quoted row sits below the tool header at
// index 1. Every driven rung here has that header first — all seven are the `exec` branch, so
// whether `edit`'s diff leaves index 0 alone is not among the things this file has measured.
// footerVisibleInSegments is the in-tree precedent for the shape.
//
// Deleting it trades an unsafe direction for a safe one. What comes back is a 0.27-shaped
// boxed composer quoting both labels now matching — an over-fire, which is NoAutoTap ->
// PanePromptManual -> NeedsInput with the prompt withheld, the direction #342 named acceptable
// and the one this package prefers. That over-fire is asserted, not assumed, by
// TestGeminiConfirmationOverFiresOnA027BoxedComposer.
//
// NOT COVERED, and disclosed rather than quietly half-covered: three confirmation states
// render no option row at all, so no literal from getOptions can reach them. ask_user and
// exit_plan_mode return options: [] outright. The third sits INSIDE the edit branch this
// comment's branch-invariance claim covers — edit is the only one of the five whose pushes are
// conditional, on `if (!confirmationDetails.isModifying)`, and choosing edit's own "Modify
// with external editor" option sets it: getOptions returns [] and the renderer draws a
// bordered box reading "Modify in progress: " with no RadioButtonSelect. So "all five branches
// push both labels" is true of the code path getOptions takes, and conditionally four of the
// dialog a user can be sitting in front of. Their headers are distinctive but sit at the TOP
// of a box that can outgrow a 19-row preview pane, which is the shape this package has twice
// found unusable, and reaching one costs an API turn per state. Tracked as #753.
func geminiConfirmationVisible(content string) bool {
	block, ok := bottomBoxBlock(content)
	if !ok {
		return false
	}
	var allow, cancel bool
	for _, line := range block {
		if strings.Contains(line, geminiAllowRow) {
			allow = true
		}
		if strings.Contains(line, geminiCancelRow) {
			cancel = true
		}
	}
	return allow && cancel
}

// geminiIdeNudgeVisible reports gemini's IDE-integration nudge (#717): the once-per-machine
// dialog asking whether to connect the detected editor, whose highlighted default installs an
// extension.
//
// It is a SEPARATE Gate from the trust dialog rather than another literal in that one, and the
// reason is the single fact this whole matcher is built around: the nudge renders its headline
// behind a "> " Text inside the same rounded box, so isInputBoxLine fires on it and
// InputBoxVisible is TRUE. geminiTrustGateVisible answers false the moment it sees a composer
// glyph in the block — correctly, for a dialog that has none — so folding the nudge into it
// would require deleting the guard that keeps the trust gate off a live composer.
//
// That glyph is also what made this worth an issue of its own rather than a second instance of
// #713. AwaitingInput() is `!GateUp && !DetectPrompt && InputBoxVisible` (session), so before
// this Gate existed the nudge read as a session waiting at its composer: Atrium delivered the
// queued initial prompt as keystrokes into a RadioButtonSelect whose highlighted row is option
// 1, "Yes". `atrium new --prompt` makes that unattended. AwaitingInput()'s own docstring already
// says the box check cannot exclude a menu-style gate and that GateUp must — this is GateUp
// doing it. TestGeminiIdeNudgeIsAGateOverAVisibleInputBox asserts both halves, InputBoxVisible
// true and the gate up, so a later "fix" of the box reading fails there instead of silently
// re-arming the delivery.
//
// Reachable in Atrium, not just in a terminal a developer typed in: detectIde returns nothing
// unless TERM_PROGRAM is "vscode"/"sublime"/"Zed", ZED_SESSION_ID or XCODE_VERSION_ACTUAL is
// set, or isJetBrains() holds — and agents inherit the tmux server's environment as captured at
// server start, so launching Atrium from an IDE's integrated terminal propagates the marker into
// every agent pane. shouldShowIdePrompt then needs only a first run with ide.hasSeenNudge unset.
// It is checked BEFORE isFolderTrustDialogOpen in the vendor's own dialog chain, so on a fresh
// path with an IDE detected this is the screen that renders, not #713's.
//
// The literals, and why they are not the ones #717 proposed. Driven natively at 0.55.1 on
// 2026-08-17 at widths 80/40/24/20 (geminiIdeNudgeLadder), each rung a fresh session rather than
// a resize, because gemini's dialogs are the measured counterexample to resize-equals-native
// (#713, and drive-agent.sh's COST-SAVER limit). The issue proposed keying on
// "No, don't ask again" plus one other row. Measured, that literal does not survive the ladder:
// the dismiss row TRUNCATES from the right, reading "3. No, don't ask …" at 24 and
// "3. No, don't …" at 20, so a gate keyed on it would have missed at exactly the widths Atrium's
// preview pane actually produces — the same shape of mistake #713 was, arrived at from a wide
// capture. TestGeminiIdeNudgeDismissRowTruncatesBelowWidth40 pins the measurement so the
// proposal cannot be re-adopted from the issue text.
//
// What survives all four rungs is "No (esc)" and the dismiss row's "No, don't" prefix, and both
// are required — a conjunction through Match, never Contains, which is an ALTERNATION and would
// make either alone sufficient. That distinction is not theoretical here: #715 round 1 shipped
// exactly that mistake on the sibling gate and had to be re-cut.
//
// "No (esc)" is the load-bearing half and it is measured, not assumed: it appears twice in each
// interactiveCli bundle file (as the option's label and its key), both inside
// IdeIntegrationNudge, and nowhere else — so it names this dialog rather than a family. The
// headline is not used at all, for #713's reason unchanged: gemini draws the dialog in a box, a
// wrapped headline has the box's own "│" between its halves, and flattenChrome joins on
// whitespace only. It wraps at 40. TestGeminiIdeNudgeHeadlineWrapsInsideTheBox pins that.
//
// Every rung FIRES, including 20 — which the trust gate's ladder cannot say, its option rows
// being cut by the directory name interpolated into them. The nudge's rows carry no
// interpolation, so the gate has no measured floor within the widths Atrium can produce.
//
// The residue, disclosed rather than closed: the composer is a box too, so a user who pastes
// text containing both literals into their composer raises this gate. The trust gate closes its
// version of that hole with the isInputBoxLine rejection, which this matcher cannot use — the
// glyph is the dialog's own. The direction is the harmless one (#342): GateUp true makes
// AwaitingInput false, so the queued prompt is WITHHELD and the row reads "waiting on setup
// screen" until the paste clears. Withholding a prompt is recoverable; typing it into a menu
// whose default installs an extension is not, and that asymmetry is why the anchor stays where
// it is. TestGeminiIdeNudgeFiresOnAComposerQuotingBothRows holds it visible.
//
// Match rather than Contains for a second reason too. gemini declared exactly one Gate until
// now, and geminiTrustGateVisible's comment turns on that: a Gate carrying Contains flattens at
// gateWindow() on every poll, and neither of gemini's two does, so the flatten stays
// unreachable and GateWindow stays inert. A third Gate with Contains changes that.
func geminiIdeNudgeVisible(content string) bool {
	block, ok := openBottomBoxBlock(content)
	if !ok {
		return false
	}
	var esc, dismiss bool
	for _, line := range block {
		if strings.Contains(line, "No (esc)") {
			esc = true
		}
		if strings.Contains(line, "No, don't") {
			dismiss = true
		}
	}
	return esc && dismiss
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
		{Name: "docs", Contains: []string{"Open documentation url for more info"}},
	},
}

// Antigravity (agy). Every string below was driven against a live agy 1.1.11 in an
// isolated tmux on 2026-08-09 (#512), down to width 20. Which widths are EVIDENCE is a
// different question from which were driven, and it is not answerable from here: only a
// KEPT capture can falsify a matcher. The ones each matcher must FIRE on live in
// paneCoverage (pane_width_test.go), where a test reads their widths; that table is
// positive-only, so the panes agy must NOT match — the answered confirmation, the
// accepted gate, the slash menu, the idle composer — are guarded in registry_test.go
// instead. Ask those, not this sentence: a width ladder recited in prose is exactly what
// #648 found rotting. The observations below name a width where the rendering is the
// point; they are not a capture list.
//
// The ladder is load-bearing for this adapter in a way it is not for the others, because
// agy renders its two dialogs with two DIFFERENT overflow behaviours:
//
//   - Headline questions are TRUNCATED, not wrapped. "Do you trust the contents of this
//     project?" renders in full at 120/60, as "…of this projec" at 40, "…of this" at 34
//     and "Do you trust the contents of" at 28. flattenChrome joins physical lines with a
//     space, so it repairs a WRAP; nothing repairs a truncation — the tail is simply not
//     in the pane. That rules the question out as the gate literal, which is why the gate
//     keys on its option row instead (the reverse of the usual "key on the question, never
//     the option text" rule, which still holds for the confirmation below).
//   - Option rows and the permission preamble WRAP. "Requesting permission for:" arrives
//     as "Requesting permiss"/"ion"/"for:" at 28 — a mid-WORD wrap that the space-join
//     turns into "Requesting permiss ion for:" — and confirmation options 2 and 3 wrap
//     mid-string at every width below 120. Neither is matchable.
//
// Sizes this narrow are reachable, and there is no floor: the agent's detached session is
// resized to the preview pane (app/app_layout.go's GetPreviewSize → ui/list.go
// SetSessionPreviewSize → session/instance.go SetPreviewSize), the session list may take up
// to maxListRatio = 0.60 of the terminal width (config/state.go), and nothing clamps what
// is left to a minimum. A 70-column terminal at that ratio leaves the agent about 24
// columns. 28 is a width the UI elsewhere calls *reachable* (ui/terminal.go's fallback
// centering, #355/#340) — it is not a bound, and the literals below are chosen to survive
// well under it rather than to sit at it.
//
// (ui/terminal.go's own SetSize is a different pane — TerminalPane owns the per-instance
// SHELL sessions, not the agent's. Do not cite it for the agent pane's width.)
//
// Both dialogs matter more than usual because until this adapter had them, agy's selection
// pointer — plain ASCII ">" (U+003E), byte-verified with cat -A on both screens — made
// isInputBoxLine report a composer on a live dialog while GateUp and DetectPrompt were
// permanently false. AwaitingInput reduced to InputBoxVisible alone, so a queued prompt was
// typed INTO the dialog whose highlighted row is "> 1. Yes" (#512). Populating Gates and
// Prompts is what makes AwaitingInput false there; nothing in chrome.go needed to change.
var agy = &Adapter{
	Key:         KeyAgy,
	DisplayName: "Antigravity",
	aliases:     []string{"agy", "antigravity"},

	// Minor granularity, and unlike gemini's this is an empirical claim rather than a
	// judgement about release cadence: 1.1.5 (driven 2026-08-08) and 1.1.11 (driven
	// 2026-08-09) render every string matched here identically. agy ships patches at a
	// rate that would make GranularityPatch a standing warning for chrome that demonstrably
	// did not move across six of them.
	VerifiedVersion:  "1.1.11",
	DriftGranularity: GranularityMinor,

	// The footer marker is present for the WHOLE turn, streaming included — sampled once a
	// second across a complete turn, it is up on every frame from the one where the turn
	// starts rendering through to the one where it settles back to "? for shortcuts",
	// including the frames where the reply is arriving mid-word. That is the
	// opposite of claude, whose footer hint is lit off its narrowest notion of busy and
	// drops mid-turn, which is the only reason claude needs a LiveSpinner (spinner.go). agy
	// needs none: the marker alone covers the turn.
	//
	// Do NOT key this on the spinner's verb. agy rotates it — "Generating…", "Running…"
	// (1.1.11), "Working…", "Loading…" (1.1.5) — so any single verb misses the others.
	//
	// MarkerWindow 0 (the footer below the input box's bottom border) is correct because
	// agy keeps the composer box on screen while it works: a busy pane is rule / ">" / rule
	// / "esc to cancel", exactly claude's geometry. A settled pane puts "? for shortcuts"
	// in that same slot. (That idle literal — "? for shortcuts", not the busy one — is the
	// SAME string claude renders; never key a shared helper on it. agy's busy marker
	// happens to match gemini's, which is a separate coincidence.)
	//
	// The footer is not a perfect idle/busy split: agy's slash-command menu also carries
	// "esc to cancel" over a live composer, so typing "/" reads as Working until the menu
	// closes. The state self-heals (the marker is a level signal, so it clears with the
	// menu), but the closing edge is not free and it is worth naming rather than calling
	// this cosmetic: Working→Ready is a real transition, so session/status.go
	// setStatusLocked stamps the row unread, and app/app_notify.go notifyEventFor turns an
	// advanced unread stamp into EventFinished. Browsing "/" and dismissing it can
	// therefore leave an unread marker and fire a "finished" notification for a turn that
	// never ran. Accepted, because the alternative — dropping the footer marker for a
	// narrower signal — costs the whole-turn coverage that lets agy skip a LiveSpinner.
	BusyMarkers:  []string{"esc to cancel"},
	MarkerWindow: 0,

	Prompts: []PromptMatcher{
		// The tool-confirmation dialog (shell execution). Keyed on the dialog's own nav
		// hint rather than on "Do you want to proceed?", for a reason the capture settled
		// empirically: the question is the dialog's TOP line, so the wrapped options below
		// it push it out of the window as the command string grows. At width 40 with a
		// long command it sits 16 non-empty lines from the bottom — past WindowPrompt's 15
		// — and the matcher misses. The nav hint is the dialog's BOTTOM row, two non-empty
		// lines up at every width from 120 down to 20, so the window is never the binding
		// constraint. It is also what distinguishes this dialog from the trust gate, whose
		// hint reads "↑/↓ Navigate · enter Confirm", so it is the "tab" half that picks out
		// the command-amendment dialog.
		//
		// The literal stops at "tab" rather than running on to "tab Amend" because the
		// hint truncates: at 24 columns it renders "↑/↓ Navigate · tab Ame" and the fuller
		// wording misses the dialog entirely (it still matches at 28, which is why the
		// 28-column fixture alone could not catch this — see
		// TestAgyConfirmationHintTruncatesBelowTheFullWording).
		//
		// The floor this leaves is 20 columns, where the hint renders exactly
		// "  ↑/↓ Navigate · tab" with nothing to spare, and it CANNOT be lowered by
		// shortening the literal further: agy truncates from the right, so what binds is
		// where "tab" ends in the line (18 cells of content after the 2-space indent), not
		// the substring's own length — "Navigate · tab" needs the same 20 columns. The only
		// anchor earlier in the line is the generic "↑/↓ Navigate ·" the slash-command menu
		// also renders, which is exactly what must not be matched. So below 20 columns the
		// confirmation is missed, and per the note below that means the row latches Working
		// rather than falling back to idle. agyConfirmFloorPane pins the boundary.
		//
		// "tab Amend" and not the generic "↑/↓ Navigate" prefix, deliberately, even though
		// the generic one would cover any dialog agy grows later: the slash-command menu
		// renders "↑/↓ Navigate · enter Select · tab Complete" over a LIVE composer, so the
		// broader matcher would make typing "/" read as blocked and withhold the user's
		// queued prompt — #512's own mechanism, pointed the other way. See
		// TestAgySlashMenuIsNotAPrompt.
		//
		// The cost of that narrowness, stated because it is not the usual one: a dialog
		// shape this misses does NOT fail safe to idle. agy's dialogs carry "esc to cancel"
		// in their own footer, so an unmatched one satisfies the busy marker and the row
		// reads Working indefinitely instead of needs-input. What bounds that risk is the
		// dialog population, re-verified against 1.1.11 rather than assumed: after the
		// trust gate is accepted, an in-workspace file write (Create) and a read OUTSIDE
		// the workspace (/etc/hostname) both complete with no dialog at all. Shell
		// execution is the only prompt observed, which is what the trust screen's own
		// wording — "read, edit, and execute files here" — predicts.
		//
		// NoAutoTap, for the reason codex's approval carries it (#347) — gemini's
		// confirmation carried it for the same reason until #736 anchored that one:
		// this is a flat-window matcher, its literal lives verbatim in this file
		// and is therefore quotable into an agy pane by an agent reading this repo, and
		// option 1 is "Yes" — a false positive that autoyes ANSWERS runs a shell command.
		// Surfacing needs-input costs one keystroke and cannot act.
		{Name: "confirmation", Window: WindowPrompt, NoAutoTap: true,
			All: []string{"↑/↓ Navigate · tab"}},
	},

	Gates: []Gate{
		// The startup folder-trust screen. Keyed on the option row, NOT the question:
		// per the overflow note above, the question truncates from 40 columns down and no
		// prefix of it is both narrow enough to survive and specific enough to be worth
		// matching ("Do you trust the contents of" is the whole 28-column line).
		//
		// Truncated to "Yes, I trust" rather than the full "Yes, I trust this folder",
		// because the full row needs 26 columns (24 plus its "> " indent) and the pane can
		// be narrower than that. This gate is the one place where a miss is not merely a
		// missed notification: the truncated row still opens with ">", so isInputBoxLine
		// reports a composer, AwaitingInput goes true, and the queued FIRST prompt is typed
		// into the trust dialog — #512's own failure, resurfacing at a width the wide
		// capture could not see. Driven live: at 26 the full row survives, at 24 it renders
		// "> Yes, I trust this fold" and at 20 "> Yes, I trust this". The shorter literal
		// holds at all three (it needs 14 columns) and stays distinctive enough to be worth
		// matching. See TestAgyTrustGateNarrowerThanTheOptionRow.
		//
		// This is a flat bottom-window match, with the cost codex's and gemini's matchers
		// carry and claude's no longer does: the literal lives verbatim in this file — in
		// fact twice, since claudeGateTitles opens with the same string — so an agy session
		// displaying registry.go or its fixtures inside the bottom WindowPrompt lines reads
		// as gated. GateUp outranks the busy marker (session/tmux/poll.go), so that pane
		// parks on needs-input with its queued prompt withheld: #342's direction, failing
		// closed rather than acting. Anchoring it structurally the way claudeGateVisible
		// does would need a live-chrome primitive this gate does not have — it renders
		// before any composer exists, so there is no input box to anchor against.
		{Name: "trust", Contains: []string{"Yes, I trust"}},
	},

	// Appended unconditionally, unlike claude's rewrite (which leaves an already-pinned
	// conversation alone) and codex's (which refuses a program carrying flags): agy
	// takes --continue after any flags, which is where its own help puts it.
	//
	// DRIVEN 2026-08-18 on agy 1.1.14, in a directory with no conversation: it starts
	// normally into an empty composer and the pane survives. agy documents --continue
	// only as "Continue the most recent conversation" and says nothing about scope, so
	// that row rests on the observation rather than on a documented per-project lookup —
	// its last-conversations cache does record the workspace, which is why the harness
	// drives each agent in a directory nothing has run in before. Nothing in Atrium asks
	// whether there is a conversation (ResumeProbe is a capability check), so this is a
	// record of the vendor's tolerance: re-check with `just drive-agent resume agy`
	// (#712).
	Resume:        func(program string) string { return program + " --continue" },
	ResumeProbe:   "--continue",
	HeadlessNamer: true,
}

// GitHub Copilot CLI (github/copilot-cli, npm @github/copilot). DRIVEN at 1.0.80 on Linux,
// 2026-08-26, in an isolated COPILOT_HOME against a scratch git repo with the organization's
// token injected via ATR_CAP_ENV — three surfaces, each with a verbatim width ladder in
// copilot_pane_test.go: the folder-trust Gate, the approval PromptMatcher, and the busy
// marker. The design record is docs/superpowers/specs/2026-08-26-copilot-cli-integration-design.md.
//
// WHAT SHAPE THIS ADAPTER IS. Its two dialogs are closed round boxes whose bottom border is
// the last non-empty line at every driven rung — gemini's shape, so both matchers anchor on
// bottomBoxBlock. Its composer and busy row are claude's arrangement: a borderless composer
// between two horizontal rules, with the status row replacing the hint row BELOW it, so
// MarkerWindow stays 0 and the footer anchor finds the marker. Two vendors' shapes in one
// CLI, which is why neither codex's GateWindow nor gemini's composer veto transfers.
//
// THE TWO SHAPES ARE THE DISCRIMINATOR, and that is what ModalVeto is built on. A dialog is
// an anchored box; the composer never is. So "a box ends the pane" separates them without
// reading a single literal, which is the only kind of test that still works at a pane height
// where the dialog's own headline has scrolled off. See ModalVeto's own doc for the delivery
// hole that needs — the matchers below are literal matches, so they cover only the heights
// where their literals are on screen, and that is a band, not the whole axis.
//
// WHY NOT GateWindow, since codex's trust gate looks like the same problem. Codex draws its
// overlay with no border at all, so its headline is intact and merely pushed out of a
// line-count budget, and a wider window reaches it. Copilot's headline is DESTROYED — the
// border runes and their padding sit between its fragments — so no window reaches it and
// flattenBottomBox is the remedy instead. TestCopilotTrustGateNeedsTheWallStrippingScan
// measures the difference at every rung.
//
// WHY NOT a composer veto INSIDE the matchers, the way geminiTrustGateVisible has one.
// Copilot's selector IS the composer glyph "❯" and it sits on the dialog's highlighted row,
// so a matcher that went false whenever a composer glyph was on screen would go false on
// every rung of both ladders — TestCopilotDialogSelectorIsTheComposerGlyph holds that
// collision. The veto therefore runs in the other direction, on the composer predicate
// rather than inside the matchers: ModalVeto, wired to copilotModalUp.
//
// HookSupport is deliberately FALSE even though copilot has hooks and they fire. The
// invocation schema is claude-compatible, keyed by camelCase event names; the OUTPUT schema is
// not. Claude's nested hookSpecificOutput.additionalContext fires and delivers nothing, while
// a flat {"additionalContext": …} works — both driven. The field routes through claude's
// emitter, so setting it here ships a brief that is registered, documented and dead, which is
// the #773 failure mode verbatim. #773 replaces the bool with a capability that can say which
// schema; this adapter waits for it.
//
// Resume is deliberately NIL. `--continue` and `-r, --resume` are both in --help (VENDOR at
// 1.0.80), but ResumeProbe's needle must pin the listing rather than the bare word and that
// has not been chosen, and the behaviour in a directory with nothing to resume has not been
// driven. A nil Resume relaunches blank, which is the adapter's safe mode; a needle guessed
// off a help line is the failure mode ResumeProbe exists to prevent.
//
// HeadlessNamer is deliberately FALSE, and this entry exists because the field is otherwise
// the one capability a reader cannot tell from an oversight. The CAPABILITY is present:
// `-p, --prompt <text>` is in --help at 1.0.80 and --allow-all-tools is documented as
// required alongside it. What is missing is the half the field's own doc requires — a
// matching branch in session/naming.go, which needs the envelope copilot prints in that mode
// to have been driven and parsed. It has not been. Setting the bool without that branch is
// the registered-and-dead shape again, so it waits for a drive of its own.
var copilot = &Adapter{
	Key:         KeyCopilot,
	DisplayName: "Copilot CLI",
	aliases:     []string{"copilot"},

	VerifiedVersion:  "1.0.80",
	DriftGranularity: GranularityMinor,

	// Narrowed to the one glyph copilot draws, "❯" (U+276F), byte-verified with cat -vet
	// against the driven panes. Nil would have worked in the sense that defaultPrompts
	// contains "❯" — but it also contains the plain ">", which this CLI never opens a
	// composer with, and accepting a glyph the agent does not draw is the fail-open this
	// field's doc exists to prevent (codex's banner line is its worked example). The cost of
	// nil is not hypothetical here: inputBoxText anchors on the BOTTOM-MOST prompt-glyph line
	// in its window, so any ">"-opening transcript row below the real composer — a quoted
	// diff, a shell transcript, agent prose — becomes the composer it reads.
	// TestCopilotComposerRejectsThePlainAngleBracket.
	InputBoxPrompts: []string{"❯"},

	// "Worki", not "Working", and the truncation is the finding. The status row reads
	// "<spinner> Working · <N> B esc interrupt", and below 26 columns the footer becomes a
	// multi-column grid that jams the cells together and splits the word mid-way: width 24
	// renders "WorkinKiB    interrup" and width 20 "WorkiKiB   interr". "Worki" is the
	// longest prefix present at every driven rung, and it is present at all eight.
	// TestCopilotBusyMarkerIsTheLongestSurvivingPrefix reads both halves off the ladder —
	// that this marker is found at every rung, and that one character more is not.
	//
	// An earlier draft of this entry keyed on "Working" and disclosed the two narrow rungs as
	// a floor that could not be repaired without a spinner frame-set. That was wrong on its
	// own fixtures, and wrong in the fail-dangerous direction: BusyMarkers being non-empty is
	// what disables the content-change fallback in session/tmux/poll.go, and copilot has no
	// hook record either, so a marker that misses does not decay to a stale Working — the
	// session never enters PaneWorking at all. It reads Ready through a live turn, dings on a
	// turn that has not ended, and promptDeliveryReady hands it a queued prompt mid-turn. A
	// 70-column terminal leaves a preview pane of about 24 columns, which is the width that
	// missed; see pane_width_test.go's header for where that number comes from.
	//
	// The two words this is deliberately not paired with are still worth recording. The byte
	// counter sits BETWEEN "Working" and "esc interrupt", so that pair is never contiguous at
	// any width; and the separator is not monotonic either — 34 renders "Working·" with no
	// space where 40 and wider render "Working ·".
	// TestCopilotBusyMarkerCannotKeyOnTheInterruptHint asserts both halves per rung.
	BusyMarkers: []string{"Worki"},
	// MarkerWindow deliberately 0. The status row REPLACES the hint row below the composer,
	// so footerRegion's below-the-box anchor finds it. This is claude's arrangement; codex
	// and gemini paint their status row above the composer, which is why they need a window,
	// and copying one of theirs here would search past the row entirely.

	// The collapsed-paste chip, wired because copilot ships one and turns it ON by default.
	// VENDOR at 1.0.80 (app.js in @github/copilot-linux-x64): a paste is compared against a
	// line threshold of 10 and, when compactPasteEnabled is set — `??!0`, i.e. defaulting to
	// true — is replaced in the composer by "[Paste #N - L lines]". Leaving this nil is not a
	// small loss: boxHoldsPrompt confirms a multi-line prompt either by its first-line
	// signature (which the chip does not carry) or by this predicate, so with neither, every
	// queued prompt over ten lines fails to confirm, is never submitted, and is re-pasted on
	// the next keeper cycle — appending one more chip each time.
	PasteCollapsed: copilotPasteCollapsed,

	// A modal is up, so the composer glyph on screen is a selector. This is the structural
	// half of dialog handling and it is not redundant with the matchers below: they read
	// literals, so they stop covering the moment the pane is shorter than the dialog, and
	// this does not. ModalVeto's doc carries the failure it closes.
	ModalVeto: copilotModalUp,

	Prompts: []PromptMatcher{
		// The out-of-worktree path approval. NoAutoTap for a strictly worse reason than the
		// one it carries on codex, where Enter approves a single command: this dialog's
		// pre-selected option is the SECOND one, "Yes, and add these directories to the
		// allowed list", so Enter widens the session's allowed-path list rather than
		// approving one action. An autoyes tap would silently extend a copilot agent's
		// filesystem reach past its worktree for the rest of the session — a sandbox
		// widening performed by a convenience feature.
		{Name: "approval", NoAutoTap: true, Match: copilotApprovalVisible},
	},

	Gates: []Gate{
		// The folder-trust screen. A conjunction through Match, not Contains: the headline
		// alone is a plausible sentence for a session to print while discussing this file,
		// and Contains would read a flat bottom-N window that cannot reconstruct it below 60
		// columns anyway. TestCopilotTrustGateNeedsBothLiterals holds the conjunction by
		// rendering each literal without the other, which is the only shape that can tell an
		// AND from an OR.
		//
		// Which two literals, and why not the title: the pair is what every driven rung
		// renders, and the title "Confirm folder trust" is the one thing that does not — at
		// width 20 it wraps and its first row scrolls off, leaving "trust" alone on the box's
		// first visible line (TestCopilotTrustGateTitleIsGoneAtWidth20). Head-truncated, not
		// absent: a matcher keyed on a title SUFFIX would still fire there, which is exactly
		// why a title is the wrong thing to key on — what it fires on is not the title.
		{Name: "trust", Match: copilotTrustGateVisible},
	},
}

// copilotModalUp backs the adapter's ModalVeto: an anchored box whose bottom border all but
// ends the pane, which for copilot means one of its two dialogs is open.
//
// It reads no literal, deliberately — that is the whole reason it exists, and ModalVeto's doc
// carries the delivery hole a literal cannot cover. What makes the structural test sound here
// is that copilot's composer is BORDERLESS (claude's arrangement), so it is never such a box:
// TestCopilotBusyPanesStayDeliverable holds that at every driven rung, which is the direction
// that would break prompt delivery outright if it were wrong.
//
// It inherits bottomBoxBlock's disclosed exposure — quoted box art that ends the pane — and
// pays for it in the safe direction: a pane that trips this holds its queued prompt instead of
// typing it onto something that is not a composer.
func copilotModalUp(content string) bool {
	_, ok := bottomBoxBlock(content)
	return ok
}

// copilotPasteChipRegex and copilotSavedPasteRegex are copilot's two collapsed-paste
// placeholders, mirroring the vendor's own pair of detectors at 1.0.80 rather than a shape
// read off one screenshot: a paste over the line threshold becomes "[Paste #N - L lines]",
// and one over the byte threshold is written to the workspace and becomes "[Saved pasted
// content to workspace (<file>) id=N]". The vendor's own regex makes the " - L lines" half
// optional, so this does too — that is the shape it accepts back from its own composer.
// Tolerant on the plural for the same reason claude's chip regex is.
var (
	copilotPasteChipRegex  = regexp.MustCompile(`\[Paste #\d+(?: - \d+ lines?)?\]`)
	copilotSavedPasteRegex = regexp.MustCompile(`\[Saved pasted content to workspace \([^)]+\) id=\d+\]`)
)

// copilotPasteCollapsed backs the copilot adapter's PasteCollapsed: whether the input-box
// readback is one of copilot's two paste placeholders rather than the pasted text itself.
// Either shape means the paste landed, which is all the delivery path asks.
func copilotPasteCollapsed(boxText string) bool {
	return copilotPasteChipRegex.MatchString(boxText) || copilotSavedPasteRegex.MatchString(boxText)
}

// copilotTrustHeadline and copilotTrustOption are the folder-trust gate's two literals, as
// consts so the guards measure against the symbol the matcher reads rather than restating a
// string. Both survive every driven rung; the title does not, which is why neither is it.
//
// THEY ARE NOT GUARDED ALIKE, and saying so is the point. Shortening either one keeps the
// ladder green — a conjunction only narrows as its terms lengthen — so what the ladder holds
// is that both are REACHABLE at every rung, not that either is the shortest sufficient form.
// Lengthening one past what the narrowest rung renders is what reddens it. That the
// conjunction is an AND at all is a separate property, and a ladder of real dialogs cannot
// test it: both dialogs differ in BOTH literals, so every fixture agrees with an OR.
// TestCopilotTrustGateNeedsBothLiterals and TestCopilotApprovalNeedsBothLiterals are the
// single-literal panes that do not.
const (
	copilotTrustHeadline = "Do you trust the files in this folder?"
	copilotTrustOption   = "Yes, and remember this folder for future sessions"
)

// copilotDialogVisible is the shape both copilot dialogs share: two literals inside the
// bottom-most anchored box, read through flattenBottomBox so a headline hard-wrapped across
// the box's own borders still reconstructs.
//
// Two clauses doing different jobs, the way geminiTrustGateVisible's do. The box says a
// dialog is LIVE — a dismissed one is replaced by the composer, which for copilot is not an
// anchored box, so this goes false; and it is what keeps the wall-stripping scan off the whole
// pane. The literal pair says WHICH dialog, and both terms are needed: each headline is
// ordinary English and each option label is the specific half.
//
// One helper for both rather than two identical bodies, so the conjunction and the anchor are
// one thing to review and one thing to change. What differs between the two dialogs is only
// their literals, which is also why no fixture can distinguish this AND from an OR.
//
// What the box clause narrows and does not close is bottomBoxBlock's own disclosed exposure —
// quoted box art that ends the pane. A transcript quoting a dialog and stopping exactly at its
// bottom border does fire this. That direction fails CLOSED (a queued prompt is held, never
// mis-delivered), which GateUp's own doc records as the acceptable one.
func copilotDialogVisible(content, headline, option string) bool {
	flat, ok := flattenBottomBox(content)
	if !ok {
		return false
	}
	return strings.Contains(flat, headline) && strings.Contains(flat, option)
}

// copilotTrustGateVisible reports copilot's folder-trust screen.
func copilotTrustGateVisible(content string) bool {
	return copilotDialogVisible(content, copilotTrustHeadline, copilotTrustOption)
}

// copilotApprovalHeadline and copilotApprovalOption are the approval dialog's two literals.
// The option label deliberately starts at "Yes," and not at the selector: the space between
// "❯" and "2." is not stable and not monotonic in width, so including it would pass a wide
// check and fail at a narrower one while passing again narrower still.
// TestCopilotApprovalOptionExcludesTheSelector carries the eight readings.
//
// Neither the decline row "3. No (Esc)" nor the "↑/↓ to navigate · enter to select · esc to
// cancel" footer appears here, though both survive every width: the folder-trust dialog
// renders them identically, so neither can discriminate.
const (
	copilotApprovalHeadline = "Do you want to allow this?"
	copilotApprovalOption   = "Yes, and add these directories to the allowed list"
)

// copilotApprovalVisible reports copilot's out-of-worktree path approval. Same two clauses as
// copilotTrustGateVisible, through the same helper, and the literal pair matters more here
// than there because this adapter's two dialogs share their decline row and their entire
// navigation footer.
//
// It carries NoAutoTap on the matcher rather than relying on this predicate's precision, and
// that is not belt-and-braces: bottomBoxBlock's disclosed exposure is quoted box art that ends
// the pane, and a session reading this very file could produce it. What NoAutoTap costs is a
// needs-input on a working session. What it buys is bounded, and the bound is worth stating
// exactly: no AUTOYES TAP can widen the agent's allowed-path list. It says nothing about the
// delivery path, because NoAutoTap is consulted only once DetectPrompt has fired — the pane
// where this matcher is blind is covered by ModalVeto instead.
func copilotApprovalVisible(content string) bool {
	return copilotDialogVisible(content, copilotApprovalHeadline, copilotApprovalOption)
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
var registry = []*Adapter{claude, codex, gemini, aider, agy, copilot}

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
