package agent

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #648, and the reason it is a whole file: nothing in this package stopped an adapter's
// matcher literal from being WIDER than the narrowest pane Atrium can hand an agent, and a
// literal that does not fit stops firing on a real dialog in silence.
//
// That shipped. agy's gate keyed on "Yes, I trust this folder" — 24 cells plus the option
// row's "> " indent, so it needs 26 columns. At 24 the row renders "> Yes, I trust this
// fold", GateUp goes false, and because the truncated row STILL opens with ">",
// isInputBoxLine reports a composer, AwaitingInput returns true, and the queued first prompt
// is typed into the folder-trust dialog. Nothing floors that width: the agent's detached
// session is sized to the preview pane (app/app_layout.go GetPreviewSize → ui/list.go
// SetSessionPreviewSize → session/instance.go SetPreviewSize), the list may take
// maxListRatio = 0.60 of the terminal (config/state.go), and the remainder is not clamped to
// any minimum — a 70-column terminal leaves about 24.
//
// The reason no test could state that rule is that a capture's WIDTH was never a value. It
// lived in const names (codexApprovalPane28) and in doc comments ("captured at width 28"),
// where nothing can read it. The table below makes it a datum, and the tests turn it into
// the invariant.
//
// TWO WAYS TO GET THIS WRONG, both of which pass while the matcher is broken:
//
//   - Comparing len(literal) to a width. Truncation is right-to-left, so what binds is
//     where the distinguishing token ENDS in the line, not how long the matched substring
//     is: "Navigate · tab" needs the same 20 columns as "↑/↓ Navigate · tab" and dies at
//     the same 19. TestAgyConfirmationFloorCannotBeLoweredByShorteningTheLiteral states
//     that truncation model, and nothing here subsumes it — it evaluates a counterfactual
//     literal on a synthetic 19-column pane, neither of which a table of real captures can
//     ever contain. Do not "de-duplicate" the two.
//   - Grepping the pane. Codex WRAPS its gate body instead of truncating it, and
//     flattenChrome's space-join repairs the wrap — so a raw grep for codex's 43-cell gate
//     literal misses at ≤40 while the real GateUp holds to 24.
//
// So every assertion here runs the PRODUCTION predicate (GateUp / DetectPrompt /
// HasBusyMarker) against a captured pane. Nothing here reasons about lengths.
//
// "Captured" means byte-verbatim for every entry that records a WIDTH, which is the direction
// that matters: two entries are TRANSCRIPTIONS of a live pane instead — claudeMCPSinglePane and
// claudeMCPMultiPane, whose box rule was spliced (#666) — and both carry width 0, because an
// edited pane cannot testify about its own width. Not every width-0 entry is a transcription;
// most are verbatim captures whose provenance simply never wrote the width down, which is what
// keysWithNoRecordedCaptureWidth is for. Both kinds still testify about SHAPE, which is what
// they are listed for.
//
// What this file does NOT reach, stated so the tables are not read as fuller than they are.
// A Match matcher IS exercised at every width it has captures for, because fires() runs the
// production predicate; read the rungs off paneCoverage rather than from a list here, which is
// what #666 had to correct when it drove claudeGateVisible below 28. What cannot be enumerated
// is its individual literals: they live inside the func (claudeGateVisible,
// claudeFetchPermissionVisible, claudeNetworkPermissionVisible, claudeSelectionFooterVisible,
// claudeLocalPermissionVisible, aiderConfirmVisible and the token helpers they call), so the
// alternatives ledger below skips them. Beyond that, the width-bearing surfaces outside
// Prompts/Gates/BusyMarkers — PermissionMode's footer markers, LiveSpinner, SuggestionVisible,
// PasteCollapsed, BackgroundWork — have no coverage here at all.
//
// BackgroundWork is the newest of those and the one with the most obvious width exposure, so
// say what is known rather than leave it implied. Its chips ("N shells", "N monitors") sit in
// the footer's TAIL, after the permission-mode indicator, and claude renders that line
// truncate-on-overflow — the same mechanism registry.go records for the interrupt marker,
// which at width 30 reads "⏸ manual mode on · esc to …". So the chip certainly stops being
// visible somewhere below full width, and no rung here proves where. It is filed with
// LiveSpinner rather than with the gates and prompts because it shares their failure mode
// rather than the prompts': a truncated chip is a MISS, which reads as the plain idle the
// poller reported before the detector existed, never a wrong action on a live dialog. Driving
// the ladder for it needs a real background shell alive in a driven session — worth doing,
// and not done here.

// paneCapture is one captured pane plus the width it was driven at (see the header on the two
// entries that are transcriptions rather than verbatim captures).
type paneCapture struct {
	// name is the fixture's Go identifier, so a failure names the capture to open.
	name string
	// width is the tmux -x the capture ran at. ZERO means the fixture's own provenance
	// does not record it — which is not the same as "narrow" and must never be read as a
	// floor. Such a capture still proves its matcher fires; it just cannot say at what
	// width, so it is excluded from the floor computation and its key is listed in
	// keysWithNoRecordedCaptureWidth below. That list is the debt #648 exposes: a capture
	// whose width was never written down can never be evidence about width.
	width int
	// note is what is notable at this rung, for the subtest name.
	note string
	pane string
}

// label names the rung in a subtest and in failure text: the width first, because that is
// what a width failure is about, then the identifier to open and whatever is notable there.
func (c paneCapture) label() string {
	s := "width " + describeWidth(c.width) + " " + c.name
	if c.note != "" {
		s += " (" + c.note + ")"
	}
	return s
}

func describeWidth(w int) string {
	if w == 0 {
		return "unrecorded"
	}
	return fmt.Sprintf("%d", w)
}

// paneCoverage maps a matcher key to the captures on which that matcher's production
// predicate must fire. Keys are "<adapter>/gate", "<adapter>/busy" and
// "<adapter>/prompt/<matcher name>" — the same shape the required set is derived in, so a
// typo here surfaces as an orphan rather than as silent non-coverage.
//
// Entries are POSITIVE only: every capture listed must make its predicate return true. The
// false-positive direction — a narrow pane that must NOT read as a prompt — is guarded
// per-adapter in the sibling files and is deliberately not generalised here, because the
// negative shapes are adapter-specific and no table of them would be derivable from the
// registry the way this one is. Those guards are agy's answered confirmation and idle
// composer (registry_test.go TestAgyConfirmationPrompt), its slash menu
// (TestAgySlashMenuIsNotAPrompt), its accepted gate (TestAgyTrustGate), codex's composer
// ladders (codex_pane_test.go TestCodexComposersReadAsNeitherPromptNorGate) and gemini's
// width-20 trust dialog, which is negative in a third way — the gate is MISSED there, so
// gemini_pane_test.go holds both that miss and the composer/prompt readings that make it
// harmless (TestGeminiTrustGateOptionRowsAreTruncatedAtWidth20,
// TestGeminiTrustGateIsNeitherComposerNorPrompt).
var paneCoverage = map[string][]paneCapture{
	// claude's gate is one Match (claudeGateVisible) covering the folder-trust dialog and
	// both MCP-approval shapes, so every capture below belongs to the same key however many
	// of them there are. (There were five when this was written; #666 added four. Say "every"
	// rather than a number in a comment sitting directly above the thing that can be counted.)
	"claude/gate/startup": {
		{name: "claudeTrustPane", width: 0, note: "folder-trust, 2.1.185", pane: claudeTrustPane},
		// Neither 2.1.210 MCP capture has a usable width, and #666 settled why rather than
		// leaving it a suspicion. claudeMCPMultiPane's comment claims 110 in a per-width
		// result table, while the pane's widest line is 105 and the box rule spliced
		// inside it is strings.Repeat("─", 56). Driving the same two
		// shapes at a real 110 (claudeMCPMultiWidePane, claudeMCPSingleWidePane) shows a
		// 110-column capture drawing a 110-cell rule — so those two are edited
		// transcriptions, and their width stays unrecorded rather than inferred. #340's
		// table remains provenance for what it measured; it was never provenance for a
		// fixture's width. (Never infer a capture width from a box rule either; that is the
		// same guess in reverse. The Wide panes carry theirs because they were driven at
		// it, and their rule agreeing is a consequence, not the evidence.)
		{name: "claudeMCPSinglePane", width: 0, note: "one server, 2.1.210, width claim disputed", pane: claudeMCPSinglePane},
		{name: "claudeMCPMultiPane", width: 0, note: "many servers, 2.1.210, width claim disputed", pane: claudeMCPMultiPane},
		{name: "claudeMCPWrappedPane", width: 40, note: "title wrapped", pane: claudeMCPWrappedPane},
		{name: "claudeMCPNarrowPane", width: 28, note: "", pane: claudeMCPNarrowPane},
		// The 2.1.228 ladder (#666). Both shapes at both ends, so the floor below is
		// carried by two independent dialogs rather than by one pane's luck.
		{name: "claudeMCPSingleWidePane", width: 110, note: "one server, width driven", pane: claudeMCPSingleWidePane},
		{name: "claudeMCPMultiWidePane", width: 110, note: "many servers, width driven", pane: claudeMCPMultiWidePane},
		{name: "claudeMCPSingleFloorPane", width: 20, note: "one server, title wrapped, options hang-indented", pane: claudeMCPSingleFloorPane},
		{name: "claudeMCPMultiFloorPane", width: 20, note: "many servers, title wrapped, footer in three pieces", pane: claudeMCPMultiFloorPane},
	},
	"claude/busy": {
		{name: "claudeBusyDefaultPane", width: 100, note: "default chat:cancel binding", pane: claudeBusyDefaultPane},
		{name: "claudeBusyRebindPane", width: 100, note: "chat:cancel rebound to ctrl+q", pane: claudeBusyRebindPane},
	},
	"claude/prompt/permission": {
		{name: "claudeFetchPane", width: 100, note: "", pane: claudeFetchPane},
		{name: "claudeFetchNarrowPane", width: 28, note: "", pane: claudeFetchNarrowPane},
	},
	// The 2.1.228 ladder (#666 item 1) is the last five. The first two keep width 0: their
	// provenance genuinely never recorded one, and TestCaptureWidthsAreNotOverstated would
	// take a guess as a claim. They still prove shape, which is what they were pinned for.
	"claude/prompt/permission-local": {
		{name: "claudeWritePermissionPane", width: 0, note: "file write, 2.1.210", pane: claudeWritePermissionPane},
		{name: "claudeBashPermissionPane", width: 0, note: "shell command, 2.1.210", pane: claudeBashPermissionPane},
		{name: "claudeWritePermissionNarrowPane", width: 30, note: "footer still one line", pane: claudeWritePermissionNarrowPane},
		{name: "claudeWritePermissionWrappedFooterPane", width: 29, note: "\"Tab to amend\" split across the wrap", pane: claudeWritePermissionWrappedFooterPane},
		{name: "claudeWritePermissionFloorPane", width: 20, note: "footer wrapped after the separator", pane: claudeWritePermissionFloorPane},
		{name: "claudeBashPermissionWrappedFooterPane", width: 29, note: "three-hint footer, same split", pane: claudeBashPermissionWrappedFooterPane},
		{name: "claudeBashPermissionFloorPane", width: 20, note: "claude's own repaint residue on the decline row", pane: claudeBashPermissionFloorPane},
	},
	"claude/prompt/plan": {
		{name: "claudePlanPane", width: 0, note: "", pane: claudePlanPane},
	},
	"claude/prompt/model-error": {
		{name: "claudeModelErrorPane", width: 0, note: "", pane: claudeModelErrorPane},
	},

	"codex/gate/trust":      codexTrustGateLadder,
	"codex/prompt/approval": codexApprovalLadder,

	// #713. The first gemini entry in this table, and the only gemini surface any pane has
	// rendered — its busy and confirmation matchers stay exempt below. The ladder stops at 24
	// on purpose: 20 was driven too and is a MISS, held as negative evidence in
	// gemini_pane_test.go rather than dropped, because a table that silently omits the rung
	// it fails at is the #648 defect one level up.
	// Both ladders: the width rungs (height 40) and the height rungs (19 and 24), which are
	// separate captures rather than a second table because #713 shipped twice — once on each
	// axis — and a coverage map that held only one of them called the gate covered while it
	// missed on seven driven geometries.
	"gemini/gate/trust": append(append([]paneCapture(nil), geminiTrustGateLadder...),
		geminiTrustGateOverflowLadder...),

	// A key of its own, which is the whole point of Gate.Name (#717). The first draft
	// appended these rungs to the trust gate's key on the reasoning that GateUp is true if
	// EITHER gate matches, so one key could carry both — and the coverage test rejected it
	// immediately, because fires() asks the NAMED gate and geminiTrustGateVisible does not
	// fire on the nudge. That is the silent half-coverage the discriminator exists to
	// refuse, caught on its first use.
	"gemini/gate/ide-nudge": geminiIdeNudgeLadder,

	"agy/gate/trust": {
		{name: "agyTrustGatePane", width: 120, note: "", pane: agyTrustGatePane},
		{name: "agyTrustGateNarrowPane", width: 28, note: "question truncated away", pane: agyTrustGateNarrowPane},
		{name: "agyTrustGateSubOptionPane", width: 24, note: "option row itself truncated", pane: agyTrustGateSubOptionPane},
	},
	// The confirmation ladder is busy evidence too, and that is not an accident of these
	// captures: agy keeps "esc to cancel" in its dialogs' OWN footer, which is why an
	// unmatched agy dialog latches the row Working rather than falling back to idle — the
	// agy "confirmation" matcher's own comment in registry.go is where that is written down,
	// not the BusyMarkers block. Listing the ladder here costs nothing and takes agy/busy
	// from proven-at-120 to proven-at-20.
	"agy/busy": {
		{name: "agyBusyPane", width: 120, note: "streaming turn", pane: agyBusyPane},
		{name: "agyConfirmPane", width: 120, note: "dialog footer", pane: agyConfirmPane},
		{name: "agyConfirmNarrowPane", width: 40, note: "dialog footer", pane: agyConfirmNarrowPane},
		{name: "agyConfirmNarrowestPane", width: 28, note: "dialog footer", pane: agyConfirmNarrowestPane},
		{name: "agyConfirmSubHintPane", width: 24, note: "dialog footer", pane: agyConfirmSubHintPane},
		{name: "agyConfirmFloorPane", width: 20, note: "dialog footer", pane: agyConfirmFloorPane},
	},
	"agy/prompt/confirmation": {
		{name: "agyConfirmPane", width: 120, note: "", pane: agyConfirmPane},
		{name: "agyConfirmNarrowPane", width: 40, note: "long command, question out of window", pane: agyConfirmNarrowPane},
		{name: "agyConfirmNarrowestPane", width: 28, note: "", pane: agyConfirmNarrowestPane},
		{name: "agyConfirmSubHintPane", width: 24, note: "hint cut to \"tab Ame\"", pane: agyConfirmSubHintPane},
		{name: "agyConfirmFloorPane", width: 20, note: "hint ends exactly at the literal", pane: agyConfirmFloorPane},
	},
}

// paneCoverageExempt is the other direction, and it exists because a `continue` on "no
// fixtures" would make this invariant vacuous for a third of the matcher table while reading
// as full coverage — which is the bug #648 is about, one level up. Asserted with
// require.Equal on the exact set, mirroring TestAutoTapRequiresAnAnchoredMatcher: adding an
// exemption must be as loud as breaking the rule.
//
// Every entry means "no VERBATIM CAPTURE exists for this matcher", never "this matcher is
// fine". A hand-composed pane is not a capture — codex_pane_test.go's own header puts it
// best, "a composed pane cannot falsify a predicate it was written to satisfy" — so
// promoting one here would launder a guess into evidence.
var paneCoverageExempt = map[string]string{
	"claude/prompt/permission-network": "shape constructed from the 2.1.210 bundle; a live capture needs sandbox mode " +
		"(TestClaudeNetworkPermissionNet says so in its own first comment)",
	"claude/prompt/login-error": "reaching this dialog means revoking auth; the literal was confirmed in the " +
		"bundle instead, and no pane has ever rendered it here",
	"claude/prompt/selection": "pinned only against hand-composed panes (TestClaudePrompts), never a capture",
	"codex/busy":              "pinned only against a hand-composed pane (TestCodexBusyMarker), never a capture",

	// gemini/gate left this map in #713 — driven at 80/40/24, plus a 20 that misses. The two
	// below did not, and the distinction is the point: reaching either needs auth and a real
	// turn, where the gate needed only a startup screen. Their literals are present in the
	// 0.55.1 bundle, which is exactly the evidence the gate's own literal had before it was
	// found to have rotted, so read these as unverified rather than as fine.
	"gemini/busy":                "still bundle-grep only at 0.55.1; the loading row needs a live streaming turn",
	"gemini/prompt/confirmation": "still bundle-grep only at 0.55.1; the dialog needs a live tool confirmation",

	// Not because aider is width-immune — it is not, and saying so would be the kind of
	// comfortable claim this file exists to stop. aiderConfirmVisible requires the
	// bottom-most non-empty line to END in "]:", so a narrow hard wrap that leaves the
	// bracket suffix on its own line kills the matcher exactly the way agy's truncation
	// did. The reason is simply that no ladder has ever been driven: its shapes were
	// captured live at 0.86.2, at a width nobody wrote down.
	"aider/gate/docs":      "no width ladder driven; its one pinned pane is an interpreted line of unrecorded width",
	"aider/prompt/confirm": "no width ladder driven; its shapes are inline single-line cases in TestAiderConfirmShapes, not named captures",
}

// wantRungs is the payoff: "the floor is 20" stops being a sentence in a comment and becomes a
// list this suite computes from real captures and real predicates.
//
// Each entry holds every recorded capture width at which that matcher's production predicate
// still fires — ascending, duplicates kept, one number per width-bearing capture. The FIRST is
// the floor. Read it as evidence, not as a guarantee: a matcher proven at 28 is not known to
// survive the 24 a 70-column terminal produces; it is merely untested there.
// claude/prompt/permission floored at 28 while its permission-local sibling reaches 20 is
// exactly the kind of thing #648 wanted visible instead of buried, and it is a gap in the
// EVIDENCE rather than a known cliff: claude's fetch dialog has simply never been driven below
// 28 (#666 does not close it — reaching that dialog needs a live fetch, not a resize).
//
// WHY THE WHOLE LIST rather than the minimum this used to hold. A floor alone stops being loud
// the moment a key owns two captures below its most interesting one, and #666 walked straight
// into that: with claude/gate pinned at 28, deleting claudeMCPNarrowPane raised the computed
// floor and reddened the suite, but once two rungs were driven to 20 the same deletion left
// the minimum at 20 and the suite green — silently discarding the only evidence at the 28 an
// 80-column terminal with the list dragged wide actually produces, and the exact width #340
// measured as a live gate miss. Pinning every rung restores that for every key at once: a
// deleted capture changes the list no matter what else the key holds.
//
// Its limit, so the list is not read as fuller than it is: a capture whose provenance records
// NO width contributes nothing here, so deleting one of those is still silent — only
// TestEveryDeclaredMatcherIsCoveredOrExempt notices, and only once the last one goes.
//
// Nor is a floor a promise about everything above it. Codex WRAPS, so a wider rung can fail
// where a narrower one passes: line-budget effects are not monotonic in width, which is why
// every rung is asserted rather than just the narrowest.
//
// A narrower first entry means new evidence was driven and captured. A wider one, or a rung
// vanishing from the middle, means either a literal got wider or a capture was deleted — and
// both should be loud.
var wantRungs = map[string][]int{
	"claude/gate/startup":            {20, 20, 28, 40, 110, 110},
	"claude/busy":                    {100, 100},
	"claude/prompt/permission":       {28, 100},
	"claude/prompt/permission-local": {20, 20, 29, 29, 30},

	"codex/gate/trust":      {20, 24, 28, 40, 60, 120},
	"codex/prompt/approval": {20, 24, 28, 40, 60, 120},

	// Floors at 24, and the rung below it was driven rather than left unknown: at 20 the
	// selector rows truncate to "Trust fo…" / "Don't tr…" and the gate misses. See
	// TestGeminiTrustGateOptionRowsAreTruncatedAtWidth20.
	//
	// agy's gate below stops at 24 too and this used to call that the same floor "for the same
	// mechanism". It is not the same fact: 24 is the narrowest width agy's gate has ever been
	// DRIVEN at, while agy/busy and agy/prompt/confirmation both reach 20 — an evidence gap,
	// not a measured cliff. Gemini's 24 is a cliff, because 20 was driven and misses.
	//
	// The trust dialog's width ladder (24/40/80) plus its two HEIGHT rungs — the second 24
	// and the 45 (geminiTrustGateOverflowLadder), driven at pane heights 24 and 19 where the
	// dialog overflows and gemini draws a hint below its bottom border. They are listed here
	// because they are width-bearing captures like any other, but the axis they were driven
	// for is not one this table can express — geminiOverflowPaneHeights records it.
	"gemini/gate/trust": {24, 24, 40, 45, 80},

	// The IDE nudge floors four columns lower than its sibling on the same adapter, and the
	// difference is measured rather than lucky: the trust dialog's option rows interpolate a
	// directory name that pushes them past the cut at 20, while the nudge's rows are fixed
	// strings. Two gates, two floors, which is why floors are per-KEY and not per-adapter.
	"gemini/gate/ide-nudge": {20, 24, 40, 80},

	"agy/gate/trust":          {24, 28, 120},
	"agy/busy":                {20, 24, 28, 40, 120, 120},
	"agy/prompt/confirmation": {20, 24, 28, 40, 120},
}

// keysWithNoRecordedCaptureWidth are covered by real captures whose provenance never wrote
// down the width they were driven at. They prove their matcher fires; they prove nothing
// about width, so they are absent from wantRungs. Listing them is the point: an undocumented
// capture width is the same defect as an undocumented anything else, and this is where it
// stops being invisible.
//
// claude/prompt/permission-local left this list in #666 — the first entry to, the list having
// been born in #665 with exactly the three it started with — and the shape of its departure is
// what the list is for. It was named here as the likeliest live defect in the set, because it
// keys on the footer PAIR "Esc to cancel" + "Tab to amend", "Tab to amend" ends at
// column 29 in claudeWritePermissionPane, and registry.go records claude truncating a footer
// on overflow ("at width 30 a busy 2.1.210 pane reads '⏸ manual mode on · esc to …'"), so the
// pair looked certain to die just under 30 — above the 28 claude's fetch dialog is already
// captured at. Driving it (2.1.228, widths 120..20, both dialog shapes) FALSIFIED that: the
// footer wraps rather than truncating, a flatten reassembles the pair either side of the wrap,
// and the matcher holds to 20. The reasoning that failed was reading a note about the status
// line BELOW the input box as a fact about the dialog's own footer; one CLI renders more than
// one way, and only the region you drove is the region you know.
//
// Which flatten reassembles it is not uniform across the ladder, and the 20 rungs — the ones
// this floor rests on — are NOT the segment one. footerVisibleInSegments segments only where a
// box border survives its window, and by 20 none does, so those rungs land on the flat
// workChromeLines fallback, which the shell shape's three-piece footer fills exactly.
// registry_test.go TestPermissionLocalFooterFlattenBudget measures that; read the mechanism
// there rather than inferring it from the 20 in the table above.
//
// What remains is what nobody has driven, and neither is a resize away — which is the whole
// reason they outlived the one that was. plan needs a plan-mode turn to run to its approval
// dialog. model-error is the cheaper of the two and should be said so rather than filed as
// hard: registry.go records it being reached with `claude --model __atrium_probe__` followed
// by a prompt, so a ladder is one bogus-model session away. It is transcript content rather
// than a dialog, which makes its width behaviour a different question from this one's, not an
// expensive one.
var keysWithNoRecordedCaptureWidth = []string{
	"claude/prompt/model-error",
	"claude/prompt/plan",
}

// requiredCoverageKeys derives, from the registry itself, every matcher key that owes
// evidence. Walking `registry` rather than listing adapters is what makes a NEW adapter
// covered the day it is added instead of the day someone remembers this file — the same
// property that has kept TestAutoTapRequiresAnAnchoredMatcher from ever drifting.
func requiredCoverageKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, a := range registry {
		gateKeys, err := gateCoverageKeys(a)
		require.NoError(t, err)
		keys = append(keys, gateKeys...)
		if len(a.BusyMarkers) > 0 {
			keys = append(keys, string(a.Key)+"/busy")
		}
		for _, m := range a.Prompts {
			keys = append(keys, string(a.Key)+"/prompt/"+m.Name)
		}
	}
	sort.Strings(keys)
	return keys
}

// adapterFor resolves a coverage key's adapter from the registry.
func adapterFor(t *testing.T, key string) (*Adapter, string) {
	t.Helper()
	adapterKey, kind, _ := strings.Cut(key, "/")
	for _, cand := range registry {
		if string(cand.Key) == adapterKey {
			return cand, kind
		}
	}
	require.Fail(t, "coverage key names no adapter in the registry", "key %q", key)
	return nil, ""
}

// gateCoverageKeys derives one coverage key per GATE, and errors on a gate with no Name.
//
// A pure function of the adapter rather than an assertion inline in requiredCoverageKeys,
// because the Name requirement guards a gate NOBODY HAS DECLARED YET: every gate in the
// registry has a name today, so an inline require would be a line no test can fail. Extracted,
// TestGateCoverageKeysRefuseANamelessGate feeds it one — the insertion mutation an omission
// needs.
//
// One key per gate, not per adapter. Until #717 every adapter declared exactly one, the key was
// "<adapter>/gate", and requiredCoverageKeys carried a require.Len(…, 1) whose failure message
// was an instruction: give Gate a discriminator before adding a second. gemini's IDE nudge is
// that second gate. Without the discriminator a ladder proving the trust dialog would satisfy
// "gemini/gate" while the nudge went uncovered — the silent half-coverage that assertion
// existed to refuse, and which the first draft of this change walked straight into.
func gateCoverageKeys(a *Adapter) ([]string, error) {
	keys := make([]string, 0, len(a.Gates))
	for _, g := range a.Gates {
		if g.Name == "" {
			return nil, fmt.Errorf("%s declares a gate with no Name; its coverage key "+
				"would be \"%s/gate/\" and could not say which gate it means", a.Key, a.Key)
		}
		keys = append(keys, string(a.Key)+"/gate/"+g.Name)
	}
	return keys, nil
}

// TestGateCoverageKeysRefuseANamelessGate is the insertion mutation for the Name requirement:
// the registry cannot supply a counterexample, so the test constructs one.
func TestGateCoverageKeysRefuseANamelessGate(t *testing.T) {
	ok, err := gateCoverageKeys(&Adapter{Key: "demo", Gates: []Gate{
		{Name: "trust", Contains: []string{"x"}},
		{Name: "nudge", Contains: []string{"y"}},
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"demo/gate/trust", "demo/gate/nudge"}, ok,
		"two gates, two keys — which is the whole point of the discriminator")

	_, err = gateCoverageKeys(&Adapter{Key: "demo", Gates: []Gate{{Contains: []string{"x"}}}})
	require.Error(t, err, "a nameless gate must be refused, not silently keyed \"demo/gate/\"")
	assert.Contains(t, err.Error(), "demo")
}

// gateFor returns the named gate of an adapter.
func gateFor(t *testing.T, a *Adapter, name string) Gate {
	t.Helper()
	for _, g := range a.Gates {
		if g.Name == name {
			return g
		}
	}
	require.Fail(t, "adapter declares no such gate", "%s has no %q", a.Key, name)
	return Gate{}
}

// gateMatches runs ONE gate's own predicate the way GateUp would run it — Match on the whole
// cleaned pane, Contains on the adapter's flattened gate window — without letting an earlier
// gate answer for it.
func gateMatches(a *Adapter, g Gate, content string) bool {
	if g.Match != nil {
		return g.Match(content)
	}
	return g.matches(flattenChrome(content, a.gateWindow()))
}

// promptMatcherFor returns the named matcher of an adapter.
func promptMatcherFor(t *testing.T, a *Adapter, name string) PromptMatcher {
	t.Helper()
	for _, m := range a.Prompts {
		if m.Name == name {
			return m
		}
	}
	require.Fail(t, "adapter declares no such prompt matcher", "%s has no %q", a.Key, name)
	return PromptMatcher{}
}

// fires runs the production predicate named by key against one capture.
//
// For prompts it asks the NAMED matcher directly (m.matches) rather than reading the winner
// out of DetectPrompt, and the distinction is load-bearing in both directions. DetectPrompt
// returns the FIRST matcher that matches, so on any pane an earlier matcher also claims, a
// later one cannot be proven through it at all — the width question ("does this literal still
// reach?") would silently become an ordering question. Asking the named matcher is a claim
// about that matcher's literals, which is what #648 is about; the ordering is a separate
// claim, asserted separately below so a capture filed under the wrong matcher is reported
// rather than silently credited to whichever matcher happened to win.
//
// Which pane an ordering hides is not decidable from the table alone. claude's "permission"
// and "permission-network" both key on the fetch/network family and "permission" is declared
// first in claude's Prompts, but it is the STRICTER of the two — it requires the dialog's
// own fetch title — so on the sandbox's "Do you want to allow this connection?" it misses and
// permission-network wins. That is exactly the arrangement claudeNetworkPermissionVisible's
// own comment documents, and the reason permission-network exists. It is shadowed on the
// fetch pane and live on the sandbox one, which is why this asks each matcher by name
// instead of reasoning about the order.
func fires(t *testing.T, key string, c paneCapture) bool {
	t.Helper()
	a, kind := adapterFor(t, key)

	switch {
	case strings.HasPrefix(kind, "gate/"):
		want := strings.TrimPrefix(kind, "gate/")
		g := gateFor(t, a, want)
		if !gateMatches(a, g, c.pane) {
			return false
		}
		// The ordering half, for the same reason the prompt branch has one. GateUp
		// returns the FIRST gate that matches, so on a pane an earlier gate also claims,
		// a later one could never be proven through it — the width question would
		// silently become an ordering question. gemini is the first adapter where that
		// is reachable at all, having two.
		//
		// What the gateMatches call above buys, stated precisely because a mutation
		// measured it: asking GateUp alone reaches the same PASS/FAIL on every input,
		// since a named gate that matches always makes GateUp true and the assertion
		// below catches the rest. What it buys is the message — a capture whose own gate
		// misses reports "does not fire at this width", which is the #648 question,
		// instead of "matched by gate X first", which is a different diagnosis. It is
		// kept for that and for symmetry with the prompt branch, not because the verdict
		// turns on it.
		won, ok := a.GateUp(c.pane)
		require.True(t, ok, "%s: %s matches %q but GateUp found no gate at all", key, c.name, want)
		require.Equal(t, want, won.Name,
			"%s: %s (%s) is matched by gate %q first, so GateUp never reaches %q — this "+
				"capture cannot be evidence for the gate it is filed under",
			key, c.name, describeWidth(c.width), won.Name, want)
		return true
	case kind == "busy":
		return a.HasBusyMarker(c.pane)
	case strings.HasPrefix(kind, "prompt/"):
		want := strings.TrimPrefix(kind, "prompt/")
		m := promptMatcherFor(t, a, want)
		if !m.matches(c.pane) {
			return false
		}
		// The ordering half. The matcher's own literals hold; if some earlier matcher
		// also claims this pane, the capture is not exercising what it is filed under.
		won, ok := a.DetectPrompt(c.pane)
		require.True(t, ok, "%s: %s matches %q but DetectPrompt found no prompt at all", key, c.name, want)
		require.Equal(t, want, won.Name,
			"%s: %s (%s) is matched by %q first, so DetectPrompt never reaches %q — this "+
				"capture cannot be evidence for the matcher it is filed under",
			key, c.name, describeWidth(c.width), won.Name, want)
		return true
	}
	require.Fail(t, "coverage key has no predicate", "key %q", key)
	return false
}

// narrowAmbiguous measures the terminals these panes were actually captured on: ambiguous
// East Asian width counts as ONE cell.
//
// The explicit Condition is not fussiness — it is the difference between a stable test and
// one that fails on somebody's laptop for a reason absent from the diff. runewidth.StringWidth
// (and lipgloss.Width, and ansi.StringWidth) all resolve through a package-level default whose
// EastAsianWidth is set from the RUNEWIDTH_EASTASIAN environment variable at init. Every agy
// and codex fixture is full of EAW=Ambiguous runes — · U+00B7, ↑↓ U+2191/93, ─ U+2500,
// ● U+25CF — so with that variable set the default condition measures agyConfirmFloorPane at
// about 30 and this file fails on nearly every fixture, for a reason nobody changed.
//
// Ambiguous=1 is not merely the convenient choice, it is the one the captures themselves
// prove: agyConfirmFloorPane came off a 20-column pane whose hint is exactly
// "  ↑/↓ Navigate · tab". If · and ↑ were two cells that line could not have fitted.
// TestPaneWidthMeasuresTheTerminalTheCapturesCameFrom pins that reasoning so the choice cannot
// be quietly reverted.
var narrowAmbiguous = &runewidth.Condition{EastAsianWidth: false, StrictEmojiNeutral: true}

// paneWidth is the capture's widest line in display cells.
func paneWidth(pane string) int {
	widest := 0
	for _, line := range strings.Split(pane, "\n") {
		if w := narrowAmbiguous.StringWidth(line); w > widest {
			widest = w
		}
	}
	return widest
}

// The measurement model, asserted against the evidence rather than asserted in a comment. A
// 20-column capture that measures 20 is only possible if its ambiguous-width runes are one
// cell each — so this fails if the condition above is ever widened, and it holds whatever
// RUNEWIDTH_EASTASIAN happens to be set to in the environment running it.
func TestPaneWidthMeasuresTheTerminalTheCapturesCameFrom(t *testing.T) {
	require.Equal(t, 20, paneWidth(agyConfirmFloorPane),
		"agy's floor capture came off a 20-column pane; measuring it wider means ambiguous "+
			"runes are being counted as two cells, which no terminal that produced these "+
			"captures did")
	require.Equal(t, 1, narrowAmbiguous.RuneWidth('·'), "U+00B7 is EAW=Ambiguous")
	require.Equal(t, 1, narrowAmbiguous.RuneWidth('↑'), "U+2191 is EAW=Ambiguous")
	require.Equal(t, 1, narrowAmbiguous.RuneWidth('─'), "U+2500 is EAW=Ambiguous")
}

// A capture may render narrower than the pane it was taken from — `capture-pane -p` strips
// trailing spaces, and a dialog need not reach the edge (codexTrustGatePane120's widest line
// is 111, codexApprovalPane120's is 94). So the check is an inequality, and it is pointed at
// the one direction that can lie: a capture LABELLED narrower than it renders would credit a
// matcher with surviving a width it was never shown, and wantRungs would report that
// invention as evidence.
// It stays an inequality, deliberately. An earlier draft also held each key's NARROWEST
// capture to equality, to close the relabel hole below — but that assertion is unsound rather
// than merely strict: it forbids the one direction this file calls good news. A newly driven
// narrow rung whose dialog does not happen to fill the pane measures under its label, is
// labelled entirely correctly, and would be rejected with "its label must be the width it was
// driven at". That every narrow rung is tight against its label today is an observation about
// five keys, not a property of narrow captures.
//
// The relabel hole is real — mislabel a capture narrower than it was driven and this test
// passes while the floor drops on no new evidence — but it is closed by wantRungs instead,
// which is the right place: a relabel changes a computed rung, so it cannot land without a
// second, deliberate edit to a pinned number in the same review.
func TestCaptureWidthsAreNotOverstated(t *testing.T) {
	for key, captures := range paneCoverage {
		for _, c := range captures {
			if c.width == 0 {
				continue // provenance records no width; nothing to contradict
			}
			require.LessOrEqual(t, paneWidth(c.pane), c.width,
				"%s: %s renders wider than the width %d it is filed under — either the label "+
					"is wrong or the capture is not the pane it claims to be",
				key, c.name, c.width)
		}
	}
}

// Nothing in Go binds a paneCapture's name or width to the pane it carries: the fields are
// prose beside a symbol, and the compiler checks only the symbol. Swap one rung's pane for a
// sibling's and every other assertion here still holds — the ladder still lists six rungs, the
// widths still ascend, and the predicate still fires, because the sibling is a real capture of
// the same dialog. The key would then report six driven widths while exercising five panes.
//
// Distinctness is what a test can actually check, and it catches that: a duplicated pane means
// a rung's evidence was replaced by a copy of its neighbour's.
func TestNoCoverageKeyListsThePaneOrTheNameTwice(t *testing.T) {
	for key, captures := range paneCoverage {
		requireDistinctCaptures(t, key, captures)
	}
}

// requireDistinctCaptures is that check as a helper, because a []paneCapture that does NOT
// feed paneCoverage needs it just as much: codex's composer ladders are negative evidence and
// so are absent from the table, which left the guard one file short of the rungs it was
// written for.
func requireDistinctCaptures(t *testing.T, label string, captures []paneCapture) {
	t.Helper()
	panes := map[string]string{}
	names := map[string]bool{}
	for _, c := range captures {
		if prev, dup := panes[c.pane]; dup {
			require.Fail(t, "a capture list holds the same pane twice",
				"%s: %s and %s carry identical panes — one rung's evidence is a copy of "+
					"the other's, so this list proves one fewer width than it names",
				label, prev, c.name)
		}
		panes[c.pane] = c.name
		require.False(t, names[c.name], "%s lists the name %s twice", label, c.name)
		names[c.name] = true
	}
}

// The coverage question, asked of the registry rather than of this file: which declared
// matchers own no captured evidence? A `continue` on "no fixtures" would answer it silently
// and read as full coverage; require.Equal on the exact set makes every gap a decision
// somebody signed for.
func TestEveryDeclaredMatcherIsCoveredOrExempt(t *testing.T) {
	required := requiredCoverageKeys(t)

	// make, not a nil slice: require.Equal distinguishes []string{} from []string(nil), so a
	// nil accumulator would redden this test on the day the last exemption is finally driven
	// and paneCoverageExempt empties — punishing the only direction this file calls good news.
	uncovered := make([]string, 0)
	for _, key := range required {
		if _, ok := paneCoverage[key]; ok {
			continue
		}
		uncovered = append(uncovered, key)
	}

	exempt := make([]string, 0, len(paneCoverageExempt))
	for key := range paneCoverageExempt {
		exempt = append(exempt, key)
	}
	sort.Strings(exempt)

	require.Equal(t, exempt, uncovered,
		"the set of matchers with no captured pane changed. Adding one means an adapter "+
			"declares a literal nothing has ever seen render — drive it (scripts/drive-agent.sh) "+
			"or add it to paneCoverageExempt WITH the reason. Removing one means deleting "+
			"evidence")

	// The mirror: coverage that no longer answers to a declared matcher. Without this a
	// renamed matcher leaves its captures pointing at nothing, uncovered picks up the new
	// name, and the old entry sits there looking like proof.
	inRegistry := make(map[string]bool, len(required))
	for _, key := range required {
		inRegistry[key] = true
	}
	var orphans []string
	for key := range paneCoverage {
		if !inRegistry[key] {
			orphans = append(orphans, key)
		}
	}
	for key := range paneCoverageExempt {
		if !inRegistry[key] {
			orphans = append(orphans, key)
		}
	}
	sort.Strings(orphans)
	require.Empty(t, orphans,
		"these keys name no matcher the registry declares — a rename left their captures "+
			"pointing at nothing, which reads as coverage while proving nothing")
}

// THE invariant. Every declarative literal an adapter ships must still fire on every pane
// this project has actually captured — including the narrow ones, which is the whole point:
// agy's gate literal passed every test in the tree until a 24-column capture existed.
//
// The predicate is the production one and the pane is verbatim, so this measures the matcher
// rather than the fixture set. Widen a literal in registry.go past what a narrow rung renders
// and this fails, naming the adapter, the matcher, the capture and the width.
func TestEveryCoveredMatcherFiresAtEveryCapturedWidth(t *testing.T) {
	for key, captures := range paneCoverage {
		for _, c := range captures {
			t.Run(key+"/"+c.label(), func(t *testing.T) {
				require.True(t, fires(t, key, c),
					"%s does not fire on %s, captured at width %s. A matcher that misses a "+
						"width Atrium can hand an agent fails silently on a real dialog — and "+
						"if this is a gate, the truncated option row still reads as a composer, "+
						"so the queued first prompt is typed into it (#512)",
					key, c.name, describeWidth(c.width))
			})
		}
	}
}

// "The floor is 20" was a sentence in a comment. Here the whole rung list is a value this
// suite computes from the captures and the predicates, and compares against what we claim to
// have proven.
//
// This is not redundant with the test above even though that one requires every capture to
// fire. That test cannot see a capture that is no longer there: delete a rung and it passes
// with fewer subtests, saying nothing. Evidence deletion is the direction this test exists
// for, which is why it pins every width and not just the smallest — a minimum only notices a
// deletion at the bottom, and only when nothing else sits there. See wantRungs.
func TestProvenWidthFloorsAreComputedNotClaimed(t *testing.T) {
	proven := map[string][]int{}
	unrecorded := make([]string, 0) // see the nil-vs-empty note in the coverage test above
	for key, captures := range paneCoverage {
		rungs := make([]int, 0, len(captures))
		for _, c := range captures {
			if c.width == 0 || !fires(t, key, c) {
				continue
			}
			rungs = append(rungs, c.width)
		}
		if len(rungs) == 0 {
			unrecorded = append(unrecorded, key)
			continue
		}
		sort.Ints(rungs)
		proven[key] = rungs
	}
	sort.Strings(unrecorded)

	require.Equal(t, wantRungs, proven,
		"the widths each matcher is PROVEN at changed. A rung that vanished means a capture "+
			"was deleted or a literal grew past it — including a rung in the MIDDLE, which no "+
			"floor would have reported; a narrower first entry means new evidence was driven, "+
			"and is the only direction to celebrate")

	require.Equal(t, keysWithNoRecordedCaptureWidth, unrecorded,
		"these matchers have captures but no recorded capture width, so they are evidence "+
			"about shape and not about width. Re-driving one at a known ladder is how it leaves "+
			"this list")
}

// Coverage is per MATCHER, and a matcher's Any list is an alternation: one entry present is
// enough to fire, so a capture proves the alternative it happens to render and says nothing
// about its siblings. Left unstated, "codex/prompt/approval covered at 20" reads as though
// all three decline wordings were verified there; two of them have never been seen at any
// width.
//
// This is a ledger, not a failure. Some alternatives are deliberately unreachable — a
// deprecation-window variant kept for an older CLI has, by construction, no capture from the
// current one, and registry.go's header calls that remediation ADDITIVE on purpose. Pinning
// the set is what keeps that a decision instead of a blind spot: an entry leaving the list
// means new evidence, an entry arriving means a literal was added on faith.
func TestUnprovenMatcherAlternativesArePinned(t *testing.T) {
	unproven := map[string][]string{}
	for key, captures := range paneCoverage {
		a, kind := adapterFor(t, key)
		if !strings.HasPrefix(kind, "prompt/") {
			continue
		}
		m := promptMatcherFor(t, a, strings.TrimPrefix(kind, "prompt/"))
		// A Match matcher ignores All/Any entirely (PromptMatcher.matches), so its
		// literals live inside the func where no walk can reach them. Those are named in
		// this file's header as a stated limit, not silently folded in here.
		if m.Match != nil || len(m.Any) < 2 {
			continue
		}
		for _, lit := range m.Any {
			seen := false
			for _, c := range captures {
				if strings.Contains(flattenChrome(c.pane, m.Window), lit) {
					seen = true
					break
				}
			}
			if !seen {
				unproven[key] = append(unproven[key], lit)
			}
		}
	}

	require.Equal(t, map[string][]string{
		// Deliberate, and registry.go says so where the matcher is declared: "No, keep
		// planning" is the binary's ALTERNATE label for the feedback option, kept for
		// releases that render it. claudePlanPane shows the other two.
		"claude/prompt/plan": {"No, keep planning"},
		// The Pro-plan access restriction. claudeModelErrorPane was driven with a bogus
		// --model, which is the 404 branch; reaching this one needs a Pro-plan account
		// asking for a model it may not have.
		"claude/prompt/model-error": {"is not available with the Claude Pro plan"},
		// Two of codex's three decline wordings. The ladder drove one command, and which
		// decline label renders depends on the sandbox/approval mode that command needed —
		// so covering these means driving a different command, not a different width.
		"codex/prompt/approval": {"No, continue without", "No, but continue without"},
	}, unproven,
		"the set of matcher alternatives no capture has ever rendered changed. A new entry "+
			"means a literal was added without a pane showing it; a departing entry means "+
			"something got driven, and only that direction is good news")
}
