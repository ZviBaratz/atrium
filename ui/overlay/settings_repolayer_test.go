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

	// A layer that contributes annotates its row.
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {".dev.vars", ".other"}}})
	assert.Contains(t, badgeFor(t, o, "carry_files"), "+2")

	// And a row the layer does NOT carry must not claim one. With a single layerable
	// key today the negative has to be an UNLAYERED row: a caller naming its key is
	// exactly the case scope routing exists to refuse, and it is the only form of
	// this negative that keeps working when a second layerable key returns.
	var unlayered string
	for _, r := range o.rows {
		if r.scope != scopeRepoLayered {
			unlayered = r.key
			break
		}
	}
	require.NotEmpty(t, unlayered)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{unlayered: {"a", "b"}}})
	assert.NotContains(t, badgeFor(t, o, unlayered), "+2",
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
	require.NotEmpty(t, o.rows[rowIndexOf(t, o, "carry_files")].timing.badge(),
		"the row must HAVE a timing badge, or this test proves nothing about precedence")

	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {".dev.vars"}}})
	assert.Contains(t, badgeFor(t, o, "carry_files"), "+1")
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
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {".dev.vars", ".other"}}})
	i := rowIndexOf(t, o, "carry_files")

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

// TestRepoLayerContextLineNamesTheRepo: the badge is a bare count, so the repository
// and its entries are named only in the help pane.
//
// contextLine is NOT the guaranteed surface, and this test says which is. Spec §10
// ranks a truncated value above the provenance line, and carry_files has a non-empty
// default (.claude/settings.local.json) that truncates on a narrow pane — so at 56
// the value legitimately wins and the layer is named by expandedHelpContent instead.
// An earlier version of this test swept 56 too and passed only because it was
// pointed at a row whose default value is empty; the claim it made about narrow
// panes was false for the row it now covers.
func TestRepoLayerContextLineNamesTheRepo(t *testing.T) {
	// A SHORT carry value on purpose. The shipped default
	// (.claude/settings.local.json) truncates in the value column at every width
	// below ~96, and spec §10 ranks a truncated value above the provenance line — so
	// a fixture using the default measures the default's length rather than this
	// row's provenance, and the widths at which it "passed" were an accident of it.
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{".env"}
	o := NewSettingsOverlay(cfg)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {".dev.vars"}}})

	for _, w := range []int{96, 80} {
		o.SetSize(w, 24)
		settingsAt(t, o, "carry_files")
		line := stripANSI(o.contextLine(o.innerWidth()))
		assert.Containsf(t, line, repocfg.RepoLocalFileName, "width %d: %q", w, line)
		assert.Containsf(t, line, "/src/web", "width %d: the repo must be named: %q", w, line)
	}

	// The surface that cannot be taken away, at a width where contextLine yields to a
	// truncated value. Restore the long default to produce that case.
	long := config.DefaultConfig()
	o = NewSettingsOverlay(long)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": {".dev.vars"}}})
	o.SetSize(56, 24)
	settingsAt(t, o, "carry_files")
	require.NotContains(t, stripANSI(o.contextLine(o.innerWidth())), "/src/web",
		"the fixture must actually produce the yielding case, or the next assertion proves nothing")
	// Assert the whole provenance sentence, not the file name: carry_files' own
	// static detail already names `.atrium.json`, so a bare Contains on it passes
	// with the layer entirely absent from the ? view. Mutating away the provenance
	// write left that assertion green and only the repo path caught it.
	help := stripANSI(o.expandedHelpContent(o.cursor))
	prov := stripANSI(o.repoLayerFor(o.cursor))
	require.NotEmpty(t, prov, "the fixture must produce a provenance sentence to look for")
	assert.Contains(t, help, prov, "the ? view is the guaranteed surface")

	// And it says nothing at all when there is nothing to say — otherwise the row's
	// own detail would be evicted for a sentence about a layer that does not exist.
	o = NewSettingsOverlay(cfg)
	o.SetSize(96, 24)
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", Lists: map[string][]string{"carry_files": nil}})
	settingsAt(t, o, "carry_files")
	assert.Empty(t, o.repoLayerContext(), "a row this repo adds nothing to has no provenance line")
	o.SetRepoLayer(nil)
	settingsAt(t, o, "carry_files")
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
			"carry_files": {".dev.vars", ".other"},
		},
	})
	for _, size := range []struct{ w, h int }{{120, 40}, {96, 32}, {80, 24}, {56, 24}, {40, 24}} {
		o.SetSize(size.w, size.h)
		settingsAt(t, o, "carry_files")
		for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
			assert.LessOrEqualf(t, len([]rune(line)), size.w,
				"%dx%d: a line overran the frame: %q", size.w, size.h, line)
		}
	}
}

// TestRepoLayerIsReachableOnEveryLayeredRow sweeps every layerable row at every
// width, rather than trusting one row at one size.
//
// The clause it exists to reach is contextLine's truncated-value one, which outranks
// the provenance clause: a row whose default value is short never reaches it, so a
// sweep parked on such a row would report the provenance line as always present.
// carry_files has a long default list and truncates at
// every width the panel supports, which is the correlated case: a long list is both
// why the value truncates and why the layer is worth saying.
//
// The invariant asserted is therefore the honest one — the fact is reachable in the
// panel, not that any one line carries it.
func TestRepoLayerIsReachableOnEveryLayeredRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{
		Repo: "/home/dev/src/a-project-with-a-long-path",
		// Only layerable keys. A `link_paths` entry here was dead weight once that key
		// was deferred — the sweep below iterates RepoLocalLayerKeys, so it was never
		// read, and deleting it changed nothing.
		Lists: map[string][]string{
			"carry_files": {".dev.vars", ".claude/settings.local.json"},
		},
	})

	for _, key := range repocfg.RepoLocalLayerKeys() {
		for _, w := range []int{120, 96, 80, 72, 56, 40} {
			o.SetSize(w, 24)
			settingsAt(t, o, key)
			help := stripANSI(o.expandedHelpContent(o.cursor))
			// The whole sentence, for the reason given in
			// TestRepoLayerContextLineNamesTheRepo: a row's static detail may name
			// `.atrium.json` on its own, so the file name alone is not evidence the
			// layer reached the view.
			prov := stripANSI(o.repoLayerFor(o.cursor))
			require.NotEmptyf(t, prov, "row %q at %d cols: no provenance sentence to look for", key, w)
			assert.Containsf(t, help, prov,
				"row %q at %d cols: the `?` view is the guaranteed surface and lost the layer", key, w)
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
