package overlay

import (
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TextInputOverlay represents a text input overlay with state management. A single type
// backs four roles — the plain prompt, the quick-send and smart-dispatch single-line
// inputs, and the full new-session create form — distinguished by the flags below and by
// which picker pointers are set. The implementation is split across textInput_*.go by
// concern: focus (the focusRing and its delegators), keys (HandleKeyPress), render, size,
// and create (the create-form constructor and its accessors).
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
	profilePicker   *ProfilePicker
	modelField      *ModelField
	modeField       *ModeField
	accountPicker   *AccountPicker
	branchPicker    *BranchPicker
	isCreateForm    bool // true for the new-session form (has a title field)
	smartDispatch   bool // true for the single-line smart-dispatch input overlay
	submitOnEnter   bool // true for the quick-send overlay: Enter submits, Alt+Enter is a newline
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
