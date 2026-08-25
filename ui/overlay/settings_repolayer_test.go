package overlay

// settings_repolayer_test.go — #815 turning the scopeGlobal seam into a real second
// scope. The facts under test are the two the panel can get wrong in opposite
// directions: showing provenance it was never told about (a lie), and showing a
// global value as the whole story in a repo where it is not (a silent lie).

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/repocfg"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rowIndexOf finds a row by key, so a test can ask about a row without moving the
// cursor onto it (the badge is a property of the row, not of the selection).
func rowIndexOf(t *testing.T, o *SettingsOverlay, key string) int {
	t.Helper()
	for i, r := range o.rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("no row with key %q", key)
	return -1
}

// badgeFor renders row key's right-aligned badge at the panel's current size.
func badgeFor(t *testing.T, o *SettingsOverlay, key string) string {
	t.Helper()
	i := rowIndexOf(t, o, key)
	_, badge := o.rowValueAndBadge(i, o.rowsPaneWidth(), o.visibleLabelWidth(), o.inertReason(i))
	return badge
}

// TestRepoLayerBadgeOnlyWhenInjected: nil means "unknown", never "the repo adds
// nothing". Until home injects a layer — and whenever there is no session to ask —
// these rows must render exactly as they did before #815, which is also what keeps
// the app-level frame goldens unchanged.
func TestRepoLayerBadgeOnlyWhenInjected(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(96, 32)

	for _, key := range repocfg.RepoLocalLayerKeys() {
		before := badgeFor(t, o, key)
		assert.Equal(t, o.rows[rowIndexOf(t, o, key)].timing.badge(), before,
			"row %q must keep its timing badge while no layer is known", key)
	}

	// A layer that contributes to one row must not annotate the other.
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"link_paths": []string{"node_modules", ".venv"}}})
	assert.Contains(t, badgeFor(t, o, "link_paths"), "+2")
	assert.NotContains(t, badgeFor(t, o, "carry_files"), "+",
		"a row this repo adds nothing to must not claim it does")

	// An empty layer is the same as none.
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web"})
	for _, key := range repocfg.RepoLocalLayerKeys() {
		assert.NotContains(t, badgeFor(t, o, key), "+", "row %q", key)
	}
}

// TestRepoLayerBadgeOutranksTheTimingBadge: the timing badge is reference
// information (spec §10 drops it first); "your value is not the effective value
// here" is not. If the layer chip lost the column the row would look ordinary.
func TestRepoLayerBadgeOutranksTheTimingBadge(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(96, 32)
	require.NotEmpty(t, o.rows[rowIndexOf(t, o, "link_paths")].timing.badge(),
		"the row must HAVE a timing badge, or this test proves nothing about precedence")

	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"link_paths": []string{"node_modules"}}})
	assert.Contains(t, badgeFor(t, o, "link_paths"), "+1")
}

// TestRepoLayerBadgeDegradesButKeepsTheCount: the count is the whole payload, so
// every rung of the ladder keeps it - a bare "+2" still says the list shown is not
// the list in force.
//
// It asserts on the RENDERED row line, and that distinction is the test. A draft
// asserted on rowValueAndBadge's return value, and a mutation bypassing fitBadge
// survived it: composeRowLine DROPS a badge that does not fit, so an un-laddered
// chip still returns a string carrying the count while the rendered row shows no
// chip at all. Only the rendered line can tell the two apart.
func TestRepoLayerBadgeDegradesButKeepsTheCount(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"link_paths": []string{"node_modules", ".venv"}}})
	i := rowIndexOf(t, o, "link_paths")

	// 80 and below is the band that matters: the widest rung (20 cells) no longer
	// fits beside the value there, so the row keeps its chip only because a shorter
	// rung exists.
	for _, w := range []int{120, 96, 84, 80, 72, 64, 56, 52, 48, 44} {
		o.SetSize(w, 24)
		line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
		assert.Containsf(t, line, "+2", "width %d: the rendered row lost the count: %q", w, line)
	}

	// The floor, stated rather than assumed: below this the rows pane cannot carry
	// any chip, and the help pane is the fallback (TestRepoLayerContextLineNamesTheRepo
	// covers it). Pinning it also stops a future "always render the chip" from
	// passing the sweep above by overrunning the pane instead of degrading.
	o.SetSize(40, 24)
	assert.NotContains(t, stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth())), "+2")

	// Every rung the ladder can produce keeps the count, so the sweep above cannot
	// pass by way of a one-rung ladder that happens to be short.
	rungs := repoLayerBadgeCandidates(2)
	assert.Greater(t, len(rungs), 1, "a one-rung ladder does not degrade")
	for _, c := range rungs {
		assert.Contains(t, c, "+2")
	}

	// And they must be ordered widest first, because fitBadge returns the FIRST rung
	// that fits: a rung wider than the one before it can never be reached, so a
	// mis-ordered ladder skips rungs invisibly at every width rather than failing.
	for i := 1; i < len(rungs); i++ {
		assert.LessOrEqualf(t, ansi.StringWidth(rungs[i]), ansi.StringWidth(rungs[i-1]),
			"rung %d (%q) is wider than the rung before it (%q)", i, rungs[i], rungs[i-1])
	}
}

// TestRepoLayerContextLineNamesTheRepo: the badge is a bare count, so the help pane
// is the only place the repository and its entries are named — and it is the surface
// a narrow pane cannot take away, which is what makes the badge safe to degrade.
func TestRepoLayerContextLineNamesTheRepo(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"link_paths": []string{"node_modules"}}})

	for _, w := range []int{96, 80, 56} {
		o.SetSize(w, 24)
		settingsAt(t, o, "link_paths")
		line := stripANSI(o.contextLine(o.innerWidth()))
		assert.Containsf(t, line, repocfg.RepoLocalFileName, "width %d: %q", w, line)
		assert.Containsf(t, line, "/src/web", "width %d: the repo must be named: %q", w, line)
	}

	// And it says nothing at all when there is nothing to say — otherwise the row's
	// own detail would be evicted for a sentence about a layer that does not exist.
	o.SetSize(96, 24)
	settingsAt(t, o, "carry_files")
	assert.Empty(t, o.repoLayerContext(), "a row this repo adds nothing to has no provenance line")
	o.SetRepoLayer(nil)
	settingsAt(t, o, "link_paths")
	assert.Empty(t, o.repoLayerContext(), "an uninjected panel has no provenance line")
}

// TestRepoLayerRowsRenderInTheFrame: the pane still lays out. The chip is a fourth
// claimant on a column three others already compete for, and composeRowLine drops
// the badge before it touches the value — so a chip that overran would misalign
// every label below it rather than failing loudly.
func TestRepoLayerRowsRenderInTheFrame(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{
		Repo: "/src/web",
		Lists: map[string][]string{
			"carry_files": []string{".dev.vars", ".other"},
			"link_paths":  []string{"node_modules"},
		},
	})
	for _, size := range []struct{ w, h int }{{120, 40}, {96, 32}, {80, 24}, {56, 24}, {40, 24}} {
		o.SetSize(size.w, size.h)
		settingsAt(t, o, "link_paths")
		for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
			assert.LessOrEqualf(t, len([]rune(line)), size.w,
				"%dx%d: a line overran the frame: %q", size.w, size.h, line)
		}
	}
}

// TestRepoLayerIsReachableOnEveryLayeredRow closes the hole every other test in this
// file had: they park on link_paths, whose default value is "(none)" and never
// truncates — so contextLine's truncated-value clause, which outranks the provenance
// clause, was never reached. carry_files has a long default list and truncates at
// every width the panel supports, which is the correlated case: a long list is both
// why the value truncates and why the layer is worth saying.
//
// The invariant asserted is therefore the honest one — the fact is reachable in the
// panel, not that any one line carries it.
func TestRepoLayerIsReachableOnEveryLayeredRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{
		Repo: "/home/dev/src/a-project-with-a-long-path",
		Lists: map[string][]string{
			"carry_files": []string{".dev.vars", ".claude/settings.local.json"},
			"link_paths":  []string{"node_modules"},
		},
	})

	for _, key := range repocfg.RepoLocalLayerKeys() {
		for _, w := range []int{120, 96, 80, 72, 56, 40} {
			o.SetSize(w, 24)
			settingsAt(t, o, key)
			help := stripANSI(o.expandedHelpContent(o.cursor))
			assert.Containsf(t, help, repocfg.RepoLocalFileName,
				"row %q at %d cols: the `?` view is the guaranteed surface and lost the layer", key, w)
			assert.Containsf(t, help, "/home/dev/src/a-project-with-a-long-path",
				"row %q at %d cols: the `?` view must name the repo", key, w)
		}
	}

	// And the row whose value truncates really does reach that clause, or the sweep
	// above proves nothing about the correlated case.
	o.SetSize(80, 24)
	settingsAt(t, o, "carry_files")
	require.True(t, o.valueWasTruncated(),
		"carry_files' default must truncate at 80 columns, or this test is about the wrong row")
	assert.NotContains(t, stripANSI(o.contextLine(o.innerWidth())), repocfg.RepoLocalFileName,
		"the truncated value outranks the provenance line here (spec §10) — which is why "+
			"the `?` view carries it and this assertion pins that it is the yielding side")

	// An unlayered panel says nothing anywhere, so the assertions above are about the
	// layer and not about text every panel happens to carry.
	plain := NewSettingsOverlay(config.DefaultConfig())
	plain.SetSize(80, 24)
	settingsAt(t, plain, "carry_files")
	assert.NotContains(t, stripANSI(plain.expandedHelpContent(plain.cursor)), "also adds")
}

// TestRepoLayerRoutesThroughTheRowScope closes the hole the two bridge guards could
// not see. Both of them passed — a row's scope agreed with
// repocfg.RepoLocalLayerKeys, which agreed with repoLocalWire's json tags — while
// nothing in production read scope at all: the render path keyed off a hardcoded
// two-case switch, so a third layered key rendered no badge, no provenance line and
// no `?` entry. That is exactly the silent "the value shown is not the effective
// value and never admits it" failure the scope seam exists to prevent.
//
// The test drives it from the seam's own vocabulary rather than from the two keys
// that exist today: any row the schema marks scopeRepoLayered must annotate when the
// injected layer carries its key.
func TestRepoLayerRoutesThroughTheRowScope(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(96, 32)

	layered := map[string][]string{}
	for _, r := range o.rows {
		if r.scope == scopeRepoLayered {
			layered[r.key] = []string{"a-" + r.key, "b-" + r.key}
		}
	}
	require.NotEmpty(t, layered, "the schema must mark some row repo-layered, or this guard is vacuous")
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: layered})

	for key := range layered {
		i := rowIndexOf(t, o, key)
		assert.Containsf(t, badgeFor(t, o, key), "+2",
			"row %q is scopeRepoLayered and the layer carries it, so it must say so", key)
		assert.Containsf(t, o.repoLayerFor(i), "a-"+key,
			"row %q must name the entries the repo adds", key)
	}

	// And the negative: a row the schema does NOT mark layered must ignore a layer
	// that names its key. Without this, routing on the key alone would pass above.
	var unlayered string
	for _, r := range o.rows {
		if r.scope != scopeRepoLayered {
			unlayered = r.key
			break
		}
	}
	require.NotEmpty(t, unlayered)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{unlayered: {"x", "y"}}})
	assert.NotContains(t, badgeFor(t, o, unlayered), "+2",
		"a row whose scope is not repo-layered must not grow a provenance badge because a caller named its key")
}

// TestRepoLayerSaysWhenLinksWereNotLinked: a dependency-isolated session receives
// NONE of the link_paths — session/git's seedLocalPaths returns before linking — so
// advertising them as added is false in a way that invites damage. The user either
// reads the panel as evidence the repo overrode their isolation choice, or believes
// the tree is shared and runs a destructive dependency upgrade expecting it to be
// private. carry_files is unaffected: isolation is about the linked trees.
func TestRepoLayerSaysWhenLinksWereNotLinked(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(120, 40)
	o.SetRepoLayer(&RepoLayer{
		Repo: "/src/web",
		Lists: map[string][]string{
			"carry_files": {".dev.vars"},
			"link_paths":  {"node_modules"},
		},
		DepsIsolated: true,
	})

	link := o.repoLayerFor(rowIndexOf(t, o, "link_paths"))
	require.NotEmpty(t, link)
	assert.Contains(t, link, "dependency-isolated", "the row must say the paths were not linked")
	assert.NotContains(t, link, "also adds", "a path that was never linked was not added")

	carry := o.repoLayerFor(rowIndexOf(t, o, "carry_files"))
	assert.Contains(t, carry, "also adds", "isolation does not withhold carried files")
}

// TestRepoLayerSanitizesRepoAuthoredText: the provenance line interpolates strings a
// repository committed, and it is measured and truncated downstream. The parse
// deliberately ALLOWS combining marks so macOS's decomposed filenames work, and a
// long run of them measures one cell in every width library while rendering as a
// smear that overruns the row — the overflow that desyncs bubbletea's incremental
// renderer into ghost rows. A per-rune parse rule cannot judge a grapheme cluster,
// so the display boundary has to.
func TestRepoLayerSanitizesRepoAuthoredText(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(96, 32)
	hostile := "a" + strings.Repeat("́", 300)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {hostile}}})

	line := o.repoLayerFor(rowIndexOf(t, o, "carry_files"))
	require.NotEmpty(t, line)
	assert.LessOrEqual(t, ansi.StringWidth(line), repoLayerPathWidth+repoLayerEntriesWidth+64,
		"the provenance line must be bounded by the display rule, not by what the repo committed")
	assert.NotContains(t, line, strings.Repeat("́", 10),
		"combining marks must be replaced at the display boundary, where their cell count can be judged")
}
