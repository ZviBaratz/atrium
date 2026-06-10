package overlay

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// tallContent builds n distinct single-width lines ("line-0" … "line-n-1").
func tallContent(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}
	return strings.Join(lines, "\n")
}

func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// An overlay taller than the terminal must cap its box at the terminal height
// and show the first content line (not clip the top).
func TestTextOverlayCapsHeight(t *testing.T) {
	o := NewTextOverlay(tallContent(60))
	o.SetSize(80, 20)

	out := o.Render()
	if h := lipgloss.Height(out); h > 20 {
		t.Fatalf("rendered box is %d lines, want <= terminal height 20", h)
	}
	plain := xansi.Strip(out)
	if !strings.Contains(plain, "line-0") {
		t.Fatalf("first content line missing from initial render:\n%s", plain)
	}
	if strings.Contains(plain, "line-59") {
		t.Fatalf("last content line visible without scrolling; the window is not capping:\n%s", plain)
	}
}

// Scrolling down must reveal the bottom of the content, and further presses at
// the end must be clamped no-ops.
func TestTextOverlayScrollRevealsBottom(t *testing.T) {
	o := NewTextOverlay(tallContent(60))
	o.SetSize(80, 20)

	for i := 0; i < 100; i++ {
		if o.HandleKeyPress(key("down")) {
			t.Fatalf("down press %d dismissed a scrollable overlay", i)
		}
	}
	plain := xansi.Strip(o.Render())
	if !strings.Contains(plain, "line-59") {
		t.Fatalf("last content line still hidden after scrolling to the end:\n%s", plain)
	}

	before := o.Render()
	if o.HandleKeyPress(key("down")) {
		t.Fatal("down at the end dismissed the overlay")
	}
	if o.Render() != before {
		t.Fatal("down at the end moved the window; want a clamped no-op")
	}
}

// While the content overflows, navigation keys scroll (and never dismiss);
// any other key dismisses.
func TestTextOverlayScrollKeySemantics(t *testing.T) {
	for _, k := range []string{"up", "down", "k", "j", "pgup", "pgdown"} {
		o := NewTextOverlay(tallContent(60))
		o.SetSize(80, 20)
		if o.HandleKeyPress(key(k)) || o.Dismissed {
			t.Fatalf("%q dismissed a scrollable overlay; want it to scroll", k)
		}
	}

	for _, k := range []string{"x", "enter", "esc", "?"} {
		o := NewTextOverlay(tallContent(60))
		o.SetSize(80, 20)
		dismissed := false
		o.OnDismiss = func() { dismissed = true }
		if !o.HandleKeyPress(key(k)) || !o.Dismissed || !dismissed {
			t.Fatalf("%q did not dismiss a scrollable overlay", k)
		}
	}
}

// When the content fits, every key — including the scroll keys — closes the
// overlay (the welcome screen and info modals rely on this).
func TestTextOverlayFittingContentClosesOnAnyKey(t *testing.T) {
	for _, k := range []string{"down", "j", "x"} {
		o := NewTextOverlay("short\ncontent")
		o.SetSize(80, 50)
		if !o.HandleKeyPress(key(k)) || !o.Dismissed {
			t.Fatalf("%q did not dismiss a fitting overlay", k)
		}
	}
}

// The footer pins the dismiss hint when the content fits and swaps to the
// scroll hint when it overflows; without a hint a fitting overlay has no footer.
func TestTextOverlayFooter(t *testing.T) {
	o := NewTextOverlay(tallContent(60))
	o.SetSize(80, 20)
	if plain := xansi.Strip(o.Render()); !strings.Contains(plain, "scroll") {
		t.Fatalf("scrollable overlay is missing the scroll hint:\n%s", plain)
	}

	o = NewTextOverlay("short\ncontent")
	o.SetHint("press any key to close")
	o.SetSize(80, 50)
	plain := xansi.Strip(o.Render())
	if got := strings.Count(plain, "press any key to close"); got != 1 {
		t.Fatalf("fitting overlay shows the hint %d times, want exactly once:\n%s", got, plain)
	}

	withHint := lipgloss.Height(plain)
	o = NewTextOverlay("short\ncontent")
	o.SetSize(80, 50)
	if h := lipgloss.Height(o.Render()); h != withHint-2 {
		t.Fatalf("hintless overlay is %d lines, want %d (no footer rows)", h, withHint-2)
	}
}

// ScrollBy (the mouse-wheel path) moves the window with clamping at both ends
// and is a no-op when the content fits.
func TestTextOverlayScrollBy(t *testing.T) {
	o := NewTextOverlay(tallContent(60))
	o.SetSize(80, 20)

	o.ScrollBy(-5) // clamped at the top
	top := xansi.Strip(o.Render())
	if !strings.Contains(top, "line-0") {
		t.Fatal("scroll up at the top moved the window")
	}
	o.ScrollBy(1000) // clamped at the end
	if plain := xansi.Strip(o.Render()); !strings.Contains(plain, "line-59") {
		t.Fatal("large scroll down did not clamp to the end of the content")
	}

	fits := NewTextOverlay("short")
	fits.SetSize(80, 40)
	beforeFit := fits.Render()
	fits.ScrollBy(3)
	if fits.Render() != beforeFit || fits.Dismissed {
		t.Fatal("ScrollBy on fitting content must be a no-op")
	}
}

// Dismiss fires OnDismiss exactly once, however many paths call it (key press
// then click outside).
func TestTextOverlayDismissIdempotent(t *testing.T) {
	o := NewTextOverlay("short")
	calls := 0
	o.OnDismiss = func() { calls++ }
	o.HandleKeyPress(key("x"))
	o.Dismiss()
	if calls != 1 {
		t.Fatalf("OnDismiss fired %d times, want exactly once", calls)
	}
}

// An overlay that was never sized renders at natural size (no windowing) and
// closes on any key — the pre-first-WindowSizeMsg behavior.
func TestTextOverlayUnsized(t *testing.T) {
	o := NewTextOverlay(tallContent(60))
	plain := xansi.Strip(o.Render())
	if !strings.Contains(plain, "line-0") || !strings.Contains(plain, "line-59") {
		t.Fatalf("unsized overlay should render all content:\n%s", plain)
	}
	if !o.HandleKeyPress(key("down")) {
		t.Fatal("down did not dismiss an unsized overlay")
	}
}
