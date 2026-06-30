package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// HandleKeyPress processes a key press and updates the state accordingly.
// Returns (shouldClose, branchFilterChanged).
func (t *TextInputOverlay) HandleKeyPress(msg tea.KeyMsg) (bool, bool) {
	// Double-tap Ctrl+R clears the form (create form only): the first press arms,
	// any other key disarms, a second consecutive press requests the clear. The app
	// performs the rebuild — it owns the config/profiles the pickers need.
	if t.isCreateForm && msg.Type == tea.KeyCtrlR {
		if t.clearArmed {
			t.clearArmed = false
			t.clearRequested = true
		} else {
			t.clearArmed = true
		}
		return false, false
	}
	t.clearArmed = false

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
		t.setFocusIndex(t.nextEnabledIndex(1))
		return false, false
	case tea.KeyShiftTab:
		t.setFocusIndex(t.nextEnabledIndex(-1))
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
		t.setFocusIndex(t.nextEnabledIndex(1))
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
