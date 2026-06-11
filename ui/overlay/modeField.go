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
// clobbering the deliberate config is exactly what the chip should mean.
const modeInherit = "default"

// ModeField is the create form's optional Claude permission-mode override: a
// pure chip row (the profile-picker idiom) over agent.ClaudePermissionModes.
// Unlike ModelField there is no free-text custom mode — --permission-mode is
// a closed enum the CLI rejects at argv parse time, so chips are the whole
// input surface. The chosen mode rides the persisted Program string, so it is
// re-applied on resume: a session created in plan mode resumes in plan mode
// (matching --model semantics). The plan-approval dialog a plan-mode session
// ends with is autoyes-safe via the NoAutoTap "plan" matcher in
// session/agent/registry.go.
//
// The chip row totals 37 cells, inside the 41-cell budget modelField.go
// established for the worst realistic overlay width (80-col terminal → 42
// inner cells). The field is disabled (dim, skipped in Tab order,
// Value() == "") while the form's effective program does not resolve to
// claude, mirroring ModelField.
type ModeField struct {
	options  []string // modeInherit + agent.ClaudePermissionModes
	cursor   int
	focused  bool
	disabled bool
}

// NewModeField builds the mode field, starting on the default chip.
func NewModeField() *ModeField {
	return &ModeField{options: append([]string{modeInherit}, agent.ClaudePermissionModes...)}
}

// Focus gives the field focus.
func (f *ModeField) Focus() { f.focused = true }

// Blur removes focus from the field.
func (f *ModeField) Blur() { f.focused = false }

// SetDisabled toggles the inert state (the effective program is not claude).
func (f *ModeField) SetDisabled(disabled bool) { f.disabled = disabled }

// Disabled reports whether the field is inert.
func (f *ModeField) Disabled() bool { return f.disabled }

// HandleKeyPress cycles the chips with the arrow keys (Up/Down accepted
// alongside Left/Right, matching the profile picker). Esc is never consumed —
// it stays the form's close key.
func (f *ModeField) HandleKeyPress(msg tea.KeyMsg) {
	if f.disabled {
		return
	}
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp:
		if f.cursor > 0 {
			f.cursor--
		}
	case tea.KeyRight, tea.KeyDown:
		if f.cursor < len(f.options)-1 {
			f.cursor++
		}
	}
}

// Value returns the permission-mode override, or "" when the field should
// contribute no flag: disabled, or sitting on the default chip.
func (f *ModeField) Value() string {
	if f.disabled || f.cursor == 0 {
		return ""
	}
	return f.options[f.cursor]
}

// Render renders the field: label + a constant-height hint row, then the chip
// row, so the form never jumps as focus changes. Disabled renders a dim
// placeholder instead, mirroring the model field's inert state.
func (f *ModeField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Permissions"))
	if f.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render("  n/a — the selected profile is not Claude Code"))
		return s.String()
	}
	if f.focused {
		s.WriteString(mfDimStyle().Render("  ↑↓ change"))
	}
	s.WriteString("\n\n")
	s.WriteString(renderChipRow(f.options, f.cursor, f.focused))
	return s.String()
}
