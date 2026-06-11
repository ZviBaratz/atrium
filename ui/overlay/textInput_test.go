package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var twoAccounts = []config.ClaudeAccount{
	{Name: "personal", ConfigDir: "~/.claude"},
	{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
}

// The account picker is a true override: until the user drives it, the form reports
// no selection (ok=false) so the caller keeps the freshly-resolved auto-route. Only a
// deliberate keypress flips it to an override that wins.
func TestSessionCreateOverlay_AccountOverrideOnlyWhenTouched(t *testing.T) {
	o := NewSessionCreateOverlay(nil, twoAccounts, []string{"/repo/a"}, "")

	_, ok := o.GetSelectedAccount()
	assert.False(t, ok, "an untouched picker must not override auto-routing")

	// Auto-routed preselection is not a user override.
	o.PreselectAccount("quantivly")
	_, ok = o.GetSelectedAccount()
	assert.False(t, ok, "auto preselect alone must not override")

	// The user drives the picker: now it overrides with the chosen account.
	o.focusStop(stopAccount)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	acct, ok := o.GetSelectedAccount()
	require.True(t, ok, "a user choice overrides auto-routing")
	assert.Equal(t, "quantivly", acct.Name)
}

// A form with no configured accounts never overrides — the feature is dormant.
func TestSessionCreateOverlay_NoAccountsNeverOverrides(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	_, ok := o.GetSelectedAccount()
	assert.False(t, ok)
}

// A single configured account renders no Account section (and adds no chrome): with
// nothing to choose, the picker would be a dead, unfocusable row. The list badge still
// conveys the account.
func TestSessionCreateOverlay_SingleAccountHidesSection(t *testing.T) {
	one := []config.ClaudeAccount{{Name: "solo", ConfigDir: "~/.claude"}}
	o := NewSessionCreateOverlay(nil, one, []string{"/repo/a"}, "")
	o.SetSize(80, 40)
	assert.NotContains(t, o.Render(), "Account", "a lone account must not render the picker section")

	o2 := NewSessionCreateOverlay(nil, twoAccounts, []string{"/repo/a"}, "")
	o2.SetSize(80, 40)
	assert.Contains(t, o2.Render(), "Account", "≥2 accounts render the picker section")
}

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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	before := o.BranchFilterVersion()
	after := o.InvalidateBranchSearch()
	assert.Greater(t, after, before)
}

func TestSessionCreateOverlay_FocusStartsOnDirectoryAndCycles(t *testing.T) {
	// No profiles → stops: [directory, branch, title, textarea, enter]; focus starts on
	// the project picker, and the base branch follows immediately since it is scoped to
	// the chosen project.
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a", "/repo/b"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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

	o := NewSessionCreateOverlay(nil, nil, []string{root}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
	// Focus starts on the project picker, not the submit button.
	assert.True(t, o.isDirectoryPicker())

	shouldClose, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlS})
	assert.True(t, shouldClose, "Ctrl+S should close the form")
	assert.True(t, o.IsSubmitted(), "Ctrl+S should submit from a non-button field")
	assert.False(t, o.IsCanceled())
}

func TestSessionCreateOverlay_GetTitle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "")
	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isDirectoryPicker())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, o.isTitle(), "Enter must skip the disabled branch picker")
}

// The quick-create contract: n focuses the title, typing a name and pressing
// Enter creates the session — no two-hand ⌃S chord on the fast path.
func TestSessionCreateOverlay_EnterOnFilledTitleSubmits(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "")
	tab(o)
	assert.True(t, o.isBranchPicker())

	o.SetTargetValidity(true, true, "")
	assert.True(t, o.isTitle(), "focus must move off the now-disabled branch picker")
}

// ClearTargetValidity (the debounce window while a new path's verdict is pending) must not
// flicker the branch section: the last known disabled/enabled state holds until the fresh
// verdict re-sets it.
func TestSessionCreateOverlay_ClearValidityKeepsBranchDisabledState(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/not/a/repo"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/nonexistent"}, "")
	o.SetTargetValidity(false, false, "")

	tab(o)
	assert.True(t, o.isTitle(), "Tab must skip the branch picker for an invalid target")
	assert.Equal(t, "", o.GetSelectedBranch())
}

// The Title label carries a dim "(required)" marker while the field is empty — the only
// hard-required input — and drops it once a title is typed. Submit-time validation stays
// as the backstop.
func TestSessionCreateOverlay_TitleRequiredMarker(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a", "/repo/b"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "Model", "a claude default program must show the model field")
	assert.GreaterOrEqual(t, o.indexOfStop(stopModel), 0)

	o2 := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "aider")
	o2.SetSize(80, 40)
	assert.NotContains(t, o2.Render(), "Model", "a non-claude form must not show the model field")
	assert.Equal(t, -1, o2.indexOfStop(stopModel))
}

// Tab in the model field completes against the alias list in place ("s" → "sonnet"),
// and only advances focus once there is nothing left to complete — the same
// "complete, then advance" contract as the project field.
func TestSessionCreateOverlay_ModelTabCompletesThenAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)
	require.True(t, o.isModelField())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	tab(o)
	assert.True(t, o.isModelField(), "Tab completes in place rather than advancing")
	assert.Equal(t, "sonnet", o.GetModel())

	tab(o)
	assert.False(t, o.isModelField(), "with nothing to complete, Tab advances focus")
}

// Tab through an untouched model field must keep meaning "default": no completion
// fires on an empty value, focus just advances.
func TestSessionCreateOverlay_ModelEmptyTabAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)

	tab(o)
	assert.False(t, o.isModelField(), "Tab on an empty model field advances immediately")
	assert.Equal(t, "", o.GetModel())
}

// Typed runes are filtered to the safe model-name charset, so the submit-time
// validation backstop can effectively never fire from keyboard input.
func TestSessionCreateOverlay_ModelCharsetFiltered(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)

	for _, r := range "op;u s$" { // ';', ' ', '$' must be dropped
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	assert.Equal(t, "opus", o.GetModel())
}

// An explicit "default" (the default chip's label, typed out in custom mode)
// contributes no override, same as leaving the field untouched.
func TestSessionCreateOverlay_ModelDefaultMeansNoOverride(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("default")})
	assert.Equal(t, "", o.GetModel())
}

// Arrowing across the chip row selects aliases without any typing — the
// typo-proof path. The first chip is default (no override).
func TestSessionCreateOverlay_ModelChipCycle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)
	assert.Equal(t, "", o.GetModel(), "the default chip contributes no override")

	for i := 0; i < 3; i++ { // default → fable → haiku → opus
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, "opus", o.GetModel())

	for i := 0; i < 3; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Equal(t, "", o.GetModel(), "cycling back to default drops the override")
}

// Typing enters custom mode; Left with the text cursor at position 0 returns to
// the chip row with the prior chip selection intact.
func TestSessionCreateOverlay_ModelCustomBackToChips(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // fable chip

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.Equal(t, "x", o.GetModel(), "typing switches to custom mode seeded with the rune")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft}) // cursor 1 → 0
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft}) // at 0 → back to chips
	assert.Equal(t, "fable", o.GetModel(), "returning to chips restores the chip selection")
}

// The rune filter pre-checks runes as if typed at the end of the value, but the
// text cursor can sit anywhere (Home/Ctrl+A): a rune that passes the append
// check can still realize an invalid value once inserted mid-string (a leading
// '.' here). The field's invariant is that it never holds an invalid non-empty
// value — such an insertion must be reverted, keeping the submit-time backstop
// unreachable from keyboard input.
func TestSessionCreateOverlay_ModelMidValueInsertionStaysValid(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopModel)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("opus")})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyHome}) // text cursor to position 0
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
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
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "")
	o.SetSize(80, 40)
	require.GreaterOrEqual(t, o.indexOfStop(stopModel), 0, "a claude profile makes the field present")

	// Claude (first profile) selected: the field takes focus and input.
	o.focusStop(stopModel)
	require.True(t, o.isModelField())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("opus")})
	assert.Equal(t, "opus", o.GetModel())

	// Switch to the aider profile: the field goes inert and contributes nothing.
	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "", o.GetModel(), "a non-claude profile must drop the override")
	o.focusStop(stopTextarea)
	tab(o) // textarea → profile
	tab(o) // profile → (model skipped) …
	assert.False(t, o.isModelField(), "Tab must skip the disabled model field")

	// Back to claude: the field re-enables and the typed value applies again.
	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, "opus", o.GetModel(), "returning to claude restores the override")
}

// The model section must hold the form's constant-height invariant: same line count
// whether or not it holds focus, and whether it is enabled or inert.
func TestSessionCreateOverlay_ModelSectionHeightConstant(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "")
	o.SetSize(80, 40)

	o.focusStop(stopModel)
	modelFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTitle)
	titleFocused := strings.Count(o.Render(), "\n")
	assert.Equal(t, modelFocused, titleFocused, "overlay height must not change with model focus")

	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // aider → model field inert
	disabled := strings.Count(o.Render(), "\n")
	assert.Equal(t, titleFocused, disabled, "overlay height must not change when the model field is inert")
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
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "Permissions", "a claude default program must show the mode field")
	assert.GreaterOrEqual(t, o.indexOfStop(stopMode), 0)

	o2 := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "aider")
	o2.SetSize(80, 40)
	assert.NotContains(t, o2.Render(), "Permissions", "a non-claude form must not show the mode field")
	assert.Equal(t, -1, o2.indexOfStop(stopMode))
}

// Arrowing across the chip row selects modes; the first chip (default)
// contributes no flag, and the cursor clamps at both ends.
func TestSessionCreateOverlay_ModeChipCycle(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopMode)
	require.True(t, o.isModeField())
	assert.Equal(t, "", o.GetPermissionMode(), "the default chip contributes no flag")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "plan", o.GetPermissionMode())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "acceptEdits", o.GetPermissionMode())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "auto", o.GetPermissionMode())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // clamps at the end
	assert.Equal(t, "auto", o.GetPermissionMode())

	for i := 0; i < 4; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Equal(t, "", o.GetPermissionMode(), "cycling back to default drops the override")
}

// The chip row displays the kebab-case label (accept-edits) while the value it
// contributes stays the camelCase CLI enum (acceptEdits, asserted by value in
// TestSessionCreateOverlay_ModeChipCycle). This pins the user-visible half of
// that decoupling — the whole point of the labels slice — so a regression that
// dropped it back to options would fail here, not just silently re-render the
// camelCase token.
func TestSessionCreateOverlay_ModeChipDisplaysKebabLabel(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.SetSize(80, 40)

	got := xansi.Strip(o.Render())
	assert.Contains(t, got, "accept-edits", "the chip row must show the kebab-case label")
	assert.NotContains(t, got, "acceptEdits", "the camelCase CLI value must never reach the display")
}

// Tab on the mode field always advances — chips have nothing to complete.
func TestSessionCreateOverlay_ModeTabAdvances(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
	o.focusStop(stopMode)

	tab(o)
	assert.False(t, o.isModeField(), "Tab on the mode field advances immediately")
}

// With mixed profiles the field tracks the selected profile's agent: inert
// (skipped, no override) while a non-claude profile is selected, re-enabled
// when the selection returns to claude — alongside the model field.
func TestSessionCreateOverlay_ModeDisabledForNonClaudeProfile(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "")
	o.SetSize(80, 40)
	require.GreaterOrEqual(t, o.indexOfStop(stopMode), 0, "a claude profile makes the field present")

	o.focusStop(stopMode)
	require.True(t, o.isModeField())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // default → plan
	assert.Equal(t, "plan", o.GetPermissionMode())

	// Switch to the aider profile: the field goes inert and contributes nothing.
	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "", o.GetPermissionMode(), "a non-claude profile must drop the override")
	o.focusStop(stopTextarea)
	tab(o) // textarea → profile
	tab(o) // profile → (model and mode both skipped) …
	assert.False(t, o.isModeField(), "Tab must skip the disabled mode field")

	// Back to claude: the field re-enables and the chip selection applies again.
	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
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

// The mode section must hold the form's constant-height invariant: same line
// count whether or not it holds focus, and whether it is enabled or inert.
func TestSessionCreateOverlay_ModeSectionHeightConstant(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, nil, []string{"/repo/a"}, "")
	o.SetSize(80, 40)

	o.focusStop(stopMode)
	modeFocused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTitle)
	titleFocused := strings.Count(o.Render(), "\n")
	assert.Equal(t, modeFocused, titleFocused, "overlay height must not change with mode focus")

	o.focusStop(stopProfile)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // aider → mode field inert
	disabled := strings.Count(o.Render(), "\n")
	assert.Equal(t, titleFocused, disabled, "overlay height must not change when the mode field is inert")
}

// Every claude-form configuration must fit an 80×24 terminal — including the
// tallest ones (profiles and a multi-account picker stacked on the model and
// mode sections), which exceed what blank-line dropping alone can absorb and
// exercise fitOverlay's divider-dropping stage. The echo-program bounds test
// in app/view_bounds_test.go cannot see any of these.
func TestSessionCreateOverlay_ClaudeFormFitsShortTerminal(t *testing.T) {
	cases := []struct {
		name     string
		profiles []config.Profile
		accounts []config.ClaudeAccount
	}{
		{"bare claude form", nil, nil},
		{"with profiles", mixedProfiles, nil},
		{"with profiles and accounts", mixedProfiles, twoAccounts},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := NewSessionCreateOverlay(c.profiles, c.accounts, []string{"/repo/a"}, "claude")
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
