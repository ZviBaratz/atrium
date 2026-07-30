package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// variantRow returns the composed frame's row carrying the variant section's label —
// the line a batch-refusal message rides. It strips ANSI, so the assertion compares
// text rather than styling, and it fails loudly rather than returning "" when no such
// row exists: a form whose Variants line had been pushed out of the frame would
// otherwise make every check below pass vacuously.
func variantRow(t *testing.T, h *home) string {
	t.Helper()
	frame := xansi.Strip(h.View().Content)
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "Variants") {
			return line
		}
	}
	t.Fatalf("no frame row carries the Variants label:\n%s", frame)
	return ""
}

// pressPlusToCap raises the focused profile's count to its ceiling. That ceiling is
// ui/overlay's variantCountMax, which is unexported, so this presses well past it — an
// increment at the cap is a no-op, so an over-long loop is safe and the test never has
// to restate a constant from another package.
func pressPlusToCap(h *home) {
	const wellPastAnyCap = 64
	for i := 0; i < wellPastAnyCap; i++ {
		plusKey(h)
	}
}

// TestVariantRefusals_SurviveAn80ColRender is the width guard for the create form's
// three batch refusals (#541), and it deliberately holds no copy of its own: it drives
// each refusal through the real submit path, then asserts that the message the app
// actually set survives into the rendered frame.
//
// VariantPicker.Render puts the message on the *label* line, so the composed line is
// "Variants" + "  " + the message — 10 cells of prefix against the 42 an 80-col
// terminal gives the overlay (app_layout.go passes int(0.6*80) = 48 to SetSize; compose
// then takes width-6). That leaves 32 cells for the message, and fitOverlay truncates
// the overflow *silently*, so a refusal's reason shipped cut mid-clause with nothing to
// say so — worse than the hint truncation of #464, because this text only appears once
// something has already gone wrong.
//
// Comparing the frame row against VariantError() rather than a literal is what lets the
// same test body be red before the fix and green after, with only the copy changing.
// Three properties are load-bearing:
//
//   - It is scoped to the *row*, not the frame. A Contains over the whole frame would
//     also pass if the message were later routed to the error box (0.9 x width = 72
//     cells) or given a line of its own — so it could not defend the constraint it
//     exists for (see variantSectionLines in ui/overlay/textInput_size.go, which
//     documents why total and error ride the label line).
//   - It drives each refusal rather than pushing a string in via SetVariantError, which
//     would test the renderer and leave a literal inlined at a call site unguarded.
//   - Each row is *digit-maximal*, because these messages interpolate counts. That is
//     the trap #541 names: TestVariantPicker_CountChangeClearsError already renders this
//     very line, but with an 8-cell "too many" — a fixture too short to reveal the
//     defect. A guard is only as wide as its widest fixture.
func TestVariantRefusals_SurviveAn80ColRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		// arm builds the home and drives the form to the brink of the refusal: title
		// typed, counts set. The shared body below resizes, submits, and measures.
		arm func(t *testing.T) *home
	}{
		{
			// A fan-out on a direct (non-git) target. No interpolation, so there is
			// nothing to maximise.
			name: "needs a git repo",
			arm: func(t *testing.T) *home {
				h := newFanOutHome(t, t.TempDir()) // a plain dir -> direct session
				typeString(h, "race")
				h.textInputOverlay.FocusVariants()
				plusKey(h) // claude 1 -> 2
				return h
			},
		},
		{
			// Over maxVariantBatch. This message is unreachable on a default install:
			// GetProfiles synthesises a single profile, and ui/overlay's
			// variantCountMax caps one profile's count at maxVariantBatch itself
			// today — so crossing the batch limit takes at least two profiles. The
			// total is then bounded only by variantCountMax x len(profiles), a *config*
			// value rather than a constant, so six profiles at their cap give a
			// three-digit total. The format costs 27 cells plus the digits of the
			// total, so it still fits the 32-cell budget at a five-digit total.
			name: "over the batch limit",
			arm: func(t *testing.T) *home {
				repo := gitInitRepo(t)
				h := newFanOutHome(t, repo)
				h.appConfig.Profiles = []config.Profile{
					{Name: "claude", Program: "claude"},
					{Name: "codex", Program: "codex"},
					{Name: "aider", Program: "aider"},
					{Name: "gemini", Program: "gemini"},
					{Name: "amp", Program: "amp"},
					{Name: "cursor", Program: "cursor"},
				}
				h.textInputOverlay = overlay.NewSessionCreateOverlay(
					h.appConfig.GetProfiles(), h.appConfig.ClaudeAccounts, []string{repo}, h.program)
				h.textInputOverlay.FocusTitle()
				typeString(h, "race")
				h.textInputOverlay.FocusVariants()
				for range h.appConfig.Profiles {
					pressPlusToCap(h)
					rightKey(h)
				}
				return h
			},
		},
		{
			// Over an explicit (hard) max_sessions. Both counts are provably two-digit:
			// total <= maxVariantBatch because that check returns first, and free <
			// total because capBlock requires count+total > Limit while free =
			// Limit-count. A limit of maxVariantBatch with one session live and a batch
			// of exactly maxVariantBatch is therefore the widest this can ever be — the
			// batch is not itself over the batch limit, so it reaches the cap gate.
			name: "over max_sessions",
			arm: func(t *testing.T) *home {
				h := newFanOutHome(t, gitInitRepo(t))
				limit := maxVariantBatch
				h.appConfig.MaxSessions = &limit
				addStubInstances(t, h, 1) // free = 20 - 1 = 19
				typeString(h, "race")
				h.textInputOverlay.FocusVariants()
				for i := 1; i < maxVariantBatch; i++ {
					plusKey(h) // claude 1 -> 20; 1 + 20 > 20 blocks
				}
				return h
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := tc.arm(t)
			// The resize is what gives the overlay its width, and 80x24 is the terminal
			// the copy has to survive. SetSize takes the *overlay's* share (0.6), which
			// is the trap in tests that pass 80 to it directly — that is a 133-col
			// terminal, three sections wider than this one.
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
			ctrlS(h)

			require.Equal(t, statePrompt, h.state, "a refused batch keeps the form open")
			msg := h.textInputOverlay.VariantError()
			require.NotEmpty(t, msg, "the refusal must set an inline message, or this proves nothing")

			row := variantRow(t, h)
			assert.Containsf(t, row, msg,
				"the refusal's reason is cut at 80 cols — fitOverlay trimmed it to fit.\n"+
					"  message (%d cells): %q\n  frame row: %q",
				len(msg), msg, strings.TrimSpace(row))
		})
	}
}
