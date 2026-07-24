package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// claudeFieldInnerWidth is the content width the create-form overlay has at the
// worst realistic terminal width: 80 cols → overlay 0.6*80 = 48 → inner 48-6 = 42
// (see app_layout.go's SetSize call and modelField.go's budget note). Every claude
// chip row must render within it, or fitOverlay truncates the rightmost chips.
const claudeFieldInnerWidth = 42

// chipField is the shared surface the width guard renders each override field
// through, including the pin setter whose hints the widest lines now come from.
type chipField interface {
	Focus()
	Render() string
	SetProfilePin(value string, mixed bool)
}

// TestClaudeChipFields_FitInnerWidth guards the shared width budget the three
// claude fields stack under: with full labels the effort row was 45 cells and the
// "max" chip truncated on an 80-col terminal (hence "medium" → "med"). Rendered
// focused, each field's widest line must stay within the inner width — across the
// no-op-chip hint states, since those label lines (e.g. "profile pins
// accept-edits") can now exceed the chip row. The pin values are each field's
// widest realistic label. (Pre-existing gap, not covered here: the model field's
// custom-mode hint is 61 cells and truncates at 80 cols — see the PR body.)
func TestClaudeChipFields_FitInnerWidth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       chipField
		pinWide string // widest single-pin label this field can show
	}{
		{"model", NewModelField(), "claude-opus-4-6"},
		{"mode", NewModeField(), "accept-edits"},
		{"effort", NewEffortField(), "xhigh"},
	} {
		for _, st := range []struct {
			name  string
			value string
			mixed bool
		}{
			{"no pin", "", false},
			{"shared pin", tc.pinWide, false},
			{"mixed pins", "", true},
		} {
			tc.f.SetProfilePin(st.value, st.mixed)
			tc.f.Focus()
			if w := lipgloss.Width(tc.f.Render()); w > claudeFieldInnerWidth {
				t.Errorf("%s field (%s) render width = %d, want <= %d (overflows the 80-col overlay inner width; content truncates)",
					tc.name, st.name, w, claudeFieldInnerWidth)
			}
		}
	}
}

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
