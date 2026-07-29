package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// RenameOverlay is a lightweight two-field dialog: a session's cosmetic display
// label and its freeform note. Tab/shift+tab move focus between the two; ctrl+d
// toggles whether submitting the name also renames the underlying git branch,
// worktree, and tmux session (deep) or only the cosmetic label.
type RenameOverlay struct {
	name      textinput.Model
	note      textinput.Model
	focusNote bool // which field has focus (false = name, true = note)
	submitted bool
	canceled  bool
	width     int
	deep      bool
}

// NewRenameOverlay creates the dialog pre-filled with the current label and note.
// focusNote starts the cursor on the note field (used by the pause flow, where the
// point is to jot why the session is being parked); otherwise the name is focused.
func NewRenameOverlay(currentLabel, currentNote string, focusNote bool) *RenameOverlay {
	name := newTitleInput()
	name.SetValue(currentLabel)

	note := newTitleInput()
	note.CharLimit = 80
	note.Placeholder = "note (optional) — e.g. blocked on review"
	note.SetValue(currentNote)

	o := &RenameOverlay{name: name, note: note, focusNote: focusNote, deep: false}
	// Through SetWidth, not by setting the field: the inputs have to be sized with
	// the box (bubbles v2 renders an unsized field as one character).
	o.SetWidth(50)
	o.applyFocus()
	return o
}

// applyFocus focuses exactly one input and blurs the other, leaving the cursor at
// the end of the focused field.
func (r *RenameOverlay) applyFocus() {
	if r.focusNote {
		r.name.Blur()
		r.note.Focus()
		r.note.CursorEnd()
	} else {
		r.note.Blur()
		r.name.Focus()
		r.name.CursorEnd()
	}
}

// HandleKeyPress processes a key press and returns true if the overlay should close.
// enter submits, esc/ctrl+c cancel, tab/shift+tab switch field, ctrl+d toggles deep,
// everything else edits the focused field.
func (r *RenameOverlay) HandleKeyPress(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter":
		r.submitted = true
		return true
	case "esc", "ctrl+c":
		r.canceled = true
		return true
	case "tab", "shift+tab":
		r.focusNote = !r.focusNote
		r.applyFocus()
		return false
	case "ctrl+d":
		r.deep = !r.deep
		return false
	default:
		if r.focusNote {
			r.note, _ = r.note.Update(msg)
		} else {
			r.name, _ = r.name.Update(msg)
		}
		return false
	}
}

// IsDeep reports whether the user chose a deep rename (branch + worktree + tmux).
func (r *RenameOverlay) IsDeep() bool { return r.deep }

// Value returns the trimmed display label.
func (r *RenameOverlay) Value() string { return strings.TrimSpace(r.name.Value()) }

// NoteValue returns the trimmed note ("" clears it).
func (r *RenameOverlay) NoteValue() string { return strings.TrimSpace(r.note.Value()) }

// IsSubmitted reports whether the user accepted the dialog.
func (r *RenameOverlay) IsSubmitted() bool { return r.submitted }

// IsCanceled reports whether the user dismissed the dialog.
func (r *RenameOverlay) IsCanceled() bool { return r.canceled }

// SetWidth sets the width of the overlay, and sizes the fields inside it.
//
// Sizing the inputs is not optional under bubbles v2. v1 treated a zero Width as
// "unbounded" and rendered the whole value and placeholder; v2's placeholder view
// builds a rune buffer of exactly Width()+1, so an unsized field shows a single
// character — which is what the frame goldens caught here as a placeholder
// rendered as "n".
//
// The inner width is the box minus its padding (2 each side) and border (1 each
// side); Render adds the border back onto width (see the note there).
func (r *RenameOverlay) SetWidth(width int) {
	r.width = width
	inner := max(1, width-4)
	r.name.SetWidth(inner)
	r.note.SetWidth(inner)
}

// Render renders the overlay as a bordered box.
func (r *RenameOverlay) Render() string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2).
		// +2 for the left/right border — v2 counts it inside Width. See theme.Panel.
		Width(r.width + 2)

	deepMark, labelMark := "○", "○"
	if r.deep {
		deepMark = "●"
	} else {
		labelMark = "●"
	}
	dim := theme.Current().DimStyle()
	nameLabel := dim.Render("name")
	noteLabel := dim.Render("note")
	mode := dim.Render("mode: " + labelMark + " label only\n      " + deepMark + " deep (branch + worktree)")
	hint := theme.Current().OverlayHintStyle().Render("tab switch field · ctrl+d deep · enter save · esc cancel")
	title := theme.Current().OverlayTitleStyle().Render("Rename session")
	content := title + "\n\n" +
		nameLabel + "\n" + r.name.View() + "\n\n" +
		noteLabel + "\n" + r.note.View() + "\n\n" +
		mode + "\n\n" + hint
	return style.Render(content)
}
