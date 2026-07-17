package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PromptHistoryOverlay lists previously-submitted prompts (most-recent-first) and
// lets the user pick one to reuse. Like QueueOverlay it is a dumb view: the app
// pushes the history texts in via SetItems and reads intent back out (Selected /
// SelectedText / ClearRequested), doing the state mutation itself. Picking a
// prompt INSERTS it into the field being composed — it never submits.
type PromptHistoryOverlay struct {
	items    []string // most-recent-first prompt texts
	cursor   int
	width    int
	selected bool // enter was pressed on a row
	clearReq bool // a clear-all was armed
}

// NewPromptHistoryOverlay builds the picker over the given most-recent-first
// prompt texts. The app widens it responsively via SetWidth.
func NewPromptHistoryOverlay(items []string) *PromptHistoryOverlay {
	return &PromptHistoryOverlay{items: items, width: 60}
}

// SetItems replaces the displayed history (e.g. after a clear) and clamps the
// cursor into range.
func (p *PromptHistoryOverlay) SetItems(items []string) {
	p.items = items
	if p.cursor >= len(p.items) {
		p.cursor = len(p.items) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// HandleKeyPress moves the cursor, selects, arms a clear, or closes. It returns
// true only when the overlay should close (esc/ctrl+c, or enter on a row). A
// clear (x) arms clearReq and keeps the overlay open so the app can empty the
// history and refresh.
func (p *PromptHistoryOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		return false
	case "down", "j":
		if p.cursor < len(p.items)-1 {
			p.cursor++
		}
		return false
	case "enter":
		if len(p.items) > 0 {
			p.selected = true
			return true
		}
		return false
	case "x":
		if len(p.items) > 0 {
			p.clearReq = true
		}
		return false
	default:
		return false
	}
}

// Selected reports whether the overlay closed by picking a row (enter) rather
// than cancelling (esc).
func (p *PromptHistoryOverlay) Selected() bool { return p.selected }

// SelectedText is the prompt under the cursor, or "" when the history is empty.
func (p *PromptHistoryOverlay) SelectedText() string {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return ""
	}
	return p.items[p.cursor]
}

// ClearRequested reports whether a clear-all was armed since the last call and
// clears the flag (read-once), so the app acts on each press exactly once.
func (p *PromptHistoryOverlay) ClearRequested() bool {
	r := p.clearReq
	p.clearReq = false
	return r
}

// SetWidth sets the box width, flooring it so the box never collapses.
func (p *PromptHistoryOverlay) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	p.width = width
}

// Render draws the bordered list.
func (p *PromptHistoryOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(p.width)

	inner := p.width - 6 // borders (2) + horizontal padding (2*2)
	if inner < 10 {
		inner = 10
	}

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render("Prompt history") + "\n\n")

	if len(p.items) == 0 {
		b.WriteString(overlayDimStyle().Render("no prompts yet") + "\n\n")
	} else {
		for idx, text := range p.items {
			bw := inner - 4 // room for the "▸ " cursor
			if bw < 1 {
				bw = 1
			}
			row := queueFirstLine(text, bw)
			if idx == p.cursor {
				b.WriteString(overlaySelectedStyle().Render("▸ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(th.OverlayHintStyle().Render("j/k move · ↵ insert · x clear · esc cancel"))
	return box.Render(b.String())
}
