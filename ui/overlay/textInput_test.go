package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tab(o *TextInputOverlay)      { o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) }
func shiftTab(o *TextInputOverlay) { o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyShiftTab}) }

func TestTextInputOverlay_SimpleFocusCycle(t *testing.T) {
	o := NewTextInputOverlay("Title", "")
	// Stops: [textarea, enter]; focus starts on the textarea.
	assert.True(t, o.isTextarea())
	tab(o)
	assert.True(t, o.isEnterButton())
	tab(o)
	assert.True(t, o.isTextarea())
}

func TestTextInputOverlay_GetSelectedPathNilWithoutPicker(t *testing.T) {
	o := NewTextInputOverlay("Title", "")
	assert.Equal(t, "", o.GetSelectedPath())
}

func TestQuickSendOverlay_EnterSubmits(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	assert.True(t, o.isTextarea(), "focus should start on the textarea")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("yes")})

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, shouldClose, "Enter should close the quick-send overlay")
	assert.True(t, o.IsSubmitted(), "Enter should submit in quick-send mode")
	assert.False(t, o.IsCanceled())
	assert.Equal(t, "yes", o.GetValue())
}

func TestQuickSendOverlay_AltEnterInsertsNewline(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one")})

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	assert.False(t, shouldClose, "Alt+Enter must not submit")
	assert.False(t, o.IsSubmitted(), "Alt+Enter must not submit")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	assert.Equal(t, "line one\nline two", o.GetValue(), "Alt+Enter should insert a newline")
}

func TestQuickSendOverlay_EscCancels(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, shouldClose)
	assert.True(t, o.IsCanceled())
	assert.False(t, o.IsSubmitted())
}

func TestTextInputOverlay_InvalidateBumpsVersion(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	before := o.BranchFilterVersion()
	after := o.InvalidateBranchSearch()
	assert.Greater(t, after, before)
}

func TestSessionCreateOverlay_FocusStartsOnDirectoryAndCycles(t *testing.T) {
	// No profiles → stops: [directory, branch, title, textarea, enter]; focus starts on
	// the project picker, and the base branch follows immediately since it is scoped to
	// the chosen project.
	o := NewSessionCreateOverlay(nil, []string{"/repo/a", "/repo/b"})
	assert.True(t, o.IsCreateForm())
	assert.True(t, o.isDirectoryPicker(), "focus should start on the project picker")

	tab(o)
	assert.True(t, o.isBranchPicker(), "base branch comes right after the project")
	tab(o)
	assert.True(t, o.isTitle())
	tab(o)
	assert.True(t, o.isTextarea())
	tab(o)
	assert.True(t, o.isEnterButton())
	tab(o)
	assert.True(t, o.isDirectoryPicker(), "Tab wraps back to the project picker")

	shiftTab(o)
	assert.True(t, o.isEnterButton())
}

// The branch section must render between the project and the title, matching the Tab order.
func TestSessionCreateOverlay_RendersBranchBetweenProjectAndTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.SetSize(80, 40)
	out := o.Render()

	proj := strings.Index(out, "Project")
	base := strings.Index(out, "Base")
	title := strings.Index(out, "Title")
	require.GreaterOrEqual(t, proj, 0, "form must show the Project field")
	require.GreaterOrEqual(t, base, 0, "form must show the Base branch field")
	require.GreaterOrEqual(t, title, 0, "form must show the Title field")
	assert.Less(t, proj, base, "Project must render above Base branch")
	assert.Less(t, base, title, "Base branch must render above Title")
}

func TestSessionCreateOverlay_RendersProjectAboveTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.SetSize(80, 24)
	out := o.Render()

	proj := strings.Index(out, "Project")
	title := strings.Index(out, "Title")
	require.GreaterOrEqual(t, proj, 0, "form must show the Project field")
	require.GreaterOrEqual(t, title, 0, "form must show the Title field")
	assert.Less(t, proj, title, "Project must render above Title")
}

func TestSessionCreateOverlay_TabCompletesDirectoryThenAdvances(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "alpha"), 0o755))

	o := NewSessionCreateOverlay(nil, []string{root})
	assert.True(t, o.isDirectoryPicker())

	// Type a unique path prefix, then Tab — completion happens in place, focus stays.
	o.HandleKeyPress(runes(root + "/al"))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.True(t, o.isDirectoryPicker(), "Tab completes in place rather than advancing")
	assert.Equal(t, filepath.Join(root, "alpha"), o.GetSelectedPath())

	// Tab again with nothing left to complete advances to the next field (base branch).
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.True(t, o.isBranchPicker(), "with nothing to complete, Tab advances focus")
}

func TestSessionCreateOverlay_CtrlSSubmitsFromAnyField(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	// Focus starts on the project picker, not the submit button.
	assert.True(t, o.isDirectoryPicker())

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlS})
	assert.True(t, shouldClose, "Ctrl+S should close the form")
	assert.True(t, o.IsSubmitted(), "Ctrl+S should submit from a non-button field")
	assert.False(t, o.IsCanceled())
}

func TestSessionCreateOverlay_GetTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	// Focus starts on the project picker; Tab past the branch picker to the title,
	// then runes land there.
	tab(o)
	tab(o)
	assert.True(t, o.isTitle())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("my-feature")})
	assert.Equal(t, "my-feature", o.GetTitle())
	// The default candidate is exposed as the chosen project.
	assert.Equal(t, "/repo/a", o.GetSelectedPath())
}

// When the target is not a git repo (direct session), the branch stop is skipped by both
// Tab directions: forward from the project lands on the title, and Shift+Tab from the
// title returns to the project.
func TestSessionCreateOverlay_TabSkipsDisabledBranch(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/not/a/repo"})
	o.SetTargetValidity(true, true, "") // valid directory, not a git repo → direct session
	assert.True(t, o.isDirectoryPicker())

	tab(o)
	assert.True(t, o.isTitle(), "Tab must skip the disabled branch picker")
	shiftTab(o)
	assert.True(t, o.isDirectoryPicker(), "Shift+Tab must skip the disabled branch picker")
}

// Enter advances past a disabled branch stop too — Enter on the project must not land the
// user on an inert field.
func TestSessionCreateOverlay_EnterSkipsDisabledBranch(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/not/a/repo"})
	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isDirectoryPicker())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, o.isTitle(), "Enter must skip the disabled branch picker")
}

// The quick-create contract: n focuses the title, typing a name and pressing
// Enter creates the session — no two-hand ⌃S chord on the fast path.
func TestSessionCreateOverlay_EnterOnFilledTitleSubmits(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.FocusTitle()
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("my-task")})

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, shouldClose, "Enter on a filled title must close the form")
	assert.True(t, o.IsSubmitted())
	assert.Equal(t, "my-task", o.GetTitle())
}

// Enter on an empty title advances instead of submitting: submitting would only
// bounce off the title-required validation, so the keystroke moves the user on.
func TestSessionCreateOverlay_EnterOnEmptyTitleAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.FocusTitle()

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.False(t, shouldClose)
	assert.False(t, o.IsSubmitted())
	assert.True(t, o.isTextarea(), "Enter on an empty title moves to the prompt")
}

// Enter inside the create-form prompt stays a newline — the prompt is multiline
// by design, which is exactly why title-enter (not prompt-enter) is the quick
// submit.
func TestSessionCreateOverlay_EnterInPromptInsertsNewline(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.FocusTitle()
	tab(o) // title → prompt
	require.True(t, o.isTextarea())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one")})

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, shouldClose, "Enter in the prompt must not submit the form")
	assert.False(t, o.IsSubmitted())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	assert.Equal(t, "line one\nline two", o.GetValue())
}

// If the disable verdict lands while the branch picker holds focus (the async validity
// check resolving after the user tabbed ahead), focus is pushed to the next enabled stop
// rather than stranding the user on an inert field.
func TestSessionCreateOverlay_FocusEvictedWhenBranchDisabled(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/not/a/repo"})
	tab(o)
	assert.True(t, o.isBranchPicker())

	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isTitle(), "focus must move off the now-disabled branch picker")
}

// ClearTargetValidity (the debounce window while a new path's verdict is pending) must not
// flicker the branch section: the last known disabled/enabled state holds until the fresh
// verdict re-sets it.
func TestSessionCreateOverlay_ClearValidityKeepsBranchDisabledState(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/not/a/repo"})
	o.SetTargetValidity(true, true, "")
	o.ClearTargetValidity()

	tab(o)
	assert.True(t, o.isTitle(), "branch stays disabled through the unknown-validity window")

	o.SetTargetValidity(true, false, "main") // fresh verdict: a git repo again
	shiftTab(o)
	assert.True(t, o.isBranchPicker(), "a git verdict re-enables the branch stop")
}

// An invalid target (not a directory at all) disables the branch picker just like a
// non-git one — there is nothing to list branches in.
func TestSessionCreateOverlay_InvalidTargetDisablesBranch(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/nonexistent"})
	o.SetTargetValidity(false, false, "")

	tab(o)
	assert.True(t, o.isTitle(), "Tab must skip the branch picker for an invalid target")
	assert.Equal(t, "", o.GetSelectedBranch())
}

// The Title label carries a dim "(required)" marker while the field is empty — the only
// hard-required input — and drops it once a title is typed. Submit-time validation stays
// as the backstop.
func TestSessionCreateOverlay_TitleRequiredMarker(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "(required)", "empty title must show the marker")

	tab(o)
	tab(o)
	require.True(t, o.isTitle())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.NotContains(t, o.Render(), "(required)", "a typed title clears the marker")
}

// The whole form must render the same number of lines no matter which field holds focus,
// so the vertically centered overlay does not jump as the user Tabs between fields.
func TestSessionCreateOverlay_RenderHeightConstantAcrossFocus(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a", "/repo/b"})
	o.SetSize(80, 40)

	o.focusStop(stopDirectory)
	dirFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTextarea)
	promptFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopBranch)
	branchFocused := strings.Count(o.Render(), "\n")

	assert.Equal(t, dirFocused, promptFocused, "overlay height must not change between fields")
	assert.Equal(t, dirFocused, branchFocused, "overlay height must not change between fields")

	// Disabling the branch picker (non-git target) must not change the form height either —
	// the inert placeholder keeps the section's exact shape.
	o.SetTargetValidity(true, true, "")
	o.focusStop(stopDirectory)
	branchDisabled := strings.Count(o.Render(), "\n")
	assert.Equal(t, dirFocused, branchDisabled, "overlay height must not change when the branch section is disabled")
}

// The form must shrink to fit short terminals (it has a fixed-height default that overflows
// otherwise), and must still render at a constant height regardless of which field is focused.
func TestSessionCreateOverlay_FitsShortTerminal(t *testing.T) {
	o := NewSessionCreateOverlay(nil, []string{"/repo/a"})
	o.SetBranchResults([]string{"main", "develop", "feature/x"}, o.BranchFilterVersion())

	// The form collapses its picker/prompt rows to fit; at a comfortable-but-short 24 rows it
	// must already shrink from its 28-line default, and never exceed the terminal height.
	for _, h := range []int{24, 30, 50} {
		o.SetSize(80, h)
		for _, stop := range []focusStop{stopTitle, stopTextarea, stopDirectory, stopBranch, stopEnter} {
			o.focusStop(stop)
			got := strings.Count(o.Render(), "\n") + 1
			assert.LessOrEqual(t, got, h, "h=%d focus=%d: overlay rendered %d lines, must fit", h, stop, got)
		}
	}
}

// dropBlankLinesToFit is the height-degradation primitive: it must shed interior blank
// lines down to the budget, but never the first line, the last line, or any line that
// carries visible content. These invariants are what keep the title and the submit
// button on screen when the form is compacted.
func TestDropBlankLinesToFit(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		budget int
		want   []string
	}{
		{
			name:   "already fits is returned unchanged",
			lines:  []string{"a", "", "b"},
			budget: 5,
			want:   []string{"a", "", "b"},
		},
		{
			name:   "drops interior blanks until it fits",
			lines:  []string{"title", "", "body", "", "button"},
			budget: 3,
			want:   []string{"title", "body", "button"},
		},
		{
			name:   "stops once the budget is met, keeping later blanks",
			lines:  []string{"title", "", "", "body", "button"},
			budget: 4,
			want:   []string{"title", "", "body", "button"},
		},
		{
			name:   "never drops the first or last line even when blank",
			lines:  []string{"", "body", ""},
			budget: 1,
			want:   []string{"", "body", ""},
		},
		{
			name:   "only width-zero lines are removable, never whitespace content",
			lines:  []string{"title", "   ", "", "button"},
			budget: 2,
			want:   []string{"title", "   ", "button"},
		},
		{
			name:   "no removable blanks leaves the slice over budget",
			lines:  []string{"a", "b", "c", "d"},
			budget: 2,
			want:   []string{"a", "b", "c", "d"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dropBlankLinesToFit(tc.lines, tc.budget))
		})
	}
}

// fitOverlay's width pass is the second line of defense behind each picker's own
// SetWidth: a line wider than innerWidth (e.g. a deep project path or profile command
// the picker did not pre-trim) must be truncated with an ellipsis so the bordered box
// can never spill past t.width. The integration bounds test cannot provoke this branch
// because the pickers usually pre-trim, so it is pinned directly here.
func TestFitOverlay_TruncatesWideLinesToInnerWidth(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	o.SetSize(80, 40) // innerWidth = 80 - 6 = 74

	const innerWidth = 74
	wide := strings.Repeat("x", 200)
	short := "kept intact"
	got := o.fitOverlay(wide+"\n"+short, innerWidth)

	// The box is anchored to t.width: no rendered line may exceed it, and the long
	// line must have been ellipsized rather than passed through whole.
	for i, l := range strings.Split(got, "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(l), 80, "line %d wider than terminal", i)
	}
	assert.Contains(t, got, "…", "the over-wide line should be ellipsized")
	assert.NotContains(t, got, wide, "the untruncated 200-char line must not survive")
	assert.Contains(t, got, short, "a line within innerWidth must pass through untouched")
}

// fitOverlay's height pass must compact a too-tall body down to t.height by shedding
// only blank lines, leaving the bordered box within the terminal.
func TestFitOverlay_CompactsHeightWithinTerminal(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	o.SetSize(80, 24) // budget = 24 - 4 = 20 inner rows

	// 30 lines, alternating content and droppable blanks, with content at both ends.
	parts := []string{"TITLE"}
	for i := 0; i < 28; i++ {
		if i%2 == 0 {
			parts = append(parts, "row")
		} else {
			parts = append(parts, "")
		}
	}
	parts = append(parts, "BUTTON")

	got := o.fitOverlay(strings.Join(parts, "\n"), 74)

	assert.LessOrEqual(t, strings.Count(got, "\n")+1, 24, "compacted box must fit the terminal height")
	assert.Contains(t, got, "TITLE", "first content line must be preserved")
	assert.Contains(t, got, "BUTTON", "last content line (the action) must be preserved")
}
