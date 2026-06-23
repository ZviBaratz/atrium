package overlay

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
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
	return theme.Current().OverlayTitleStyle().MarginBottom(1)
}

func tiButtonStyle() lipgloss.Style { return theme.Current().FgStyle() }

func tiFocusedButtonStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().Palette.Accent).
		Foreground(theme.Current().Palette.Bg)
}

func tiDividerStyle() lipgloss.Style { return theme.Current().DimStyle() }

func tiLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

func tiHintStyle() lipgloss.Style { return theme.Current().OverlayHintStyle() }

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
	// maxPickerRows caps how far the picker lists grow on tall terminals. With
	// background repo discovery the candidate list can hold hundreds of repos,
	// so extra vertical room goes to the lists first (each extra row costs 2
	// lines — the directory and branch pickers share the count).
	maxPickerRows = 6
	// formChromeLines is every create-form line that is neither a picker row nor a prompt
	// row: the rounded border (2) + vertical padding (2) + the overlay title, the Title
	// field and its divider, each picker's header/blank/divider, the prompt label and its
	// divider, the help line, and the Create button. Used to size the form to the terminal.
	formChromeLines = 18
	// accountSectionLines is the height the account section adds when present, mirroring
	// profileSectionLines (label + blank + options row + spacing).
	accountSectionLines = 4
	// profileSectionLines is the height the profile section adds when present (label + blank
	// + the names row + a divider).
	profileSectionLines = 4
	// modelSectionLines is the height the model section adds when present, mirroring
	// profileSectionLines (label + blank + input row + a divider).
	modelSectionLines = 4
	// modeSectionLines is the height the permission-mode section adds when present,
	// mirroring modelSectionLines (label + blank + chips row + a divider).
	modeSectionLines = 4
)

// createFormHelp is the footer shown for every create-form field except the prompt.
// Enter advances between fields (and submits from a filled title — the one-handed quick
// create), so submission is surfaced as Ctrl+S, which works from any field, rather than an
// ambiguous "Enter create".
const createFormHelp = "Tab complete/move · ↑↓ select · ↵ create from name · ⌃S create"

// promptFocusHelp is the footer shown while the prompt textarea holds focus, where Enter
// advances like Tab and the newline keys differ. Shift+Enter needs a Claude-Code-style
// terminal setup; Ctrl+J always works.
const promptFocusHelp = "⇧↵ / ⌃J newline · ↵ next field · ⌃S create"

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
	stopModel
	stopMode
	stopAccount
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
	modelField      *ModelField
	modeField       *ModeField
	accountPicker   *AccountPicker
	branchPicker    *BranchPicker
	stops           []focusStop // ordered focusable stops actually present
	isCreateForm    bool        // true for the new-session form (has a title field)
	smartDispatch   bool        // true for the single-line smart-dispatch input overlay
	submitOnEnter   bool        // true for the quick-send overlay: Enter submits, Alt+Enter is a newline
	// projectHint is a transient inline note rendered beside the project picker on the
	// create form (e.g. "detecting…" while smart dispatch routes asynchronously). Empty = none.
	projectHint    string
	defaultProgram string // the program used when no profile is selected (create form only)
	// titleError is the inline validation message rendered (in the danger color) on
	// the title label — e.g. a duplicate name in the target's repo group. The overlay
	// is a dumb view: the app layer computes the verdict (live on keystrokes/path
	// changes, and again at submit) and pushes it in via SetTitleError. Empty = none.
	titleError string
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

// NewSmartDispatchOverlay creates the single-line input that opens the smart-dispatch
// flow: the user types one free-form description, Enter submits it (like quick-send),
// and the app routes it to a project and pre-fills the new-session form. The
// smartDispatch flag lets the submit dispatcher tell it apart from quick-send.
func NewSmartDispatchOverlay(title string) *TextInputOverlay {
	o := NewTextInputOverlay(title, "")
	o.submitOnEnter = true
	o.smartDispatch = true
	return o
}

// IsSmartDispatch reports whether this overlay is the smart-dispatch input.
func (t *TextInputOverlay) IsSmartDispatch() bool {
	return t.smartDispatch
}

// SetPrompt sets the prompt textarea's contents (used to pre-fill the create form).
func (t *TextInputOverlay) SetPrompt(s string) {
	t.textarea.SetValue(s)
}

// SetTitleValue sets the title field's text (create form only). It is distinct from
// the Title struct field, which is the overlay's header caption.
func (t *TextInputOverlay) SetTitleValue(s string) {
	t.titleInput.SetValue(s)
}

// SelectPath pre-selects path in the project picker, returning false when path is not
// a candidate. No-op (false) without a directory picker.
func (t *TextInputOverlay) SelectPath(path string) bool {
	if t.directoryPicker == nil {
		return false
	}
	return t.directoryPicker.SelectPath(path)
}

// SetProjectHint sets (or, with "", clears) the transient note rendered beside the
// project picker — e.g. "detecting…" while smart dispatch routes in the background.
func (t *TextInputOverlay) SetProjectHint(s string) {
	t.projectHint = s
}

// NewSessionCreateOverlay creates the unified new-session form: a title field, a prompt
// textarea, a project (directory) picker, an optional profile picker (only when more than
// one profile exists), an optional Claude model override (only when a selectable program
// resolves to claude), and a branch picker. Focus starts on the project picker (the `N`
// flow); the quick flow (`n`) moves it to the title via FocusTitle. Every section renders
// at a constant height so the centered overlay does not jump as focus moves. dirCandidates
// is the ordered list of candidate repo paths with the default/contextual target first.
// defaultProgram is the program used when no profile is selected; with profiles present
// the selected profile's program always wins (see createSessionFromForm), so the model
// field keys its visibility and enabled state off the profiles instead.
func NewSessionCreateOverlay(profiles []config.Profile, accounts []config.ClaudeAccount, dirCandidates []string, defaultProgram string) *TextInputOverlay {
	ti := newTextarea("")
	// The prompt is optional and auto-sent to the agent once the session boots, so say so.
	ti.Placeholder = "Optional — sent to the agent once it starts (Enter or Tab to skip)"
	bp := NewBranchPicker()
	dp := NewDirectoryPicker(dirCandidates)

	var pp *ProfilePicker
	if len(profiles) > 0 {
		pp = NewProfilePicker(profiles)
	}

	var ap *AccountPicker
	if len(accounts) > 0 {
		ap = NewAccountPicker(accounts)
	}

	// The model and permission-mode fields exist only when some selectable program
	// resolves to claude (the candidates are the profiles when any exist — a profile's
	// program always overrides the default — else the default program). Their *enabled*
	// state then tracks the effective program: present-but-inert while a non-claude
	// profile is selected, so a typed model is visibly n/a rather than silently dropped.
	candidates := []string{defaultProgram}
	if len(profiles) > 0 {
		candidates = candidates[:0]
		for _, p := range profiles {
			candidates = append(candidates, p.Program)
		}
	}
	var mf *ModelField
	var pmf *ModeField
	for _, c := range candidates {
		if agent.Resolve(c).Key == agent.KeyClaude {
			mf = NewModelField()
			pmf = NewModeField()
			break
		}
	}

	// Project first (where focus starts), immediately followed by the base branch — the two
	// form one repo-context unit, since branches are scoped to the chosen project. Then the
	// title and the prompt — the input that distinguishes this flow from the inline `n` flow
	// (which jumps straight to the title via FocusTitle) — then the optional profile with its
	// dependent model override, and finally the optional Claude-account override.
	stops := []focusStop{stopDirectory, stopBranch, stopTitle, stopTextarea}
	if pp != nil && pp.HasMultiple() {
		stops = append(stops, stopProfile)
	}
	if mf != nil {
		stops = append(stops, stopModel)
	}
	if pmf != nil {
		stops = append(stops, stopMode)
	}
	if ap != nil && ap.HasMultiple() {
		stops = append(stops, stopAccount)
	}
	stops = append(stops, stopEnter)

	overlay := &TextInputOverlay{
		textarea:        ti,
		titleInput:      newTitleInput(),
		Title:           "New session",
		directoryPicker: dp,
		profilePicker:   pp,
		modelField:      mf,
		modeField:       pmf,
		accountPicker:   ap,
		branchPicker:    bp,
		stops:           stops,
		isCreateForm:    true,
		defaultProgram:  defaultProgram,
	}
	overlay.syncClaudeFieldsEnabled()
	overlay.focusStop(stopDirectory)
	return overlay
}

// syncClaudeFieldsEnabled re-derives the model and permission-mode fields' enabled
// state from the effective program (the selected profile's program when a picker
// exists, else the configured default). Called at construction and after every
// profile-picker keypress.
func (t *TextInputOverlay) syncClaudeFieldsEnabled() {
	// The two fields are created together or not at all (see NewSessionCreateOverlay),
	// so one presence check covers both.
	if t.modelField == nil {
		return
	}
	program := t.defaultProgram
	if t.profilePicker != nil {
		program = t.profilePicker.GetSelectedProfile().Program
	}
	disabled := agent.Resolve(program).Key != agent.KeyClaude
	t.modelField.SetDisabled(disabled)
	t.modeField.SetDisabled(disabled)
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
	// Match the single-line title field, which already binds ctrl+arrow for word
	// motion; the textarea default binds only alt+arrow. Make ctrl+j the textarea's
	// newline: the overlay intercepts Enter for field navigation, so the literal
	// "enter" binding never fires here, and Alt+Enter is handled explicitly in
	// HandleKeyPress. ctrl+j is the one newline key that works in every terminal.
	ti.KeyMap.WordForward.SetKeys("alt+right", "ctrl+right", "alt+f")
	ti.KeyMap.WordBackward.SetKeys("alt+left", "ctrl+left", "alt+b")
	ti.KeyMap.InsertNewline.SetKeys("ctrl+j")
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
	if t.modelField != nil {
		t.modelField.SetWidth(width - 6)
	}
	if t.accountPicker != nil {
		t.accountPicker.SetWidth(width - 6)
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
	if t.modelField != nil {
		chrome += modelSectionLines
	}
	if t.modeField != nil {
		chrome += modeSectionLines
	}
	if t.hasAccountSection() {
		chrome += accountSectionLines
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
	// Spare room on tall terminals goes to the picker lists (each increment
	// costs 2 lines: the directory and branch pickers share the count) — with
	// repo discovery the candidate list is worth the rows. The prompt keeps its
	// preferred height: a taller textarea doesn't show more useful information.
	for pickerRows < maxPickerRows && total()+2 <= height-margin {
		pickerRows++
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

// FocusTitle moves focus to the title field. The quick-create flow (`n`) calls
// it right after building the form so typing a name is immediate; the full flow
// (`N`) keeps the default project-picker focus.
func (t *TextInputOverlay) FocusTitle() { t.focusStop(stopTitle) }

// FocusMode moves focus to the Permissions (mode) chip when it can take focus,
// falling back to the Create button otherwise (the mode field is absent for a
// non-claude program and disabled while a non-claude profile is selected). Smart
// dispatch uses this on a confident match so the one decision it defers — the
// permission mode — is the active field, a ←/→ away from plan and ⌃S from create.
func (t *TextInputOverlay) FocusMode() {
	if i := t.indexOfStop(stopMode); i >= 0 && t.stopEnabled(stopMode) {
		t.setFocusIndex(i)
		return
	}
	t.focusStop(stopEnter)
}

// ModeFocused reports whether the Permissions (mode) chip currently has focus.
func (t *TextInputOverlay) ModeFocused() bool { return t.isModeField() }

// TitleFocused reports whether the title field currently has focus.
func (t *TextInputOverlay) TitleFocused() bool { return t.isTitle() }

func (t *TextInputOverlay) isTitle() bool           { return t.currentStop() == stopTitle }
func (t *TextInputOverlay) isDirectoryPicker() bool { return t.currentStop() == stopDirectory }
func (t *TextInputOverlay) isProfilePicker() bool   { return t.currentStop() == stopProfile }
func (t *TextInputOverlay) isModelField() bool      { return t.currentStop() == stopModel }
func (t *TextInputOverlay) isModeField() bool       { return t.currentStop() == stopMode }
func (t *TextInputOverlay) isAccountPicker() bool   { return t.currentStop() == stopAccount }
func (t *TextInputOverlay) isTextarea() bool        { return t.currentStop() == stopTextarea }
func (t *TextInputOverlay) isBranchPicker() bool    { return t.currentStop() == stopBranch }
func (t *TextInputOverlay) isEnterButton() bool     { return t.currentStop() == stopEnter }

// hasAccountSection reports whether the form shows the Account picker. It requires
// ≥2 accounts: a lone account offers no choice, so rendering it would be a dead,
// unfocusable row (and stopAccount is likewise only added when HasMultiple).
func (t *TextInputOverlay) hasAccountSection() bool {
	return t.accountPicker != nil && t.accountPicker.HasMultiple()
}

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

// stopEnabled reports whether a stop can take focus: the branch picker is disabled when
// the target is not a git repo, the model field when the effective program is not claude;
// every other stop is always enabled.
func (t *TextInputOverlay) stopEnabled(kind focusStop) bool {
	if kind == stopBranch && t.branchPicker != nil && t.branchPicker.Disabled() {
		return false
	}
	if kind == stopMode && t.modeField != nil && t.modeField.Disabled() {
		return false
	}
	if kind == stopModel && t.modelField != nil && t.modelField.Disabled() {
		return false
	}
	return true
}

// nextEnabledIndex returns the first enabled stop index reached from `from` by repeatedly
// stepping `delta` (+1 forward, -1 backward), wrapping around the stop list. The loop
// visits each stop at most once, so it terminates even with several stops disabled;
// the Create button is never disabled, so an enabled stop always exists.
func (t *TextInputOverlay) nextEnabledIndex(from, delta int) int {
	n := len(t.stops)
	i := from
	for range t.stops {
		i = (i + delta + n) % n
		if t.stopEnabled(t.stops[i]) {
			return i
		}
	}
	return from
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
	if t.modelField != nil {
		if t.isModelField() {
			t.modelField.Focus()
		} else {
			t.modelField.Blur()
		}
	}
	if t.modeField != nil {
		if t.isModeField() {
			t.modeField.Focus()
		} else {
			t.modeField.Blur()
		}
	}
	if t.accountPicker != nil {
		if t.isAccountPicker() {
			t.accountPicker.Focus()
		} else {
			t.accountPicker.Blur()
		}
	}
}

// HandleKeyPress processes a key press and updates the state accordingly.
// Returns (shouldClose, branchFilterChanged).
func (t *TextInputOverlay) HandleKeyPress(msg tea.KeyMsg) (bool, bool) {
	switch msg.Type {
	case tea.KeyTab:
		// In the project field, Tab first tries shell-style path completion; only when
		// there is nothing left to complete does it advance to the next field. The model
		// field gets the same "complete, then advance" treatment against its alias list.
		if t.isDirectoryPicker() && t.directoryPicker.CompletePrefix() {
			return false, false
		}
		if t.isModelField() && t.modelField.CompletePrefix() {
			return false, false
		}
		t.setFocusIndex(t.nextEnabledIndex(t.FocusIndex, 1))
		return false, false
	case tea.KeyShiftTab:
		t.setFocusIndex(t.nextEnabledIndex(t.FocusIndex, -1))
		return false, false
	case tea.KeyEsc:
		t.Canceled = true
		return true, false
	case tea.KeyCtrlS:
		// Submit from any field. Enter submits only on the Create button and a filled
		// title (it advances to the next field everywhere else, including the prompt), so
		// Ctrl+S is the submit-from-anywhere shortcut; the Create button remains the
		// fallback.
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
		if t.isTitle() && strings.TrimSpace(t.titleInput.Value()) != "" {
			// The quick-create contract: n focuses the title, so "n → name → ↵"
			// creates the session one-handed. Tab from the title reaches the prompt
			// for the create-with-prompt journey; an *empty* title falls through to
			// the advance below (submitting would only bounce off the title-required
			// validation).
			t.Submitted = true
			if t.OnSubmit != nil {
				t.OnSubmit()
			}
			return true, false
		}
		if t.isTextarea() {
			// Alt+Enter inserts a newline — and that is exactly what a Claude-Code-style
			// terminal's Shift+Enter sends, so "shift+enter for a newline" works once the
			// terminal is set up. The textarea's own newline binding matches the literal
			// "enter", which is intercepted here, so insert the newline explicitly.
			if msg.Alt {
				t.textarea.InsertRune('\n')
				return false, false
			}
			if t.submitOnEnter {
				// Quick-send reply box: a bare Enter sends.
				t.Submitted = true
				if t.OnSubmit != nil {
					t.OnSubmit()
				}
				return true, false
			}
			// Create-form prompt: a bare Enter advances to the next field, like Tab
			// (Shift+Enter / Ctrl+J make a newline). Fall through to the shared advance.
		}
		// Every other field (title, pickers) — and the prompt on a bare Enter — advances to
		// the next enabled stop. Advancing by one — rather than jumping to the button — keeps
		// Enter consistent regardless of where a field sits in the order.
		t.setFocusIndex(t.nextEnabledIndex(t.FocusIndex, 1))
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
				// The model and permission-mode overrides only apply to claude; keep
				// their enabled state in step with the newly selected profile's agent.
				t.syncClaudeFieldsEnabled()
			}
			return false, false
		}
		if t.isModelField() {
			t.modelField.HandleKeyPress(msg)
			return false, false
		}
		if t.isModeField() {
			// No pre-filter: the field itself acts only on arrow keys (unlike the
			// profile picker's filter above, which is load-bearing for the sync call).
			t.modeField.HandleKeyPress(msg)
			return false, false
		}
		if t.isAccountPicker() {
			switch msg.Type {
			case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown:
				t.accountPicker.HandleKeyPress(msg)
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

// SetTitleError sets (or, with "", clears) the inline validation message shown
// on the title label. The error never disables submit — the app layer blocks a
// conflicting submit itself and re-focuses the title.
func (t *TextInputOverlay) SetTitleError(msg string) {
	t.titleError = msg
}

// TitleError returns the current inline title validation message ("" = none).
func (t *TextInputOverlay) TitleError() string {
	return t.titleError
}

// GetSelectedPath returns the selected target directory from the directory picker.
// Returns empty string if no directory picker is present.
func (t *TextInputOverlay) GetSelectedPath() string {
	if t.directoryPicker == nil {
		return ""
	}
	return t.directoryPicker.GetSelectedPath()
}

// UpdateDirCandidates refreshes the project picker's candidate list, preserving
// the user's typed filter and selection — used when a background repo scan
// completes while the form is open. No-op without a directory picker.
func (t *TextInputOverlay) UpdateDirCandidates(paths []string) {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.UpdateCandidates(paths)
}

// SetTargetValidity marks the currently selected target directory's state so the picker
// can surface an inline indicator while the user chooses: valid means it exists and is a
// directory; direct means it is a directory but not a git repo (a direct session).
// It also enables/disables the branch picker — a non-git (or invalid) target has no
// branches to base on, so the section goes inert and is skipped by navigation. If the
// verdict lands while the branch picker holds focus, focus moves to the next enabled
// stop rather than stranding the user on an inert field. headBranch (the resolved name
// of the branch HEAD points at, "" when unknown) labels the picker's default base option.
// No-op when there is no directory picker.
func (t *TextInputOverlay) SetTargetValidity(valid, direct bool, headBranch string) {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.SetSelectionState(valid, direct)
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetHeadLabel(headBranch)
	t.branchPicker.SetDisabled(!valid || direct)
	if t.isBranchPicker() && !t.stopEnabled(stopBranch) {
		t.setFocusIndex(t.nextEnabledIndex(t.FocusIndex, 1))
	}
}

// ClearTargetValidity resets the target-directory state indicator to "unknown", so no
// hint is shown until a fresh check resolves. The branch picker's disabled state is
// deliberately left untouched — flipping it during the debounce window would flicker the
// section on every path keystroke; the fresh verdict re-sets it via SetTargetValidity.
// No-op when there is no directory picker.
func (t *TextInputOverlay) ClearTargetValidity() {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.ClearSelectionState()
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

// GetModel returns the Claude model override typed into the model field, or ""
// when no flag should be composed: the form has no model field, the field is
// inert (non-claude profile selected), or it was left empty / "default".
func (t *TextInputOverlay) GetModel() string {
	if t.modelField == nil {
		return ""
	}
	return t.modelField.Value()
}

// GetPermissionMode returns the selected Claude permission-mode override, or
// "" when no flag should be composed: no mode field, the field is inert
// (non-claude profile selected), or it sits on the default chip.
func (t *TextInputOverlay) GetPermissionMode() string {
	if t.modeField == nil {
		return ""
	}
	return t.modeField.Value()
}

// GetSelectedAccount returns the chosen account and true only when the user has
// deliberately driven the picker, i.e. an override. Otherwise it returns
// (zero, false) so the caller keeps the freshly-resolved auto-route — whether the
// form has no picker, or has one the user never touched (its selection is just the
// auto-routed preselection, which the caller already computes itself).
func (t *TextInputOverlay) GetSelectedAccount() (config.ClaudeAccount, bool) {
	if t.accountPicker == nil || !t.accountPicker.Touched() {
		return config.ClaudeAccount{}, false
	}
	return t.accountPicker.GetSelectedAccount(), true
}

// PreselectAccount points the picker at the auto-routed account name. It is a no-op
// once the user has taken manual control (see AccountPicker.SelectByName), so the
// form can re-preselect as the target project changes without clobbering a choice.
func (t *TextInputOverlay) PreselectAccount(name string) {
	if t.accountPicker != nil {
		t.accountPicker.SelectByName(name)
	}
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

// SetBranchSearchError marks the branch search for the given version as failed, so the
// picker stops showing "searching…" and surfaces an error hint instead.
func (t *TextInputOverlay) SetBranchSearchError(version uint64) {
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetError(version)
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

	// Set component widths to fit within the overlay. The title input's width is
	// owned by renderCreateForm, which carves the verdict suffix out of it.
	t.textarea.SetWidth(innerWidth)

	// Build a horizontal divider line
	divider := tiDividerStyle().Render(strings.Repeat("─", innerWidth))

	if t.isCreateForm {
		return t.fitOverlay(t.renderCreateForm(divider), innerWidth, divider)
	}

	// Plain prompt overlay (the `p` flow): no pickers — just a title, the prompt textarea,
	// and the submit button.
	var content string
	content += tiTitleStyle().Render(t.Title) + "\n"
	content += t.textarea.View() + "\n\n"
	content += divider + "\n\n"
	if t.submitOnEnter {
		// Mirror the create form's newline vocabulary: Shift+Enter (alt+enter on the
		// wire, needs a configured terminal) or the universal Ctrl+J — see newTextarea.
		content += tiHintStyle().Render("↵ send · ⇧↵ / ⌃J newline · esc cancel") + "\n"
	}
	content += t.renderEnterButton()

	return t.fitOverlay(content, innerWidth, divider)
}

// fitOverlay constrains the assembled inner content to the overlay's terminal share
// before drawing the bordered box. Two invariants matter: the composed View() must
// never be wider or taller than the terminal, or PlaceOverlay spills past the screen
// and bubbletea's line-diffing desyncs (stale fragments, a mis-placed popup).
//
//   - Width: every line is truncated to innerWidth, so a long value (e.g. a deep
//     project path or profile command) can never widen the box past t.width. The
//     dividers, already innerWidth wide, anchor the box to a stable width.
//   - Height: the create form's constant-height sections can total several rows more
//     than a short terminal (an 80×24 screen with profiles, the claude fields, and an
//     account picker needs over 30). Spacing is shed in stages — blank filler lines
//     first, then divider lines (pure visual separation) — so the form compacts
//     instead of scrolling. If even that is not enough (a terminal below the 24-row
//     floor), the tail is clipped outright: a partially visible form is degraded but
//     stable, while an oversize View() is not.
func (t *TextInputOverlay) fitOverlay(content string, innerWidth int, divider string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > innerWidth {
			lines[i] = truncate.StringWithTail(l, uint(innerWidth), "…")
		}
	}
	// The bordered, padded box adds 4 rows (border top/bottom + vertical padding),
	// so the inner content must fit within t.height-4 for the box to fit the screen.
	if budget := t.height - 4; budget > 0 {
		lines = dropLinesToFit(lines, budget, func(l string) bool { return lipgloss.Width(l) == 0 })
		lines = dropLinesToFit(lines, budget, func(l string) bool { return l == divider })
		if len(lines) > budget {
			lines = lines[:budget]
		}
	}
	return tiStyle().Render(strings.Join(lines, "\n"))
}

// dropBlankLinesToFit removes interior blank lines (and only blank lines) until the
// slice is at most budget lines long or no removable blanks remain. It is the first
// stage of fitOverlay's graceful degradation for short terminals.
func dropBlankLinesToFit(lines []string, budget int) []string {
	return dropLinesToFit(lines, budget, func(l string) bool { return lipgloss.Width(l) == 0 })
}

// dropLinesToFit removes interior lines matching droppable — leading, trailing, and
// non-matching lines are always preserved — until the slice is at most budget lines
// long or no droppable lines remain. fitOverlay layers it: blanks go first (natural
// spacing), then dividers (visual separation), so real content is only ever lost to
// the final clip on terminals below the supported 24-row floor.
func dropLinesToFit(lines []string, budget int, droppable func(string) bool) []string {
	excess := len(lines) - budget
	if excess <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if excess > 0 && i > 0 && i < len(lines)-1 && droppable(l) {
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
	if t.directoryPicker != nil {
		project := t.directoryPicker.Render()
		if t.projectHint != "" {
			project += "  " + tiHintStyle().Render(t.projectHint)
		}
		section(project)
	}
	if t.branchPicker != nil {
		section(t.branchPicker.Render())
	}
	// The title is the form's only hard-required input; carry a dim marker while it is
	// empty so the requirement is visible before the submit-time error backstop. A
	// validation error (duplicate name in the target group) wins over the hint. Both
	// trail the input rather than sitting between the label and the field: a
	// variable-width prefix would shift the text under the user's caret on exactly
	// the keystrokes that recompute the verdict. The input pads itself to its Width,
	// so the suffix's columns are carved out of the input up front — otherwise the
	// message would land past fitOverlay's truncation edge, invisible. (innerWidth
	// is recovered from the divider, which is rendered exactly that wide.)
	titleLabel := tiLabelStyle().Render("Title")
	var suffixPlain, suffix string
	switch {
	case t.titleError != "":
		suffixPlain = " (" + t.titleError + ")"
		suffix = theme.Current().DangerStyle().Render(suffixPlain)
	case t.GetTitle() == "":
		suffixPlain = " (required)"
		suffix = tiHintStyle().Render(suffixPlain)
	}
	// -1: the input renders one column past Width for the end-of-line cursor cell.
	inputWidth := lipgloss.Width(divider) - lipgloss.Width(titleLabel) - 2 -
		lipgloss.Width(suffixPlain) - 1
	if inputWidth < 10 {
		// Floor: on absurdly narrow terminals keep the field usable and let the
		// suffix tail be what the row truncation eats.
		inputWidth = 10
	}
	t.titleInput.Width = inputWidth
	section(titleLabel + "  " + t.titleInput.View() + suffix)
	section(tiLabelStyle().Render("Prompt") + "\n" + t.textarea.View())
	if t.profilePicker != nil {
		section(t.profilePicker.Render())
	}
	if t.modelField != nil {
		section(t.modelField.Render())
	}
	if t.modeField != nil {
		section(t.modeField.Render())
	}
	if t.hasAccountSection() {
		section(t.accountPicker.Render())
	}

	help := createFormHelp
	if t.isTextarea() {
		help = promptFocusHelp
	}
	b.WriteString(tiHintStyle().Render(help) + "\n")
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
