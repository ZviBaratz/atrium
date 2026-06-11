package overlay

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDirectoryPicker_DedupAndDefaultSelection(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a", "/repo/b", "/repo/a", ""})
	// Dedup preserves order and drops empties.
	assert.Equal(t, []string{"/repo/a", "/repo/b"}, dp.candidates)
	// Cursor starts on the first (default/contextual) candidate.
	assert.Equal(t, "/repo/a", dp.GetSelectedPath())
}

func TestDirectoryPicker_Navigation(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a", "/repo/b"})

	consumed, changed := dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, consumed)
	assert.True(t, changed)
	assert.Equal(t, "/repo/b", dp.GetSelectedPath())

	// Down at the end is consumed but does not change the selection.
	consumed, changed = dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, consumed)
	assert.False(t, changed)
	assert.Equal(t, "/repo/b", dp.GetSelectedPath())

	consumed, changed = dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.True(t, consumed)
	assert.True(t, changed)
	assert.Equal(t, "/repo/a", dp.GetSelectedPath())
}

func TestDirectoryPicker_FilterMatchesCandidates(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/home/me/repoA", "/home/me/other"})

	// Typing a non-path fragment filters the candidates.
	dp.HandleKeyPress(runes("repo"))
	items := dp.visibleItems()
	require.Len(t, items, 1)
	assert.Equal(t, "/home/me/repoA", items[0])
	assert.Equal(t, "/home/me/repoA", dp.GetSelectedPath())
}

func TestDirectoryPicker_FreeTextPathEntry(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/home/me/repoA"})

	// A filter that looks like a path to a not-yet-existing location is offered as a
	// selectable entry, resolved to abs. Use an empty temp dir so no on-disk sibling
	// can fuzzy-match and steal the selection.
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	for _, r := range target {
		dp.HandleKeyPress(runes(string(r)))
	}
	assert.Equal(t, target, dp.GetSelectedPath())
}

func TestDirectoryPicker_RelativePathExpandsToAbs(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/home/me/repoA"})
	dp.HandleKeyPress(runes("."))
	got := dp.GetSelectedPath()
	assert.True(t, filepath.IsAbs(got), "expected absolute path, got %q", got)
}

func TestDirectoryPicker_TypingReportsSelectionChanged(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a"})
	consumed, changed := dp.HandleKeyPress(runes("x"))
	assert.True(t, consumed)
	assert.True(t, changed)
}

func TestDirectoryPicker_Backspace(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/home/me/repoA", "/home/me/repoB"})
	dp.HandleKeyPress(runes("repoB"))
	assert.Equal(t, "/home/me/repoB", dp.GetSelectedPath())

	// Removing the last char widens the filter back out.
	consumed, changed := dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.True(t, consumed)
	assert.True(t, changed)
	assert.Equal(t, "repoB"[:4], dp.filter)
}

func TestDirectoryPicker_UnfocusedRenderShowsTargetWithoutListOrHint(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a", "/repo/b"})
	out := dp.Render()
	// The chosen target is always visible on the header line...
	assert.Contains(t, out, "Project:")
	assert.Contains(t, out, "/repo/a")
	// ...the misleading "Tab to change" hint is gone (Tab cycles all fields, not the picker)...
	assert.NotContains(t, out, "Tab to change")
	// ...and the candidate list is blank (reserved but empty) when unfocused.
	assert.NotContains(t, out, "/repo/b")
}

// The picker renders the same number of lines focused and unfocused, so the surrounding
// overlay does not change height — and therefore does not jump — as focus moves.
func TestDirectoryPicker_RenderHeightConstantAcrossFocus(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a", "/repo/b"})
	unfocused := strings.Count(dp.Render(), "\n")
	dp.Focus()
	focused := strings.Count(dp.Render(), "\n")
	assert.Equal(t, unfocused, focused, "directory picker height must not change with focus")
}

func TestDirectoryPicker_FocusedRenderListsCandidates(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a", "/repo/b"})
	dp.Focus()
	out := dp.Render()
	assert.Contains(t, out, "/repo/a")
	assert.Contains(t, out, "/repo/b")
}

func TestDirectoryPicker_SelectionStateIndicator(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a"})
	dp.Focus()

	// Not a directory at all → red invalid hint.
	dp.SetSelectionState(false, false)
	assert.Contains(t, dp.Render(), "not a directory")

	// A valid directory that is not a git repo → direct-session hint.
	dp.SetSelectionState(true, true)
	out := dp.Render()
	assert.Contains(t, out, "direct session")
	assert.NotContains(t, out, "not a directory")

	// A valid git repo → no hint at all.
	dp.SetSelectionState(true, false)
	out = dp.Render()
	assert.NotContains(t, out, "not a directory")
	assert.NotContains(t, out, "direct session")
}

func TestDirectoryPicker_ClearSelectionStateHidesHint(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a"})
	dp.Focus()
	dp.SetSelectionState(false, false)
	require.Contains(t, dp.Render(), "not a directory")

	// Clearing returns the indicator to "unknown": no hint of any kind until a fresh
	// check resolves.
	dp.ClearSelectionState()
	out := dp.Render()
	assert.NotContains(t, out, "not a directory")
	assert.NotContains(t, out, "direct session")
}

func TestDirectoryPicker_EmptyMatchHintsFreeText(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repo/a"})
	dp.Focus()
	dp.HandleKeyPress(runes("zzz")) // matches nothing, not path-like
	require.Empty(t, dp.visibleItems())
	assert.Contains(t, dp.Render(), "type a path")
}

func TestDirectoryPicker_UpdateCandidatesPreservesFilter(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/repos/alpha"})
	dp.HandleKeyPress(runes("br"))
	if items := dp.visibleItems(); len(items) != 0 {
		t.Fatalf("precondition: %q should match nothing, got %v", "br", items)
	}

	dp.UpdateCandidates([]string{"/repos/alpha", "/repos/bravo"})

	items := dp.visibleItems()
	if len(items) != 1 || items[0] != "/repos/bravo" {
		t.Fatalf("after update, filter %q should match the new candidate, got %v", "br", items)
	}
}

func TestDirectoryPicker_UpdateCandidatesKeepsCursorOnSelection(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/a", "/b", "/c"})
	dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // select /b
	if got := dp.GetSelectedPath(); got != "/b" {
		t.Fatalf("precondition: selected %q, want /b", got)
	}

	// New scan results land, inserting an entry ahead of the selection.
	dp.UpdateCandidates([]string{"/x", "/a", "/b", "/c"})

	if got := dp.GetSelectedPath(); got != "/b" {
		t.Fatalf("selection moved to %q after update, want /b", got)
	}
}

func TestDirectoryPicker_UpdateCandidatesClampsCursorWhenListShrinks(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/a", "/b", "/c"})
	dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	dp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // select /c

	dp.UpdateCandidates([]string{"/z"})

	if got := dp.GetSelectedPath(); got != "/z" {
		t.Fatalf("after shrink, selected %q, want /z", got)
	}
}

func TestDirectoryPicker_UpdateCandidatesLeavesPathModeAlone(t *testing.T) {
	dir := t.TempDir()
	dp := NewDirectoryPicker([]string{"/a"})
	dp.HandleKeyPress(runes(dir)) // absolute path: filesystem-browse mode
	before := dp.GetSelectedPath()

	dp.UpdateCandidates([]string{"/x", "/y"})

	if got := dp.GetSelectedPath(); got != before {
		t.Fatalf("path-mode selection changed by update: %q -> %q", before, got)
	}
}

func TestDirectoryPicker_UpdateCandidatesDedups(t *testing.T) {
	dp := NewDirectoryPicker([]string{"/a"})
	dp.UpdateCandidates([]string{"/a", "/b", "/a", "", "/b"})

	items := dp.visibleItems()
	if len(items) != 2 || items[0] != "/a" || items[1] != "/b" {
		t.Fatalf("got %v, want [/a /b]", items)
	}
}

func TestDirectoryPicker_FilterMatchesDisplayPathNotHomePrefix(t *testing.T) {
	// Regression: ranking ran on raw absolute paths, so the 'h' in "/home/…"
	// matched a query's 'h' and every home-dir path matched "hub" — three box
	// paths outranked ~/quantivly/hub (PR #120 screenshot). Matching must use
	// the rendered (~-collapsed) form.
	home := t.TempDir()
	t.Setenv("HOME", home)
	hub := filepath.Join(home, "quantivly", "hub")
	dp := NewDirectoryPicker([]string{
		filepath.Join(home, "quantivly", "platform", "src", "box"),
		filepath.Join(home, "quantivly", "testing-box-baseline"),
		filepath.Join(home, "quantivly", "box"),
		hub,
	})
	dp.HandleKeyPress(runes("hub"))
	assert.Equal(t, []string{hub}, dp.visibleItems(),
		"only the hub path matches 'hub' once the home prefix is out of the match string")
}

func TestDirectoryPicker_FilterWeighsBasenameMatches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mid := filepath.Join(home, "box-tools", "legacy")
	exact := filepath.Join(home, "quantivly", "box")
	// Priority order puts the mid-path match first; the basename hit must
	// still outrank it.
	dp := NewDirectoryPicker([]string{mid, exact})
	dp.HandleKeyPress(runes("box"))
	assert.Equal(t, []string{exact, mid}, dp.visibleItems(),
		"a candidate whose name matches the query outranks one matching mid-path")
}
