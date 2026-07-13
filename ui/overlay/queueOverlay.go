package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// QueueOverlay lists a session's pending prompts (head first) and lets the user
// cancel one. It is a dumb view over primitives: the app pushes the queue
// snapshot in via SetQueue and reads the user's intent back out
// (RemoveRequested/SelectedIndex/SelectedText), performing the actual mutation on
// the session.Instance itself. It holds no session type.
type QueueOverlay struct {
	title        string   // the session's display name
	items        []string // head-first prompt texts
	cursor       int
	headInFlight bool
	message      string // transient in-overlay note (e.g. a cancel refusal); cleared by SetQueue
	width        int
	removeReq    bool
}

// queueInFlightMark rides the head row while its prompt is being delivered; a
// locked head cannot be cancelled (see Instance.CancelQueuedPrompt), so the mark
// doubles as the "why did d do nothing here" cue.
const queueInFlightMark = "⟳"

// NewQueueOverlay builds the overlay for a session with the given display name.
// Width defaults to a sensible box; the app widens it responsively via SetWidth.
func NewQueueOverlay(name string) *QueueOverlay {
	return &QueueOverlay{title: name, width: 60}
}

// SetQueue replaces the displayed queue, clamps the cursor into range, and clears
// the pending remove request and any transient message, so a refresh after an
// action starts clean.
func (q *QueueOverlay) SetQueue(texts []string, headInFlight bool) {
	q.items = texts
	q.headInFlight = headInFlight
	q.removeReq = false
	q.message = ""
	if q.cursor >= len(q.items) {
		q.cursor = len(q.items) - 1
	}
	if q.cursor < 0 {
		q.cursor = 0
	}
}

// SetMessage sets a transient note rendered above the footer hint (cleared by the
// next SetQueue).
func (q *QueueOverlay) SetMessage(text string) { q.message = text }

// HandleKeyPress moves the cursor, arms a cancel, or closes. It returns true only
// when the overlay should close (esc/ctrl+c); a cancel (d/x) arms removeReq and
// keeps the overlay open so the app can act and refresh.
func (q *QueueOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "up", "k":
		if q.cursor > 0 {
			q.cursor--
		}
		return false
	case "down", "j":
		if q.cursor < len(q.items)-1 {
			q.cursor++
		}
		return false
	case "d", "x":
		if len(q.items) > 0 {
			q.removeReq = true
		}
		return false
	default:
		return false
	}
}

// RemoveRequested reports whether a cancel was armed since the last call and
// clears the flag (read-once), so the app acts on each press exactly once.
func (q *QueueOverlay) RemoveRequested() bool {
	r := q.removeReq
	q.removeReq = false
	return r
}

// SelectedIndex is the 0-based cursor position (head = 0).
func (q *QueueOverlay) SelectedIndex() int { return q.cursor }

// SelectedText is the prompt under the cursor, or "" when the queue is empty.
func (q *QueueOverlay) SelectedText() string {
	if q.cursor < 0 || q.cursor >= len(q.items) {
		return ""
	}
	return q.items[q.cursor]
}

// HeadInFlight reports whether the displayed head prompt is being delivered — the
// app reads this to tell an in-flight-head cancel refusal apart from a stale one.
func (q *QueueOverlay) HeadInFlight() bool { return q.headInFlight }

// SetWidth sets the box width, flooring it so the box never collapses.
func (q *QueueOverlay) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	q.width = width
}

// queueFirstLine collapses a possibly multi-line prompt to its first line,
// truncated to fit width with an ellipsis tail.
func queueFirstLine(s string, width int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimRight(s[:i], " ") + " …"
	}
	return truncate.StringWithTail(s, uint(width), "…")
}

// Render draws the bordered list.
func (q *QueueOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(q.width)

	inner := q.width - 6 // borders (2) + horizontal padding (2*2)
	if inner < 10 {
		inner = 10
	}

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render(`Queue for "`+q.title+`"`) + "\n\n")

	if len(q.items) == 0 {
		b.WriteString(overlayDimStyle().Render("no pending prompts") + "\n\n")
	} else {
		for idx, text := range q.items {
			num := fmt.Sprintf("%d. ", idx+1)
			bw := inner - len(num) - 4 // room for the "▸ " cursor and a trailing mark
			if bw < 1 {
				bw = 1
			}
			row := num + queueFirstLine(text, bw)
			if idx == 0 && q.headInFlight {
				row += " " + queueInFlightMark
			}
			if idx == q.cursor {
				b.WriteString(overlaySelectedStyle().Render("▸ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if q.message != "" {
		b.WriteString(th.AttentionStyle().Render(q.message) + "\n\n")
	}
	b.WriteString(th.OverlayHintStyle().Render("j/k move · d cancel · esc close"))
	return box.Render(b.String())
}
