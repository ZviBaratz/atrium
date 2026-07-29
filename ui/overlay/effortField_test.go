package overlay

import (
	"testing"

	"charm.land/lipgloss/v2"
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
	SetProfilePin(label string, pinned, mixed bool)
}

// TestClaudeChipFields_FitInnerWidth guards the shared width budget the three
// claude fields stack under: with full labels the effort row was 45 cells and the
// "max" chip truncated on an 80-col terminal (hence "medium" → "med"). Rendered
// focused, each field's widest line must stay within the inner width — across the
// no-op-chip hint states, since those label lines (e.g. "program pins
// accept-edits") can now exceed the chip row.
//
// pinWide is each field's widest *nameable* label, which is the whole reason the
// budget holds: syncClaudeFieldsEnabled hands over a label only for a value inside
// Atrium's enum, so these are genuine maxima rather than optimistic samples. The
// "unnameable pin" state below is the other half of that contract — an unvalidated
// --effort token arrives pinned with no label, and must fall back to the short
// "program pins it" rather than being echoed. (Pre-existing gap, not covered here:
// the model field's custom-mode hint is 61 cells and truncates at 80 cols.)
func TestClaudeChipFields_FitInnerWidth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       chipField
		pinWide string // widest nameable single-pin label this field can show
	}{
		{"model", NewModelField(), "claude-opus-4-6"},
		{"mode", NewModeField(), "accept-edits"},
		{"effort", NewEffortField(), "xhigh"},
	} {
		for _, st := range []struct {
			name   string
			label  string
			pinned bool
			mixed  bool
		}{
			{"no pin", "", false, false},
			{"shared pin", tc.pinWide, true, false},
			{"unnameable pin", "", true, false},
			{"mixed pins", "", false, true},
		} {
			tc.f.SetProfilePin(st.label, st.pinned, st.mixed)
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
		t.Errorf("new EffortField Value() = %q, want \"\" (no-op chip)", got)
	}
}

func TestEffortField_CycleSelectsLevel(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(keyMsg("right")) // default -> low
	if got := f.Value(); got != "low" {
		t.Errorf("after one Right, Value() = %q, want \"low\"", got)
	}
	f.HandleKeyPress(keyMsg("left")) // low -> default
	if got := f.Value(); got != "" {
		t.Errorf("back on default, Value() = %q, want \"\"", got)
	}
}

func TestEffortField_DisabledContributesNoFlag(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(keyMsg("right")) // default -> low
	f.SetDisabled(true)
	if got := f.Value(); got != "" {
		t.Errorf("disabled Value() = %q, want \"\"", got)
	}
	f.HandleKeyPress(keyMsg("right")) // disabled: no-op
	if got := f.Value(); got != "" {
		t.Errorf("disabled after key, Value() = %q, want \"\"", got)
	}
}
