package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// modelInherit is the chip that contributes no --model flag.
const modelInherit = "inherit"

// ModelField is the create form's optional Claude model override. It is a
// two-mode component: a horizontal chip row over the known aliases (the
// profile/account picker idiom — typo-proof for the common path), and a
// free-text custom mode entered by simply typing, because aliases churn with
// claude releases and full model names must always work. There is no fetched
// allowlist on purpose: no headless channel exposes the CLI's model list, and
// the Models API needs an API key subscription installs don't have — the
// launched session itself is the validator (see the claude "model-error"
// matcher in session/agent/registry.go).
//
// In custom mode, typed runes are filtered to agent.ValidModelName's charset,
// making the submit-time validation in createSessionFromForm a backstop, not a
// UX path. The chip row is width-budgeted to 41 cells so it survives the
// worst realistic overlay width (80-col terminal → 42 inner cells) without
// truncation; fitOverlay truncates rather than wraps, so height is safe
// regardless.
//
// The field is disabled (dim, skipped in Tab order, Value() == "") while the
// form's effective program does not resolve to claude — the only agent whose
// --model flag this composes.
type ModelField struct {
	options  []string // modelInherit + agent.ClaudeModelAliases
	cursor   int
	custom   bool
	input    textinput.Model
	focused  bool
	disabled bool
	width    int
}

// NewModelField builds the model field, starting on the inherit chip.
func NewModelField() *ModelField {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 64 // matches agent.ValidModelName's length cap
	return &ModelField{
		options: append([]string{modelInherit}, agent.ClaudeModelAliases...),
		input:   in,
	}
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

// HandleKeyPress routes a key by mode. Chips: arrows cycle (Up/Down accepted
// alongside Left/Right, matching the profile picker); typing a charset-valid
// rune enters custom mode seeded with it. Custom: keys go to the input, runes
// outside the safe model-name charset are dropped so the field can never hold
// a value the launch command would need to quote, and Left at position 0
// returns to the chips. Esc is never consumed — it stays the form's close key.
func (mf *ModelField) HandleKeyPress(msg tea.KeyMsg) {
	if mf.disabled {
		return
	}
	if !mf.custom {
		switch msg.Type {
		case tea.KeyLeft, tea.KeyUp:
			if mf.cursor > 0 {
				mf.cursor--
			}
		case tea.KeyRunes:
			if kept := mf.keepValidRunes(msg.Runes, ""); len(kept) > 0 {
				mf.custom = true
				mf.input.SetValue(string(kept))
				mf.input.CursorEnd()
			}
		case tea.KeyRight, tea.KeyDown:
			if mf.cursor < len(mf.options)-1 {
				mf.cursor++
			}
		}
		return
	}
	if msg.Type == tea.KeyLeft && mf.input.Position() == 0 {
		mf.custom = false
		return
	}
	if msg.Type == tea.KeyRunes {
		kept := mf.keepValidRunes(msg.Runes, strings.TrimSpace(mf.input.Value()))
		if len(kept) == 0 {
			return
		}
		msg.Runes = kept
	}
	// The rune filter above checks runes as if appended, but the text cursor can
	// sit anywhere (Home/Ctrl+A), so an accepted rune can still realize an
	// invalid value once inserted (".opus" from '.' at position 0). Apply the
	// key, then hold the field's invariant — never a non-empty invalid value —
	// by reverting the edit wholesale.
	prev, prevPos := mf.input.Value(), mf.input.Position()
	mf.input, _ = mf.input.Update(msg)
	if v := strings.TrimSpace(mf.input.Value()); v != "" && !agent.ValidModelName(v) {
		mf.input.SetValue(prev)
		mf.input.SetCursor(prevPos)
	}
}

// keepValidRunes filters runes to those that keep base+rune a valid model
// name, accumulating so a paste is filtered as a whole.
func (mf *ModelField) keepValidRunes(runes []rune, base string) []rune {
	var kept []rune
	for _, r := range runes {
		if agent.ValidModelName(base + string(kept) + string(r)) {
			kept = append(kept, r)
		}
	}
	return kept
}

// CompletePrefix implements Tab completion against the known aliases with the
// same "complete, then advance" semantics as the project field: it extends the
// value to the longest common prefix of the matching aliases and returns true
// when it grew (the caller consumes Tab) or false otherwise (Tab advances
// focus). Only custom mode completes — on the chip row Tab must keep meaning
// "advance".
func (mf *ModelField) CompletePrefix() bool {
	if mf.disabled || !mf.custom {
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
// flag: disabled, the inherit chip, or a custom value left empty (or typed as
// "inherit").
func (mf *ModelField) Value() string {
	if mf.disabled {
		return ""
	}
	if mf.custom {
		val := strings.TrimSpace(mf.input.Value())
		if strings.EqualFold(val, modelInherit) {
			return ""
		}
		return val
	}
	if mf.cursor == 0 {
		return ""
	}
	return mf.options[mf.cursor]
}

func mfLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }
func mfDimStyle() lipgloss.Style   { return theme.Current().DimStyle() }

// Render renders the field: label + a constant-height hint row, then the
// single chip-or-input row, so the form never jumps as focus or mode changes.
// Disabled renders a dim placeholder instead, mirroring the branch picker's
// inert state.
func (mf *ModelField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Model"))
	if mf.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render("  n/a — the selected profile is not Claude Code"))
		return s.String()
	}
	if mf.focused {
		if mf.custom {
			s.WriteString(mfDimStyle().Render("  ←/clear to go back · Tab completes · checked at launch"))
		} else {
			s.WriteString(mfDimStyle().Render("  ↑↓ change · type a custom name"))
		}
	}
	s.WriteString("\n\n")
	if mf.custom {
		s.WriteString(mf.input.View())
		return s.String()
	}
	for i, opt := range mf.options {
		label := " " + opt + " "
		switch {
		case i == mf.cursor && mf.focused:
			s.WriteString(ppSelectedStyle().Render(label))
		case i == mf.cursor:
			s.WriteString(label)
		default:
			s.WriteString(mfDimStyle().Render(label))
		}
		if i < len(mf.options)-1 {
			s.WriteString(mfDimStyle().Render("·"))
		}
	}
	return s.String()
}
