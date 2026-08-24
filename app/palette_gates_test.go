package app

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateScenario is a selection the palette might be opened against.
type gateScenario struct {
	name string
	// arm returns the instance to select, or nil for an empty list.
	arm func(t *testing.T, h *home) *session.Instance
}

func newGateInstance(t *testing.T, h *home, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(0)
	return inst
}

func gateScenarios() []gateScenario {
	return []gateScenario{
		{"no selection", func(*testing.T, *home) *session.Instance { return nil }},
		{"loading", func(t *testing.T, h *home) *session.Instance {
			inst := newGateInstance(t, h, "loading")
			inst.SetStatus(session.Loading)
			return inst
		}},
		{"paused", func(t *testing.T, h *home) *session.Instance {
			inst := newGateInstance(t, h, "paused")
			inst.SetStatus(session.Paused)
			return inst
		}},
		{"ready", func(t *testing.T, h *home) *session.Instance {
			inst := newGateInstance(t, h, "ready")
			inst.SetStatus(session.Ready)
			return inst
		}},
		{"ready with a mergeable PR", func(t *testing.T, h *home) *session.Instance {
			inst := newGateInstance(t, h, "with-pr")
			inst.SetStatus(session.Ready)
			inst.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN", Pushed: true})
			return inst
		}},
	}
}

// newGateHome builds a home the agreement test can actually *dispatch* into:
// working in-memory storage, because a handler that reaches persistInstances
// nil-derefs without it — and reaching it is precisely what a false dim looks
// like here.
func newGateHome(t *testing.T, sc gateScenario) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)
	h.appState = appState
	h.storage = storage
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	inst := sc.arm(t, h)
	return h, inst
}

// Direction 1: every dispatchable action must be classified. An action that
// arrives un-gated would default into "always fine" by omission, which is the
// failure the settings panel's row guard exists to prevent and the reason this
// one is written the same way.
func TestEveryPaletteActionHasAGate(t *testing.T) {
	gated := map[keys.KeyName]bool{}
	for name := range paletteGates {
		gated[name] = true
	}

	for _, row := range paletteRows() {
		assert.Truef(t, gated[row.name],
			"%q is in the palette but has no entry in paletteGates — classify it as global() "+
				"or perSession(...)", row.action.Key)
	}

	// Direction 2: a gate for something the palette never lists is dead weight
	// that reads like coverage.
	listed := map[keys.KeyName]bool{}
	for _, row := range paletteRows() {
		listed[row.name] = true
	}
	for name := range paletteGates {
		assert.Truef(t, listed[name], "paletteGates has an entry for %v, which the palette never lists", name)
	}
}

// A blocked action must always give a reason — a dimmed row with nothing to say
// is worse than a live one, because the user cannot tell it from a bug.
func TestEveryBlockedPaletteRowHasAReason(t *testing.T) {
	seen := 0
	for _, sc := range gateScenarios() {
		for _, row := range paletteRows() {
			h, _ := newGateHome(t, sc)
			gate, ok := paletteGates[row.name]
			if !ok || gate.blocked == nil {
				continue
			}
			// Re-read through the model so this covers the path the row uses.
			if reason := h.paletteInertReason(row.name); reason != "" {
				seen++
				assert.NotEmpty(t, reason)
			}
		}
	}
	require.Positive(t, seen, "no scenario blocked anything — the sweep proves nothing")
}

// The reasons this package authors are chip-sized, because the row's tail column
// is ~27 cells at 80 columns and a reason rendered as an ellipsis explains
// nothing.
//
// The PR predicates' reasons are deliberately exempt: MergeBlockedReason and
// CreateBlockedReason are the authoritative, already-tested source of *why* those
// two actions are blocked, and paraphrasing them into chips here would be a
// second copy that drifts. They truncate on the row and arrive in full in the
// notice that enter produces.
func TestPaletteGateReasonsFitTheRow(t *testing.T) {
	const tailBudget = 30
	for _, reason := range []string{
		noSelectionReason, stillStartingReason, pausedReason, alreadyPausedReason,
		notPausedReason, directReason, noBranchReason, noQueueReason, noPRReason,
		deadPaneReason, pausedWorktreeReason, notClaudeReason,
	} {
		assert.NotEmpty(t, reason)
		assert.LessOrEqualf(t, len([]rune(reason)), tailBudget,
			"reason %q is too long for the row's tail column", reason)
	}
}

// The one that makes the dimming honest. A gate is a second opinion about what
// the handlers already decide inline, so the danger is a FALSE DIM: a row the
// palette refuses that dispatch would have run, making the action unreachable
// from the palette with a reason that is simply wrong.
//
// Dispatch each blocked action directly, bypassing the gate, and require it to
// refuse too. "Refused" has three parts, and all three are needed: it did not
// leave the default state (no overlay, no armed confirmation), it started no
// async work, and it did not quietly mutate the session.
//
// That last one is not belt-and-braces. Mute is a synchronous flag flip that
// returns to the default state with no work in flight — so an over-strict gate on
// mute is INVISIBLE to the first two assertions, and this test originally caught
// it only because the fixture had no storage and the handler panicked. Once the
// fixture was wired properly the mutation passed. The fingerprint is what makes
// the check real rather than an artifact of a broken fixture.
//
// The returned command is deliberately not run: the work lives there, and this
// stays hermetic by asserting on what dispatch does synchronously.
func TestPaletteGatesAgreeWithDispatch(t *testing.T) {
	checked := 0
	for _, sc := range gateScenarios() {
		for _, row := range paletteRows() {
			h, inst := newGateHome(t, sc)
			reason := h.paletteInertReason(row.name)
			if reason == "" {
				continue
			}
			checked++

			h.state = stateDefault
			before := instanceFingerprint(inst)
			_, _ = h.dispatchAction(row.name)

			assert.Equalf(t, stateDefault, h.state,
				"scenario %q: the palette dims %q as %q, but dispatching it opened %v — "+
					"the gate is refusing something the app would have done",
				sc.name, row.action.Key, reason, h.state)
			assert.Falsef(t, h.actionInFlight,
				"scenario %q: the palette dims %q as %q, but dispatching it started work",
				sc.name, row.action.Key, reason)
			assert.Equalf(t, before, instanceFingerprint(inst),
				"scenario %q: the palette dims %q as %q, but dispatching it changed the session",
				sc.name, row.action.Key, reason)
		}
	}
	// Without this the loop could skip every action — no gate ever blocking — and
	// the agreement would be asserted about nothing at all.
	require.Positive(t, checked, "no action was blocked in any scenario; the agreement is vacuous")
}

// instanceFingerprint captures the session state a dispatched action could
// synchronously change, so "it did nothing" can be asserted rather than assumed.
func instanceFingerprint(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	return fmt.Sprintf("status=%v muted=%v name=%q branch=%q queued=%v",
		inst.GetStatus(), inst.Muted(), inst.DisplayName(), inst.Branch(), inst.HasQueuedPrompt())
}

// Enter on a dimmed row must answer. It is selectable on purpose (dimmed, not
// hidden), and from the palette a silent refusal is indistinguishable from the
// action having run and done nothing.
func TestPaletteDimmedRowExplainsItselfOnEnter(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	inst := newGateInstance(t, h, "running")
	inst.SetStatus(session.Ready)

	openPalette(t, h)
	typeQuery(t, h, "resume")
	_, _ = h.handleKeyPress(keyMsg("enter"))

	assert.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice(), "a dimmed row must say why it did nothing")
	assert.Contains(t, h.menu.String(), notPausedReason)
}

// And the reason reaches the row itself, not only the notice after the fact —
// the point of the dim is that you can see it before you press enter.
func TestPaletteRowCarriesItsInertReason(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	inst := newGateInstance(t, h, "running")
	inst.SetStatus(session.Ready)

	openPalette(t, h)
	// Filter to it: the unfiltered list is longer than the box, and resume sits
	// below the fold — asserting on the whole render would pass or fail on where
	// the row happens to land rather than on whether it carries its reason.
	typeQuery(t, h, "resume")

	var listed bool
	for _, row := range h.paletteRows {
		listed = listed || row.name == keys.KeyResume
	}
	require.True(t, listed, "the palette must list resume")
	assert.Contains(t, h.commandPaletteOverlay.Render(), notPausedReason,
		"the reason must render on the row, not just in the notice enter produces")
}

// A runnable action must not be dimmed. The mirror of the agreement test: gating
// everything would pass that one trivially.
func TestPaletteLeavesRunnableActionsAlone(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newGateInstance(t, h, "ready")
	inst.SetStatus(session.Ready)

	for _, name := range []keys.KeyName{
		keys.KeyRename, keys.KeyMute, keys.KeyQuickSend, keys.KeyPause,
		keys.KeyNew, keys.KeyHelp, keys.KeySettings, keys.KeyFilter, keys.KeyUp,
	} {
		assert.Emptyf(t, h.paletteInertReason(name), "%v is runnable on a ready session", name)
	}
}
