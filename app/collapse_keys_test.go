package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// The fold keys are directional arrows, quick-send lives on "s", and space is
// deliberately unbound — reserved for a future mark/select action.
func TestKeymap_FoldArrowsQuickSendAndFreeSpace(t *testing.T) {
	require.Equal(t, keys.KeyCollapse, keys.GlobalKeyStringsMap["left"])
	require.Equal(t, keys.KeyExpand, keys.GlobalKeyStringsMap["right"])
	require.Equal(t, keys.KeyQuickSend, keys.GlobalKeyStringsMap["s"])

	_, bound := keys.GlobalKeyStringsMap[" "]
	require.False(t, bound, "space must stay unbound (reserved)")
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
	press(t, h, tea.KeyMsg{Type: tea.KeyLeft})
	require.Equal(t, []string{"repoA"}, h.list.CollapsedRepos())
	require.Equal(t, []string{"repoA"}, h.appState.GetCollapsedRepos(), "fold set is persisted")

	// → on the collapsed header unfolds it.
	press(t, h, tea.KeyMsg{Type: tea.KeyRight})
	require.Empty(t, h.list.CollapsedRepos())
	require.Empty(t, h.appState.GetCollapsedRepos(), "persisted fold set is cleared")
}
