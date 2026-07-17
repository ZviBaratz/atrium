package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	tea "github.com/charmbracelet/bubbletea"
)

func detectedFixture() []config.Profile {
	return []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
		{Name: "aider", Program: "aider"},
	}
}

func TestWelcomeOverlay_DetectingThenPick(t *testing.T) {
	w := NewWelcomeOverlay()
	w.SetWidth(54)

	// Before detection resolves, Enter/nav must not close or confirm.
	if w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) {
		t.Fatal("nav during detecting should not close")
	}
	if !strings.Contains(w.Render(), "Detecting") {
		t.Errorf("detecting state should render a Detecting… line, got:\n%s", w.Render())
	}

	w.SetDetected(detectedFixture())

	// First profile (registry order → claude) is selected by default.
	if got := w.SelectedProfile().Name; got != "claude" {
		t.Errorf("default selection = %q, want \"claude\"", got)
	}
	// Down moves selection to codex.
	w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	if got := w.SelectedProfile().Name; got != "codex" {
		t.Errorf("after Down, selection = %q, want \"codex\"", got)
	}
	// Enter confirms and closes.
	if !w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter should close the overlay")
	}
	if !w.Confirmed() {
		t.Error("Enter should mark the overlay confirmed")
	}
	if len(w.Detected()) != 3 {
		t.Errorf("Detected() = %d profiles, want 3", len(w.Detected()))
	}
}

func TestWelcomeOverlay_SkipDoesNotConfirm(t *testing.T) {
	w := NewWelcomeOverlay()
	w.SetDetected(detectedFixture())
	if !w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) {
		t.Fatal("Esc should close the overlay")
	}
	if w.Confirmed() {
		t.Error("Esc must not confirm")
	}
}

// ctrl+c closes the welcome as a skip (not a confirm), matching the app's
// overlay-cancel idiom, so a first-run user's reflexive quit is not swallowed.
func TestWelcomeOverlay_CtrlCSkips(t *testing.T) {
	w := NewWelcomeOverlay()
	w.SetDetected(detectedFixture())
	if !w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Fatal("ctrl+c should close the overlay")
	}
	if w.Confirmed() {
		t.Error("ctrl+c must not confirm (it skips)")
	}
}

// normalizeWS collapses every run of whitespace down to single spaces.
func normalizeWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// flowText strips ANSI and the modal's box-drawing border, then collapses
// whitespace — so the renderer's wrapped lines rejoin into one flowing string.
// A word split across a wrap boundary would surface as two tokens and break a
// Contains of the canonical sentence; a clean word-wrap leaves it intact.
func flowText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 0x2500 && r <= 0x257F { // box-drawing block (border runes)
			return ' '
		}
		return r
	}, stripANSI(s))
	return normalizeWS(s)
}

// The intro is wrapped by the renderer to the modal's content width, never
// hard-broken, so it never breaks mid-phrase (#381) — checked across the widths
// the live drive used (140→54 clamp, 80) plus narrower clamps.
func TestWelcomeOverlay_IntroNeverBreaksMidWord(t *testing.T) {
	for _, width := range []int{28, 40, 50, 54} {
		w := NewWelcomeOverlay()
		w.SetWidth(width)
		w.SetDetected(detectedFixture())
		if got := flowText(w.Render()); !strings.Contains(got, normalizeWS(welcomeIntro)) {
			t.Errorf("width %d: intro broke mid-word.\n got: %s\nwant substring: %s",
				width, got, normalizeWS(welcomeIntro))
		}
	}
}

// The detected-count line pluralizes properly (#381): one agent is singular, two
// or more plural, and the lazy "agent(s)" is gone.
func TestWelcomeOverlay_Pluralization(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{1, "1 agent detected"},
		{2, "2 agents detected"},
		{3, "3 agents detected"},
	} {
		w := NewWelcomeOverlay()
		w.SetWidth(54)
		w.SetDetected(detectedFixture()[:tc.n])
		out := stripANSI(w.Render())
		if !strings.Contains(out, tc.want) {
			t.Errorf("n=%d: render missing %q\n%s", tc.n, tc.want, out)
		}
		if strings.Contains(out, "agent(s)") {
			t.Errorf("n=%d: render still contains lazy 'agent(s)'", tc.n)
		}
	}
}

// contentWidth subtracts the border+padding chrome and floors at 1 so a
// degenerate modal width can never hand lipgloss a zero/negative wrap width.
func TestWelcomeOverlay_ContentWidthFloor(t *testing.T) {
	w := NewWelcomeOverlay()
	for _, tc := range []struct{ width, want int }{
		{54, 50}, {24, 20}, {5, 1}, {4, 1}, {0, 1},
	} {
		w.SetWidth(tc.width)
		if got := w.contentWidth(); got != tc.want {
			t.Errorf("contentWidth(width=%d) = %d, want %d", tc.width, got, tc.want)
		}
	}
	// Render must not panic at a degenerate width.
	w.SetWidth(3)
	w.SetDetected(detectedFixture())
	_ = w.Render()
}

func TestWelcomeOverlay_EmptyDetection(t *testing.T) {
	w := NewWelcomeOverlay()
	w.SetWidth(54)
	w.SetDetected(nil)

	if got := w.SelectedProfile().Program; got != "" {
		t.Errorf("empty detection SelectedProfile().Program = %q, want \"\"", got)
	}
	out := w.Render()
	if !strings.Contains(out, "No supported agent") {
		t.Errorf("empty-detection render should warn about no agents, got:\n%s", out)
	}
	// Enter/Esc both close; Enter acknowledges (Confirmed true) but has no program.
	if !w.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter should close even with no agents")
	}
}
