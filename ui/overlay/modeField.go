package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// modeInherit is the chip that contributes no --permission-mode flag. It is
// labeled "default" — the mode vocabulary claude users know — rather than
// "inherit": no flag IS default mode unless the user's settings.json pins
// defaultMode or the profile pins --permission-mode, and in those cases not
// clobbering the deliberate config is exactly what the chip should mean. The
// flip side is accepted: with a profile pin the row still renders "default"
// as selected (the form cannot see settings.json, so reflecting only the
// profile half would mislead in the other direction), and clearing a pin
// back to true default mode means editing the profile, not the form.
const modeInherit = "default"

// ModeField is the create form's optional Claude permission-mode override: a
// pure chip row (the profile-picker idiom) over agent.ClaudePermissionModes.
// Unlike ModelField there is no free-text custom mode — --permission-mode is
// a closed enum the CLI rejects at argv parse time, so chips are the whole
// input surface. The chosen mode rides the persisted Program string, so it is
// re-applied whenever the program is re-executed: a session created in plan
// mode resumes in plan mode (matching --model semantics). Known tradeoff: a
// *dead* session resurrected via --continue re-enters plan mode even if the
// user had approved a plan and moved on — mode-aware resume rewriting was
// judged not worth the plumbing for a state one shift+tab undoes. The
// plan-approval dialog a plan-mode session ends with is autoyes-safe via the
// NoAutoTap "plan" matcher in session/agent/registry.go.
//
// The chip row totals 37 cells, inside the 41-cell budget modelField.go
// established for the worst realistic overlay width (80-col terminal → 42
// inner cells). The field is disabled (dim, skipped in Tab order,
// Value() == "") while the form's effective program does not resolve to
// claude, mirroring ModelField.
type ModeField struct {
	chipRow
}

// NewModeField builds the mode field, starting on the default chip.
func NewModeField() *ModeField {
	return &ModeField{chipRow{
		options: append([]string{modeInherit}, agent.ClaudePermissionModes...),
		labels:  append([]string{modeInherit}, agent.ClaudePermissionModeLabels...),
	}}
}

// HandleKeyPress cycles the chips with the arrow keys; every other key is a
// no-op (see chipRow.moveCursor).
func (f *ModeField) HandleKeyPress(msg tea.KeyMsg) {
	if f.disabled {
		return
	}
	f.moveCursor(msg)
}

// Value returns the permission-mode override, or "" when the field should
// contribute no flag: disabled, or sitting on the default chip.
func (f *ModeField) Value() string { return f.selected() }

// Render renders the field: label + a constant-height hint row, then the chip
// row, so the form never jumps as focus changes. Disabled renders a dim
// placeholder instead, mirroring the model field's inert state.
func (f *ModeField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Permissions"))
	if f.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render(claudeFieldNA))
		return s.String()
	}
	if f.focused {
		s.WriteString(mfDimStyle().Render("  ↑↓ change"))
	}
	s.WriteString("\n\n")
	s.WriteString(f.render())
	return s.String()
}
