package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrapIndex tests ─────────────────────────────────────────────────────────────

func TestWrapIndex_ForwardAndBackward(t *testing.T) {
	assert.Equal(t, 1, wrapIndex(0, +1, 3), "forward from 0")
	assert.Equal(t, 2, wrapIndex(1, +1, 3), "forward from 1")
	assert.Equal(t, 1, wrapIndex(2, -1, 3), "backward from 2")
	assert.Equal(t, 0, wrapIndex(1, -1, 3), "backward from 1")
}

func TestWrapIndex_WrapsAtEnd(t *testing.T) {
	assert.Equal(t, 0, wrapIndex(2, +1, 3), "right past last wraps to 0")
}

func TestWrapIndex_WrapsAtStart(t *testing.T) {
	assert.Equal(t, 2, wrapIndex(0, -1, 3), "left past 0 wraps to last")
}

func TestWrapIndex_ZeroOrNegativeN_ReturnsZero(t *testing.T) {
	assert.Equal(t, 0, wrapIndex(0, +1, 0), "n=0 must not panic")
	assert.Equal(t, 0, wrapIndex(0, +1, -1), "n<0 must not panic")
}

// moveCursor tests ─────────────────────────────────────────────────────────────

func TestMoveCursor_RightWrapsToStart(t *testing.T) {
	c := chipRow{options: []string{"a", "b", "c"}, cursor: 2}
	c.moveCursor(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, 0, c.cursor, "right past last wraps to 0")
}

func TestMoveCursor_LeftWrapsToEnd(t *testing.T) {
	c := chipRow{options: []string{"a", "b", "c"}, cursor: 0}
	c.moveCursor(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, 2, c.cursor, "left past 0 wraps to last")
}

func TestMoveCursor_DownAliasesRight(t *testing.T) {
	c := chipRow{options: []string{"a", "b", "c"}}
	c.moveCursor(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 1, c.cursor)
}

func TestMoveCursor_UpAliasesLeft(t *testing.T) {
	c := chipRow{options: []string{"a", "b", "c"}, cursor: 1}
	c.moveCursor(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, c.cursor)
}

func TestMoveCursor_OtherKeyIsNoop(t *testing.T) {
	c := chipRow{options: []string{"a", "b", "c"}, cursor: 1}
	c.moveCursor(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, 1, c.cursor, "Esc must not move cursor")
}

// selected tests ──────────────────────────────────────────────────────────────

func TestChipRow_SelectedReturnsEmptyForDefaultChip(t *testing.T) {
	c := chipRow{options: []string{"default", "plan", "auto"}}
	require.Equal(t, "", c.selected(), "cursor=0 is the no-op chip")
}

func TestChipRow_SelectedReturnsOptionWhenNotDefault(t *testing.T) {
	c := chipRow{options: []string{"default", "plan"}, cursor: 1}
	require.Equal(t, "plan", c.selected())
}

func TestChipRow_SelectedEmptyWhenDisabled(t *testing.T) {
	c := chipRow{options: []string{"default", "plan"}, cursor: 1, disabled: true}
	require.Equal(t, "", c.selected(), "disabled chip must never contribute a flag")
}

// render tests ────────────────────────────────────────────────────────────────

func TestChipRow_RenderShowsAllOptions(t *testing.T) {
	c := chipRow{options: []string{"default", "plan", "auto"}}
	out := xansi.Strip(c.render())
	assert.Contains(t, out, "default")
	assert.Contains(t, out, "plan")
	assert.Contains(t, out, "auto")
}

func TestChipRow_RenderLabelsOverrideOptions(t *testing.T) {
	c := chipRow{
		options: []string{"default", "acceptEdits"},
		labels:  []string{"default", "accept-edits"},
		cursor:  1,
	}
	out := xansi.Strip(c.render())
	assert.Contains(t, out, "accept-edits", "display label must appear")
	assert.NotContains(t, out, "acceptEdits", "raw option must not appear when labels is set")
}
