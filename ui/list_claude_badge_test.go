package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The Claude account badge's width behaviour (#671). The presence/absence half lives
// in list_test.go's TestRender_AccountBadge; these guards pin what the badge is
// allowed to cost on a pane too narrow to hold it and the session name both.
//
// Every claim here is either a measurement taken on BOTH sides of the badge or a
// byte comparison, never a total-row-width assertion: line 1's name column is
// flexible, so it absorbs an added chip and a width assertion passes unchanged while
// the name silently loses cells (#478/#479 → #501).

// claudeAcctName is the badge's account, and the name the issue measured with. No
// other cell on these rows can contain it, so "is the badge shown?" is a substring
// test that can actually fail.
const claudeAcctName = "quantivly-work"

// fullyBadgedClaudeRow renders a claude session at `width` columns carrying the
// worst-case line-1 cluster the issue measured — account, AUTO, model, effort,
// permission — under `name`, ANSI-stripped. `withClaude` toggles the one chip under
// test; no agy account is pinned, so this row exercises the Claude rung alone.
func fullyBadgedClaudeRow(t *testing.T, width int, name string, withClaude bool) string {
	t.Helper()
	return claudeRowMuted(t, width, name, withClaude, false)
}

// claudeRowMuted is fullyBadgedClaudeRow with control over whether a chip already
// precedes the account badge. It matters for cost: the AUTO chip prepends a
// one-column pad only when the cluster is already non-empty, so on a bare cluster the
// badge switches that pad on and pays for it too.
func claudeRowMuted(t *testing.T, width int, name string, withClaude, muted bool) string {
	t.Helper()
	acct := ""
	if withClaude {
		acct = claudeAcctName
	}
	inst := agyInst(t, name, "/tmp/api", "claude", acct, "" /* no agy route */)
	inst.AutoYes = true
	inst.SetMuted(muted)
	inst.SetModeMeta("acceptEdits")
	inst.SetModelMeta("claude-opus-5", transcript.Stamp{Path: "m", Size: 1})
	inst.SetEffortMeta("max")
	return agyRow(t, width, inst)
}

// TestRender_ClaudeBadgeYieldsRatherThanErasingTheName is the invariant that makes
// the badge safe on a narrow pane, and the guard for the defect #671 reported:
// composeLine can only shrink line 1's single flex segment, so an unbounded account
// name took the name to ZERO and then overhung. Measured on dc8ae48 across renderer
// widths 40–52, a fully badged claude row rendered 53 cells with no session name at
// all, overhanging by up to 13 — which the panel clips, taking the live
// permission-mode chip and the agent icon with it. Line 1 exists to say which
// session this is.
//
// Two claims, swept across the whole width range rather than sampled: wherever the
// badge is shown the name is non-empty, and wherever it is dropped the row is
// BYTE-IDENTICAL to the same session with no Claude account. The second is what
// makes the yield exactly reversible — the badge cannot make a narrow pane worse
// than it is without it.
//
// The name is a run of "Z" because no other cell on the row can contain one, so "is
// any of the name left?" is a substring test that can fail. A descriptive name
// cannot carry that assertion: every letter in one also appears in "opus 5", "max"
// or "accept-edits", so the check would pass against a row whose name was emptied.
func TestRender_ClaudeBadgeYieldsRatherThanErasingTheName(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const name = "ZZZZZZZZZZZZZZZZZZZZ"
	shown := 0
	for w := 10; w <= 140; w++ {
		row := fullyBadgedClaudeRow(t, w, name, true)
		line1 := strings.Split(row, "\n")[0]
		if strings.Contains(line1, claudeAcctName) {
			shown++
			require.Containsf(t, line1, "Z",
				"at %d columns the badge is shown but the name is gone:\n%s", w, row)
			continue
		}
		require.Equalf(t, fullyBadgedClaudeRow(t, w, name, false), row,
			"at %d columns the badge yielded, so the row must be byte-identical to a no-account row", w)
	}
	require.Greater(t, shown, 40, "…and the badge must actually be shown somewhere, or this proves nothing")

	// The band the issue measured, logged rather than only computed: in #457 the
	// defect was a chip taking the name to zero across a whole band, and only rendering
	// the band made that obvious. Read these, do not just count them.
	for w := 38; w <= 56; w++ {
		t.Logf("w=%-3d |%s|", w, strings.Split(fullyBadgedClaudeRow(t, w, "my-session-name", true), "\n")[0])
	}
}

// TestRender_ClaudeBadgeYieldsLeavesNoStrandedPad covers the arrangements where
// yielding could leave litter. The AUTO chip and the agent icon each prepend a pad
// when anything precedes them, so on a row whose ONLY earlier chip is the account
// badge, dropping it would strand that pad. The pad is invisible — it abuts
// composeLine's gap — which is exactly why only a byte comparison catches it, and
// why it would otherwise silently cost the name a column.
//
// Both arrangements are covered because they strand different pads: with AUTO the
// orphan is the pad at the AUTO chip, without it the orphan is the pad before the
// agent icon.
func TestRender_ClaudeBadgeYieldsLeavesNoStrandedPad(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	for _, tc := range []struct {
		name    string
		autoYes bool
	}{
		{"the AUTO pad", true},
		{"the agent-icon pad", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func(acct string) *session.Instance {
				inst := agyInst(t, "ZZZZZZZZZZZZZZZZ", "/tmp/api", "claude", acct, "")
				inst.AutoYes = tc.autoYes
				return inst
			}
			yielded := 0
			for w := 10; w <= 60; w++ {
				row := agyRow(t, w, build(claudeAcctName))
				if strings.Contains(row, claudeAcctName) {
					continue
				}
				yielded++
				require.Equalf(t, agyRow(t, w, build("")), row,
					"at %d columns a yielded badge must leave no pad behind", w)
			}
			require.NotZero(t, yielded, "the badge must yield somewhere in this range, or nothing was compared")
		})
	}
}

// TestRender_ClaudeBadgeDoesNotMoveTheRowsFitFloor is the width claim, measured on
// both sides rather than asserted at one convenient column count. The floor is the
// number a flexible name column cannot absorb: the narrowest width at which every
// line still measures exactly, below which the right cluster overhangs and the panel
// clips it. Because the badge yields, that floor must not move at all.
func TestRender_ClaudeBadgeDoesNotMoveTheRowsFitFloor(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	without := minExactWidth(t, func(w int) string { return fullyBadgedClaudeRow(t, w, "n", false) })
	with := minExactWidth(t, func(w int) string { return fullyBadgedClaudeRow(t, w, "n", true) })
	t.Logf("fully badged claude row fit floor: %d columns without the badge, %d with it", without, with)
	require.Equal(t, without, with,
		"a chip that yields cannot raise the width at which the row stops fitting")
}

// TestRender_ClaudeBadgeCostsSlackNotTheName pins WHO pays for those cells at a width
// where the row comfortably fits: the name's flex slack, not the row's width and not
// another chip. Measuring the budget on both sides is what makes this non-vacuous —
// an assertion that "a short name survived" holds just as well against a chip four
// times this size.
//
// The cost is not simply the badge's own cells, and the two cases here are what say
// so. The AUTO chip prepends a one-column pad only when the cluster is already
// non-empty, so on a bare cluster the badge ALSO switches that pad on and the name
// pays 17 for a 16-cell chip; behind a chip that already turned the pad on (a muted
// row) it pays exactly 16. Asserting only the first number would leave the extra
// column unattributed, and asserting only the second would miss it entirely — the
// same invisible pad the yield has to strip to stay byte-reversible.
func TestRender_ClaudeBadgeCostsSlackNotTheName(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const width = 80
	badgeW := ansi.StringWidth(" " + claudeAcctName + " ")
	budget := func(withClaude, muted bool) int {
		return maxWholeNameOf(t, width, func(n string) string {
			return claudeRowMuted(t, width, n, withClaude, muted)
		})
	}

	bareWithout, bareWith := budget(false, false), budget(true, false)
	t.Logf("name budget at %d columns, bare cluster: %d cells without the badge, %d with it",
		width, bareWithout, bareWith)
	require.Equal(t, badgeW+1, bareWithout-bareWith,
		"on a bare cluster the badge costs its own cells plus the AUTO pad it switches on")

	mutedWithout, mutedWith := budget(false, true), budget(true, true)
	t.Logf("name budget at %d columns, behind a muted chip: %d cells without the badge, %d with it",
		width, mutedWithout, mutedWith)
	require.Equal(t, badgeW, mutedWithout-mutedWith,
		"behind a chip that already turned the pad on, the badge costs exactly its own cells")

	require.Greater(t, bareWith, 0, "…and must not take the whole name column at a width the row fits in")

	// The boundary itself, so the number above is a real edge and not a lucky sample.
	atBudget := strings.Repeat("n", bareWith)
	require.Contains(t, strings.Split(fullyBadgedClaudeRow(t, width, atBudget, true), "\n")[0], atBudget)
	over := strings.Repeat("n", bareWith+1)
	require.NotContains(t, strings.Split(fullyBadgedClaudeRow(t, width, over, true), "\n")[0], over,
		"one cell past the budget must truncate")
}

// TestRender_ClaudeBadgeLadderStaysExact walks the row up a width ladder rather than
// asserting one column count: an off-by-one in the right cluster shows up at one
// width and hides at the next. Every rung must measure exactly AND carry the badge,
// so a row that "fits" by yielding below its threshold cannot pass here.
//
// The rungs are anchored to the measured threshold rather than to literals, so the
// ladder keeps testing the boundary if another chip moves it. The rung below it is
// the negative control that makes the threshold mean something: there the badge is
// gone and the name — a run of "Z", for the reason the yield test uses one — is not.
func TestRender_ClaudeBadgeLadderStaysExact(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const name = "ZZZZZZZZZZZZZZZZZ"

	threshold := 0
	for w := 10; w <= 140 && threshold == 0; w++ {
		if strings.Contains(fullyBadgedClaudeRow(t, w, name, true), claudeAcctName) {
			threshold = w
		}
	}
	require.NotZero(t, threshold, "the badge must appear at some width")
	t.Logf("the Claude badge appears from %d columns up", threshold)

	under := fullyBadgedClaudeRow(t, threshold-1, name, true)
	require.NotContains(t, under, claudeAcctName, "one column under the threshold the badge must be gone")
	require.Contains(t, strings.Split(under, "\n")[0], "Z",
		"…and the name it yielded to must still be there")

	for _, w := range []int{threshold, threshold + 1, threshold + 2, threshold + 7, 100, 120} {
		row := fullyBadgedClaudeRow(t, w, name, true)
		for i, line := range strings.Split(row, "\n") {
			require.Equalf(t, w, ansi.StringWidth(line),
				"line %d at %d columns must be exactly %d cells:\n%s", i, w, w, row)
		}
		require.Containsf(t, row, claudeAcctName, "the badge must survive at %d columns", w)
	}
}

// TestListString_ClaudeBadgeCannotBreakThePanelFrame is the frame contract for the
// shape #671 measured — a claude session whose account badge is long enough to erase
// the name — run over a size matrix that deliberately includes widths BELOW the row's
// fit floor. There the right cluster overhangs and the panel's clamp is the only
// thing between an overlong row and a wrapped line, which in Bubble Tea's alt-screen
// renderer is not a cosmetic offset but a ghost row that survives until a full
// repaint. The yield changes what renders across that whole band, so the contract is
// re-pinned rather than inherited from the agy row's matrix.
func TestListString_ClaudeBadgeCannotBreakThePanelFrame(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	for _, w := range []int{20, 30, 40, 55, 65, 80, 120} {
		for _, h := range []int{6, 12, 30} {
			s := spinner.New()
			l := NewList(&s)
			inst := agyInst(t, "a-claude-session-with-a-long-name", "/tmp/api", "claude", claudeAcctName, "")
			inst.AutoYes = true
			inst.SetModeMeta("acceptEdits")
			inst.SetEffortMeta("max")
			l.AddInstance(inst)
			l.SetSize(w, h)

			out := ansi.Strip(l.String())
			lines := strings.Split(out, "\n")
			require.LessOrEqualf(t, len(lines), h, "panel at %dx%d must not exceed its height", w, h)
			for i, line := range lines {
				require.Equalf(t, w, ansi.StringWidth(line),
					"line %d of the %dx%d panel must be exactly %d cells:\n%s", i, w, h, w, out)
			}
		}
	}
}

// TestRender_BadgeYieldOrderIsAgyThenClaude pins the drop order on a row carrying
// BOTH account badges, and it is the guard for a decision the issue left open.
//
// The order is forced rather than chosen. #457 asserts that below its threshold an
// agy-badged row is byte-identical to the same session with no agy account
// (TestRender_AgyBadgeYieldsRatherThanErasingTheName). Drop the Claude badge first
// and that is false: there would be a band where an agy row shows no Claude badge
// while the same session WITHOUT an agy account still shows one — the agy badge
// making the pane worse, which is the property that test exists to deny.
//
// So: no width may show the agy badge while the Claude badge has already gone. The
// second assertion is what stops that from being vacuous — the reverse band, where
// agy has yielded and Claude survives, must be non-empty, or a row that simply never
// dropped either badge would pass.
//
// Both account names are chosen so no other cell on the row can contain them
// ("gemini-3-pro", "max", "accept-edits", the AUTO badge and the agent icon share no
// substring with either).
func TestRender_BadgeYieldOrderIsAgyThenClaude(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	row := func(w int) string {
		inst := agyInst(t, "ZZZZZZZZZZZZZZZZZZZZ", "/tmp/api", "agy", "quantivly", "grav-one")
		inst.AutoYes = true
		inst.SetModeMeta("acceptEdits")
		inst.SetModelMeta("gemini-3-pro", transcript.Stamp{Path: "m", Size: 1})
		inst.SetEffortMeta("max")
		return agyRow(t, w, inst)
	}

	both, agyGoneOnly, neither := 0, 0, 0
	for w := 10; w <= 140; w++ {
		out := row(w)
		hasAgy := strings.Contains(out, "agy:grav-one")
		hasClaude := strings.Contains(out, "quantivly")
		switch {
		case hasAgy:
			require.Truef(t, hasClaude,
				"at %d columns the agy badge outlived the Claude badge; agy must yield first:\n%s", w, out)
			both++
		case hasClaude:
			agyGoneOnly++
		default:
			neither++
		}
	}
	t.Logf("widths 10–140: %d carry both badges, %d dropped agy only, %d dropped both", both, agyGoneOnly, neither)
	require.NotZero(t, both, "the ladder must have a band where both badges fit")
	require.NotZero(t, agyGoneOnly, "…a band where only agy has yielded, or the order claim is vacuous")
	require.NotZero(t, neither, "…and a band where both have yielded, or the Claude rung never ran")
}
