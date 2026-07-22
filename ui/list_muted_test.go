package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/stretchr/testify/require"
)

// A muted session carries a mute glyph on its row; an unmuted one does not. The
// glyph tells the user which sessions they've silenced (the mute itself is otherwise
// invisible).
func TestMutedRow_ShowsMuteGlyph(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	l := newGroupList(t, "/x/repoA")

	require.NotContains(t, l.String(), th.Glyphs.Muted, "an unmuted session shows no mute glyph")

	l.items[0].SetMuted(true)
	require.Contains(t, l.String(), th.Glyphs.Muted, "a muted session shows the mute glyph")
}
