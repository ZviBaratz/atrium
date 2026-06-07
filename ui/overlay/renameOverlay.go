package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RenameOverlay is a minimal single-line dialog for editing a session's cosmetic display
// label. It mirrors ConfirmationOverlay's lightweight shape rather than the heavier
// create-form TextInputOverlay, since renaming only needs one short field.
type RenameOverlay struct {
	input     textinput.Model
	submitted bool
	canceled  bool
	width     int
	// deep selects whether submitting renames the underlying git branch, worktree, and
	// tmux session (true) or only the cosmetic display label (false). Tab toggles it.
	deep bool
}

// NewRenameOverlay creates a rename dialog pre-filled with the instance's current label,
// reusing the shared single-line title input (32-char cap, matching the new-session form).
func NewRenameOverlay(currentLabel string) *RenameOverlay {
	in := newTitleInput()
	in.SetValue(currentLabel)
	in.Focus()
	in.CursorEnd()
	return &RenameOverlay{
		input: in,
		width: 50,
		// Default to the safe, non-destructive label-only rename (the historical R
		// behavior). A deep rename mutates git refs, the worktree dir, and a live tmux
		// session, so it should be a deliberate opt-in via Tab rather than the reflex.
		deep: false,
	}
}

// HandleKeyPress processes a key press and returns true if the overlay should be closed.
// enter submits, esc/ctrl+c cancel, tab toggles deep/label-only, everything else edits.
func (r *RenameOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "enter":
		r.submitted = true
		return true
	case "esc", "ctrl+c":
		r.canceled = true
		return true
	case "tab":
		r.deep = !r.deep
		return false
	default:
		r.input, _ = r.input.Update(msg)
		return false
	}
}

// IsDeep reports whether the user chose a deep rename (branch + worktree + tmux session)
// rather than a cosmetic label-only change.
func (r *RenameOverlay) IsDeep() bool { return r.deep }

// Value returns the trimmed label the user entered.
func (r *RenameOverlay) Value() string {
	return strings.TrimSpace(r.input.Value())
}

// IsSubmitted reports whether the user accepted the new label.
func (r *RenameOverlay) IsSubmitted() bool { return r.submitted }

// IsCanceled reports whether the user dismissed the dialog without renaming.
func (r *RenameOverlay) IsCanceled() bool { return r.canceled }

// SetWidth sets the width of the overlay.
func (r *RenameOverlay) SetWidth(width int) { r.width = width }

// Render renders the rename overlay as a bordered box.
func (r *RenameOverlay) Render() string {
	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2).
		Width(r.width)

	deepMark, labelMark := "○", "○"
	if r.deep {
		deepMark = "●"
	} else {
		labelMark = "●"
	}
	// List the default (label only) first so the leading option is the one that's selected
	// on open; deep rename is the deliberate opt-in below it.
	mode := theme.Current().DimStyle().Render(
		"mode: " + labelMark + " label only\n      " + deepMark + " deep (branch + worktree)")
	hint := theme.Current().OverlayHintStyle().Render("tab toggle · enter save · esc cancel")
	title := theme.Current().OverlayTitleStyle().Render("Rename session")
	content := title + "\n\n" + r.input.View() + "\n\n" + mode + "\n\n" + hint
	return style.Render(content)
}
