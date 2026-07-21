package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// variantCountMax caps how many sessions a single profile may contribute to one
// batch. It bounds runaway key-repeat and keeps the rendered row sane; the true
// whole-batch total (across profiles) is enforced app-side at submit.
const variantCountMax = 20

// VariantPicker is the create form's fan-out control (#387): a per-profile count
// stepper. Each configured profile carries a count, and one form submit spawns
// that many sessions of each — e.g. claude ×3, or claude ×1 + codex ×1 for a
// bake-off. It defaults to one session of the default profile (the first entry,
// which config.GetProfiles orders first), so an untouched form is identical to
// the pre-#387 single-session create. Distinct from ProfilePicker, which stays a
// single-select control for the first-run welcome modal.
type VariantPicker struct {
	profiles []config.Profile
	counts   []int // parallel to profiles: how many sessions of each to spawn
	cursor   int
	focused  bool
	width    int
	// errMsg is an inline validation message (batch too large / over max_sessions)
	// pushed in by the app on a rejected submit, rendered in the danger color right
	// under the total. Kept in the overlay body so it is visible with the form still
	// open — a toast is swallowed behind the modal. Cleared on any count change.
	errMsg string
}

// NewVariantPicker builds a variant picker over profiles, defaulting to one
// session of the first profile (the default program) and zero of the rest — so
// GetVariants yields exactly the pre-#387 single-session behavior until touched.
func NewVariantPicker(profiles []config.Profile) *VariantPicker {
	counts := make([]int, len(profiles))
	if len(profiles) > 0 {
		counts[0] = 1
	}
	return &VariantPicker{profiles: profiles, counts: counts}
}

// Focus gives the picker focus.
func (vp *VariantPicker) Focus() { vp.focused = true }

// Blur removes focus from the picker.
func (vp *VariantPicker) Blur() { vp.focused = false }

// SetWidth sets the rendering width.
func (vp *VariantPicker) SetWidth(w int) { vp.width = w }

// SetError sets (or, with "", clears) the inline batch-validation message.
func (vp *VariantPicker) SetError(msg string) { vp.errMsg = msg }

// HandleKeyPress processes a key event, returning true if consumed. Left/Right
// move between profiles (wrapping); Up/Down and +/- (and = for the unshifted +)
// adjust the focused profile's count. Any count change clears a stale error.
func (vp *VariantPicker) HandleKeyPress(msg tea.KeyMsg) bool {
	if len(vp.profiles) == 0 {
		return false
	}
	switch msg.String() {
	case "left":
		vp.cursor = wrapIndex(vp.cursor, -1, len(vp.profiles))
		return true
	case "right":
		vp.cursor = wrapIndex(vp.cursor, +1, len(vp.profiles))
		return true
	case "up", "+", "=":
		vp.increment()
		return true
	case "down", "-":
		vp.decrement()
		return true
	}
	return false
}

func (vp *VariantPicker) increment() {
	if vp.counts[vp.cursor] < variantCountMax {
		vp.counts[vp.cursor]++
		vp.errMsg = ""
	}
}

// decrement lowers the focused profile's count, but never below zero and never so
// far that the whole batch would reach zero sessions — a submit must always have
// at least one session to create, so the floor of one total is invariant.
func (vp *VariantPicker) decrement() {
	if vp.counts[vp.cursor] == 0 {
		return
	}
	if vp.Total() == 1 {
		return
	}
	vp.counts[vp.cursor]--
	vp.errMsg = ""
}

// Total is the number of sessions the current counts would spawn (always ≥1).
func (vp *VariantPicker) Total() int {
	sum := 0
	for _, c := range vp.counts {
		sum += c
	}
	return sum
}

// Variants returns the flattened list of programs to spawn, in picker order, each
// profile's program repeated by its count. The floor-of-one invariant keeps it
// non-empty.
func (vp *VariantPicker) Variants() []string {
	out := make([]string, 0, vp.Total())
	for i, p := range vp.profiles {
		for j := 0; j < vp.counts[i]; j++ {
			out = append(out, p.Program)
		}
	}
	return out
}

// SelectedIncludesClaude reports whether any profile with a positive count
// resolves to claude. The model/effort/permission overrides apply only then, so
// this drives the create form's enable/disable of those fields for a mixed batch.
func (vp *VariantPicker) SelectedIncludesClaude() bool {
	for i, p := range vp.profiles {
		if vp.counts[i] > 0 && agent.Resolve(p.Program).Key == agent.KeyClaude {
			return true
		}
	}
	return false
}

func vpLabelStyle() lipgloss.Style    { return overlayLabelStyle() }
func vpSelectedStyle() lipgloss.Style { return overlaySelectedStyle() }
func vpDimStyle() lipgloss.Style      { return overlayDimStyle() }

// Render draws the label — trailed by the session total, the focus hint, or (taking
// priority) an inline batch error — then a blank line and a horizontal row of
// "glyph name ×N" chips (the cursor one highlighted when focused). The line count is
// constant (label, blank, chips), matching the other picker sections, so the centered
// overlay does not jump as focus moves — variantSectionLines in textInput_size.go must
// match. Total and error ride the label line rather than a fourth row so the tallest
// claude form (variants + model + effort + mode + accounts) still fits an 80×24 screen.
func (vp *VariantPicker) Render() string {
	var s strings.Builder
	s.WriteString(vpLabelStyle().Render("Variants"))
	if vp.errMsg != "" {
		s.WriteString(theme.Current().DangerStyle().Render("  " + vp.errMsg))
	} else {
		total := vp.Total()
		noun := "sessions"
		if total == 1 {
			noun = "session"
		}
		s.WriteString(vpDimStyle().Render(fmt.Sprintf("  → %d %s", total, noun)))
		if vp.focused {
			s.WriteString(vpDimStyle().Render("   ←→ profile · ↑↓ count"))
		}
	}
	s.WriteString("\n\n")

	for i, p := range vp.profiles {
		// Prefix each profile with its agent's identity glyph (the same glyph the
		// session list shows) so a mixed bake-off reads at a glance.
		glyph, _ := theme.Current().AgentGlyph(string(agent.Resolve(p.Program).Key))
		label := fmt.Sprintf(" %s %s ×%d ", glyph, p.Name, vp.counts[i])
		switch {
		case i == vp.cursor && vp.focused:
			s.WriteString(vpSelectedStyle().Render(label))
		case i == vp.cursor:
			s.WriteString(label)
		default:
			s.WriteString(vpDimStyle().Render(label))
		}
		if i < len(vp.profiles)-1 {
			s.WriteString(vpDimStyle().Render(" | "))
		}
	}
	return s.String()
}
