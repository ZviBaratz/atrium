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
		// dialog (session/tmux AwaitingInput — the dialog's "❯ 1. Yes" reads as an
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
		// dialogs (both captured live at 2.1.210, #340 — registry_test.go
		// claudeMCPSinglePane / claudeMCPMultiPane):
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
		{Match: claudeGateVisible},
	},

	// tmux word-splits the trailing command string itself, so appending to the
	// single program argv element is sufficient — no shell wrapping.
	Resume:        func(program string) string { return program + " --continue" },
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
// pinned by every captured shape (registry_test.go claudeTrustPane, claudeMCPSinglePane,
// claudeMCPMultiPane, claudeMCPWrappedPane, claudeMCPNarrowPane). So the last border on a
// gated pane is the dialog's own top rule and everything below it IS the dialog, while on a
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
// The cap restores the ceiling without restoring the floor. It sits well clear of the tallest
// dialog ever captured (claudeMCPNarrowPane: 17 non-empty lines at width 28 — the width that
// used to miss), so it never truncates a real gate, and bites only when the anchor turns out
// not to be live chrome. Same role aboveBoxBlockCap plays for the upward scan (chrome.go).
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
// lines above the region's bottom at width 28 (claudeFetchNarrowPane — the narrowest
// reachable pane, since an agent's pane is atrium's PREVIEW pane), so 20 clears every
// captured shape with better than 2x margin while exposing half the surface 40 would.
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
		// NoAutoTap, and unlike codex's this is the settled answer rather than a
		// holding position (#347). The literal is quotable from this file exactly as
		// codex's is, and Enter on a real confirmation runs the shell command or
		// writes the file — but the CLI is deprecated in favour of Antigravity
		// (docs/superpowers/specs/2026-07-23-antigravity-integration-design.md), and
		// anchoring it would take a primitive nothing else needs: the confirmation
		// renders INSIDE a rounded box with the app footer below it, so
		// footerBelowBox hands back the footer and never the dialog, and
		// aboveBoxBlock anchors on a composer the confirmation has replaced.
		// Surfacing as needs-input costs a gemini user one keystroke; autoyes users
		// who want the taps have gemini's own --yolo / --approval-mode.
		{Name: "confirmation", Window: WindowPrompt, NoAutoTap: true,
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

// Antigravity (agy). Every string below was driven against a live agy 1.1.11 in an
// isolated tmux on 2026-08-09 (#512) and captured at widths 120/60/40/34/28. The width
// ladder is load-bearing for this adapter in a way it is not for the others, because agy
// renders its two dialogs with two DIFFERENT overflow behaviours:
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
// Sizes this narrow are reachable: ui/terminal.go SetSize resizes the detached session to
// the preview pane's width, and 28 is the reachable floor that pane already sizes its own
// placeholder copy against (ui/terminal.go's fallback centering, #355/#340).
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
	// / "esc to cancel", exactly claude's geometry. The idle pane puts "? for shortcuts"
	// in that same slot, so the footer separates idle from busy on its own. Note that
	// literal is the SAME one claude renders — never key a shared helper on it.
	BusyMarkers:  []string{"esc to cancel"},
	MarkerWindow: 0,

	Prompts: []PromptMatcher{
		// The tool-confirmation dialog (shell execution). Keyed on the dialog's own nav
		// hint rather than on "Do you want to proceed?", for a reason the capture settled
		// empirically: the question is the dialog's TOP line, so the wrapped options below
		// it push it out of the window as the command string grows. At width 40 with a
		// long command it sits 16 non-empty lines from the bottom — past WindowPrompt's 15
		// — and the matcher misses. The nav hint is the dialog's BOTTOM row, two non-empty
		// lines up at every width from 120 down to 28, so the window is never the binding
		// constraint. It is also what distinguishes this dialog from the trust gate, whose
		// hint reads "↑/↓ Navigate · enter Confirm"; "tab Amend" is unique to the
		// command-amendment dialog.
		//
		// Truncated at the point where 28 columns cut it, so the literal is a prefix that
		// survives every width rather than one that only fits the wide pane.
		//
		// NoAutoTap, for the reason codex's approval and gemini's confirmation carry it
		// (#347): this is a flat-window matcher, its literal lives verbatim in this file
		// and is therefore quotable into an agy pane by an agent reading this repo, and
		// option 1 is "Yes" — a false positive that autoyes ANSWERS runs a shell command.
		// Surfacing needs-input costs one keystroke and cannot act.
		{Name: "confirmation", Window: WindowPrompt, NoAutoTap: true,
			All: []string{"↑/↓ Navigate · tab Amend"}},
	},

	Gates: []Gate{
		// The startup folder-trust screen. Keyed on the option row, NOT the question:
		// per the overflow note above, the question truncates from 40 columns down and no
		// prefix of it is both narrow enough to survive 28 and specific enough to be worth
		// matching ("Do you trust the contents of" is the whole 28-column line). The option
		// row is 24 columns wide, so it survives intact everywhere, and its wording is far
		// more distinctive than the truncated question's.
		{Contains: []string{"Yes, I trust this folder"}},
	},

	Resume:        func(program string) string { return program + " --continue" },
	ResumeProbe:   "--continue",
	HeadlessNamer: true,
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
var registry = []*Adapter{claude, codex, gemini, aider, agy}

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
