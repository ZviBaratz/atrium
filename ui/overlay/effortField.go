package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// effortInherit is the chip that contributes no --effort flag. Labeled "default"
// (matching the ModeField idiom): no flag means claude uses its resolved default
// effort — the user's settings.json effortLevel, or the built-in default — which
// is exactly what the chip should preserve.
const effortInherit = "default"

// EffortField is the create form's optional Claude reasoning-effort override: a
// pure chip row over agent.ClaudeEffortLevels, a sibling of ModeField. The chosen
// level rides the persisted Program string as --effort, so it is re-applied on
// pause/resume and by the daemon (matching --model / --permission-mode). Unlike
// --permission-mode the CLI does not reject an unknown level (it warns and uses
// the default), so an unsupported-for-model pick degrades gracefully rather than
// killing the launch. The field is disabled (dim, skipped in Tab order,
// Value() == "") while the effective program does not resolve to claude,
// mirroring ModelField / ModeField.
//
// The chip row totals 42 cells — the tightest of the three claude fields, right
// at the 42-cell inner width an 80-col terminal yields (see modelField.go). That
// fit is why agent.ClaudeEffortLabels abbreviates "medium" to "med": the full
// labels overflow to 45 and fitOverlay would truncate the "max" chip.
// TestClaudeChipFields_FitInnerWidth pins the budget.
type EffortField struct {
	chipRow
}

// NewEffortField builds the effort field, starting on the default chip.
func NewEffortField() *EffortField {
	return &EffortField{chipRow{
		options: append([]string{effortInherit}, agent.ClaudeEffortLevels...),
		labels:  append([]string{effortInherit}, agent.ClaudeEffortLabels...),
	}}
}

// HandleKeyPress cycles the chips with the arrow keys; every other key is a
// no-op (see chipRow.moveCursor).
func (f *EffortField) HandleKeyPress(msg tea.KeyMsg) {
	if f.disabled {
		return
	}
	f.moveCursor(msg)
}

// Value returns the effort override, or "" when the field should contribute no
// flag: disabled, or sitting on the default chip.
func (f *EffortField) Value() string { return f.selected() }

// Render renders the field: label + a constant-height hint row, then the chip
// row, so the form never jumps as focus changes. Disabled renders a dim
// placeholder instead, mirroring the model and mode fields' inert state.
func (f *EffortField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Effort"))
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
