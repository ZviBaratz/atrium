package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/ui"
	"github.com/stretchr/testify/require"
)

// 1/2/3 jump straight to the corresponding tab.
func TestTabJumpKeys(t *testing.T) {
	h := newFilterHome()

	for _, tc := range []struct {
		key  string
		want int
	}{
		{"2", ui.DiffTab},
		{"3", ui.TerminalTab},
		{"1", ui.PreviewTab},
	} {
		press(t, h, runeKey(tc.key))
		require.Equal(t, tc.want, h.tabbedWindow.GetActiveTab(), "key %q", tc.key)
	}
}
