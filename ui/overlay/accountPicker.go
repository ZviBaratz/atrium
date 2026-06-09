package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"

	tea "github.com/charmbracelet/bubbletea"
)

// AccountPicker is an embeddable horizontal selector for choosing the Claude Code
// account a new session runs under. It mirrors ProfilePicker.
type AccountPicker struct {
	accounts []config.ClaudeAccount
	cursor   int
	focused  bool
	width    int
	// touched flips true the first time the user drives the picker with a nav key.
	// It separates an auto-routed preselection (which the form revises as the target
	// project changes) from a deliberate override (which must stick). Programmatic
	// SelectByName never sets it.
	touched bool
}

// NewAccountPicker creates a picker over accounts; the first is selected by default.
func NewAccountPicker(accounts []config.ClaudeAccount) *AccountPicker {
	return &AccountPicker{accounts: accounts}
}

// SelectByName preselects the account with the given name (e.g. the auto-routed
// one). No-op if the name is not present, or once the user has taken manual
// control (Touched) — a deliberate choice outranks later auto-routing.
func (ap *AccountPicker) SelectByName(name string) {
	if ap.touched {
		return
	}
	for i, a := range ap.accounts {
		if a.Name == name {
			ap.cursor = i
			return
		}
	}
}

// Focus gives the picker focus.
func (ap *AccountPicker) Focus() { ap.focused = true }

// Blur removes focus from the picker.
func (ap *AccountPicker) Blur() { ap.focused = false }

// SetWidth sets the rendering width.
func (ap *AccountPicker) SetWidth(w int) { ap.width = w }

// HasMultiple returns true if there is more than one account to choose from.
func (ap *AccountPicker) HasMultiple() bool { return len(ap.accounts) > 1 }

// Touched reports whether the user has driven the picker with a nav key. The form
// uses it to decide auto-routed preselection (untouched) versus an override (touched).
func (ap *AccountPicker) Touched() bool { return ap.touched }

// HandleKeyPress moves the cursor; Up/Down mirror Left/Right (the form navigates ↑↓).
// Any nav key marks the picker touched — engaging the control signals intent even if
// the cursor does not move (already at an end).
func (ap *AccountPicker) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		ap.touched = true
		if ap.cursor > 0 {
			ap.cursor--
		}
		return true
	case tea.KeyRight, tea.KeyDown:
		ap.touched = true
		if ap.cursor < len(ap.accounts)-1 {
			ap.cursor++
		}
		return true
	}
	return false
}

// GetSelectedAccount returns the selected account, or the zero value when empty.
func (ap *AccountPicker) GetSelectedAccount() config.ClaudeAccount {
	if len(ap.accounts) == 0 {
		return config.ClaudeAccount{}
	}
	if ap.cursor < 0 || ap.cursor >= len(ap.accounts) {
		return ap.accounts[0]
	}
	return ap.accounts[ap.cursor]
}

// Render draws the picker (label + horizontal options), mirroring ProfilePicker.
func (ap *AccountPicker) Render() string {
	var s strings.Builder
	s.WriteString(ppLabelStyle().Render("Account"))
	if ap.HasMultiple() && ap.focused {
		s.WriteString(ppDimStyle().Render("  ↑↓ to change"))
	}
	s.WriteString("\n\n")
	for i, a := range ap.accounts {
		label := " " + a.Name + " "
		switch {
		case i == ap.cursor && ap.focused:
			s.WriteString(ppSelectedStyle().Render(label))
		case i == ap.cursor:
			s.WriteString(label)
		default:
			s.WriteString(ppDimStyle().Render(label))
		}
		if i < len(ap.accounts)-1 {
			s.WriteString(ppDimStyle().Render(" | "))
		}
	}
	return s.String()
}
