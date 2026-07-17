package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func pickerRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// A sync picker (local, re-ranked list) resets the cursor to the top on a filter
// edit and never uses a version.
func TestPicker_SyncEditResetsCursor(t *testing.T) {
	p := newPicker(false)
	p.cursor = 2
	consumed, filterChanged, cursorMoved := p.handleKey(pickerRunes("a"), 5)
	require.True(t, consumed)
	require.True(t, filterChanged)
	require.False(t, cursorMoved)
	require.Equal(t, 0, p.cursor, "sync edit resets the cursor to the top")
	require.Equal(t, uint64(0), p.filterVersion, "sync picker never bumps a version")
	require.Equal(t, "a", p.filter)
}

// An async picker (out-of-band results) bumps its version on a filter edit so stale
// results are dropped, and leaves the cursor for the owner to clamp when results
// land. This plus the sync test above mutation-cover the async/sync branch in
// onEdit: dropping either arm flips one of these assertions.
func TestPicker_AsyncEditBumpsVersionKeepsCursor(t *testing.T) {
	p := newPicker(true)
	p.cursor = 2
	_, filterChanged, _ := p.handleKey(pickerRunes("a"), 5)
	require.True(t, filterChanged)
	require.Equal(t, uint64(1), p.filterVersion, "async edit bumps the version")
	require.Equal(t, 2, p.cursor, "async edit does not reset the cursor")

	_, _, _ = p.handleKey(tea.KeyMsg{Type: tea.KeyBackspace}, 5)
	require.Equal(t, uint64(2), p.filterVersion, "each edit bumps exactly once")
}

// Nav clamps to [0, itemCount) and reports cursorMoved (not filterChanged). The
// last-item case mutation-covers the `cursor < itemCount-1` bound.
func TestPicker_NavClamps(t *testing.T) {
	p := newPicker(false)

	_, _, moved := p.handleKey(tea.KeyMsg{Type: tea.KeyUp}, 3)
	require.False(t, moved, "Up at the top does not move")

	_, fc, moved := p.handleKey(tea.KeyMsg{Type: tea.KeyDown}, 3)
	require.True(t, moved)
	require.False(t, fc, "nav is not a filter change")
	require.Equal(t, 1, p.cursor)

	p.handleKey(tea.KeyMsg{Type: tea.KeyDown}, 3) // → 2 (last)
	_, _, moved = p.handleKey(tea.KeyMsg{Type: tea.KeyDown}, 3)
	require.False(t, moved, "cannot move past the last item")
	require.Equal(t, 2, p.cursor)
}

// clampCursor pulls a cursor at or past the end back to the last index, and to 0
// for an empty list. The cursor==itemCount case mutation-covers the `>=` bound.
func TestPicker_ClampCursor(t *testing.T) {
	p := newPicker(true)
	p.cursor = 4
	p.clampCursor(4)
	require.Equal(t, 3, p.cursor, "a cursor at itemCount is clamped to the last index")

	p.cursor = 9
	p.clampCursor(4)
	require.Equal(t, 3, p.cursor)

	p.clampCursor(0)
	require.Equal(t, 0, p.cursor, "an empty list clamps to 0")
}

// The optional preview hook fires with the highlighted item; a nil hook is a no-op.
func TestPicker_PreviewHook(t *testing.T) {
	p := newPicker(false)
	p.notifyHighlight("x") // nil hook must not panic

	var got string
	p.SetPreviewHook(func(item string) { got = item })
	p.notifyHighlight("branch-a")
	require.Equal(t, "branch-a", got)
}

// rankCandidates matches the display form and adds a basename bonus so a name hit
// outranks an equal-score mid-path hit; the matcher itself lives in internal/fuzzy.
func TestRankCandidates_NameHitOutranksMidPath(t *testing.T) {
	id := func(s string) string { return s }
	got := rankCandidates([]string{
		"/home/zvi/quantivly/platform/src/box",
		"/home/zvi/quantivly/hub",
	}, "hub", id)
	require.Equal(t, "/home/zvi/quantivly/hub", got[0],
		"a contiguous basename 'hub' outranks a scattered mid-path embedding")
}
