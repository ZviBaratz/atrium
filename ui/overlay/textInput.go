package overlay

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// Overlay styles read the active theme at render time (package-level style vars
// would capture the default theme at import, before config selects one).
func tiStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2)
}

func tiTitleStyle() lipgloss.Style {
	return theme.Current().AccentStyle().Bold(true).MarginBottom(1)
}

func tiButtonStyle() lipgloss.Style { return theme.Current().FgStyle() }

func tiFocusedButtonStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().Palette.Accent).
		Foreground(theme.Current().Palette.Bg)
}

func tiDividerStyle() lipgloss.Style { return theme.Current().DimStyle() }

func tiLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

func tiHintStyle() lipgloss.Style { return theme.Current().DimStyle().Italic(true) }

// defaultPickerRows / defaultPromptRows are the preferred number of list rows the directory
// and branch pickers render and the preferred prompt-textarea height. They are also the
// upper bound: SetSize only ever shrinks below these to fit a short terminal, never grows
// past them. The chosen counts are fixed for a given window size (computed in SetSize, not
// per render) so the overlay's height never changes as focus moves between sections —
// otherwise the vertically centered overlay (see app View / PlaceOverlay) would jump on Tab.
const (
	defaultPickerRows = 3
	defaultPromptRows = 4
	// minPickerRows / minPromptRows are the floors the form collapses to on short terminals.
	minPickerRows = 1
	minPromptRows = 1
	// formChromeLines is every create-form line that is neither a picker row nor a prompt
	// row: the rounded border (2) + vertical padding (2) + the overlay title, the Title
	// field and its divider, each picker's header/blank/divider, the prompt label and its
	// divider, the help line, and the Create button. Used to size the form to the terminal.
	formChromeLines = 18
	// profileSectionLines is the height the profile section adds when present (label + blank
	// + the names row + a divider).
	profileSectionLines = 4
)

// createFormHelp is the single footer line describing how to navigate the create form.
// Enter advances between fields (and inserts a newline in the prompt), so submission is
// surfaced as Ctrl+S — which works from any field — rather than an ambiguous "Enter create".
const createFormHelp = "Tab/⇧Tab move · ↑↓ select · type to filter · ⌃S create"

// renderPickerRows renders a list of pre-formatted labels windowed around the cursor,
// always emitting exactly rows lines (padding with blanks) so the caller's height is
// constant. When labels is empty, placeholder (if set) occupies the first row.
func renderPickerRows(labels []string, cursor, rows int, focused bool, placeholder string, selStyle, dimStyle lipgloss.Style) string {
	if rows < 1 {
		rows = 1
	}
	lines := make([]string, 0, rows)
	if len(labels) == 0 {
		if placeholder != "" {
			lines = append(lines, dimStyle.Render("  "+placeholder))
		}
	} else {
		start := 0
		if cursor >= rows {
			start = cursor - rows + 1
		}
		end := start + rows
		if end > len(labels) {
			end = len(labels)
		}
		for i := start; i < end; i++ {
			switch {
			case i == cursor && focused:
				lines = append(lines, selStyle.Render("> "+labels[i]))
			case i == cursor:
				lines = append(lines, "  "+labels[i])
			default:
				lines = append(lines, dimStyle.Render("  "+labels[i]))
			}
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// focusStop identifies a focusable component in the overlay. The overlay holds an
// ordered slice of the stops that are actually present, so adding or removing a
// component is a matter of editing that slice rather than juggling hardcoded indices.
type focusStop int

const (
	stopTitle focusStop = iota
	stopDirectory
	stopProfile
	stopTextarea
	stopBranch
	stopEnter
)

// TextInputOverlay represents a text input overlay with state management.
type TextInputOverlay struct {
	textarea        textarea.Model
	titleInput      textinput.Model
	Title           string
	FocusIndex      int // index into stops
	Submitted       bool
	Canceled        bool
	OnSubmit        func()
	width           int
	height          int
	directoryPicker *DirectoryPicker
	profilePicker   *ProfilePicker
	branchPicker    *BranchPicker
	stops           []focusStop // ordered focusable stops actually present
	isCreateForm    bool        // true for the new-session form (has a title field)
	submitOnEnter   bool        // true for the quick-send overlay: Enter submits, Alt+Enter is a newline
}

// NewTextInputOverlay creates a new text input overlay with the given title and initial value.
func NewTextInputOverlay(title string, initialValue string) *TextInputOverlay {
	ti := newTextarea(initialValue)
	overlay := &TextInputOverlay{
		textarea: ti,
		Title:    title,
		stops:    []focusStop{stopTextarea, stopEnter},
	}
	overlay.focusStop(stopTextarea)
	return overlay
}

// NewQuickSendOverlay creates the compose-and-send overlay used to fire an ad-hoc message at
// the selected running session without attaching. It is the same single textarea + submit button
// as NewTextInputOverlay, but Enter submits immediately (Alt+Enter inserts a newline) so a short
// reply is one keystroke away — see HandleKeyPress and the submitOnEnter hint in Render.
func NewQuickSendOverlay(title string) *TextInputOverlay {
	o := NewTextInputOverlay(title, "")
	o.submitOnEnter = true
	return o
}

// NewSessionCreateOverlay creates the unified new-session form: a title field, a prompt
// textarea, a project (directory) picker, an optional profile picker (only when more than
// one profile exists), and a branch picker. Focus starts on the title so the user can name
// the session immediately, and every section renders at a constant height so the centered
// overlay does not jump as focus moves. dirCandidates is the ordered list of candidate repo
// paths with the default/contextual target first.
func NewSessionCreateOverlay(profiles []config.Profile, dirCandidates []string) *TextInputOverlay {
	ti := newTextarea("")
	// The prompt is optional and auto-sent to the agent once the session boots, so say so.
	ti.Placeholder = "Optional — sent to the agent once it starts (Tab to skip)"
	bp := NewBranchPicker()
	dp := NewDirectoryPicker(dirCandidates)

	var pp *ProfilePicker
	if len(profiles) > 0 {
		pp = NewProfilePicker(profiles)
	}

	// Title first (where focus starts), then the prompt — the input that distinguishes this
	// flow from the inline `n` flow — followed by project, optional profile, and branch.
	// Branch stays after project because branches are scoped to the chosen project.
	stops := []focusStop{stopTitle, stopTextarea, stopDirectory}
	if pp != nil && pp.HasMultiple() {
		stops = append(stops, stopProfile)
	}
	stops = append(stops, stopBranch, stopEnter)

	overlay := &TextInputOverlay{
		textarea:        ti,
		titleInput:      newTitleInput(),
		Title:           "New session",
		directoryPicker: dp,
		profilePicker:   pp,
		branchPicker:    bp,
		stops:           stops,
		isCreateForm:    true,
	}
	overlay.focusStop(stopTitle)
	return overlay
}

func newTextarea(initialValue string) textarea.Model {
	ti := textarea.New()
	ti.SetValue(initialValue)
	ti.Focus()
	ti.ShowLineNumbers = false
	ti.Prompt = ""
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.CharLimit = 0
	ti.MaxHeight = 0
	return ti
}

// newTitleInput builds the single-line session-title field, capped at 32 characters to
// match the inline-naming limit enforced in the quick `n` flow.
func newTitleInput() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 32
	in.Placeholder = "name this session"
	return in
}

// SetSize is given the full terminal dimensions. The create form keeps every section at a
// constant height so the centered overlay does not jump as focus moves, but it sizes those
// sections to fit the terminal (shrinking the pickers and prompt on short screens). The plain
// prompt overlay keeps its original behavior of a textarea ~40% of the screen tall.
func (t *TextInputOverlay) SetSize(width, height int) {
	t.width = width
	t.height = height
	if t.isCreateForm {
		pickerRows, promptRows := t.fitRows(height)
		t.textarea.SetHeight(promptRows)
		if t.directoryPicker != nil {
			t.directoryPicker.SetVisibleRows(pickerRows)
		}
		if t.branchPicker != nil {
			t.branchPicker.SetVisibleRows(pickerRows)
		}
	} else {
		t.textarea.SetHeight(int(float32(height) * 0.4))
	}
	t.titleInput.Width = width - 6
	if t.directoryPicker != nil {
		t.directoryPicker.SetWidth(width - 6)
	}
	if t.branchPicker != nil {
		t.branchPicker.SetWidth(width - 6)
	}
	if t.profilePicker != nil {
		t.profilePicker.SetWidth(width - 6)
	}
}

// fitRows chooses the picker-row and prompt-row counts that make the create form fit within
// a terminal of the given height. It starts from the preferred defaults and shrinks to fit —
// picker rows first (the windowed list degrades gracefully to a single scrolling row), then
// the prompt — but never below the floors. On terminals too short for even the floors it
// returns the floors and the overlay clips minimally rather than misbehaving.
func (t *TextInputOverlay) fitRows(height int) (pickerRows, promptRows int) {
	pickerRows, promptRows = defaultPickerRows, defaultPromptRows
	chrome := formChromeLines
	if t.profilePicker != nil {
		chrome += profileSectionLines
	}
	const margin = 2 // keep a row above and below so the overlay isn't flush to the edges
	total := func() int { return 2*pickerRows + promptRows + chrome }
	for total() > height-margin {
		switch {
		case pickerRows > minPickerRows:
			pickerRows--
		case promptRows > minPromptRows:
			promptRows--
		default:
			return pickerRows, promptRows
		}
	}
	return pickerRows, promptRows
}

// Init initializes the text input overlay model
func (t *TextInputOverlay) Init() tea.Cmd {
	return textarea.Blink
}

// View renders the model's view
func (t *TextInputOverlay) View() string {
	return t.Render()
}

// currentStop returns the focusStop the FocusIndex currently points at.
func (t *TextInputOverlay) currentStop() focusStop {
	if t.FocusIndex < 0 || t.FocusIndex >= len(t.stops) {
		return stopEnter
	}
	return t.stops[t.FocusIndex]
}

func (t *TextInputOverlay) isTitle() bool           { return t.currentStop() == stopTitle }
func (t *TextInputOverlay) isDirectoryPicker() bool { return t.currentStop() == stopDirectory }
func (t *TextInputOverlay) isProfilePicker() bool   { return t.currentStop() == stopProfile }
func (t *TextInputOverlay) isTextarea() bool        { return t.currentStop() == stopTextarea }
func (t *TextInputOverlay) isBranchPicker() bool    { return t.currentStop() == stopBranch }
func (t *TextInputOverlay) isEnterButton() bool     { return t.currentStop() == stopEnter }

// indexOfStop returns the FocusIndex of a stop kind, or -1 if absent.
func (t *TextInputOverlay) indexOfStop(kind focusStop) int {
	for i, s := range t.stops {
		if s == kind {
			return i
		}
	}
	return -1
}

// focusStop moves focus to the given stop kind (if present) and syncs focus state.
func (t *TextInputOverlay) focusStop(kind focusStop) {
	if i := t.indexOfStop(kind); i >= 0 {
		t.setFocusIndex(i)
	}
}

// setFocusIndex sets the focus index and syncs focus state.
func (t *TextInputOverlay) setFocusIndex(i int) {
	t.FocusIndex = i
	t.updateFocusState()
}

// updateFocusState syncs each component's focus/blur state to the current stop.
func (t *TextInputOverlay) updateFocusState() {
	if t.isTitle() {
		t.titleInput.Focus()
	} else {
		t.titleInput.Blur()
	}
	if t.isTextarea() {
		t.textarea.Focus()
	} else {
		t.textarea.Blur()
	}
	if t.directoryPicker != nil {
		if t.isDirectoryPicker() {
			t.directoryPicker.Focus()
		} else {
			t.directoryPicker.Blur()
		}
	}
	if t.branchPicker != nil {
		if t.isBranchPicker() {
			t.branchPicker.Focus()
		} else {
			t.branchPicker.Blur()
		}
	}
	if t.profilePicker != nil {
		if t.isProfilePicker() {
			t.profilePicker.Focus()
		} else {
			t.profilePicker.Blur()
		}
	}
}

// HandleKeyPress processes a key press and updates the state accordingly.
// Returns (shouldClose, branchFilterChanged).
func (t *TextInputOverlay) HandleKeyPress(msg tea.KeyMsg) (bool, bool) {
	numStops := len(t.stops)
	switch msg.Type {
	case tea.KeyTab:
		t.setFocusIndex((t.FocusIndex + 1) % numStops)
		return false, false
	case tea.KeyShiftTab:
		t.setFocusIndex((t.FocusIndex - 1 + numStops) % numStops)
		return false, false
	case tea.KeyEsc:
		t.Canceled = true
		return true, false
	case tea.KeyCtrlS:
		// Submit from any field. Enter only submits on the focused Create button (so it can
		// stay a newline in the prompt and an "advance" elsewhere), so Ctrl+S is the
		// submit-from-anywhere shortcut; the Create button remains the fallback.
		t.Submitted = true
		if t.OnSubmit != nil {
			t.OnSubmit()
		}
		return true, false
	case tea.KeyEnter:
		if t.isEnterButton() {
			t.Submitted = true
			if t.OnSubmit != nil {
				t.OnSubmit()
			}
			return true, false
		}
		if t.isTextarea() {
			// Quick-send: bare Enter sends, Alt+Enter is the newline. The textarea's newline
			// binding matches the literal "enter", so an Alt-modified Enter would be ignored if
			// forwarded — insert the newline explicitly instead.
			if t.submitOnEnter {
				if msg.Alt {
					t.textarea.InsertRune('\n')
					return false, false
				}
				t.Submitted = true
				if t.OnSubmit != nil {
					t.OnSubmit()
				}
				return true, false
			}
			// In the create-form prompt, Enter inserts a newline.
			t.textarea, _ = t.textarea.Update(msg)
			return false, false
		}
		// Every other field (title, pickers) advances to the next stop. Advancing by one —
		// rather than jumping to the button — keeps Enter consistent regardless of where a
		// field sits in the order.
		t.setFocusIndex(t.FocusIndex + 1)
		return false, false
	default:
		if t.isTitle() {
			t.titleInput, _ = t.titleInput.Update(msg)
			return false, false
		}
		if t.isTextarea() {
			t.textarea, _ = t.textarea.Update(msg)
			return false, false
		}
		if t.isDirectoryPicker() {
			t.directoryPicker.HandleKeyPress(msg)
			return false, false
		}
		if t.isProfilePicker() {
			switch msg.Type {
			case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown:
				t.profilePicker.HandleKeyPress(msg)
			}
			return false, false
		}
		if t.isBranchPicker() {
			_, filterChanged := t.branchPicker.HandleKeyPress(msg)
			return false, filterChanged
		}
		return false, false
	}
}

// GetValue returns the current value of the text input.
func (t *TextInputOverlay) GetValue() string {
	return t.textarea.Value()
}

// GetTitle returns the trimmed session title from the title field (create form only).
func (t *TextInputOverlay) GetTitle() string {
	return strings.TrimSpace(t.titleInput.Value())
}

// IsCreateForm reports whether this overlay is the new-session creation form (as opposed
// to the plain prompt overlay used to send a prompt to a running session).
func (t *TextInputOverlay) IsCreateForm() bool {
	return t.isCreateForm
}

// GetSelectedPath returns the selected target directory from the directory picker.
// Returns empty string if no directory picker is present.
func (t *TextInputOverlay) GetSelectedPath() string {
	if t.directoryPicker == nil {
		return ""
	}
	return t.directoryPicker.GetSelectedPath()
}

// SetTargetValidity marks the currently selected target directory's state so the picker
// can surface an inline indicator while the user chooses: valid means it exists and is a
// directory; direct means it is a directory but not a git repo (a direct session).
// No-op when there is no directory picker.
func (t *TextInputOverlay) SetTargetValidity(valid, direct bool) {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.SetSelectionState(valid, direct)
}

// GetSelectedBranch returns the selected branch name from the branch picker.
// Returns empty string if no branch picker is present or "New branch" is selected.
func (t *TextInputOverlay) GetSelectedBranch() string {
	if t.branchPicker == nil {
		return ""
	}
	return t.branchPicker.GetSelectedBranch()
}

// GetSelectedProgram returns the program string from the selected profile.
// Returns empty string if no profile picker is present.
func (t *TextInputOverlay) GetSelectedProgram() string {
	if t.profilePicker == nil {
		return ""
	}
	return t.profilePicker.GetSelectedProfile().Program
}

// BranchFilterVersion returns the current filter version from the branch picker.
// Returns 0 if no branch picker is present.
func (t *TextInputOverlay) BranchFilterVersion() uint64 {
	if t.branchPicker == nil {
		return 0
	}
	return t.branchPicker.GetFilterVersion()
}

// BranchFilter returns the current filter text from the branch picker.
func (t *TextInputOverlay) BranchFilter() string {
	if t.branchPicker == nil {
		return ""
	}
	return t.branchPicker.GetFilter()
}

// InvalidateBranchSearch bumps the branch filter version and clears stale results,
// returning the new version. Used when the target directory changes so in-flight
// searches for the previous repo are rejected. Returns 0 if no branch picker.
func (t *TextInputOverlay) InvalidateBranchSearch() uint64 {
	if t.branchPicker == nil {
		return 0
	}
	return t.branchPicker.Invalidate()
}

// SetBranchResults updates the branch picker with search results.
// version must match the picker's current filterVersion to be accepted.
func (t *TextInputOverlay) SetBranchResults(branches []string, version uint64) {
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetResults(branches, version)
}

// IsSubmitted returns whether the form was submitted.
func (t *TextInputOverlay) IsSubmitted() bool {
	return t.Submitted
}

// IsCanceled returns whether the form was canceled.
func (t *TextInputOverlay) IsCanceled() bool {
	return t.Canceled
}

// SetOnSubmit sets a callback function for form submission.
func (t *TextInputOverlay) SetOnSubmit(onSubmit func()) {
	t.OnSubmit = onSubmit
}

// Render renders the text input overlay.
func (t *TextInputOverlay) Render() string {
	// Inner content width (accounting for padding and borders)
	innerWidth := t.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Set component widths to fit within the overlay
	t.textarea.SetWidth(innerWidth)
	t.titleInput.Width = innerWidth

	// Build a horizontal divider line
	divider := tiDividerStyle().Render(strings.Repeat("─", innerWidth))

	if t.isCreateForm {
		return t.fitOverlay(t.renderCreateForm(divider), innerWidth)
	}

	// Plain prompt overlay (the `p` flow): no pickers — just a title, the prompt textarea,
	// and the submit button.
	var content string
	content += tiTitleStyle().Render(t.Title) + "\n"
	content += t.textarea.View() + "\n\n"
	content += divider + "\n\n"
	if t.submitOnEnter {
		content += tiHintStyle().Render("enter send · ⌥enter newline · esc cancel") + "\n"
	}
	content += t.renderEnterButton()

	return t.fitOverlay(content, innerWidth)
}

// fitOverlay constrains the assembled inner content to the overlay's terminal share
// before drawing the bordered box. Two invariants matter: the composed View() must
// never be wider or taller than the terminal, or PlaceOverlay spills past the screen
// and bubbletea's line-diffing desyncs (stale fragments, a mis-placed popup).
//
//   - Width: every line is truncated to innerWidth, so a long value (e.g. a deep
//     project path or profile command) can never widen the box past t.width. The
//     dividers, already innerWidth wide, anchor the box to a stable width.
//   - Height: the create form's constant-height sections can total a row or two more
//     than a short terminal (an 80×24 screen with a profile section needs 25 rows).
//     Blank filler lines are dropped — never real content like the Create button —
//     until the box fits t.height, so the form compacts instead of scrolling.
func (t *TextInputOverlay) fitOverlay(content string, innerWidth int) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > innerWidth {
			lines[i] = truncate.StringWithTail(l, uint(innerWidth), "…")
		}
	}
	// The bordered, padded box adds 4 rows (border top/bottom + vertical padding),
	// so the inner content must fit within t.height-4 for the box to fit the screen.
	if budget := t.height - 4; budget > 0 {
		lines = dropBlankLinesToFit(lines, budget)
	}
	return tiStyle().Render(strings.Join(lines, "\n"))
}

// dropBlankLinesToFit removes interior blank lines (and only blank lines — leading,
// trailing, and any line carrying visible content are preserved) until the slice is
// at most budget lines long or no removable blanks remain. It is the graceful
// degradation for terminals too short to hold the form at its natural spacing.
//
// It never drops visible content, so a terminal shorter than the form's irreducible
// height (its content rows plus the few unavoidable blanks) still overflows by the
// residual rows. The supported floor is 24 rows: at 80×24 the create form has enough
// removable blanks to fit, which TestViewFitsTerminalBounds and the unit tests pin.
func dropBlankLinesToFit(lines []string, budget int) []string {
	excess := len(lines) - budget
	if excess <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if excess > 0 && i > 0 && i < len(lines)-1 && lipgloss.Width(l) == 0 {
			excess--
			continue
		}
		out = append(out, l)
	}
	return out
}

// renderCreateForm renders the unified new-session form. Every section is constant-height
// for a given window size (the picker/prompt row counts are fixed in SetSize via fitRows),
// so the vertically centered overlay never jumps as focus moves between fields.
func (t *TextInputOverlay) renderCreateForm(divider string) string {
	var b strings.Builder

	// Each section sits directly above a divider with one blank line after it, keeping the
	// form compact while still visually separating fields.
	section := func(content string) {
		b.WriteString(content + "\n")
		b.WriteString(divider + "\n")
	}

	b.WriteString(tiTitleStyle().Render(t.Title) + "\n")
	section(tiLabelStyle().Render("Title") + "  " + t.titleInput.View())
	section(tiLabelStyle().Render("Prompt") + "\n" + t.textarea.View())
	if t.directoryPicker != nil {
		section(t.directoryPicker.Render())
	}
	if t.profilePicker != nil {
		section(t.profilePicker.Render())
	}
	if t.branchPicker != nil {
		section(t.branchPicker.Render())
	}

	b.WriteString(tiHintStyle().Render(createFormHelp) + "\n")
	b.WriteString(t.renderEnterButton())

	return b.String()
}

// renderEnterButton renders the submit button, highlighted when it holds focus.
func (t *TextInputOverlay) renderEnterButton() string {
	enterButton := " Enter "
	if t.isCreateForm {
		enterButton = " Create " // matches the ⌃S create hint in the footer
	}
	if t.isEnterButton() {
		return tiFocusedButtonStyle().Render(enterButton)
	}
	return tiButtonStyle().Render(enterButton)
}
