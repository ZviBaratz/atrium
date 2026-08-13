package overlay

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TextInputOverlay represents a text input overlay with state management. A single type
// backs three live roles — the quick-send box (which the diff-comment composer also
// uses), the smart-dispatch input, and the full new-session create form —
// distinguished by the flags below and by which picker pointers are set.
// NewTextInputOverlay is the shared constructor underneath them rather than a fourth
// role: nothing in the app builds a bare one.
//
// The implementation is split across textInput_*.go by concern: focus (the focusRing
// and its delegators), keys (HandleKeyPress), render, size, and create (the create-form
// constructor and its accessors).
type TextInputOverlay struct {
	textarea        textarea.Model
	titleInput      textinput.Model
	Title           string
	focus           focusRing // ordered focusable stops actually present + the cursor
	Submitted       bool
	Canceled        bool
	OnSubmit        func()
	width           int
	height          int
	directoryPicker *DirectoryPicker
	variantPicker   *VariantPicker
	modelField      *ModelField
	modeField       *ModeField
	effortField     *EffortField
	depsField       *DepsField
	accountPicker   *AccountPicker
	branchPicker    *BranchPicker
	isCreateForm    bool // true for the new-session form (has a title field)
	smartDispatch   bool // true for the one-field smart-dispatch input overlay
	submitOnEnter   bool // true for the quick-send overlay: Enter submits, the newline keys still insert
	clearArmed      bool // first Ctrl+R seen; a second consecutive press confirms a clear
	clearRequested  bool // a confirmed double-tap Ctrl+R; the app rebuilds a fresh form
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
		focus:    focusRing{stops: []focusStop{stopTextarea, stopEnter}},
	}
	overlay.focusStop(stopTextarea)
	return overlay
}

// NewQuickSendOverlay creates the compose-and-send overlay used to fire an ad-hoc message at
// the selected running session without attaching. It is the same single textarea + submit button
// NewTextInputOverlay builds, but Enter submits immediately — Shift+Enter, Alt+Enter and Ctrl+J
// are the newline keys — so a short reply is one keystroke away. See HandleKeyPress and the
// submitOnEnter hint in Render.
func NewQuickSendOverlay(title string) *TextInputOverlay {
	o := NewTextInputOverlay(title, "")
	o.submitOnEnter = true
	return o
}

// NewSmartDispatchOverlay creates the one-field input that opens the smart-dispatch
// flow: the user types one free-form description, Enter submits it (like quick-send),
// and the app routes it to a project and pre-fills the new-session form. The
// smartDispatch flag lets the submit dispatcher tell it apart from quick-send.
//
// One field, not one line — it is the same textarea as quick-send, so the newline
// keys work here too and a description can run to several lines.
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

func newTextarea(initialValue string) textarea.Model {
	ti := textarea.New()
	ti.SetValue(initialValue)
	ti.Focus()
	ti.ShowLineNumbers = false
	ti.Prompt = ""
	// v2 moved the style states behind an accessor pair, so this is a
	// read-modify-write rather than a field poke. Same intent: the create form's
	// prompt is a plain multi-line field, and the textarea's default cursor-line
	// highlight would paint a band across it.
	styles := ti.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	ti.SetStyles(styles)
	ti.CharLimit = 0
	ti.MaxHeight = 0
	// Match the single-line title field, which already binds ctrl+arrow for word
	// motion; the textarea default binds only alt+arrow. Make ctrl+j the textarea's
	// newline: the overlay intercepts Enter for field navigation, so the literal
	// "enter" binding never fires here, and Shift+Enter and Alt+Enter are handled
	// explicitly in HandleKeyPress. ctrl+j is the one newline key that works in every
	// terminal — Shift+Enter needs one that disambiguates modified keys, and Alt+Enter
	// needs a hand not already on ctrl.
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

// Init initializes the text input overlay model
func (t *TextInputOverlay) Init() tea.Cmd {
	return textarea.Blink
}

// View renders the model's view
func (t *TextInputOverlay) View() string {
	return t.Render()
}

// GetValue returns the current value of the text input.
func (t *TextInputOverlay) GetValue() string {
	return t.textarea.Value()
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
