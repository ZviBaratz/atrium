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

// The Antigravity (agy) account badge (#457). The account axes are independent —
// every session is stamped with BOTH a resolved Claude account and a resolved agy
// account (app.spawn calls SetClaudeAccount and SetAgyAccount unconditionally), so
// these guards pin which of the two a given row is allowed to claim, and pin the
// cost of the extra chip in cells.

// agyInst builds an instance running `program` with the given Claude account and
// agy account; either may be "" to leave that axis unstamped.
func agyInst(t *testing.T, title, path, program, claudeAcct, agyAcct string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: path, Program: program})
	require.NoError(t, err)
	if claudeAcct != "" {
		inst.SetClaudeAccount(claudeAcct, "", false)
	}
	if agyAcct != "" {
		inst.SetAgyAccount(agyAcct, "/home/x/.agy-"+agyAcct)
	}
	return inst
}

// agyRow renders one instance through a bare renderer at `width` columns and
// returns it ANSI-stripped.
func agyRow(t *testing.T, width int, inst *session.Instance) string {
	t.Helper()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(width)
	return ansi.Strip(r.Render(inst, 1, false, false))
}

// TestRender_AgyBadgeShownWhenRouted is the presence half of the feature: an agy
// session with a resolved account says so on its row.
func TestRender_AgyBadgeShownWhenRouted(t *testing.T) {
	out := agyRow(t, 80, agyInst(t, "routed", "/tmp/api", "agy", "", "grav-one"))
	require.Contains(t, out, "agy:grav-one", "a routed agy session must name its account on the row")
}

// TestRender_AgyBadgeAbsentWithoutARoute is the absence half — the legacy /
// feature-off state, which is every session that existed before #456 and every
// session of a user with no agy_accounts section.
func TestRender_AgyBadgeAbsentWithoutARoute(t *testing.T) {
	out := agyRow(t, 80, agyInst(t, "unrouted", "/tmp/api", "agy", "", ""))
	require.NotContains(t, out, "agy:", "an unrouted agy session must render no badge")
	require.Len(t, strings.Split(out, "\n"), 2, "…and no extra line")
}

// TestRender_AgyBadgeAbsentOnANonAgySession is the gate that makes the badge a
// truthful claim rather than a stamp. ResolveAgyAccount runs for EVERY session
// (app/app_session.go's spawn calls SetAgyAccount unconditionally), so a Claude
// session in a repo an agy account routes — or any session at all under a
// catch-all agy account — carries a non-empty agyAccount it never uses: the dir is
// consumed only when the launch path resolves the agy adapter
// (session/tmux/tmux.go's `if t.adapter.Key == agent.KeyAgy`). Badging it there
// would report a route that does nothing.
func TestRender_AgyBadgeAbsentOnANonAgySession(t *testing.T) {
	inst := agyInst(t, "claude-session", "/tmp/api", "claude", "", "grav-one")
	require.Equal(t, "grav-one", inst.AgyAccountName(),
		"precondition: the account IS stamped — the row is what must stay silent")
	require.NotContains(t, agyRow(t, 80, inst), "agy:",
		"a session that does not run agy must not advertise an agy route")
}

// TestRender_AgyBadgeAbsentWithoutAPinnedDir is the second half of "a badge must not
// report a route that does nothing". An AgyAccount with no `config_dir` is reachable
// — the accounts overlay's validate() rejects only an empty or duplicate NAME, and
// ResolvedConfigDir expands "" to "" — and the overlay's own routing preview already
// renders that state as "<name> (inherit ambient env)". wrapAgyBwrap no-ops on an
// empty dir (session/tmux/agy.go), so such a session runs on the ambient config and
// is indistinguishable from one with no account at all. Naming it on the row would
// claim an identity the session does not have.
func TestRender_AgyBadgeAbsentWithoutAPinnedDir(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "inherit", Path: "/tmp/api", Program: "agy"})
	require.NoError(t, err)
	inst.SetAgyAccount("grav-one", "") // a named account whose config_dir is empty
	require.Equal(t, "grav-one", inst.AgyAccountName(),
		"precondition: the name IS stamped — the row is what must stay silent")
	require.NotContains(t, agyRow(t, 80, inst), "agy:",
		"an account that isolates nothing must not be badged")
}

// TestRender_AgyBadgeFollowsTheResolvedAgent pins the gate to agent.Resolve rather
// than to the literal "agy": the same session launched as a path, under the
// `antigravity` alias, and with flags appended is one agent, while another agent's
// program is not. Without this the gate could be a `== "agy"` comparison and every
// other guard here would still pass.
//
// Resolve matches an alias as a SUBSTRING of the first token's basename, so a
// program merely CONTAINING "agy" (`magyar`, a `run-agy.sh` wrapper) resolves to the
// agy adapter too. That is deliberate — it is how launcher wrappers resolve, and it
// is the launch path's behaviour as much as the badge's — so it is neither asserted
// nor worked around here; the point of this table is only that the row and the
// launch decide by the same function.
func TestRender_AgyBadgeFollowsTheResolvedAgent(t *testing.T) {
	for _, tc := range []struct {
		program string
		badged  bool
	}{
		{"agy", true},
		{"antigravity", true},
		{"agy --continue", true},
		{"/usr/local/bin/agy", true},
		{"claude", false},
		{"codex", false},
	} {
		out := agyRow(t, 80, agyInst(t, "s", "/tmp/api", tc.program, "", "grav-one"))
		if tc.badged {
			require.Containsf(t, out, "agy:grav-one", "%q resolves to the agy adapter", tc.program)
		} else {
			require.NotContainsf(t, out, "agy:", "%q does not resolve to the agy adapter", tc.program)
		}
	}
}

// TestListString_AgyBadgeSurvivesClaudeAccountClustering is the guard the shared
// suppression bool fails.
//
// The row badge is suppressed when it is redundant with the cluster divider above
// it. That divider is Claude-only — accountKey delegates to
// session.Instance.AccountClusterKey, which reads claudeAccountPool then
// claudeAccount — so a matching CLAUDE account says nothing about whether the AGY
// badge repeats anything. Gating both on one bool would make an agy badge vanish
// because a session's Claude account happened to equal the divider label.
//
// Names are chosen so no substring of one is a substring of another: counting
// "work" separates the surviving divider from a suppressed row badge.
func TestListString_AgyBadgeSurvivesClaudeAccountClustering(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	l.AddInstance(agyInst(t, "a", "/tmp/api", "agy", "work", "grav-one"))
	l.AddInstance(agyInst(t, "b", "/tmp/sideproj", "agy", "personal", "grav-two"))
	l.SetSize(100, 40)
	l.SetGroupMode("account")

	out := ansi.Strip(l.String())
	require.True(t, l.AccountClusteringVisible(), "precondition: two accounts render clusters")
	require.Equal(t, 1, strings.Count(out, "work"),
		"precondition: the Claude badge IS suppressed — only the divider survives")
	require.Contains(t, out, "agy:grav-one",
		"the agy badge is redundant with no divider, so clustering must not hide it")
	require.Contains(t, out, "agy:grav-two")
}

// TestListString_AgyBadgeCannotBreakThePanelFrame is the frame contract, run over
// a size matrix that deliberately includes widths BELOW the row's fit floor. There
// the right cluster overhangs (see minExactWidth) and the panel's clamp is the only
// thing standing between an overlong row and a wrapped line — which in Bubble Tea's
// alt-screen renderer is not a cosmetic offset but a ghost row that survives until a
// full repaint. Every line must measure exactly the panel width and the panel must
// never exceed its height, at every size.
func TestListString_AgyBadgeCannotBreakThePanelFrame(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	for _, w := range []int{20, 30, 40, 55, 65, 80, 120} {
		for _, h := range []int{6, 12, 30} {
			s := spinner.New()
			l := NewList(&s)
			inst := agyInst(t, "an-agy-session-with-a-long-name", "/tmp/api", "agy", "work", "grav-one")
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

// fullyBadgedAgyRow renders an agy session carrying the full right cluster —
// Claude account, agy account, AUTO, model, effort, permission — at `width`
// columns. withAgy is the control: the identical row minus the agy route, which is
// what makes the width delta below attributable to this chip alone.
func fullyBadgedAgyRow(t *testing.T, width int, name string, withAgy bool) string {
	t.Helper()
	agyAcct := ""
	if withAgy {
		agyAcct = "grav-one"
	}
	inst := agyInst(t, name, "/tmp/api", "agy", "work", agyAcct)
	inst.AutoYes = true
	inst.SetModeMeta("acceptEdits")
	inst.SetModelMeta("gemini-3-pro", transcript.Stamp{Path: "m", Size: 1})
	inst.SetEffortMeta("max")
	return agyRow(t, width, inst)
}

// minExactWidth is the narrowest column count at which every line of the rendered
// row measures exactly that many cells — the row's fit floor. Below it composeLine
// has nothing left to give: it shrinks only the single flex segment (the name), and
// once that is empty the right group renders OVERLONG. The panel then clips the
// overhang from the right edge, so the visible cost of going under the floor is the
// far-right agent icon and the chips beside it, not a wrapped row.
func minExactWidth(t *testing.T, render func(int) string) int {
	t.Helper()
	for w := 1; w <= 240; w++ {
		exact := true
		for _, line := range strings.Split(render(w), "\n") {
			if ansi.StringWidth(line) != w {
				exact = false
				break
			}
		}
		if exact {
			return w
		}
	}
	t.Fatalf("no width up to 240 renders exactly")
	return 0
}

// maxWholeName is the longest name that still survives un-truncated on line 1 at
// `width` columns — the name column's real budget, measured rather than derived.
func maxWholeName(t *testing.T, width int, withAgy bool) int {
	t.Helper()
	last := 0
	for n := 1; n <= width; n++ {
		name := strings.Repeat("n", n)
		if !strings.Contains(strings.Split(fullyBadgedAgyRow(t, width, name, withAgy), "\n")[0], name) {
			break
		}
		last = n
	}
	return last
}

// TestRender_AgyBadgeYieldsRatherThanErasingTheName is the invariant that makes the
// badge safe on a narrow pane, and the guard for the regression the first round of
// this feature shipped: composeLine can only shrink line 1's single flex segment,
// so an unbounded second account name took the name to ZERO and then overhung — at a
// 55-column list pane (a ~185-column terminal at the default 0.30 split) the row
// rendered no session name and no agent icon. Line 1 exists to say which session
// this is.
//
// Two claims, swept across the whole width range rather than sampled: wherever the
// badge is shown the name is non-empty, and wherever it is dropped the row is
// BYTE-IDENTICAL to the same session with no agy account. The second is what makes
// the yield exactly reversible — this feature cannot make a narrow pane worse than
// it was before the feature existed. The name is a run of "Z" because no other cell
// on the row can contain one, so "is any of the name left?" is a substring test.
func TestRender_AgyBadgeYieldsRatherThanErasingTheName(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const name = "ZZZZZZZZZZZZZZZZZZZZ"
	shown := 0
	for w := 10; w <= 140; w++ {
		row := fullyBadgedAgyRow(t, w, name, true)
		line1 := strings.Split(row, "\n")[0]
		if strings.Contains(line1, "agy:") {
			shown++
			require.Containsf(t, line1, "Z",
				"at %d columns the badge is shown but the name is gone:\n%s", w, row)
			continue
		}
		require.Equalf(t, fullyBadgedAgyRow(t, w, name, false), row,
			"at %d columns the badge yielded, so the row must be byte-identical to a no-account row", w)
	}
	require.Greater(t, shown, 40, "…and the badge must actually be shown somewhere, or this proves nothing")
}

// TestRender_AgyBadgeYieldsWithNoOtherChipBeforeAuto covers the one arrangement
// where yielding could leave litter: the AUTO chip prepends a pad when anything
// precedes it, so on a row whose ONLY earlier chip is the agy badge (no mute, no
// queue, no Claude account) dropping the badge would strand that pad. It is
// invisible — it abuts composeLine's gap — which is exactly why only a byte
// comparison catches it, and why it would otherwise silently cost the name a column.
func TestRender_AgyBadgeYieldsWithNoOtherChipBeforeAuto(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	build := func(agyAcct string) *session.Instance {
		inst := agyInst(t, "ZZZZZZZZZZZZZZZZ", "/tmp/api", "agy", "" /* no Claude account */, agyAcct)
		inst.AutoYes = true
		return inst
	}
	for w := 10; w <= 60; w++ {
		row := agyRow(t, w, build("grav-one"))
		if strings.Contains(row, "agy:") {
			continue
		}
		require.Equalf(t, agyRow(t, w, build("")), row,
			"at %d columns a yielded badge must leave no pad behind", w)
	}
}

// TestRender_AgyBadgeDoesNotMoveTheRowsFitFloor is the width claim, measured on both
// sides rather than asserted at one convenient column count. A badge added to a row
// with a flexible name column is invisible to a total-width test — the flex absorbs
// it and the assertion still passes while the name silently loses cells (#478/#479 →
// #501). The floor is the number that cannot be absorbed: the narrowest width at
// which every line still measures exactly, below which the right cluster overhangs
// and the panel clips it. Because the badge yields, that floor must not move at all.
func TestRender_AgyBadgeDoesNotMoveTheRowsFitFloor(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	without := minExactWidth(t, func(w int) string { return fullyBadgedAgyRow(t, w, "n", false) })
	with := minExactWidth(t, func(w int) string { return fullyBadgedAgyRow(t, w, "n", true) })
	t.Logf("fully badged agy row fit floor: %d columns without the badge, %d with it", without, with)
	require.Equal(t, without, with,
		"a chip that yields cannot raise the width at which the row stops fitting")
}

// TestRender_AgyBadgeLadderStaysExact walks the row up a width ladder rather than
// asserting one column count: an off-by-one in the right cluster shows up at one
// width and hides at the next. Every rung must measure exactly AND carry the badge,
// so a row that "fits" by yielding below its threshold cannot pass here.
//
// The rungs are anchored to the measured threshold — the narrowest width at which
// the badge is shown — rather than to literals, so the ladder keeps testing the
// boundary if another chip moves it. The rung below it is the negative control that
// makes the threshold mean something: there the badge is gone and the name is not.
func TestRender_AgyBadgeLadderStaysExact(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	threshold := 0
	for w := 10; w <= 140 && threshold == 0; w++ {
		if strings.Contains(fullyBadgedAgyRow(t, w, "agy-account-badge", true), "agy:") {
			threshold = w
		}
	}
	require.NotZero(t, threshold, "the badge must appear at some width")
	t.Logf("the agy badge appears from %d columns up", threshold)

	under := fullyBadgedAgyRow(t, threshold-1, "agy-account-badge", true)
	require.NotContains(t, under, "agy:", "one column under the threshold the badge must be gone")
	require.Contains(t, strings.Split(under, "\n")[0], "a",
		"…and the name it yielded to must still be there")

	for _, w := range []int{threshold, threshold + 1, threshold + 2, threshold + 7, 100, 120} {
		row := fullyBadgedAgyRow(t, w, "agy-account-badge", true)
		for i, line := range strings.Split(row, "\n") {
			require.Equalf(t, w, ansi.StringWidth(line),
				"line %d at %d columns must be exactly %d cells:\n%s", i, w, w, row)
		}
		require.Containsf(t, row, "agy:grav-one", "the badge must survive at %d columns", w)
	}
}

// TestRender_AgyBadgeCostsSlackNotTheName pins WHO pays for those cells at a width
// where the row comfortably fits: the name's flex slack, not the row's width and
// not another chip. Measuring the budget on both sides is what makes this
// non-vacuous — an assertion that "a short name survived" holds just as well
// against a chip four times this size, which is exactly how a flexible column
// hides a width regression (#478/#479 → #501).
func TestRender_AgyBadgeCostsSlackNotTheName(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)

	const width = 80
	without, with := maxWholeName(t, width, false), maxWholeName(t, width, true)
	t.Logf("name budget at %d columns: %d cells without the agy badge, %d with it", width, without, with)

	require.Equal(t, ansi.StringWidth(" agy:grav-one "), without-with,
		"the badge must take its cells from the name column and nothing else")
	require.Greater(t, with, 0, "…and must not take the whole name column at a width the row fits in")

	// The boundary itself, so the number above is a real edge and not a lucky sample.
	atBudget := strings.Repeat("n", with)
	require.Contains(t, strings.Split(fullyBadgedAgyRow(t, width, atBudget, true), "\n")[0], atBudget)
	over := strings.Repeat("n", with+1)
	require.NotContains(t, strings.Split(fullyBadgedAgyRow(t, width, over, true), "\n")[0], over,
		"one cell past the budget must truncate")
}
