package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Changing the target repo invalidates the branch results, but the focused picker must
// keep its height and show a "searching…" hint rather than blanking to "No matching
// branches" — otherwise the list flickers and the overlay jumps on every directory move.
func TestBranchPicker_RenderHeightConstantWhileLoading(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetResults([]string{"main", "develop", "feature"}, bp.GetFilterVersion())
	withResults := strings.Count(bp.Render(), "\n")

	bp.Invalidate() // directory changed: results cleared, now loading
	out := bp.Render()

	assert.Equal(t, withResults, strings.Count(out, "\n"), "height must not change while reloading")
	assert.Contains(t, out, "searching")
	assert.NotContains(t, out, "No matching branches")
}

// SetResults with a matching version clears the loading state.
func TestBranchPicker_SetResultsClearsLoading(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	version := bp.Invalidate()
	assert.Contains(t, bp.Render(), "searching")

	bp.SetResults([]string{"main"}, version)
	assert.NotContains(t, bp.Render(), "searching")
}

// The default base option names the actual branch HEAD points at once it is resolved,
// falling back to the generic label until then and flagging a detached HEAD.
func TestBranchPicker_HeadLabelResolves(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetResults(nil, bp.GetFilterVersion())
	assert.Contains(t, bp.Render(), "HEAD (current branch)", "unresolved → generic label")

	bp.SetHeadLabel("main")
	assert.Contains(t, bp.Render(), "HEAD (main)", "resolved → the actual branch name")

	bp.SetHeadLabel("HEAD") // git's --abbrev-ref result for a detached HEAD
	assert.Contains(t, bp.Render(), "HEAD (detached)")
}

// Selecting the HEAD option must mean "no explicit base" regardless of its label — the
// option is identified by position, not by its (now dynamic) display text.
func TestBranchPicker_HeadOptionSelectionIsPositional(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetHeadLabel("main")
	bp.SetResults([]string{"develop"}, bp.GetFilterVersion())

	assert.Empty(t, bp.GetSelectedBranch(), "cursor on the HEAD option → no explicit base")
	bp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "develop", bp.GetSelectedBranch(), "cursor on a result → that branch")
}

// An exact filter match still hides the HEAD option (the user is homing in on that
// branch as the base), and the first result is then selectable at cursor 0.
func TestBranchPicker_ExactMatchHidesHeadOption(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetHeadLabel("main")
	bp.HandleKeyPress(runes("develop"))
	bp.SetResults([]string{"develop"}, bp.GetFilterVersion())

	assert.NotContains(t, bp.Render(), "HEAD (main)", "exact match hides the HEAD option")
	assert.Equal(t, "develop", bp.GetSelectedBranch())
}

// A failed search must clear the loading state and surface an error hint — never spin on
// "searching…" forever (the old behavior when the search errored, e.g. in a non-git dir).
func TestBranchPicker_SetErrorClearsLoadingAndShowsHint(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	version := bp.Invalidate()
	require.Contains(t, bp.Render(), "searching")

	bp.SetError(version)
	out := bp.Render()
	assert.NotContains(t, out, "searching", "error must clear the loading state")
	assert.Contains(t, out, "couldn't list branches")
}

// The error hint must survive losing focus: a search that fails while (or before) the
// picker is blurred would otherwise leave the unfocused header showing a normal selection
// with no sign anything went wrong, and the height must stay at the unfocused shape.
func TestBranchPicker_ErrorHintVisibleWhenUnfocused(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetResults(nil, bp.GetFilterVersion())
	unfocusedHeight := strings.Count(bp.Render(), "\n")

	bp.SetError(bp.Invalidate())
	out := bp.Render()
	assert.Contains(t, out, "couldn't list branches", "the unfocused header must surface the error")
	assert.Equal(t, unfocusedHeight, strings.Count(out, "\n"), "the hint must not change the picker height")
}

// SetError is version-checked like SetResults: a stale error (for an abandoned search)
// must not clobber the current state.
func TestBranchPicker_SetErrorIgnoresStaleVersion(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	stale := bp.Invalidate()
	fresh := bp.Invalidate()

	bp.SetError(stale)
	assert.Contains(t, bp.Render(), "searching", "stale error must not clear the in-flight state")

	bp.SetResults([]string{"main"}, fresh)
	assert.Contains(t, bp.Render(), "main")
}

// Editing the filter after an error returns to the loading state — the error describes the
// previous search, not the new one.
func TestBranchPicker_FilterEditClearsError(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetError(bp.Invalidate())
	require.Contains(t, bp.Render(), "couldn't list branches")

	bp.HandleKeyPress(runes("ma"))
	out := bp.Render()
	assert.NotContains(t, out, "couldn't list branches")
	assert.Contains(t, out, "searching")
}

// Fresh results after an error replace the hint with the list.
func TestBranchPicker_ResultsClearError(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	version := bp.Invalidate()
	bp.SetError(version)

	version = bp.Invalidate()
	bp.SetResults([]string{"main"}, version)
	out := bp.Render()
	assert.NotContains(t, out, "couldn't list branches")
	assert.Contains(t, out, "main")
}

// A disabled picker (non-git target → direct session) renders an explanatory placeholder
// instead of the filter/list UI, at the same height as the enabled unfocused render so
// the surrounding form never jumps when the project selection crosses a git/non-git
// boundary.
func TestBranchPicker_DisabledRendersPlaceholderAtConstantHeight(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetResults([]string{"main"}, bp.GetFilterVersion())
	enabledHeight := strings.Count(bp.Render(), "\n")

	bp.SetDisabled(true)
	out := bp.Render()

	assert.Contains(t, out, "direct session", "placeholder must explain why the picker is inert")
	assert.NotContains(t, out, "searching")
	assert.Equal(t, enabledHeight, strings.Count(out, "\n"), "height must not change when disabled")
}

// Disabling must clamp the selection: a branch chosen while the previous project was a git
// repo cannot leak into a direct session's submit.
func TestBranchPicker_DisabledSelectionIsEmpty(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetResults([]string{"main", "develop"}, bp.GetFilterVersion())
	bp.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	require.NotEmpty(t, bp.GetSelectedBranch(), "sanity: a real branch is selected")

	bp.SetDisabled(true)
	assert.Empty(t, bp.GetSelectedBranch(), "disabled picker must report no base branch")

	bp.SetDisabled(false)
	assert.NotEmpty(t, bp.GetSelectedBranch(), "re-enabling restores the selection")
}

// A disabled picker ignores input (it is skipped in the form's Tab order, so this is a
// defensive backstop, not a reachable path).
func TestBranchPicker_DisabledIgnoresKeys(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetDisabled(true)
	consumed, filterChanged := bp.HandleKeyPress(runes("abc"))
	assert.False(t, consumed)
	assert.False(t, filterChanged)
	assert.Empty(t, bp.GetFilter())
}
