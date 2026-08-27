package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// WelcomeOverlay is the interactive first-run modal: it greets the user, lets
// them pick a default agent from the ones detected on their PATH, and warns when
// none are found. It follows the same local-bordered-box idiom as the
// confirmation and rename overlays (fixed width, centered by PlaceOverlay).
type WelcomeOverlay struct {
	detecting bool
	detected  []config.Profile
	picker    *ProfilePicker
	confirmed bool
	width     int
}

// welcomeIntro is the one-paragraph pitch shown under the title. It is authored
// as a single flowing sentence and wrapped by the renderer to the modal's
// content width: hard newlines baked into a fixed-width string get re-wrapped a
// second time by lipgloss inside the narrower padded box, and the two wraps
// fight — that is what produced the mid-phrase breaks reported in #381.
const welcomeIntro = "Run multiple coding agents in parallel — each in its own git worktree and tmux session, managed from one place."

// NewWelcomeOverlay creates the overlay in its "detecting" state; the caller
// fills it in with SetDetected once agent detection resolves.
func NewWelcomeOverlay() *WelcomeOverlay {
	return &WelcomeOverlay{detecting: true, width: 56}
}

// contentWidth is the usable text width inside the modal's border (one column
// per side) and horizontal padding (Padding(1, 2) eats two columns per side).
// The intro paragraph and the picker are both sized to it so nothing spills
// past the box.
func (w *WelcomeOverlay) contentWidth() int {
	if cw := w.width - 6; cw > 0 {
		return cw
	}
	return 1
}

// SetDetected leaves the detecting state and installs a picker over the detected
// agents. An empty slice renders the no-agents guidance instead of a picker.
func (w *WelcomeOverlay) SetDetected(detected []config.Profile) {
	w.detecting = false
	w.detected = detected
	if len(detected) > 0 {
		w.picker = NewProfilePicker(detected)
		w.picker.Focus()
		w.picker.SetWidth(w.contentWidth())
	}
}

// WelcomeSize is the confirmation dialog's idiom with a little more room for
// the welcome's copy: keep the authored width on normal terminals, shrink so
// the box never spills off a narrow one.
var WelcomeSize = SizeSpec{WFrac: 1, WExtra: -2, WMax: 56, WMin: 22}

// SetSize sets the modal's TOTAL width, border and padding included — outer
// cells, which is what lipgloss v2's Width means (see theme.Panel). The
// height is accepted and ignored so the resize walk can hand every overlay
// the same call: the modal hugs its copy.
func (w *WelcomeOverlay) SetSize(width, _ int) {
	w.width = width
	if w.picker != nil {
		w.picker.SetWidth(w.contentWidth())
	}
}

// HandleKeyPress returns true when the overlay should close. Enter confirms
// (Confirmed() == true); Esc and ctrl+c skip (ctrl+c mirrors the app's
// overlay-cancel idiom, so a first-run quit reflex is not swallowed). While
// detecting, only the skip keys close.
func (w *WelcomeOverlay) HandleKeyPress(msg tea.KeyPressMsg) bool {
	if msg.Code == tea.KeyEsc || msg.String() == "ctrl+c" {
		return true
	}
	if w.detecting {
		return false
	}
	if msg.Code == tea.KeyEnter {
		w.confirmed = true
		return true
	}
	if w.picker != nil {
		w.picker.HandleKeyPress(msg)
	}
	return false
}

// Confirmed reports whether the overlay was closed by confirming (Enter).
func (w *WelcomeOverlay) Confirmed() bool { return w.confirmed }

// SelectedProfile is the chosen profile (Name + Program), or the zero Profile
// when there was no picker (empty detection). The caller persists its Name as
// the default program so resolution keeps flowing through the profile list.
func (w *WelcomeOverlay) SelectedProfile() config.Profile {
	if w.picker == nil {
		return config.Profile{}
	}
	return w.picker.GetSelectedProfile()
}

// Detected returns the profiles detection found (for the caller to merge on confirm).
func (w *WelcomeOverlay) Detected() []config.Profile { return w.detected }

// installableAgents renders the binaries detection probes as "a, b, c, d or e",
// derived from config.KnownAgentBins rather than written out: the zero-agents
// line is the one screen that tells a user what to install, and it named four of
// the five until #887. It is the only site here that needs the "or" spelling —
// the two help strings in main.go want a plain comma list — so the conjunction
// lives at the site that reads it rather than in a shared joiner.
//
// The line it feeds is wrapped by the modal's box, not truncated, so a sixth
// agent costs a row rather than a word — which is why
// TestWelcomeOverlay_ZeroAgentsLineNamesEveryBin reads the render at the authored
// width and pins the modal's height as well as the names.
func installableAgents() string {
	bins := config.KnownAgentBins()
	switch len(bins) {
	case 0:
		return "an agent CLI"
	case 1:
		return bins[0]
	}
	return strings.Join(bins[:len(bins)-1], ", ") + " or " + bins[len(bins)-1]
}

// Render draws the bordered welcome modal. The trailing hint line is the one place
// the modal names its keys — the profile picker above it deliberately carries none,
// since a second spelling of ↑/↓ two lines up is noise, not guidance (#466; the same
// rule the create form follows, stated at createFormHelp).
func (w *WelcomeOverlay) Render() string {
	var b strings.Builder
	b.WriteString(theme.Current().OverlayTitleStyle().Render("Welcome to Atrium"))
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Width(w.contentWidth()).Render(welcomeIntro))
	b.WriteString("\n\n")

	var hint string
	switch {
	case w.detecting:
		b.WriteString(overlayDimStyle().Render("Detecting installed agents…"))
		hint = "esc skip"
	case len(w.detected) == 0:
		b.WriteString("⚠ No supported agent CLIs found on PATH.\n")
		b.WriteString(overlayDimStyle().Render(fmt.Sprintf(
			"Install %s (or press %s later).",
			installableAgents(), keys.LabelOf(keys.KeySettings))))
		hint = "enter continue · esc skip"
	default:
		b.WriteString("Choose your default agent:\n\n")
		b.WriteString(w.picker.Render())
		b.WriteString("\n\n")
		noun := "agents"
		if len(w.detected) == 1 {
			noun = "agent"
		}
		b.WriteString(overlayDimStyle().Render(fmt.Sprintf("✓ %d %s detected on your PATH", len(w.detected), noun)))
		hint = "↑/↓ choose · enter confirm · esc skip"
	}

	b.WriteString("\n\n")
	b.WriteString(theme.Current().OverlayHintStyle().Render(hint))

	style := lipgloss.NewStyle().
		Border(theme.Current().Borders.Style).
		BorderForeground(theme.Current().Palette.Accent).
		Padding(1, 2).
		Width(w.width)
	return style.Render(b.String())
}
