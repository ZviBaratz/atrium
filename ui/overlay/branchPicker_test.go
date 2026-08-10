package overlay

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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
	bp.HandleKeyPress(keyMsg("down"))
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
	assert.Contains(t, out, strings.TrimSpace(searchFailedNote))
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
	assert.Contains(t, out, strings.TrimSpace(searchFailedNote), "the unfocused header must surface the error")
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
	require.Contains(t, bp.Render(), strings.TrimSpace(searchFailedNote))

	bp.HandleKeyPress(runes("ma"))
	out := bp.Render()
	assert.NotContains(t, out, strings.TrimSpace(searchFailedNote))
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
	assert.NotContains(t, out, strings.TrimSpace(searchFailedNote))
	assert.Contains(t, out, "main")
}

// The search-error note's cells are carved out of the header's variable middle, so an
// unbounded base label or a long typed filter truncates and the note always survives.
//
// This is the defect #557 reported, and the reason the tests above could not see it: they
// assert the note is *present*, at width 0, so they render the very line and never measure
// it. The focused case needs no user content at all — "Base branch (filter: ▌)" is 23 of
// the 42 cells, and the old 24-cell note put the header at 47 before a keystroke.
func TestBranchPicker_SearchErrorNoteSurvivesAnUnboundedHeader(t *testing.T) {
	const width = claudeFieldInnerWidth
	note := strings.TrimSpace(searchFailedNote)

	t.Run("focused, empty filter", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.SetWidth(width)
		bp.Focus()
		bp.SetError(bp.GetFilterVersion())

		header := firstLine(bp.Render())
		assert.LessOrEqual(t, lipgloss.Width(header), width, "the header must fit: %q", header)
		assert.Contains(t, header, note, "with nothing typed there is nothing to truncate but the note")
	})

	t.Run("focused, long filter", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.SetWidth(width)
		bp.Focus()
		bp.HandleKeyPress(runes("feature/a-very-long-branch-name"))
		bp.SetError(bp.GetFilterVersion())

		header := firstLine(bp.Render())
		assert.LessOrEqual(t, lipgloss.Width(header), width, "the header must fit: %q", header)
		assert.Contains(t, header, note, "the filter truncates, never the note")
	})

	t.Run("blurred, long base label", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.SetWidth(width)
		bp.SetHeadLabel("feature/adaptive-light-dark-theming")
		bp.SetResults(nil, bp.GetFilterVersion())
		bp.SetError(bp.GetFilterVersion())

		header := firstLine(bp.Render())
		assert.LessOrEqual(t, lipgloss.Width(header), width, "the header must fit: %q", header)
		assert.Contains(t, header, note, "the base label truncates, never the note")
		assert.Contains(t, header, "…", "and the cut is marked")
	})

	// "develop" is the regression the sweep's fixture pins: every branch name but one
	// literally called "main" pushed the old note off the row.
	t.Run("blurred, an ordinary branch name", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.SetWidth(width)
		bp.SetHeadLabel("develop")
		bp.SetResults(nil, bp.GetFilterVersion())
		bp.SetError(bp.GetFilterVersion())

		header := firstLine(bp.Render())
		assert.LessOrEqual(t, lipgloss.Width(header), width, "the header must fit: %q", header)
		assert.Contains(t, header, "HEAD (develop)", "an ordinary branch name must not need truncating")
		assert.Contains(t, header, note)
	})
}

// An inert picker renders an explanatory placeholder instead of the filter/list UI, at
// the same height as the enabled unfocused render so the surrounding form never jumps
// when the project selection crosses a git/non-git boundary.
//
// This covers the direct-target case; TestBranchPicker_InvalidTargetIsNotCalledDirect
// covers the other one. Reading "disabled" as "direct" is what let the placeholder call
// an invalid target a direct session for as long as it did (#545), so the two inert
// cases get a test each rather than one test and an assumption.
func TestBranchPicker_DisabledRendersPlaceholderAtConstantHeight(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetResults([]string{"main"}, bp.GetFilterVersion())
	enabledHeight := strings.Count(bp.Render(), "\n")

	bp.SetTarget(targetDirect)
	out := bp.Render()

	assert.Contains(t, out, "direct session", "placeholder must explain why the picker is inert")
	assert.NotContains(t, out, "searching")
	assert.Equal(t, enabledHeight, strings.Count(out, "\n"), "height must not change when disabled")
}

// The picker is inert for a direct target and for an invalid one alike, but the two are
// not the same fact and the placeholder must not conflate them.
//
// Until #545 it said "direct session — no git branching" for both, so pointing the form
// at a path that is not a directory produced a form claiming it would create a direct
// session there. The bug was invisible because the only test of the disabled placeholder
// set the disabled bit directly, never through the invalid path.
func TestBranchPicker_InvalidTargetIsNotCalledDirect(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetTarget(targetInvalid)

	out := bp.Render()
	assert.Contains(t, out, "not a directory", "the placeholder must name the real reason")
	assert.NotContains(t, out, "direct session", "an invalid target is not a direct session")
	assert.True(t, bp.Disabled(), "it is still inert — there is nothing to branch from")
}

// Disabling must clamp the selection: a branch chosen while the previous project was a git
// repo cannot leak into a direct session's submit.
func TestBranchPicker_DisabledSelectionIsEmpty(t *testing.T) {
	bp := NewBranchPicker()
	bp.Focus()
	bp.SetResults([]string{"main", "develop"}, bp.GetFilterVersion())
	bp.HandleKeyPress(keyMsg("down"))
	require.NotEmpty(t, bp.GetSelectedBranch(), "sanity: a real branch is selected")

	bp.SetTarget(targetDirect)
	assert.Empty(t, bp.GetSelectedBranch(), "disabled picker must report no base branch")

	bp.SetTarget(targetGit)
	assert.NotEmpty(t, bp.GetSelectedBranch(), "re-enabling restores the selection")
}

// A disabled picker ignores input (it is skipped in the form's Tab order, so this is a
// defensive backstop, not a reachable path).
func TestBranchPicker_DisabledIgnoresKeys(t *testing.T) {
	bp := NewBranchPicker()
	bp.SetTarget(targetDirect)
	consumed, filterChanged := bp.HandleKeyPress(runes("abc"))
	assert.False(t, consumed)
	assert.False(t, filterChanged)
	assert.Empty(t, bp.GetFilter())
}

// TestBranchPicker_PreferBranch covers the fork form's base-branch default (#657)
// at the level the app tests cannot reach: with the HEAD option showing.
//
// Selection here is positional, and item 0 is the HEAD option whenever it is shown,
// so a preference that indexes straight into results lands one row short. The app
// path never exercises that — it seeds a filter that exactly matches, which hides
// the HEAD option — so without this the offset is unguarded in both directions.
func TestBranchPicker_PreferBranch(t *testing.T) {
	t.Run("offsets past the HEAD option", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.PreferBranch("feature/b")
		bp.SetResults([]string{"feature/a", "feature/b", "feature/c"}, bp.filterVersion)

		if got := bp.GetSelectedBranch(); got != "feature/b" {
			t.Errorf("GetSelectedBranch = %q, want %q — item 0 is the HEAD option, so the "+
				"preference must index past it", got, "feature/b")
		}
	})

	t.Run("matches exactly, not by containment", func(t *testing.T) {
		// The hazard this feature generates itself: forking session "x" makes "x-fork",
		// so a repo routinely holds a branch whose name contains another's. Ordered
		// with the longer name first, since that is what a containment match would take.
		bp := NewBranchPicker()
		bp.PreferBranch("zvi/issue-644")
		bp.SetResults([]string{"zvi/issue-644-fork", "zvi/issue-644"}, bp.filterVersion)

		if got := bp.GetSelectedBranch(); got != "zvi/issue-644" {
			t.Errorf("GetSelectedBranch = %q, want %q — a fork would be based on its own "+
				"sibling rather than the conversation's branch", got, "zvi/issue-644")
		}
	})

	t.Run("applies to the first result set only", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.PreferBranch("feature/b")
		bp.SetResults([]string{"feature/a", "feature/b"}, bp.filterVersion)
		bp.SetResults([]string{"feature/b", "feature/a"}, bp.filterVersion)

		if got := bp.GetSelectedBranch(); got == "feature/b" {
			t.Error("the preference re-applied on a second delivery; it would drag the " +
				"cursor back on every keystroke while the user filters")
		}
	})

	t.Run("an empty name arms nothing", func(t *testing.T) {
		bp := NewBranchPicker()
		bp.PreferBranch("")
		bp.SetResults([]string{"feature/a"}, bp.filterVersion)
		if got := bp.GetSelectedBranch(); got != "" {
			t.Errorf("GetSelectedBranch = %q, want the HEAD default", got)
		}
	})

	// SetFilter must bump the version, or the search the caller issues for the seeded
	// filter is indistinguishable from the one already in flight for the old text —
	// and whichever lands last wins.
	t.Run("seeding the filter invalidates in-flight results", func(t *testing.T) {
		bp := NewBranchPicker()
		stale := bp.filterVersion
		bp.SetFilter("feature/b")
		if bp.filterVersion == stale {
			t.Fatal("SetFilter did not bump the version; a stale search would be accepted")
		}
		bp.PreferBranch("feature/b")
		bp.SetResults([]string{"feature/a", "feature/b"}, stale) // the in-flight one
		if got := bp.GetSelectedBranch(); got != "" {
			t.Errorf("results from before the filter was seeded were accepted (selected %q)", got)
		}
		bp.SetResults([]string{"feature/a", "feature/b"}, bp.filterVersion)
		if got := bp.GetSelectedBranch(); got != "feature/b" {
			t.Errorf("GetSelectedBranch = %q, want %q once the fresh results land", got, "feature/b")
		}
	})
}
