package app

import (
	"strconv"
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
// four batch refusals (#541, #644), and it deliberately holds no copy of its own: it drives
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
//   - Each row is *digit-maximal* wherever a count is interpolated. That is the trap
//     #541 names: TestVariantPicker_CountChangeClearsError already renders this very
//     line, but with an 8-cell "too many" — a fixture too short to reveal the defect. A
//     guard is only as wide as its widest fixture.
//
// Rendering at the widest *reachable* fixture is not the same as proving a bound,
// though, and one case needs the difference spelled out: see the batch-limit case's
// also hook, which pins that its message interpolates nothing config-derived. Rendering
// alone would stay green through a reword that reintroduced an unbounded value, because
// no fixture can configure the thousands of profiles it would take to overflow.
func TestVariantRefusals_SurviveAn80ColRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		// arm builds the home and drives the form to the brink of the refusal: title
		// typed, counts set. The shared body below resizes, submits, and measures.
		arm func(t *testing.T) *home
		// also is an optional per-case assertion on the message the app set, for a
		// property the shared render check cannot see. Skipped when nil.
		also func(t *testing.T, h *home, msg string)
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
			// today — so crossing the batch limit takes at least two profiles, and six
			// at their cap are what make the total three digits here. The message does
			// not carry that total (see also below); the fixture still needs it to
			// reach the refusal at all.
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
			// The only value this refusal may interpolate is maxVariantBatch, a
			// compile-time constant. The batch's own total is variantCountMax x
			// len(profiles), and config.Profiles has no ceiling anywhere, so any
			// spelling that carries it is bounded only by a claim about realistic use.
			//
			// The render check above cannot defend that, and the reason is worth being
			// exact about: a reword carrying the total can *fit* at the total a fixture
			// can build. Mutating this call to "%d over the %d-session limit" is 29
			// cells at this fixture's 120 — the row assertion stays green, and this one
			// is what fails. Overflowing the render check that way would take thousands
			// of configured profiles, which no test can arrange. So the property is
			// asserted directly rather than measured.
			also: func(t *testing.T, h *home, msg string) {
				total := len(h.textInputOverlay.GetVariants())
				require.Greater(t, total, maxVariantBatch,
					"the fixture must be over the batch limit, or this proves nothing")
				assert.NotContainsf(t, msg, strconv.Itoa(total),
					"this refusal must not interpolate the batch total (%d): it is bounded by "+
						"len(profiles), which nothing caps. Interpolate maxVariantBatch alone.", total)
			},
		},
		{
			// A fork that also asks for a fan-out. No interpolation, so there is
			// nothing to maximise — but it is the longest of the four at 31 cells,
			// one under the 32 the row gives, which is what makes rendering it the
			// real test rather than a formality.
			//
			// The fork is armed on the model rather than driven through the timeline:
			// what this case needs is the armed *state*, and reaching it through `f`
			// would drag a claude session and a loaded enumeration into a fixture whose
			// subject is a width. The refusal itself is still driven, by a real submit.
			name: "fork cannot fan out",
			arm: func(t *testing.T) *home {
				h := newFanOutHome(t, gitInitRepo(t))
				h.pendingFork = &pendingFork{
					sourceTitle:      "alpha",
					sourceTranscript: "/cfg/projects/-src/s.jsonl",
					cutEntryID:       "aaaa1111-1111-4111-8111-111111111111",
					droppedMessageID: "bbbb2222-2222-4222-8222-222222222222",
				}
				typeString(h, "race")
				h.textInputOverlay.FocusVariants()
				plusKey(h) // claude 1 -> 2
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

			if tc.also != nil {
				tc.also(t, h, msg)
			}
		})
	}
}
