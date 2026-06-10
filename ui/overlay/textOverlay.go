package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// Vertical chrome around the body: border (2) + Padding(1, 2)'s vertical part (2).
const textOverlayVChrome = 4

// TextOverlay represents a text screen overlay
type TextOverlay struct {
	// Whether the overlay has been dismissed
	Dismissed bool
	// Callback function to be called when the overlay is dismissed
	OnDismiss func()
	// Content to display in the overlay
	content string
	// Footer hint ("press any key to close"); replaced by the scroll hint when
	// the content overflows the terminal. Empty means no footer.
	hint string

	// Full terminal size; the overlay caps its own box within it. Zero means
	// never sized (no resize seen yet): render at natural size, no scrolling.
	width, height int
	// First visible wrapped-content line when scrolling; clamped on render.
	scroll int
}

// NewTextOverlay creates a new text screen overlay with the given title and content
func NewTextOverlay(content string) *TextOverlay {
	return &TextOverlay{
		Dismissed: false,
		content:   content,
	}
}

// HandleKeyPress processes a key press and updates the state
// Returns true if the overlay should be closed
func (t *TextOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	// While the content overflows, navigation keys scroll instead of closing.
	if t.maxScroll() > 0 {
		_, budget, _ := t.window()
		scrolled := true
		switch msg.String() {
		case "up", "k":
			t.scroll--
		case "down", "j":
			t.scroll++
		case "pgup":
			t.scroll -= budget
		case "pgdown":
			t.scroll += budget
		default:
			scrolled = false
		}
		if scrolled {
			t.scroll = clamp(t.scroll, 0, t.maxScroll())
			return false
		}
	}

	// Close on any key
	t.Dismiss()
	return true
}

// Dismiss marks the overlay dismissed and fires the OnDismiss callback once,
// no matter how the dismissal was triggered (key press or click outside).
func (t *TextOverlay) Dismiss() {
	if t.Dismissed {
		return
	}
	t.Dismissed = true
	if t.OnDismiss != nil {
		t.OnDismiss()
	}
}

// ScrollBy moves the scroll window by delta lines, clamped to the content;
// a no-op when the content fits.
func (t *TextOverlay) ScrollBy(delta int) {
	t.scroll = clamp(t.scroll+delta, 0, t.maxScroll())
}

// Render renders the text overlay
func (t *TextOverlay) Render(opts ...WhitespaceOption) string {
	lines, budget, scrollable := t.window()

	body := lines
	var footer []string
	if scrollable {
		t.scroll = clamp(t.scroll, 0, t.maxScroll())
		body = lines[t.scroll : t.scroll+budget]
		footer = []string{"", t.footerHint(scrollHint(t.scroll+budget, len(lines)))}
	} else if t.hint != "" {
		footer = []string{"", t.footerHint(t.hint)}
	}

	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2).
		Width(t.boxWidth())

	return style.Render(strings.Join(append(body, footer...), "\n"))
}

// SetSize records the full terminal size. The overlay hugs its content width
// and windows its lines to fit short terminals.
func (t *TextOverlay) SetSize(width, height int) {
	t.width = width
	t.height = height
	t.scroll = clamp(t.scroll, 0, t.maxScroll())
}

// SetHint sets the footer hint shown below the content.
func (t *TextOverlay) SetHint(hint string) {
	t.hint = hint
}

// footerHint styles a footer line, truncated to the inner width so a long hint
// on a narrow terminal can't wrap and grow the footer past its two rows.
func (t *TextOverlay) footerHint(hint string) string {
	if inner := t.boxWidth() - 4; inner > 0 {
		hint = xansi.Truncate(hint, inner, "…")
	}
	return theme.Current().OverlayHintStyle().Render(hint)
}

// scrollHint formats the footer shown while the content overflows.
func scrollHint(through, total int) string {
	return fmt.Sprintf("↑/↓ scroll (%d/%d) · any other key closes", through, total)
}

// boxWidth returns the lipgloss style width (padding-inclusive, border-exclusive):
// the natural width of the content and its footer hint, capped so the box plus
// its border keeps a one-column margin inside the terminal. Zero (natural size)
// when never sized.
func (t *TextOverlay) boxWidth() int {
	lines, natural := getLines(t.content)
	natural = max(natural, xansi.StringWidth(t.hint))
	if t.height > 0 && len(lines)+2 > t.height-textOverlayVChrome {
		// The content will (or may, once wrapped) scroll: leave room for the
		// scroll hint at its widest, the max-scroll position readout.
		natural = max(natural, xansi.StringWidth(scrollHint(len(lines), len(lines))))
	}
	w := natural + 4 // Padding(1, 2): two columns each side
	if t.width > 0 {
		w = min(w, t.width-4) // border (2) + margin (1) per side
	}
	return max(w, 0)
}

// wrappedLines returns the content wrapped (ANSI-aware) to the box's inner width.
func (t *TextOverlay) wrappedLines() []string {
	inner := t.boxWidth() - 4
	if inner <= 0 {
		return strings.Split(t.content, "\n")
	}
	return strings.Split(lipgloss.NewStyle().Width(inner).Render(t.content), "\n")
}

// window returns the wrapped content lines, how many of them are visible at
// once, and whether the content overflows the terminal (i.e. scrolling is on).
// The footer always claims two rows while scrolling (spacer + hint), and the
// same two when a fitting overlay carries a hint.
func (t *TextOverlay) window() (lines []string, budget int, scrollable bool) {
	lines = t.wrappedLines()
	if t.height <= 0 {
		return lines, len(lines), false
	}
	inner := t.height - textOverlayVChrome
	footerRows := 0
	if t.hint != "" {
		footerRows = 2
	}
	if len(lines)+footerRows <= inner {
		return lines, len(lines), false
	}
	return lines, max(1, inner-2), true
}

// maxScroll returns the largest valid scroll offset (0 when the content fits).
func (t *TextOverlay) maxScroll() int {
	lines, budget, scrollable := t.window()
	if !scrollable {
		return 0
	}
	return len(lines) - budget
}
