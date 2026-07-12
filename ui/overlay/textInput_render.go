package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// Overlay styles read the active theme at render time (package-level style vars
// would capture the default theme at import, before config selects one).
func tiStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2)
}

func tiTitleStyle() lipgloss.Style {
	return theme.Current().OverlayTitleStyle().MarginBottom(1)
}

func tiButtonStyle() lipgloss.Style { return theme.Current().FgStyle() }

func tiFocusedButtonStyle() lipgloss.Style { return overlaySelectedStyle() }

func tiDividerStyle() lipgloss.Style { return overlayDimStyle() }

func tiLabelStyle() lipgloss.Style { return overlayLabelStyle() }

func tiHintStyle() lipgloss.Style { return theme.Current().OverlayHintStyle() }

// createFormHelp is the footer shown for every create-form field except the prompt.
// Enter advances between fields (and submits from a filled title — the one-handed quick
// create), so submission is surfaced as Ctrl+S, which works from any field, rather than an
// ambiguous "Enter create".
const createFormHelp = "Tab complete/move · ↑↓ select · ↵ create from name · ⌃S create"

// promptFocusHelp is the footer shown while the prompt textarea holds focus, where Enter
// advances like Tab and the newline keys differ. Shift+Enter needs a Claude-Code-style
// terminal setup; Ctrl+J always works.
const promptFocusHelp = "⇧↵ / ⌃J newline · ↵ next field · ⌃S create"

// renderPickerRows renders a list of pre-formatted labels windowed around the cursor,
// always emitting exactly rows lines (padding with blanks) so the caller's height is
// constant. When labels is empty, placeholder (if set) occupies the first row.
func renderPickerRows(labels []string, cursor, rows int, focused bool, placeholder string, selStyle, dimStyle lipgloss.Style) string {
	if rows < 1 {
		rows = 1
	}
	lines := make([]string, 0, rows)
	if len(labels) == 0 {
		if placeholder != "" {
			lines = append(lines, dimStyle.Render("  "+placeholder))
		}
	} else {
		start := 0
		if cursor >= rows {
			start = cursor - rows + 1
		}
		end := start + rows
		if end > len(labels) {
			end = len(labels)
		}
		for i := start; i < end; i++ {
			switch {
			case i == cursor && focused:
				lines = append(lines, selStyle.Render("> "+labels[i]))
			case i == cursor:
				lines = append(lines, "  "+labels[i])
			default:
				lines = append(lines, dimStyle.Render("  "+labels[i]))
			}
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// Render renders the text input overlay.
func (t *TextInputOverlay) Render() string {
	// Inner content width (accounting for padding and borders)
	innerWidth := t.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Set component widths to fit within the overlay. The title input's width is
	// owned by renderCreateForm, which carves the verdict suffix out of it.
	t.textarea.SetWidth(innerWidth)

	// Build a horizontal divider line
	divider := tiDividerStyle().Render(strings.Repeat("─", innerWidth))

	if t.isCreateForm {
		return t.fitOverlay(t.renderCreateForm(divider), innerWidth, divider)
	}

	// Plain prompt overlay (the `p` flow): no pickers — just a title, the prompt textarea,
	// and the submit button.
	var content string
	content += tiTitleStyle().Render(t.Title) + "\n"
	content += t.textarea.View() + "\n\n"
	content += divider + "\n\n"
	if t.submitOnEnter {
		// Mirror the create form's newline vocabulary: Shift+Enter (alt+enter on the
		// wire, needs a configured terminal) or the universal Ctrl+J — see newTextarea.
		content += tiHintStyle().Render("↵ send · ⇧↵ / ⌃J newline · esc cancel") + "\n"
	}
	content += t.renderEnterButton()

	return t.fitOverlay(content, innerWidth, divider)
}

// fitOverlay constrains the assembled inner content to the overlay's terminal share
// before drawing the bordered box. Two invariants matter: the composed View() must
// never be wider or taller than the terminal, or PlaceOverlay spills past the screen
// and bubbletea's line-diffing desyncs (stale fragments, a mis-placed popup).
//
//   - Width: every line is truncated to innerWidth, so a long value (e.g. a deep
//     project path or profile command) can never widen the box past t.width. The
//     dividers, already innerWidth wide, anchor the box to a stable width.
//   - Height: the create form's constant-height sections can total several rows more
//     than a short terminal (an 80×24 screen with profiles, the claude fields, and an
//     account picker needs over 30). Spacing is shed in stages — blank filler lines
//     first, then divider lines (pure visual separation) — so the form compacts
//     instead of scrolling. If even that is not enough (a terminal below the 24-row
//     floor), the tail is clipped outright: a partially visible form is degraded but
//     stable, while an oversize View() is not.
func (t *TextInputOverlay) fitOverlay(content string, innerWidth int, divider string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > innerWidth {
			lines[i] = truncate.StringWithTail(l, uint(innerWidth), "…")
		}
	}
	// The bordered, padded box adds 4 rows (border top/bottom + vertical padding),
	// so the inner content must fit within t.height-4 for the box to fit the screen.
	if budget := t.height - 4; budget > 0 {
		lines = dropLinesToFit(lines, budget, func(l string) bool { return lipgloss.Width(l) == 0 })
		lines = dropLinesToFit(lines, budget, func(l string) bool { return l == divider })
		if len(lines) > budget {
			lines = lines[:budget]
		}
	}
	return tiStyle().Render(strings.Join(lines, "\n"))
}

// dropBlankLinesToFit removes interior blank lines (and only blank lines) until the
// slice is at most budget lines long or no removable blanks remain. It is the first
// stage of fitOverlay's graceful degradation for short terminals.
func dropBlankLinesToFit(lines []string, budget int) []string {
	return dropLinesToFit(lines, budget, func(l string) bool { return lipgloss.Width(l) == 0 })
}

// dropLinesToFit removes interior lines matching droppable — leading, trailing, and
// non-matching lines are always preserved — until the slice is at most budget lines
// long or no droppable lines remain. fitOverlay layers it: blanks go first (natural
// spacing), then dividers (visual separation), so real content is only ever lost to
// the final clip on terminals below the supported 24-row floor.
func dropLinesToFit(lines []string, budget int, droppable func(string) bool) []string {
	excess := len(lines) - budget
	if excess <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if excess > 0 && i > 0 && i < len(lines)-1 && droppable(l) {
			excess--
			continue
		}
		out = append(out, l)
	}
	return out
}

// renderCreateForm renders the unified new-session form. Every section is constant-height
// for a given window size (the picker/prompt row counts are fixed in SetSize via fitRows),
// so the vertically centered overlay never jumps as focus moves between fields.
func (t *TextInputOverlay) renderCreateForm(divider string) string {
	var b strings.Builder

	// Each section sits directly above a divider with one blank line after it, keeping the
	// form compact while still visually separating fields.
	section := func(content string) {
		b.WriteString(content + "\n")
		b.WriteString(divider + "\n")
	}

	b.WriteString(tiTitleStyle().Render(t.Title) + "\n")
	if t.directoryPicker != nil {
		project := t.directoryPicker.Render()
		if t.projectHint != "" {
			project += "  " + tiHintStyle().Render(t.projectHint)
		}
		section(project)
	}
	if t.branchPicker != nil {
		section(t.branchPicker.Render())
	}
	// The title is the form's only hard-required input; carry a dim marker while it is
	// empty so the requirement is visible before the submit-time error backstop. A
	// validation error (duplicate name in the target group) wins over the hint. Both
	// trail the input rather than sitting between the label and the field: a
	// variable-width prefix would shift the text under the user's caret on exactly
	// the keystrokes that recompute the verdict. The input pads itself to its Width,
	// so the suffix's columns are carved out of the input up front — otherwise the
	// message would land past fitOverlay's truncation edge, invisible. (innerWidth
	// is recovered from the divider, which is rendered exactly that wide.)
	titleLabel := tiLabelStyle().Render("Title")
	var suffixPlain, suffix string
	switch {
	case t.titleError != "":
		suffixPlain = " (" + t.titleError + ")"
		suffix = theme.Current().DangerStyle().Render(suffixPlain)
	case t.GetTitle() == "":
		suffixPlain = " (required)"
		suffix = tiHintStyle().Render(suffixPlain)
	}
	// -1: the input renders one column past Width for the end-of-line cursor cell.
	inputWidth := lipgloss.Width(divider) - lipgloss.Width(titleLabel) - 2 -
		lipgloss.Width(suffixPlain) - 1
	if inputWidth < 10 {
		// Floor: on absurdly narrow terminals keep the field usable and let the
		// suffix tail be what the row truncation eats.
		inputWidth = 10
	}
	t.titleInput.Width = inputWidth
	section(titleLabel + "  " + t.titleInput.View() + suffix)
	section(tiLabelStyle().Render("Prompt") + "\n" + t.textarea.View())
	if t.profilePicker != nil {
		section(t.profilePicker.Render())
	}
	if t.modelField != nil {
		section(t.modelField.Render())
	}
	if t.effortField != nil {
		section(t.effortField.Render())
	}
	if t.modeField != nil {
		section(t.modeField.Render())
	}
	if t.hasAccountSection() {
		section(t.accountPicker.Render())
	}

	help := createFormHelp
	if t.isTextarea() {
		help = promptFocusHelp
	}
	clearHint := "⌃R clear"
	if t.clearArmed {
		clearHint = "⌃R again"
	}
	b.WriteString(tiHintStyle().Render(help+" · "+clearHint) + "\n")
	b.WriteString(t.renderEnterButton())

	return b.String()
}

// renderEnterButton renders the submit button, highlighted when it holds focus.
func (t *TextInputOverlay) renderEnterButton() string {
	enterButton := " Enter "
	if t.isCreateForm {
		enterButton = " Create " // matches the ⌃S create hint in the footer
	}
	if t.isEnterButton() {
		return tiFocusedButtonStyle().Render(enterButton)
	}
	return tiButtonStyle().Render(enterButton)
}
