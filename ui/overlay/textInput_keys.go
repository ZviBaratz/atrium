package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// HandlePaste inserts pasted text into the focused field. Returns
// branchFilterChanged so the app schedules the same debounced branch search a
// typed edit schedules.
//
// It mirrors HandleKeyPress's default branch and nothing else, which is the whole
// point: the cases above that branch are commands, and a paste must never reach
// them. Routing a paste through HandleKeyPress instead would make a clipboard
// holding the word "esc" cancel the form and discard the draft, because the switch
// is keyed on msg.String() and v2's String() returns the pasted text verbatim.
//
// The textarea and title input get the real tea.PasteMsg: both bubbles models
// handle it natively via insertRunesFromUserInput, which keeps newlines intact.
// The selection fields (variant, model, mode, effort, account) take no text, so a
// paste there is inert — as is a paste on the Create button.
func (t *TextInputOverlay) HandlePaste(msg tea.PasteMsg) (branchFilterChanged bool) {
	// A paste is not the second half of a double-tap: it disarms the clear gesture
	// exactly as any other non-Ctrl+R input does.
	t.clearArmed = false

	switch {
	case t.isTitle():
		t.titleInput, _ = t.titleInput.Update(msg)
	case t.isTextarea():
		t.textarea, _ = t.textarea.Update(msg)
	case t.isDirectoryPicker():
		t.directoryPicker.HandlePaste(msg.Content)
	case t.isBranchPicker():
		_, branchFilterChanged = t.branchPicker.HandlePaste(msg.Content)
	}
	return branchFilterChanged
}

// HandleKeyPress processes a key press and updates the state accordingly.
// Returns (shouldClose, branchFilterChanged).
func (t *TextInputOverlay) HandleKeyPress(msg tea.KeyPressMsg) (bool, bool) {
	// Double-tap Ctrl+R clears the form (create form only): the first press arms,
	// any other key disarms, a second consecutive press requests the clear. The app
	// performs the rebuild — it owns the config/profiles the pickers need.
	if t.isCreateForm && msg.String() == "ctrl+r" {
		if t.clearArmed {
			t.clearArmed = false
			t.clearRequested = true
		} else {
			t.clearArmed = true
		}
		return false, false
	}
	t.clearArmed = false

	switch msg.String() {
	case "tab":
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
	case "shift+tab":
		t.setFocusIndex(t.nextEnabledIndex(-1))
		return false, false
	case "esc":
		t.Canceled = true
		return true, false
	case "ctrl+s":
		// Submit from any field. Enter submits only on the Create button and a filled
		// title (it advances to the next field everywhere else, including the prompt), so
		// Ctrl+S is the submit-from-anywhere shortcut; the Create button remains the
		// fallback.
		t.Submitted = true
		if t.OnSubmit != nil {
			t.OnSubmit()
		}
		return true, false
	case "enter", "alt+enter":
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
			if msg.Mod.Contains(tea.ModAlt) {
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
		if t.isVariantPicker() {
			if t.variantPicker.HandleKeyPress(msg) {
				// The model, effort, and permission-mode overrides only apply to claude;
				// a count/profile change can flip whether any selected variant is claude,
				// so keep those fields' enabled state in step.
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
		if t.isEffortField() {
			t.effortField.HandleKeyPress(msg)
			return false, false
		}
		if t.isAccountPicker() {
			switch msg.Code {
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
