package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/ui"
	"github.com/stretchr/testify/require"
)

// Each number key jumps straight to its tab. This is also the guard on the
// offset trick: dispatch derives the tab index from the key's offset in the
// keys package's KeyTab* run, so a key name moved out of that run, a tab
// const reordered in ui, or a tab missing from the window's list all land the
// press on the wrong tab (or, past the end of the list, on none — SetActiveTab
// ignores out-of-range indices). Every press lands on a tab other than the
// current one, so a silently ignored jump fails rather than passing as a no-op.
func TestTabJumpKeys(t *testing.T) {
	h := newFilterHome()

	for _, tc := range []struct {
		key  string
		want int
	}{
		{"2", ui.DiffTab},
		{"4", ui.InspectorTab},
		{"3", ui.TerminalTab},
		{"1", ui.PreviewTab},
	} {
		press(t, h, runeKey(tc.key))
		require.Equal(t, tc.want, h.tabbedWindow.GetActiveTab(), "key %q", tc.key)
	}
}
