package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var twoAccounts = []config.ClaudeAccount{
	{Name: "personal", ConfigDir: "~/.claude"},
	{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
}

func TestSessionCreateOverlay_PrefillSetters(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a", "/repo/b"}, "", nil)

	o.SetTitleValue("Review box 123")
	assert.Equal(t, "Review box 123", o.GetTitle())

	o.SetPrompt("Review box#123")
	assert.Equal(t, "Review box#123", o.GetValue())

	require.True(t, o.SelectPath("/repo/b"))
	assert.Equal(t, "/repo/b", o.GetSelectedPath())
}

func TestSessionCreateOverlay_ProjectHint(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	o.SetProjectHint("detecting…")
	assert.Contains(t, o.Render(), "detecting…")
	o.SetProjectHint("")
	assert.NotContains(t, o.Render(), "detecting…")
}

func TestNewSmartDispatchOverlay(t *testing.T) {
	o := NewSmartDispatchOverlay("Describe the session")
	assert.True(t, o.IsSmartDispatch())
	assert.False(t, o.IsCreateForm())

	plain := NewTextInputOverlay("x", "")
	assert.False(t, plain.IsSmartDispatch())
}

// The account picker is a true override: until the user drives it, the form reports
// no selection (ok=false) so the caller keeps the freshly-resolved auto-route. Only a
// deliberate keypress flips it to an override that wins.
func TestSessionCreateOverlay_AccountOverrideOnlyWhenTouched(t *testing.T) {
	o := NewSessionCreateOverlay(nil, twoAccounts, []string{"/repo/a"}, "", nil)

	sel := o.GetSelectedAccount()
	assert.Nil(t, sel, "an untouched picker must not override auto-routing")

	// Auto-routed preselection is not a user override.
	o.PreselectAccount("quantivly")
	sel = o.GetSelectedAccount()
	assert.Nil(t, sel, "auto preselect alone must not override")

	// The user drives the picker: now it overrides with the chosen account. From the
	// preselected quantivly (last of two), one step wraps around to personal — the
	// point is that the override is engaged, whichever account it lands on.
	o.focusStop(stopAccount)
	o.HandleKeyPress(keyMsg("down"))
	sel = o.GetSelectedAccount()
	require.NotNil(t, sel, "a user choice overrides auto-routing")
	require.NotNil(t, sel.Member, "a user choice pins a specific member")
	assert.Equal(t, "personal", sel.Member.Name)
}

// A form with no configured accounts never overrides — the feature is dormant.
func TestSessionCreateOverlay_NoAccountsNeverOverrides(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	assert.Nil(t, o.GetSelectedAccount())
}

// A single configured account renders no Account section (and adds no chrome): with
// nothing to choose, the picker would be a dead, unfocusable row. The list badge still
// conveys the account.
func TestSessionCreateOverlay_SingleAccountHidesSection(t *testing.T) {
	one := []config.ClaudeAccount{{Name: "solo", ConfigDir: "~/.claude"}}
	o := NewSessionCreateOverlay(nil, one, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	assert.NotContains(t, o.Render(), "Account", "a lone account must not render the picker section")

	o2 := NewSessionCreateOverlay(nil, twoAccounts, []string{"/repo/a"}, "", nil)
	o2.SetSize(80, 40)
	assert.Contains(t, o2.Render(), "Account", "≥2 accounts render the picker section")
}

func tab(o *TextInputOverlay)      { o.HandleKeyPress(keyMsg("tab")) }
func shiftTab(o *TextInputOverlay) { o.HandleKeyPress(keyMsg("shift+tab")) }
func ctrlR(o *TextInputOverlay)    { o.HandleKeyPress(keyMsg("ctrl+r")) }

// vpPlus/vpMinus press the count +/- keys on the (assumed focused) variant control.
func vpPlus(o *TextInputOverlay) {
	o.HandleKeyPress(textMsg("+"))
}
func vpMinus(o *TextInputOverlay) { o.HandleKeyPress(keyMsg("down")) }

// selectOnlyNonClaude drives the variant control to Claude ×0 + non-claude ×1 for a
// two-profile [claude, non-claude] form (mixedProfiles order), so no selected variant
// is claude and the claude-only override fields go inert. It leaves focus and the
// cursor on the claude (first) profile so selectClaude can raise it back.
func selectOnlyNonClaude(o *TextInputOverlay) {
	o.focusStop(stopVariants)
	o.HandleKeyPress(keyMsg("right")) // cursor → non-claude
	vpPlus(o)                         // non-claude 0 → 1
	o.HandleKeyPress(keyMsg("left"))  // cursor → claude
	vpMinus(o)                        // claude 1 → 0
}

// selectClaude raises the claude variant back to ×1 (the cursor is left on claude by
// selectOnlyNonClaude), re-enabling the claude-only override fields.
func selectClaude(o *TextInputOverlay) {
	o.focusStop(stopVariants)
	vpPlus(o) // claude 0 → 1
}

func TestSessionCreateOverlay_DoubleCtrlRClears(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)

	ctrlR(o)
	assert.False(t, o.ClearRequested(), "one Ctrl+R only arms")

	ctrlR(o)
	assert.True(t, o.ClearRequested(), "a second consecutive Ctrl+R requests the clear")
}

func TestSessionCreateOverlay_CtrlRDisarmsOnOtherKey(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()

	ctrlR(o) // arm
	o.HandleKeyPress(textMsg("x"))
	ctrlR(o) // this is now a first press again, not a confirm
	assert.False(t, o.ClearRequested(), "an intervening key disarms the clear")
}

func TestSessionCreateOverlay_ClearHintInFooter(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "⌃R clear")

	ctrlR(o)
	assert.Contains(t, o.Render(), "⌃R again")
}

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
	o.HandleKeyPress(textMsg("yes"))

	shouldClose, _ := o.HandleKeyPress(keyMsg("enter"))
	assert.True(t, shouldClose, "Enter should close the quick-send overlay")
	assert.True(t, o.IsSubmitted(), "Enter should submit in quick-send mode")
	assert.False(t, o.IsCanceled())
	assert.Equal(t, "yes", o.GetValue())
}

func TestQuickSendOverlay_AltEnterInsertsNewline(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	o.HandleKeyPress(textMsg("line one"))

	shouldClose, _ := o.HandleKeyPress(keyMsg("alt+enter"))
	assert.False(t, shouldClose, "Alt+Enter must not submit")
	assert.False(t, o.IsSubmitted(), "Alt+Enter must not submit")

	o.HandleKeyPress(textMsg("line two"))
	assert.Equal(t, "line one\nline two", o.GetValue(), "Alt+Enter should insert a newline")
}

func TestQuickSendOverlay_EscCancels(t *testing.T) {
	o := NewQuickSendOverlay("Send to foo")
	shouldClose, _ := o.HandleKeyPress(keyMsg("esc"))
	assert.True(t, shouldClose)
	assert.True(t, o.IsCanceled())
	assert.False(t, o.IsSubmitted())
}

func TestTextInputOverlay_InvalidateBumpsVersion(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	before := o.BranchFilterVersion()
	after := o.InvalidateBranchSearch()
	assert.Greater(t, after, before)
}

func TestSessionCreateOverlay_FocusStartsOnDirectoryAndCycles(t *testing.T) {
	// No profiles → stops: [directory, branch, title, textarea, enter]; focus starts on
	// the project picker, and the base branch follows immediately since it is scoped to
	// the chosen project.
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a", "/repo/b"}, "", nil)
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

func TestSessionCreateOverlay_FocusModeLandsOnPermissions(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.FocusMode()
	assert.True(t, o.isModeField(), "FocusMode focuses the Permissions chip when it is enabled")
}

func TestSessionCreateOverlay_FocusModeFallsBackToCreateWhenAbsent(t *testing.T) {
	// A non-claude program has no permission-mode field at all; focus falls back to Create.
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "gemini", nil)
	o.FocusMode()
	assert.True(t, o.isEnterButton(), "with no mode field, FocusMode falls back to the Create button")
}

// The branch section must render between the project and the title, matching the Tab order.
func TestSessionCreateOverlay_RendersBranchBetweenProjectAndTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
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

	o := NewSessionCreateOverlay(nil, nil, []string{root}, "", nil)
	assert.True(t, o.isDirectoryPicker())

	// Type a unique path prefix, then Tab — completion happens in place, focus stays.
	o.HandleKeyPress(runes(root + "/al"))
	o.HandleKeyPress(keyMsg("tab"))
	assert.True(t, o.isDirectoryPicker(), "Tab completes in place rather than advancing")
	assert.Equal(t, filepath.Join(root, "alpha"), o.GetSelectedPath())

	// Tab again with nothing left to complete advances to the next field (base branch).
	o.HandleKeyPress(keyMsg("tab"))
	assert.True(t, o.isBranchPicker(), "with nothing to complete, Tab advances focus")
}

func TestSessionCreateOverlay_CtrlSSubmitsFromAnyField(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	// Focus starts on the project picker, not the submit button.
	assert.True(t, o.isDirectoryPicker())

	shouldClose, _ := o.HandleKeyPress(keyMsg("ctrl+s"))
	assert.True(t, shouldClose, "Ctrl+S should close the form")
	assert.True(t, o.IsSubmitted(), "Ctrl+S should submit from a non-button field")
	assert.False(t, o.IsCanceled())
}

func TestSessionCreateOverlay_GetTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	// Focus starts on the project picker; Tab past the branch picker to the title,
	// then runes land there.
	tab(o)
	tab(o)
	assert.True(t, o.isTitle())
	o.HandleKeyPress(textMsg("my-feature"))
	assert.Equal(t, "my-feature", o.GetTitle())
	// The default candidate is exposed as the chosen project.
	assert.Equal(t, "/repo/a", o.GetSelectedPath())
}

// When the target is not a git repo (direct session), the branch stop is skipped by both
// Tab directions: forward from the project lands on the title, and Shift+Tab from the
// title returns to the project.
func TestSessionCreateOverlay_TabSkipsDisabledBranch(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "", nil)
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "", nil)
	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isDirectoryPicker())

	o.HandleKeyPress(keyMsg("enter"))
	assert.True(t, o.isTitle(), "Enter must skip the disabled branch picker")
}

// The quick-create contract: n focuses the title, typing a name and pressing
// Enter creates the session — no two-hand ⌃S chord on the fast path.
func TestSessionCreateOverlay_EnterOnFilledTitleSubmits(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	o.HandleKeyPress(textMsg("my-task"))

	shouldClose, _ := o.HandleKeyPress(keyMsg("enter"))

	assert.True(t, shouldClose, "Enter on a filled title must close the form")
	assert.True(t, o.IsSubmitted())
	assert.Equal(t, "my-task", o.GetTitle())
}

// Enter on an empty title advances instead of submitting: submitting would only
// bounce off the title-required validation, so the keystroke moves the user on.
func TestSessionCreateOverlay_EnterOnEmptyTitleAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()

	shouldClose, _ := o.HandleKeyPress(keyMsg("enter"))

	assert.False(t, shouldClose)
	assert.False(t, o.IsSubmitted())
	assert.True(t, o.isTextarea(), "Enter on an empty title moves to the prompt")
}

// Enter inside the create-form prompt advances to the next field, like Tab — the
// newline keys are Shift+Enter (Alt+Enter on the wire) and Ctrl+J.
func TestSessionCreateOverlay_EnterInPromptAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	tab(o) // title → prompt
	require.True(t, o.isTextarea())
	o.HandleKeyPress(textMsg("line one"))

	shouldClose, _ := o.HandleKeyPress(keyMsg("enter"))
	assert.False(t, shouldClose, "Enter in the prompt must not submit the form")
	assert.False(t, o.IsSubmitted())
	assert.False(t, o.isTextarea(), "Enter should move focus off the prompt")
	assert.Equal(t, "line one", o.GetValue(), "Enter must not insert a newline")
}

// Alt+Enter (what a configured terminal's Shift+Enter sends) inserts a newline and
// keeps focus on the prompt.
func TestSessionCreateOverlay_AltEnterInPromptInsertsNewline(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	tab(o)
	require.True(t, o.isTextarea())
	o.HandleKeyPress(textMsg("line one"))

	shouldClose, _ := o.HandleKeyPress(keyMsg("alt+enter"))
	assert.False(t, shouldClose)
	assert.True(t, o.isTextarea(), "Alt+Enter stays on the prompt")

	o.HandleKeyPress(textMsg("line two"))
	assert.Equal(t, "line one\nline two", o.GetValue())
}

// Ctrl+J is the universal newline that works in any terminal.
func TestSessionCreateOverlay_CtrlJInPromptInsertsNewline(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	tab(o)
	require.True(t, o.isTextarea())
	o.HandleKeyPress(textMsg("line one"))

	o.HandleKeyPress(keyMsg("ctrl+j"))
	assert.True(t, o.isTextarea(), "Ctrl+J stays on the prompt")

	o.HandleKeyPress(textMsg("line two"))
	assert.Equal(t, "line one\nline two", o.GetValue())
}

// Ctrl+Left jumps back a word in the prompt (the textarea default binds only
// Alt+arrow; we add Ctrl+arrow to match the title field).
func TestSessionCreateOverlay_CtrlLeftJumpsWordInPrompt(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.FocusTitle()
	tab(o)
	require.True(t, o.isTextarea())
	o.HandleKeyPress(textMsg("foo bar"))

	o.HandleKeyPress(keyMsg("ctrl+left")) // cursor → start of "bar"
	o.HandleKeyPress(textMsg("X"))
	assert.Equal(t, "foo Xbar", o.GetValue(), "Ctrl+Left should jump back one word")
}

// If the disable verdict lands while the branch picker holds focus (the async validity
// check resolving after the user tabbed ahead), focus is pushed to the next enabled stop
// rather than stranding the user on an inert field.
func TestSessionCreateOverlay_FocusEvictedWhenBranchDisabled(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "", nil)
	tab(o)
	assert.True(t, o.isBranchPicker())

	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isTitle(), "focus must move off the now-disabled branch picker")
}

// ClearTargetValidity (the debounce window while a new path's verdict is pending) must not
// flicker the branch section: the last known disabled/enabled state holds until the fresh
// verdict re-sets it.
func TestSessionCreateOverlay_ClearValidityKeepsBranchDisabledState(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "", nil)
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/nonexistent"}, "", nil)
	o.SetTargetValidity(false, false, "")

	tab(o)
	assert.True(t, o.isTitle(), "Tab must skip the branch picker for an invalid target")
	assert.Equal(t, "", o.GetSelectedBranch())
}

// The Title label carries a dim "(required)" marker while the field is empty — the only
// hard-required input — and drops it once a title is typed. Submit-time validation stays
// as the backstop.
func TestSessionCreateOverlay_TitleRequiredMarker(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "(required)", "empty title must show the marker")

	tab(o)
	tab(o)
	require.True(t, o.isTitle())
	o.HandleKeyPress(textMsg("x"))
	assert.NotContains(t, o.Render(), "(required)", "a typed title clears the marker")
}

// The title row's verdicts — the dim "(required)" hint and the danger error —
// trail the input rather than sitting between the label and the field: a
// variable-width prefix would shift the text under the user's caret on exactly
// the keystrokes that recompute the verdict. And because the input pads itself
// to its Width, the suffix's columns must be carved out of the input, or the
// message would always sit past fitOverlay's truncation edge, invisible.
func TestSessionCreateOverlay_TitleVerdictsTrailInput(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	// The width the copy actually has to survive. This read SetSize(80, 40) — inner 74,
	// a 133-col terminal — while claiming below that the error "must survive width
	// truncation intact", so the truncation it named could never happen and every title
	// verdict app produced overflowed the real row unnoticed (#545).
	o.SetSize(createOverlayWidth, 40)

	titleRow := func() string {
		for _, l := range strings.Split(xansi.Strip(o.Render()), "\n") {
			if strings.Contains(l, "Title") {
				return l
			}
		}
		t.Fatal("no title row rendered")
		return ""
	}

	// Empty field: the placeholder and the "(required)" hint share the row.
	emptyRow := titleRow()
	placeholderCol := strings.Index(emptyRow, "name this session")
	require.GreaterOrEqual(t, placeholderCol, 0, "placeholder must be visible while empty")
	require.Contains(t, emptyRow, "(required)")

	tab(o)
	tab(o)
	require.True(t, o.isTitle())
	o.HandleKeyPress(textMsg("x"))

	// Typing the first character drops the hint; the input must not move.
	typedRow := titleRow()
	typedCol := strings.Index(typedRow, "x")
	assert.Equal(t, placeholderCol, typedCol,
		"the input must not shift when the (required) hint disappears")

	// An error appearing must neither move the input nor precede it, and must
	// survive the row's width truncation in full.
	// A budget-width verdict rather than a quoted literal: app owns the copy and this
	// package cannot import it, so what is testable here is the geometry at the widest
	// verdict the row affords. "z" so the filler cannot be confused with the typed "x".
	verdict := strings.Repeat("z", titleVerdictBudget)
	o.SetTitleError(verdict)
	errorRow := titleRow()
	require.Contains(t, errorRow, "("+verdict+")",
		"the error must survive width truncation intact")
	assert.Equal(t, typedCol, strings.Index(errorRow, "x"),
		"the input must not shift when the error appears")
	assert.Greater(t, strings.Index(errorRow, "("+verdict), strings.Index(errorRow, "x"),
		"the error must trail the input, not precede it")
}

// The whole form must render the same number of lines no matter which field holds focus,
// so the vertically centered overlay does not jump as the user Tabs between fields.
func TestSessionCreateOverlay_RenderHeightConstantAcrossFocus(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a", "/repo/b"}, "", nil)
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
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
	got := o.fitOverlay(wide+"\n"+short, innerWidth, strings.Repeat("─", innerWidth))

	// The box is anchored to t.width: no rendered line may exceed it, and the long
	// line must have been ellipsized rather than passed through whole.
	for i, l := range strings.Split(got, "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(l), 80, "line %d wider than terminal", i)
	}
	assert.Contains(t, got, "…", "the over-wide line should be ellipsized")
	assert.NotContains(t, got, wide, "the untruncated 200-char line must not survive")
	assert.Contains(t, got, short, "a line within innerWidth must pass through untouched")
}

// --- Model field (the optional Claude model override) ---

var mixedProfiles = []config.Profile{
	{Name: "Claude", Program: "claude"},
	{Name: "Aider", Program: "aider --model ollama_chat/gemma3:1b"},
}

// The model field exists only when a selectable program resolves to claude: a claude
// default (or any claude profile) shows it, a non-claude-only form omits it entirely.
func TestSessionCreateOverlay_ModelFieldOnlyForClaude(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "Model", "a claude default program must show the model field")
	assert.GreaterOrEqual(t, o.indexOfStop(stopModel), 0)

	o2 := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "aider", nil)
	o2.SetSize(80, 40)
	assert.NotContains(t, o2.Render(), "Model", "a non-claude form must not show the model field")
	assert.Equal(t, -1, o2.indexOfStop(stopModel))
}

// Tab in the model field completes against the alias list in place ("s" → "sonnet"),
// and only advances focus once there is nothing left to complete — the same
// "complete, then advance" contract as the project field.
func TestSessionCreateOverlay_ModelTabCompletesThenAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)
	require.True(t, o.isModelField())

	o.HandleKeyPress(textMsg("s"))
	tab(o)
	assert.True(t, o.isModelField(), "Tab completes in place rather than advancing")
	assert.Equal(t, "sonnet", o.GetModel())

	tab(o)
	assert.False(t, o.isModelField(), "with nothing to complete, Tab advances focus")
}

// Tab through an untouched model field must keep meaning "default": no completion
// fires on an empty value, focus just advances.
func TestSessionCreateOverlay_ModelEmptyTabAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)

	tab(o)
	assert.False(t, o.isModelField(), "Tab on an empty model field advances immediately")
	assert.Equal(t, "", o.GetModel())
}

// Typed runes are filtered to the safe model-name charset, so the submit-time
// validation backstop can effectively never fire from keyboard input.
func TestSessionCreateOverlay_ModelCharsetFiltered(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)

	for _, r := range "op;u s$" { // ';', ' ', '$' must be dropped
		o.HandleKeyPress(textMsg(string(r)))
	}
	assert.Equal(t, "opus", o.GetModel())
}

// An explicit "default" (the word the no-op chip used to be labeled, typed out in
// custom mode) contributes no override, same as leaving the field untouched. See
// TestModelField_InheritOrDefaultTypedMeansNoOverride for the "inherit" half.
func TestSessionCreateOverlay_ModelDefaultMeansNoOverride(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)
	o.HandleKeyPress(textMsg("default"))
	assert.Equal(t, "", o.GetModel())
}

// Arrowing across the chip row selects aliases without any typing — the
// typo-proof path. The first chip is the no-op "inherit" (no override).
func TestSessionCreateOverlay_ModelChipCycle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)
	assert.Equal(t, "", o.GetModel(), "the no-op chip contributes no override")

	for i := 0; i < 3; i++ { // inherit → fable → haiku → opus
		o.HandleKeyPress(keyMsg("down"))
	}
	assert.Equal(t, "opus", o.GetModel())

	for i := 0; i < 3; i++ {
		o.HandleKeyPress(keyMsg("up"))
	}
	assert.Equal(t, "", o.GetModel(), "cycling back to default drops the override")
}

// One step back from the no-op chip wraps to the last alias — the motivating
// case: reach "sonnet" with a single ← instead of arrowing all the way right.
func TestSessionCreateOverlay_ModelChipWrapsToLast(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)
	require.True(t, o.isModelField())
	assert.Equal(t, "", o.GetModel(), "starts on the no-op chip")

	o.HandleKeyPress(keyMsg("left"))
	assert.Equal(t, "sonnet", o.GetModel(), "← from default wraps to the last alias")

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "", o.GetModel(), "→ from the last alias wraps back to default")
}

// Typing enters custom mode; Left with the text cursor at position 0 returns to
// the chip row with the prior chip selection intact.
func TestSessionCreateOverlay_ModelCustomBackToChips(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)
	o.HandleKeyPress(keyMsg("down")) // fable chip

	o.HandleKeyPress(textMsg("x"))
	assert.Equal(t, "x", o.GetModel(), "typing switches to custom mode seeded with the rune")

	o.HandleKeyPress(keyMsg("left")) // cursor 1 → 0
	o.HandleKeyPress(keyMsg("left")) // at 0 → back to chips
	assert.Equal(t, "fable", o.GetModel(), "returning to chips restores the chip selection")
}

// The rune filter pre-checks runes as if typed at the end of the value, but the
// text cursor can sit anywhere (Home/Ctrl+A): a rune that passes the append
// check can still realize an invalid value once inserted mid-string (a leading
// '.' here). The field's invariant is that it never holds an invalid non-empty
// value — such an insertion must be reverted, keeping the submit-time backstop
// unreachable from keyboard input.
func TestSessionCreateOverlay_ModelMidValueInsertionStaysValid(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopModel)

	o.HandleKeyPress(textMsg("opus"))
	o.HandleKeyPress(keyMsg("home")) // text cursor to position 0
	o.HandleKeyPress(textMsg("."))
	assert.Equal(t, "opus", o.GetModel(), "an insertion realizing an invalid value must be reverted")
}

// The chip row must fit the worst realistic overlay width — an 80-col terminal
// gives the form 42 inner cells — so every chip (and the cursor) stays visible.
func TestModelFieldChipRowWidth(t *testing.T) {
	mf := NewModelField()
	mf.Focus()
	lines := strings.Split(mf.Render(), "\n")
	row := lines[len(lines)-1]
	assert.LessOrEqual(t, lipgloss.Width(row), 41, "chip row must fit 42 inner cells")
}

// With mixed profiles the field is present but tracks the selected profile's agent:
// inert (skipped, no override) while a non-claude profile is selected, re-enabled
// when the selection returns to claude.
func TestSessionCreateOverlay_ModelDisabledForNonClaudeProfile(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	require.GreaterOrEqual(t, o.indexOfStop(stopModel), 0, "a claude profile makes the field present")

	// Claude (first profile) selected: the field takes focus and input.
	o.focusStop(stopModel)
	require.True(t, o.isModelField())
	o.HandleKeyPress(textMsg("opus"))
	assert.Equal(t, "opus", o.GetModel())

	// Drop claude from the batch (Claude ×0, Aider ×1): the field goes inert.
	selectOnlyNonClaude(o)
	assert.Equal(t, "", o.GetModel(), "no claude variant must drop the override")
	o.focusStop(stopTextarea)
	tab(o) // textarea → variants
	tab(o) // variants → (model skipped) …
	assert.False(t, o.isModelField(), "Tab must skip the disabled model field")

	// Re-add claude: the field re-enables and the typed value applies again.
	selectClaude(o)
	assert.Equal(t, "opus", o.GetModel(), "returning to claude restores the override")
}

// The model section must hold the form's constant-height invariant: the same line
// count whether or not it holds focus. That is the "no jump" invariant the form is
// designed around, and focus is the axis it covers.
//
// What the collapse does to the form's height is a different question, and it is
// not asked here — TestCollapsedClaudeFields_HeightHoldsAsTheVariantFlips owns it,
// because the answer is not a property of this section. Until #797 this test
// asserted the inert case as an equality too, and that reading is what the nine
// repeated n/a rows cost: a form that could not stop repeating itself without
// changing size.
func TestSessionCreateOverlay_ModelSectionHeightConstant(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)

	o.focusStop(stopModel)
	modelFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTitle)
	titleFocused := strings.Count(o.Render(), "\n")
	assert.Equal(t, modelFocused, titleFocused, "overlay height must not change with model focus")
}

// collapsedClaudeRowsSaved is the number of rows renderCollapsedClaudeFields frees,
// computed from the size constants rather than written down: three full sections
// replaced by one. A test asserting the number by hand would keep passing if the
// collapsed block grew a row.
//
// Rows freed, not rows the form loses. fitRows hands them to the pickers and the
// prompt wherever those are below their caps, so the form's height moves by much
// less than this and often not at all — which is the point, and what
// TestCollapsedClaudeFields_HeightHoldsAsTheVariantFlips uses this as the ceiling
// for. At the 80×24 floor fitOverlay has already shed the blank rows before the
// collapse can free them, so what is visibly recovered there is smaller again
// (#690 measures it there; TestCreateForm_FloorGoldens renders it).
func collapsedClaudeRowsSaved() int {
	return modelSectionLines + effortSectionLines + modeSectionLines - collapsedClaudeSectionLines
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

	got := o.fitOverlay(strings.Join(parts, "\n"), 74, strings.Repeat("─", 74))

	assert.LessOrEqual(t, strings.Count(got, "\n")+1, 24, "compacted box must fit the terminal height")
	assert.Contains(t, got, "TITLE", "first content line must be preserved")
	assert.Contains(t, got, "BUTTON", "last content line (the action) must be preserved")
}

// --- Mode field (the optional Claude permission-mode override) ---

// The mode field exists only when a selectable program resolves to claude,
// exactly like the model field.
func TestSessionCreateOverlay_ModeFieldOnlyForClaude(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "Permissions", "a claude default program must show the mode field")
	assert.GreaterOrEqual(t, o.indexOfStop(stopMode), 0)

	o2 := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "aider", nil)
	o2.SetSize(80, 40)
	assert.NotContains(t, o2.Render(), "Permissions", "a non-claude form must not show the mode field")
	assert.Equal(t, -1, o2.indexOfStop(stopMode))
}

// Arrowing across the chip row selects modes; the first chip (default)
// contributes no flag, and the cursor wraps at both ends so one keypress
// reaches the opposite end.
func TestSessionCreateOverlay_ModeChipCycle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopMode)
	require.True(t, o.isModeField())
	assert.Equal(t, "", o.GetPermissionMode(), "the no-op chip contributes no flag")

	o.HandleKeyPress(keyMsg("down"))
	assert.Equal(t, "plan", o.GetPermissionMode())
	o.HandleKeyPress(keyMsg("down"))
	assert.Equal(t, "acceptEdits", o.GetPermissionMode())
	o.HandleKeyPress(keyMsg("down"))
	assert.Equal(t, "auto", o.GetPermissionMode())
	o.HandleKeyPress(keyMsg("down")) // wraps past the last chip
	assert.Equal(t, "", o.GetPermissionMode(), "past the last chip wraps to the no-op chip")

	// From the no-op chip, one step back wraps to the last chip.
	o.HandleKeyPress(keyMsg("up"))
	assert.Equal(t, "auto", o.GetPermissionMode(), "before the no-op chip wraps to the last")
}

// The chip row displays the kebab-case label (accept-edits) while the value it
// contributes stays the camelCase CLI enum (acceptEdits, asserted by value in
// TestSessionCreateOverlay_ModeChipCycle). This pins the user-visible half of
// that decoupling — the whole point of the labels slice — so a regression that
// dropped it back to options would fail here, not just silently re-render the
// camelCase token.
func TestSessionCreateOverlay_ModeChipDisplaysKebabLabel(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.SetSize(80, 40)

	got := xansi.Strip(o.Render())
	assert.Contains(t, got, "accept-edits", "the chip row must show the kebab-case label")
	assert.NotContains(t, got, "acceptEdits", "the camelCase CLI value must never reach the display")
}

// Tab on the mode field always advances — chips have nothing to complete.
func TestSessionCreateOverlay_ModeTabAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	o.focusStop(stopMode)

	tab(o)
	assert.False(t, o.isModeField(), "Tab on the mode field advances immediately")
}

// With mixed profiles the field tracks the selected profile's agent: inert
// (skipped, no override) while a non-claude profile is selected, re-enabled
// when the selection returns to claude — alongside the model field.
func TestSessionCreateOverlay_ModeDisabledForNonClaudeProfile(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)
	require.GreaterOrEqual(t, o.indexOfStop(stopMode), 0, "a claude profile makes the field present")

	o.focusStop(stopMode)
	require.True(t, o.isModeField())
	o.HandleKeyPress(keyMsg("down")) // default → plan
	assert.Equal(t, "plan", o.GetPermissionMode())

	// Drop claude from the batch (Claude ×0, Aider ×1): the field goes inert.
	selectOnlyNonClaude(o)
	assert.Equal(t, "", o.GetPermissionMode(), "no claude variant must drop the override")
	o.focusStop(stopTextarea)
	tab(o) // textarea → variants
	tab(o) // variants → (model and mode both skipped) …
	assert.False(t, o.isModeField(), "Tab must skip the disabled mode field")

	// Re-add claude: the field re-enables and the chip selection applies again.
	selectClaude(o)
	assert.Equal(t, "plan", o.GetPermissionMode(), "returning to claude restores the override")
}

// The chip row must fit the worst realistic overlay width — an 80-col
// terminal gives the form 42 inner cells.
func TestModeFieldChipRowWidth(t *testing.T) {
	f := NewModeField()
	f.Focus()
	lines := strings.Split(f.Render(), "\n")
	row := lines[len(lines)-1]
	assert.LessOrEqual(t, lipgloss.Width(row), 41, "chip row must fit 42 inner cells")
}

// The mode section's half of the same pair — see
// TestSessionCreateOverlay_ModelSectionHeightConstant for why the inert case is
// asked elsewhere.
func TestSessionCreateOverlay_ModeSectionHeightConstant(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "", nil)
	o.SetSize(80, 40)

	o.focusStop(stopMode)
	modeFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTitle)
	titleFocused := strings.Count(o.Render(), "\n")
	assert.Equal(t, modeFocused, titleFocused, "overlay height must not change with mode focus")
}

// Every claude-form configuration must fit an 80×24 terminal — including the
// tallest ones (profiles and a multi-account picker stacked on the model and
// mode sections), which exceed what blank-line dropping alone can absorb and
// exercise fitOverlay's divider-dropping stage. The echo-program bounds test
// in app/view_bounds_test.go cannot see any of these.
func TestSessionCreateOverlay_ClaudeFormFitsShortTerminal(t *testing.T) {
	cases := []struct {
		name      string
		profiles  []config.Profile
		accounts  []config.ClaudeAccount
		linkPaths []string
	}{
		{"bare claude form", nil, nil, nil},
		{"with profiles", mixedProfiles, nil, nil},
		{"with profiles and accounts", mixedProfiles, twoAccounts, nil},
		// The tallest form there is: every optional section at once. This is the case
		// that decides how many lines the Dependencies section may cost — a four-line
		// section here exhausts fitOverlay's droppable lines and hard-clips the Create
		// button off the bottom (#481).
		{"with profiles, accounts and link paths", mixedProfiles, twoAccounts, []string{"node_modules"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := NewSessionCreateOverlay(c.profiles, c.accounts, []string{"/repo/a"}, "claude", c.linkPaths)
			o.SetBranchResults([]string{"main", "develop", "feature/x"}, o.BranchFilterVersion())
			o.SetSize(80, 24)

			height := strings.Count(o.Render(), "\n") + 1
			assert.LessOrEqual(t, height, 24, "the claude create form must fit a 80×24 terminal")
			assert.Contains(t, o.Render(), "Create", "the Create button must survive compaction at 80×24")
		})
	}
}

// fitOverlay sheds divider lines (stage two, after blanks) when blank-dropping
// alone cannot fit the budget, and hard-clips as the last resort — real content
// is preserved through the divider stage.
func TestDropLinesToFit_DividerStage(t *testing.T) {
	isDivider := func(l string) bool { return l == "───" }
	lines := []string{"title", "───", "body", "───", "button"}

	got := dropLinesToFit(lines, 3, isDivider)
	assert.Equal(t, []string{"title", "body", "button"}, got)

	// Non-divider lines are never dropped, even over budget.
	got = dropLinesToFit([]string{"a", "b", "c"}, 2, isDivider)
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// On tall terminals the create form grows the picker lists beyond the 3-row
// default (up to maxPickerRows) — with background repo discovery the candidate
// list is much richer, and 3 rows undersells it. Short terminals keep the
// existing shrink-to-fit behavior.
func TestFitRows_GrowsPickersOnTallTerminals(t *testing.T) {
	ov := NewSessionCreateOverlay(nil, nil, []string{"/a"}, "echo", nil)

	// Plenty of room: grow to the cap.
	pickerRows, promptRows := ov.fitRows(60)
	if pickerRows != maxPickerRows {
		t.Fatalf("height 60: pickerRows = %d, want %d", pickerRows, maxPickerRows)
	}
	if promptRows != defaultPromptRows {
		t.Fatalf("height 60: promptRows = %d, want %d (growth must not touch the prompt)", promptRows, defaultPromptRows)
	}

	// Just enough room for one extra row pair: partial growth.
	pickerRows, _ = ov.fitRows(32)
	if pickerRows != 4 {
		t.Fatalf("height 32: pickerRows = %d, want 4", pickerRows)
	}

	// Typical short terminal: the existing shrink behavior is untouched.
	pickerRows, promptRows = ov.fitRows(24)
	if pickerRows != 1 || promptRows != 2 {
		t.Fatalf("height 24: got (%d, %d), want (1, 2)", pickerRows, promptRows)
	}
}

func TestSessionCreateOverlay_IsDirty(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "", nil)
	assert.False(t, o.IsDirty(), "a fresh form is not dirty")

	o.SetTitleValue("draft")
	assert.True(t, o.IsDirty(), "a typed title makes the form dirty")

	o.SetTitleValue("")
	o.SetPrompt("some prompt")
	assert.True(t, o.IsDirty(), "a typed prompt makes the form dirty")

	o.SetPrompt("   ")
	o.SetTitleValue("  ")
	assert.False(t, o.IsDirty(), "whitespace-only is not dirty")
}
