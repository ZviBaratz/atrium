package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ModelField is the create form's optional Claude model override: a free-text
// input with Tab completion over the known aliases (agent.ClaudeModelAliases).
// Free text is deliberate — aliases churn with claude releases and full model
// names must always work — so the alias list is completion sugar, never an
// allowlist. Typed runes are filtered to agent.ValidModelName's charset, making
// the submit-time validation in createSessionFromForm a backstop, not a UX path.
//
// The field is disabled (dim, skipped in Tab order, Value() == "") while the
// form's effective program does not resolve to claude — the only agent whose
// --model flag this composes.
type ModelField struct {
	input    textinput.Model
	focused  bool
	disabled bool
	width    int
}

// NewModelField builds the model field. Empty means "inherit" (no flag).
func NewModelField() *ModelField {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 64 // matches agent.ValidModelName's length cap
	in.Placeholder = "inherit"
	return &ModelField{input: in}
}

// Focus gives the field focus (no-op visual change while disabled; the form
// never focuses a disabled stop).
func (mf *ModelField) Focus() {
	mf.focused = true
	mf.input.Focus()
}

// Blur removes focus from the field.
func (mf *ModelField) Blur() {
	mf.focused = false
	mf.input.Blur()
}

// SetWidth sets the rendering width.
func (mf *ModelField) SetWidth(w int) {
	mf.width = w
	mf.input.Width = w
}

// SetDisabled toggles the inert state (the effective program is not claude).
func (mf *ModelField) SetDisabled(disabled bool) { mf.disabled = disabled }

// Disabled reports whether the field is inert.
func (mf *ModelField) Disabled() bool { return mf.disabled }

// HandleKeyPress forwards a key to the input, dropping runes outside the safe
// model-name charset so the field can never hold a value the launch command
// would need to quote.
func (mf *ModelField) HandleKeyPress(msg tea.KeyMsg) {
	if mf.disabled {
		return
	}
	if msg.Type == tea.KeyRunes {
		kept := msg.Runes[:0]
		for _, r := range msg.Runes {
			if agent.ValidModelName(strings.TrimSpace(mf.input.Value()) + string(r)) {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			return
		}
		msg.Runes = kept
	}
	mf.input, _ = mf.input.Update(msg)
}

// CompletePrefix implements Tab completion against the known aliases with the
// same "complete, then advance" semantics as the project field: it extends the
// value to the longest common prefix of the matching aliases and returns true
// when it grew (the caller consumes Tab) or false otherwise (Tab advances
// focus). An empty value never completes — Tab through an untouched field must
// keep meaning "inherit".
func (mf *ModelField) CompletePrefix() bool {
	if mf.disabled {
		return false
	}
	val := strings.TrimSpace(mf.input.Value())
	if val == "" {
		return false
	}
	lower := strings.ToLower(val)
	var matches []string
	for _, a := range agent.ClaudeModelAliases {
		if strings.HasPrefix(a, lower) {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		return false
	}
	completed := longestCommonPrefix(matches)
	if completed == val {
		return false
	}
	mf.input.SetValue(completed)
	mf.input.CursorEnd()
	return true
}

// Value returns the model override, or "" when the field should contribute no
// flag: disabled, left empty, or explicitly set to "inherit".
func (mf *ModelField) Value() string {
	if mf.disabled {
		return ""
	}
	val := strings.TrimSpace(mf.input.Value())
	if strings.EqualFold(val, "inherit") {
		return ""
	}
	return val
}

func mfLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }
func mfDimStyle() lipgloss.Style   { return theme.Current().DimStyle() }

// Render renders the field: label + input on one line, with a constant-height
// hint row below (the alias list when focused, a blank otherwise) so the form
// never jumps as focus moves. Disabled renders a dim placeholder instead of
// the input, mirroring the branch picker's inert state.
func (mf *ModelField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Model"))
	if mf.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render("  n/a — the selected profile is not Claude Code"))
		return s.String()
	}
	if mf.focused {
		s.WriteString(mfDimStyle().Render("  Tab to complete: " + strings.Join(agent.ClaudeModelAliases, " · ")))
	}
	s.WriteString("\n\n")
	s.WriteString(mf.input.View())
	return s.String()
}
