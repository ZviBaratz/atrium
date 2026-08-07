package overlay

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui/theme"
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

// createFormHelp / promptFocusHelp are the footer's rungs, widest first, for every
// create-form field except the prompt and for the prompt textarea respectively (see
// fitHint; the trailing "⌃R clear" is appended by renderCreateForm, so these are
// measured with it).
//
// Enter advances between fields (and submits from a filled title — the one-handed quick
// create), so submission is surfaced as Ctrl+S, which works from any field, rather than an
// ambiguous "Enter create". That makes ⌃S the clause that must survive the narrowest
// rung: it is the only way to submit without tabbing to the button. The prompt's
// Shift+Enter goes first because it is the half of that pair that is not universally
// true — it needs a terminal that disambiguates modified keys, which Ctrl+J does not —
// so it is both the first rung a narrow terminal drops and the rung
// promptFocusHelpLegacy drops on capability grounds. Those two reasons are separate
// and the ordering serves both.
//
// The full rungs are 73 and 53 cells against the 42 an 80-col terminal gives, i.e. the
// footer used to be cut at "↵ create from n…" — losing ⌃S and ⌃R outright — on the
// narrowest terminal Atrium supports.
//
// THE RULE (#466): this footer owns the navigation keys. A per-field hint earns its
// place on a label line only by saying something the footer does not — the form has
// nine sections, and repeating "↑↓ …" on each turns a hint into wallpaper. #460
// applied this to Effort and Permissions and stopped there, leaving Account and the
// Profile picker restating it; #466 finished the sweep and pinned the result with
// TestCreateFormHints_FooterOwnsTheNavKeys, which counts the glyph across the whole
// rendered form once per focus stop.
//
// Two hints stay, on the merits, and both are asserted by that test so a later
// consistency pass has to argue with them rather than delete them:
//
//   - Variants (widest rung "←→ profile · ↑↓ count") — the one field where this footer
//     is wrong. There ↑↓ steps a count and ←→ moves between profiles, so the hint
//     corrects the footer rather than restating it.
//   - Model ("type a name") — a custom-entry affordance. The chips are not the whole
//     input surface for that field, and nothing else says so.
//
// Account keeps a pool gloss ("⇄ rotates the pool" / "pins this member") for the same
// reason: it names a distinction the footer cannot.
//
// The ladder puts that rule under a width: if a rung dropped "↑↓" to save cells, the
// footer would stop owning the nav keys on exactly the narrow terminal where the
// per-field hints are least affordable, and #466's guard — which renders at one wide
// size — could not see it. So every rung names "↑↓" exactly once, and that is what
// TestCreateFormHelp_EveryRungKeepsTheNavKeys pins — with
// TestPromptFocusHelp_NoRungNamesTheNavKeys holding the mirror image for the prompt,
// whose count under that rule is zero at every width.
var createFormHelp = []string{
	"Tab complete/move · ↑↓ select · ↵ create from name · ⌃S create",
	"Tab complete/move · ↑↓ select · ⌃S create",
	"Tab/↑↓ move · ⌃S create",
}

var promptFocusHelp = []string{
	"⇧↵ / ⌃J newline · ↵ next field · ⌃S create",
	"⌃J newline · ↵ next field · ⌃S create",
	"⌃J newline · ⌃S create",
}

// promptFocusHelpLegacy is promptFocusHelp without its ⇧↵ rung — the ladder for a
// terminal that never told Atrium it disambiguates modified keys, where Shift+Enter
// is byte-identical to Enter and ⌃J is the only newline key Atrium can promise.
//
// Derived from promptFocusHelp rather than re-authored, so an edit to a shared rung
// cannot land on one ladder and miss the other, and so the two can never disagree
// about anything except the clause that is actually in question. That the honest
// ladder is exactly the tail of the optimistic one is not a coincidence — it is why
// the ⇧↵ clause was put on its own rung.
var promptFocusHelpLegacy = promptFocusHelp[1:]

// promptHelpFor picks the ladder the terminal has earned. Read the capability, not
// the keystroke: the handler for Shift+Enter is unconditional (it costs nothing on a
// terminal that never sends it), and only what Atrium PROMISES has to be gated.
//
// It understates on two terminals, deliberately. An xterm honouring modifyOtherKeys
// delivers shift+enter without answering the kitty query, as does tmux configured
// with `extended-keys always`; both get the ⌃J ladder while ⇧↵ quietly works. That
// is the safe direction to be wrong in — a user who tries the key and finds it works
// has lost nothing, which is the opposite of the failure this ladder exists to fix.
// Inferring support from an arriving shift+enter would fix it at the cost of a footer
// that changes after the user already found the key, and of two identical terminals
// rendering different frames.
func promptHelpFor(disambiguates bool) []string {
	if disambiguates {
		return promptFocusHelp
	}
	return promptFocusHelpLegacy
}

// quickSendHelp / quickSendHelpLegacy are the same choice for the quick-send,
// smart-dispatch and diff-comment box, whose footer is one line rather than a width
// ladder. Same clause, same gate, same asymmetry.
const (
	quickSendHelp       = "↵ send · ⇧↵ / ⌃J newline · esc cancel"
	quickSendHelpLegacy = "↵ send · ⌃J newline · esc cancel"
)

// quickSendHelpFor is promptHelpFor for that footer.
func quickSendHelpFor(disambiguates bool) string {
	if disambiguates {
		return quickSendHelp
	}
	return quickSendHelpLegacy
}

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
	content, innerWidth, divider := t.compose()
	return t.fitOverlay(content, innerWidth, divider)
}

// compose assembles the overlay's inner content at the current width, and is
// split out of Render for one reason: it is the only place a test can see the
// lines the form *meant* to draw. fitOverlay runs after it and truncates
// silently, so a measurement taken from Render() can never distinguish copy that
// fits from copy that was cut to fit — which is how three overflows shipped (see
// TestCreateForm_ComposesWithinInnerWidth).
func (t *TextInputOverlay) compose() (content string, innerWidth int, divider string) {
	// Inner content width (accounting for padding and borders)
	innerWidth = t.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Set component widths to fit within the overlay. The title input's width is
	// owned by renderCreateForm, which carves the verdict suffix out of it.
	t.textarea.SetWidth(innerWidth)

	// Build a horizontal divider line
	divider = tiDividerStyle().Render(strings.Repeat("─", innerWidth))

	if t.isCreateForm {
		return t.renderCreateForm(divider), innerWidth, divider
	}

	// Everything that is not the create form — quick send, the diff-comment composer,
	// smart dispatch: no pickers, just a title, the textarea, and the submit button.
	content += tiTitleStyle().Render(t.Title) + "\n"
	content += t.textarea.View() + "\n\n"
	content += divider + "\n\n"
	if t.submitOnEnter {
		// Mirror the create form's newline vocabulary, and its honesty about it: ⇧↵
		// only where the terminal disambiguates modified keys, the universal ⌃J
		// everywhere — see quickSendHelpFor and newTextarea.
		content += tiHintStyle().Render(quickSendHelpFor(keys.TerminalDisambiguates())) + "\n"
	}
	content += t.renderEnterButton()

	return content, innerWidth, divider
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
	t.titleInput.SetWidth(inputWidth)
	section(titleLabel + "  " + t.titleInput.View() + suffix)
	section(tiLabelStyle().Render("Prompt") + "\n" + t.textarea.View())
	if t.variantPicker != nil {
		section(t.variantPicker.Render())
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
		help = promptHelpFor(keys.TerminalDisambiguates())
	}
	clearHint := "⌃R clear"
	if t.clearArmed {
		clearHint = "⌃R again"
	}
	// The rungs are laddered composed — with the clear hint already on the end —
	// because that suffix is what pushes each of them over, and both its spellings
	// are the same width, so the choice cannot flip as the clear arms.
	rungs := make([]string, len(help))
	for i, h := range help {
		rungs[i] = h + " · " + clearHint
	}
	b.WriteString(tiHintStyle().Render(fitHint(lipgloss.Width(divider), "", rungs...)) + "\n")
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
