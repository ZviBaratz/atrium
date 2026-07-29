package app

import (
	"context"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fold keys are directional arrows, quick-send lives on "s", approve on
// "a", and space now drives the mark/unmark action it was reserved for (only
// consumed in multi-select mode; a no-op in the default state).
func TestKeymap_FoldArrowsQuickSendAndMarkSpace(t *testing.T) {
	require.Equal(t, keys.KeyCollapse, keys.GlobalKeyStringsMap["left"])
	require.Equal(t, keys.KeyExpand, keys.GlobalKeyStringsMap["right"])
	require.Equal(t, keys.KeyQuickSend, keys.GlobalKeyStringsMap["s"])
	require.Equal(t, keys.KeyApprove, keys.GlobalKeyStringsMap["a"])
	require.Equal(t, keys.KeyToggleMark, keys.GlobalKeyStringsMap["space"])
	require.Equal(t, keys.KeyMultiSelect, keys.GlobalKeyStringsMap["v"])
}

// ←/→ drive the directional fold end-to-end through handleKeyPress: ← folds the
// selected session's group and persists the set, → unfolds it again.
func TestArrowKeys_CollapseAndExpandGroup(t *testing.T) {
	h := newTestHomeWithInstances(t, "/x/repoA", "/x/repoA", "/x/repoB")
	h.state = stateDefault
	h.menu = ui.NewMenu()
	h.tabbedWindow = ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background()))

	// ← from a non-anchor member folds the whole group.
	h.list.SetSelectedInstance(1)
	press(t, h, keyMsg("left"))
	require.Equal(t, []string{"repoA"}, h.list.CollapsedRepos())
	require.Equal(t, []string{"repoA"}, h.appState.GetCollapsedRepos(), "fold set is persisted")

	// → on the collapsed header unfolds it.
	press(t, h, keyMsg("right"))
	require.Empty(t, h.list.CollapsedRepos())
	require.Empty(t, h.appState.GetCollapsedRepos(), "persisted fold set is cleared")
}

// The other half of the fold contract (#399): a fold key that cannot act says why,
// unless the reason is one the user can neither see nor clear.

func left(t *testing.T, h *home)  { t.Helper(); press(t, h, keyMsg("left")) }
func right(t *testing.T, h *home) { t.Helper(); press(t, h, keyMsg("right")) }

// ← on a group that is already folded used to return nil, so the key felt broken.
func TestFoldKeys_AlreadyFoldedSaysSo(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"alpha", "repoA", ""},
		[3]string{"bravo", "repoB", ""})

	left(t, h)
	require.Equal(t, []string{"repoA"}, h.list.CollapsedRepos(), "precondition: repoA is folded")
	h.menu.ClearNotice()

	left(t, h)

	require.True(t, h.menu.HasNotice(), "a dead fold key must explain itself")
	assert.Contains(t, h.menu.String(), "already collapsed")
	assert.Contains(t, h.menu.String(), "repo group",
		"the scope noun separates this from Z, which shares the fold vocabulary")
}

// → on a group that is already unfolded, mirroring the above.
func TestFoldKeys_AlreadyExpandedSaysSo(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"alpha", "repoA", ""},
		[3]string{"bravo", "repoB", ""})

	right(t, h)

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "already expanded")
}

// A lone repo group renders no fold marker, so the keys are inert there — and unlike
// the filter, nothing the user can clear lifts it. Explaining would name a state no
// key reaches, so this one refusal stays silent on purpose.
func TestFoldKeys_LoneGroupStaysSilent(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"alpha", "repoA", ""},
		[3]string{"bravo", "repoA", ""})

	left(t, h)
	assert.False(t, h.menu.HasNotice(), "nothing to fold is not a refusal worth a toast")
	right(t, h)
	assert.False(t, h.menu.HasNotice())
	assert.Empty(t, h.appState.GetCollapsedRepos())
}

// The repro for the review finding: a filter is the sole visibility gate and overrides
// the fold in the render (ui.List.isHidden), so ← under one folded a group that stayed
// on screen expanded — persisting the set with the list standing still (#339). It now
// refuses and names the filter, the way hiddenNeighborNotice does for the reorder keys.
func TestFoldKeys_RefuseAndNameTheFilter(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoB", ""})
	h.list.SetFilter("api") // both groups still render

	left(t, h)

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "filter")
	assert.Contains(t, h.menu.String(), "esc to clear", "the refusal names the key that lifts it")
	assert.Empty(t, h.list.CollapsedRepos(), "nothing may fold while the fold is invisible")
	assert.Empty(t, h.appState.GetCollapsedRepos(),
		"and nothing may reach state.json when the screen cannot change")
}

// The same guard from the other side: a group whose flag is already set renders
// *expanded* under a filter, so "already collapsed" would describe something the user
// cannot see — exactly what hiddenNeighborNotice's docstring rules out.
func TestFoldKeys_FilterRefusalOutranksAlreadyFolded(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoB", ""})
	h.list.SetSelectedInstance(0)
	require.True(t, h.list.Collapse(), "precondition: repoA carries the fold flag")
	h.list.SetFilter("api") // which the render now overrides

	left(t, h)

	assert.Contains(t, h.menu.String(), "filter")
	assert.NotContains(t, h.menu.String(), "already collapsed",
		"the group is on screen expanded, so claiming it is folded contradicts the screen")
}

// Precedence the other way: with one group, folding is meaningless whether or not a
// filter is live, so naming the filter would promise a fix that clearing it never
// delivers (the over-claim #346 fixed in the status-sort hint).
func TestFoldKeys_LoneGroupOutranksTheFilterRefusal(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""})
	h.list.SetFilter("api")

	left(t, h)

	assert.False(t, h.menu.HasNotice(), "the durable reason wins, and it is a silent one")
}

// Z is the same contract at list scope: under a filter it would flip every group's
// stored flag with nothing on screen moving.
func TestFoldKeys_CollapseAllRefusesWhileFiltering(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoB", ""})
	h.list.SetFilter("api")

	pressRune(h, 'Z')

	require.True(t, h.menu.HasNotice(), "Z must not fold invisibly either")
	assert.Contains(t, h.menu.String(), "filter")
	assert.Empty(t, h.appState.GetCollapsedRepos())
}

// The header click is the same gesture as ←/→ (README: "click a repo header to
// fold/unfold it"), so the filter guard has to cover it too — otherwise the mouse
// would keep folding invisibly after the keys stopped.
func TestFoldClick_HeaderRefusesWhileFiltering(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoB", ""})
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.list.SetFilter("api")

	// ui.listHeaderZoneID's format, spelled out because it is unexported.
	headerZone := "list-header-" + h.list.InstancesForPersist()[0].GroupKey()

	// Render and click inside one retry loop: bounds read from an earlier frame can
	// be stale, and repo headers abut, so a stale read lands on a neighbor (#434). A
	// miss leaves the notice unset and fails the wait — it can never pass vacuously.
	require.Eventually(t, func() bool {
		_ = h.View().Content
		zi := zone.Get(headerZone)
		if zi.IsZero() {
			return false
		}
		h.Update(testutil.MouseClick(zi.StartX, zi.StartY, tea.MouseLeft))
		return h.menu.HasNotice()
	}, time.Second, 5*time.Millisecond, "the header click never reached the fold guard")

	assert.Contains(t, h.menu.String(), "filter")
	assert.Empty(t, h.list.CollapsedRepos(), "a click may not fold what the filter overrides")
	assert.Empty(t, h.appState.GetCollapsedRepos())
}

// Z with no filter still flips every group and persists — the guards must not cost the
// key its job.
func TestFoldKeys_CollapseAllStillFolds(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"alpha", "repoA", ""},
		[3]string{"bravo", "repoB", ""})

	pressRune(h, 'Z')

	assert.Equal(t, []string{"repoA", "repoB"}, h.list.CollapsedRepos())
	assert.Equal(t, []string{"repoA", "repoB"}, h.appState.GetCollapsedRepos())
	assert.False(t, h.menu.HasNotice(), "a fold the user can see needs no explanation")
}
