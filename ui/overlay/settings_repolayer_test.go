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
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", LinkPaths: []string{"node_modules", ".venv"}})
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

	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", LinkPaths: []string{"node_modules"}})
	assert.Contains(t, badgeFor(t, o, "link_paths"), "+1")
}

// TestRepoLayerBadgeDegradesButKeepsTheCount: the count is the whole payload, so
// every rung of the ladder keeps it — a bare "+2" still says the list shown is not
// the list in force. Dropping the chip is the one degradation this column must not
// make silently, which is why the help pane carries the same fact (below).
func TestRepoLayerBadgeDegradesButKeepsTheCount(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", LinkPaths: []string{"node_modules", ".venv"}})

	for _, w := range []int{120, 96, 80, 72, 64, 56, 48, 40} {
		o.SetSize(w, 24)
		badge := badgeFor(t, o, "link_paths")
		if badge == "" {
			continue // too narrow for any chip; contextLine is the fallback, asserted below
		}
		assert.Containsf(t, badge, "+2", "width %d: badge %q dropped the count", w, badge)
	}

	// Every rung the ladder can produce keeps the count, so the loop above cannot
	// pass by never reaching a narrow rung.
	for _, c := range repoLayerBadgeCandidates(2) {
		assert.Contains(t, c, "+2")
	}
	assert.Greater(t, len(repoLayerBadgeCandidates(2)), 1, "a one-rung ladder does not degrade")
}

// TestRepoLayerContextLineNamesTheRepo: the badge is a bare count, so the help pane
// is the only place the repository and its entries are named — and it is the surface
// a narrow pane cannot take away, which is what makes the badge safe to degrade.
func TestRepoLayerContextLineNamesTheRepo(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRepoLayer(&RepoLayer{Repo: "/src/web", LinkPaths: []string{"node_modules"}})

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
		Repo:       "/src/web",
		CarryFiles: []string{".dev.vars", ".other"},
		LinkPaths:  []string{"node_modules"},
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
