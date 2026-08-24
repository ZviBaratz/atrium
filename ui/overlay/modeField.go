package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/ZviBaratz/atrium/session/agent"
)

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
// The first chip is the shared noOverrideChip ("inherit"): it composes no
// --permission-mode flag, deferring to whatever claude resolves — default
// (manual) mode unless the user's settings.json pins defaultMode or the profile
// pins --permission-mode. "inherit" rather than "default" matters most for this
// field: "default" is a real member of the CLI's full enum, of which the modes
// this row offers are a subset (agent.ClaudePermissionModes), and it is already
// the word the session list and the `mode:` filter use for "no chip shown" — so
// spending it here on "send no flag" made one word name two different states (see
// noOverrideChip). The row itself never renders a "default" chip.
//
// The form cannot read settings.json, but it can read the program's pin, so a
// pinned flag surfaces in the focused hint ("program pins accept-edits"). That
// narrows — it does not retire — the tradeoff #106 accepted: the hint shows only
// while this field is focused with the cursor on the no-op chip, so the *selected
// chip* still cannot reflect the pin, and clearing a pin still means editing the
// program, not the form.
//
// The chip row totals 38 cells, inside the 41-cell budget modelField.go
// established for the worst realistic overlay width (80-col terminal → 42
// inner cells). The field is disabled (dim, skipped in Tab order,
// Value() == "") while the form's effective program does not resolve to
// claude, mirroring ModelField.
type ModeField struct {
	chipRow
}

// modeLabel is the field's label — "Permissions", not "Mode": the row is the
// --permission-mode override and the form has no other mode. A const for the
// reason effortLabel is one.
const modeLabel = "Permissions"

// NewModeField builds the mode field, starting on the no-op chip.
func NewModeField() *ModeField {
	return &ModeField{chipRow{
		options: append([]string{noOverrideChip}, agent.ClaudePermissionModes...),
		labels:  append([]string{noOverrideChip}, agent.ClaudePermissionModeLabels...),
	}}
}

// HandleKeyPress cycles the chips with the arrow keys; every other key is a
// no-op (see chipRow.moveCursor).
func (f *ModeField) HandleKeyPress(msg tea.KeyPressMsg) {
	if f.disabled {
		return
	}
	f.moveCursor(msg)
}

// Value returns the permission-mode override, or "" when the field should
// contribute no flag: disabled, or sitting on the no-op chip.
func (f *ModeField) Value() string { return f.selected() }

// Render renders the field: label + a constant-height hint row, then the chip
// row, so the form never jumps as focus changes. Disabled renders a dim
// placeholder instead, mirroring the model field's inert state.
func (f *ModeField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render(modeLabel))
	if f.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render(claudeFieldNA))
		return s.String()
	}
	// The hint explains the no-op chip, so it shows only while focused and on it
	// (see EffortField.Render); on a real value it would confuse. echoValue=true —
	// syncClaudeFieldsEnabled labels a pin only when it is inside the enum, whose
	// widest label ("accept-edits") leaves the line 4 cells clear.
	if f.focused && f.cursor == 0 {
		s.WriteString(mfDimStyle().Render("  " + f.noOverrideHint(true)))
	}
	s.WriteString("\n\n")
	s.WriteString(f.render())
	return s.String()
}
