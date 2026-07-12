package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEffortField_DefaultChipContributesNoFlag(t *testing.T) {
	f := NewEffortField()
	if got := f.Value(); got != "" {
		t.Errorf("new EffortField Value() = %q, want \"\" (default chip)", got)
	}
}

func TestEffortField_CycleSelectsLevel(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default -> low
	if got := f.Value(); got != "low" {
		t.Errorf("after one Right, Value() = %q, want \"low\"", got)
	}
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft}) // low -> default
	if got := f.Value(); got != "" {
		t.Errorf("back on default, Value() = %q, want \"\"", got)
	}
}

func TestEffortField_DisabledContributesNoFlag(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default -> low
	f.SetDisabled(true)
	if got := f.Value(); got != "" {
		t.Errorf("disabled Value() = %q, want \"\"", got)
	}
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // disabled: no-op
	if got := f.Value(); got != "" {
		t.Errorf("disabled after key, Value() = %q, want \"\"", got)
	}
}
