package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session/git"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The double-tap generalized (#520/#798): every confirmation opened by an unambiguous
// key answers to a second press of that same key.
//
// What this file guards is the whole mechanism end to end, per dialog, because the
// three halves fail independently and only the first is visible in a diff:
//
//  1. the dialog ARMS the key it was opened by (ConfirmAltKey),
//  2. the dialog ADVERTISES it (the rendered "(or P)" hint), and
//  3. pressing it again really CONFIRMS — through handleKeyPress, not through the
//     overlay directly, because nothing else in the tree asserts that a key reaches
//     the handler that answers it.
//
// The third is the one a green suite has never proved. An earlier version of the
// arming code that set the field and was never routed to would satisfy (1) and (2)
// while the user's second press did nothing at all.
//
// Confirming here is side-effect-free by construction: every in-scope dialog stages a
// named busyLabel, so handleConfirmState hands the action to beginAsyncAction and
// RETURNS the Cmd rather than running it. actionInFlight is therefore the signal that
// separates a confirm from a cancel — a declined dialog returns nil having touched
// nothing, which is exactly the contract armOnConfirm exists for.

// doubleTapDialog is one confirmation, the key that opens it, and how to reach it.
type doubleTapDialog struct {
	name string
	// key is both the key pressed to open the dialog and the key the dialog must
	// echo. Stating it once is the point: the assertion is that the two are the same
	// string, not that either matches a constant copied out of the registry.
	key string
	// open arranges the fleet and drives the opening keypress through handleKeyPress.
	open func(t *testing.T, h *home)
}

// doubleTapDialogs is the in-scope set from #520: every confirmation whose trigger is
// an unambiguous key. Batch pause and batch resume appear twice each, because they
// have two entry points bound to different keys — the all-sessions chord from the
// list and the plain verb key from visual mode — and a shared core that could only
// echo one of them is the exact defect
// TestMultiSelect_KillDoubleTapEchoesTheOpeningKey caught for kill.
//
// Deliberately absent, and why, so the next reader does not read the gap as an
// oversight: over-capacity create and all-accounts-exhausted (there the dialog IS the
// feature — it exists to state a fact the user does not know, and a reflex confirm
// defeats it); the two quit confirmations, which fire a handful of times ever;
// cleanup-after-merge, opened by a message rather than a key; the branch-busy
// detach-and-resume follow-up, whose question is not the one r asked; custom-command
// confirms, where Confirm:true is the user asking to be asked; and undo-restore, a
// recovery verb rather than a destructive one.
//
// Single pause is absent for a different reason again: it has no confirmation at all
// (pauseSelected goes straight to beginAsyncAction), so there is nothing to double-tap.
// Single resume is present only in its over-capacity form, the one branch of
// resumeSelected that opens a dialog.
//
// Batch pause is PRESENT despite carrying the sharpest fact in the set — its message
// is the only place a user is told that removing a worktree deletes gitignored files
// like .env for good, and unlike kill there is no retention ref to undo it from. That
// is not an oversight against the over-capacity exclusion just above, but the other
// side of the same test. The over-capacity dialogs exist to state a fact the user
// meets rarely and cannot anticipate, so a reflex confirm defeats their whole reason
// to exist; pause is a verb the user runs constantly, whose repetition is the friction
// #520 exists to relieve, and whose fact stays on screen at full size either way — a
// double-tap shortens the motion, never the copy. The user who wants the second press
// to cost more has one switch, double_tap_confirm, and it does not take the warning
// with it. That asymmetry is the whole argument against #391's per-dialog opt-outs.
func doubleTapDialogs() []doubleTapDialog {
	return []doubleTapDialog{
		{
			name: "kill",
			key:  "ctrl+x",
			open: func(t *testing.T, h *home) {
				addActive(t, h, "alpha")
				h.list.SetSelectedInstance(0)
				_, _ = h.handleKeyPress(keyMsg("ctrl+x"))
			},
		},
		{
			name: "batch kill (visual x)",
			key:  "x",
			open: func(t *testing.T, h *home) {
				a := addActive(t, h, "alpha")
				pressRune(h, 'v')
				h.list.ToggleMark(a)
				pressRune(h, 'x')
			},
		},
		{
			name: "push",
			key:  "P",
			open: func(t *testing.T, h *home) {
				addActive(t, h, "alpha")
				h.list.SetSelectedInstance(0)
				_, _ = h.handleKeyPress(textMsg("P"))
			},
		},
		{
			name: "create PR",
			key:  "c",
			open: func(t *testing.T, h *home) {
				inst := addActive(t, h, "alpha")
				inst.SetPRStatus(&git.PRStatus{Pushed: true})
				h.list.SetSelectedInstance(0)
				_, _ = h.handleKeyPress(textMsg("c"))
			},
		},
		{
			name: "merge PR",
			key:  "m",
			open: func(t *testing.T, h *home) {
				inst := addActive(t, h, "alpha")
				inst.SetPRStatus(&git.PRStatus{
					Pushed: true, HasPR: true, Number: 12, State: "OPEN",
				})
				h.list.SetSelectedInstance(0)
				_, _ = h.handleKeyPress(textMsg("m"))
			},
		},
		{
			name: "pause all",
			key:  "ctrl+p",
			open: func(t *testing.T, h *home) {
				addActive(t, h, "alpha")
				addActive(t, h, "bravo")
				_, _ = h.handleKeyPress(keyMsg("ctrl+p"))
			},
		},
		{
			name: "pause marked",
			key:  "p",
			open: func(t *testing.T, h *home) {
				a := addActive(t, h, "alpha")
				pressRune(h, 'v')
				h.list.ToggleMark(a)
				pressRune(h, 'p')
			},
		},
		{
			name: "resume all",
			key:  "ctrl+r",
			open: func(t *testing.T, h *home) {
				addPaused(t, h, "alpha")
				_, _ = h.handleKeyPress(keyMsg("ctrl+r"))
			},
		},
		{
			name: "resume marked",
			key:  "r",
			open: func(t *testing.T, h *home) {
				a := addPaused(t, h, "alpha")
				pressRune(h, 'v')
				h.list.ToggleMark(a)
				pressRune(h, 'r')
			},
		},
		{
			// The only single-resume dialog there is: within budget r resumes on the
			// first press, so the arrangement below (2 live against a capacity of 2)
			// is what makes a dialog exist to double-tap at all.
			name: "resume over the host budget",
			key:  "r",
			open: func(t *testing.T, h *home) {
				h.hostCap = 2
				h.appConfig.MaxSessions = nil
				addActive(t, h, "live-1")
				addActive(t, h, "live-2")
				addPaused(t, h, "alpha")
				h.list.SetSelectedInstance(2)
				pressRune(h, 'r')
			},
		},
	}
}

// TestDoubleTapConfirmsEveryKeyedDialog is the arming + advertising + dispatch sweep,
// per dialog, with the shortcut on.
func TestDoubleTapConfirmsEveryKeyedDialog(t *testing.T) {
	for _, d := range doubleTapDialogs() {
		t.Run(d.name, func(t *testing.T) {
			h := newCreateFormHome(t)
			d.open(t, h)

			require.Equal(t, stateConfirm, h.state, "%s must open a confirmation", d.name)
			require.NotNil(t, h.confirmationOverlay)
			assert.Equal(t, d.key, h.confirmationOverlay.ConfirmAltKey,
				"the dialog must arm the key that opened it")
			assert.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "(or "+d.key+")",
				"the dialog must teach the second press it accepts")

			// Through handleKeyPress, not the overlay: this is the half nothing else
			// asserts. The press must reach handleConfirmState, be recognised there,
			// and resolve the dialog as CONFIRMED.
			_, _ = h.handleKeyPress(pressableKey(d.key))
			assert.Equal(t, stateDefault, h.state, "the second press must close the dialog")
			assert.Nil(t, h.confirmationOverlay)
			assert.True(t, h.actionInFlight,
				"the second press must CONFIRM, not cancel: a declined dialog stages nothing")
		})
	}
}

// TestDoubleTapOffLeavesEveryDialogAtY is the same sweep with the gate off. Two
// assertions per dialog, and the second is the one that matters: an implementation
// that skipped SetConfirmAltKey but still matched the key somewhere downstream would
// pass the first.
//
// It also pins what the switch does NOT do — the dialog is still there, still asking,
// still answerable with y. That is the whole argument for one gate instead of #391's
// fourteen opt-outs: turning the shortcut off must never turn a warning off.
func TestDoubleTapOffLeavesEveryDialogAtY(t *testing.T) {
	for _, d := range doubleTapDialogs() {
		t.Run(d.name, func(t *testing.T) {
			off := false
			h := newCreateFormHome(t)
			h.appConfig.DoubleTapConfirm = &off
			d.open(t, h)

			require.Equal(t, stateConfirm, h.state)
			require.NotNil(t, h.confirmationOverlay)
			assert.Empty(t, h.confirmationOverlay.ConfirmAltKey)

			_, _ = h.handleKeyPress(pressableKey(d.key))
			assert.Equal(t, stateConfirm, h.state,
				"with the shortcut off the second press must not answer the dialog")
			require.NotNil(t, h.confirmationOverlay, "the dialog and its copy must stay on screen")
			assert.False(t, h.actionInFlight)

			_, _ = h.handleKeyPress(textMsg("y"))
			assert.Equal(t, stateDefault, h.state, "y must still confirm")
			assert.True(t, h.actionInFlight)
		})
	}
}

// The legacy kill-only spelling still gates the generalized shortcut, so a user who
// turned the double-tap off before #798 does not silently regain it, on every keyed
// confirmation rather than the one they refused. Driven through a real dialog rather than the
// accessor alone (config's own test covers the ladder) because the wiring is the part
// that could read the wrong field.
func TestDoubleTapHonorsTheDeprecatedKillOnlyKey(t *testing.T) {
	off := false
	h := newCreateFormHome(t)
	h.appConfig.DoubleTapConfirm = nil
	h.appConfig.KillDoubleTapConfirm = &off
	addActive(t, h, "alpha")
	h.list.SetSelectedInstance(0)

	_, _ = h.handleKeyPress(textMsg("P"))

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	assert.Empty(t, h.confirmationOverlay.ConfirmAltKey,
		"a pre-#798 opt-out must still silence the shortcut it was written for")
}

// The armed key is read from the registry, never spelled, so the shortcut follows a
// rebind instead of listening for a key nobody presses any more. Push is the case:
// rebound onto ctrl+u, the dialog must open on ctrl+u, teach ctrl+u, and answer to
// ctrl+u — while the old P does nothing at all.
//
// This is what a literal in armDoubleTap's callers would break, and the reason
// keys.KillKey() exists (#448). It is also why the sweep above presses the same
// string it opened with rather than comparing against keys.PrimaryKey: an assertion
// built from the registry would agree with a call site built from the registry no
// matter which key the user had actually pressed.
func TestDoubleTapFollowsARebind(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{
		"push_branch": {Keys: []string{"ctrl+u"}},
	})
	defer restore()
	require.Empty(t, problems, "the rebind must be applied, not refused")

	h := newCreateFormHome(t)
	addActive(t, h, "alpha")
	h.list.SetSelectedInstance(0)

	_, _ = h.handleKeyPress(keyMsg("ctrl+u"))

	require.Equal(t, stateConfirm, h.state, "the rebound key must open the push dialog")
	require.NotNil(t, h.confirmationOverlay)
	assert.Equal(t, "ctrl+u", h.confirmationOverlay.ConfirmAltKey)
	assert.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "(or ctrl+u)")

	_, _ = h.handleKeyPress(textMsg("P"))
	assert.Equal(t, stateConfirm, h.state, "the unbound old key must not confirm")

	_, _ = h.handleKeyPress(keyMsg("ctrl+u"))
	assert.Equal(t, stateDefault, h.state)
	assert.True(t, h.actionInFlight)
}

// A key that the dialog ALREADY answers must not be armed as its double-tap.
//
// The keymap is user-owned, so an override can land a verb on y, n or esc, and
// ConfirmationOverlay.HandleKeyPress tests the alt key BEFORE the cancel key. Without
// the refusal, a user who rebound pause onto n would press the n this very dialog
// prints as "n or esc to cancel" and have it pause instead — a confirmation that
// answers its own cancel key with a yes. The failure is silent and lands on the one
// keystroke a confirmation must never get wrong.
//
// n rather than y because y-as-alt is harmless (both mean confirm) and would not
// distinguish a correct implementation from an absent guard.
func TestDoubleTapRefusesAKeyTheDialogAlreadyAnswers(t *testing.T) {
	// new must move off n first: keys.Apply refuses an override onto a key another
	// action still holds, and an ignored override would make this test pass by
	// arming nothing for a reason that has nothing to do with the guard.
	problems, restore := keys.Apply(map[string]keys.Spec{
		"new":       {Keys: []string{"ctrl+n"}},
		"pause_all": {Keys: []string{"n"}},
	})
	defer restore()
	require.Empty(t, problems, "the rebind must be applied, not refused")
	require.Equal(t, "n", keys.PrimaryKey(keys.KeyPauseAll))

	h := newCreateFormHome(t)
	addActive(t, h, "alpha")
	addActive(t, h, "bravo")

	_, _ = h.handleKeyPress(textMsg("n"))

	require.Equal(t, stateConfirm, h.state, "the rebound key must still open the dialog")
	require.NotNil(t, h.confirmationOverlay)
	assert.Empty(t, h.confirmationOverlay.ConfirmAltKey,
		"the cancel key must not be re-armed as a confirm key")

	_, _ = h.handleKeyPress(textMsg("n"))
	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.actionInFlight, "n must cancel, as the dialog says it does")
}

// An unbound verb has no second press to make: the dialog still opens (from the
// palette, which reaches every action whatever the keymap says), asks its question,
// and teaches no alternate key.
//
// The property is the rendered outcome, not the field, and deliberately so.
// armDoubleTap's own `key == ""` early return is belt-and-braces rather than
// load-bearing — Render gates the "(or …)" run on ConfirmAltKey != "", and
// HandleKeyPress gates the alt-key match the same way, so arming "" and not arming at
// all are the same dialog. Asserting the field alone would therefore be asserting an
// implementation detail that nothing downstream can tell apart. What must never
// happen is a box that offers a key the user cannot press, and that is what is
// checked.
func TestDoubleTapLeavesAnUnboundVerbAlone(t *testing.T) {
	problems, restore := keys.Apply(map[string]keys.Spec{
		"pause_all": {Disabled: true},
	})
	defer restore()
	require.Empty(t, problems)

	h := newCreateFormHome(t)
	addActive(t, h, "alpha")
	require.Empty(t, keys.PrimaryKey(keys.KeyPauseAll), "the arrangement must really unbind it")

	// Opened directly: there is no key left to press, which is the state under test.
	_ = h.pauseAll()

	require.NotNil(t, h.confirmationOverlay)
	assert.Empty(t, h.confirmationOverlay.ConfirmAltKey)
	assert.NotContains(t, flattenOverlay(h.confirmationOverlay.Render()), "(or ")
}

// pressableKey turns a dialog's armed key string into the message a real press of it
// produces: a printable single rune arrives as text, a chord as a named key.
func pressableKey(k string) tea.KeyPressMsg {
	if len([]rune(k)) == 1 {
		return textMsg(k)
	}
	return keyMsg(k)
}

// A binding of SEVERAL keys must echo the one that was pressed, not the first one
// declared.
//
// keys.PrimaryKey answers "which key is this action's" with Keys()[0], which is the
// pressed key for every binding this repo ships and for none of the interesting ones a
// user can write. Bind pause to both p and P, press P in visual mode, and a call site
// that resolved the key from the KeyName instead of forwarding msg.String() arms p:
// the dialog then teaches a key the user did not press and ignores the one they did.
//
// Visual mode is where this is fixable and therefore where it is asserted.
// handleMultiSelectState still holds the keystroke; dispatchAction has already turned
// it into a KeyName and thrown it away, which armDoubleTap's comment discloses rather
// than papering over.
func TestDoubleTapEchoesTheSecondKeyOfAMultiKeyBinding(t *testing.T) {
	// push_branch must move off P first: keys.Apply refuses an override onto a key
	// another action still holds, and an ignored override would leave pause on its
	// shipped single key, where the defect under test cannot occur.
	problems, restore := keys.Apply(map[string]keys.Spec{
		"push_branch": {Keys: []string{"ctrl+u"}},
		"pause":       {Keys: []string{"p", "P"}},
	})
	defer restore()
	require.Empty(t, problems, "the rebind must be applied, not refused")
	require.Equal(t, "p", keys.PrimaryKey(keys.KeyPause),
		"p must stay first, or this asserts nothing about which key was pressed")

	h := newCreateFormHome(t)
	a := addActive(t, h, "alpha")
	pressRune(h, 'v')
	h.list.ToggleMark(a)
	pressRune(h, 'P')

	require.Equal(t, stateConfirm, h.state, "the second key of the binding must open the dialog")
	require.NotNil(t, h.confirmationOverlay)
	assert.Equal(t, "P", h.confirmationOverlay.ConfirmAltKey)
	assert.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "(or P)")

	_, _ = h.handleKeyPress(textMsg("P"))
	assert.Equal(t, stateDefault, h.state)
	assert.True(t, h.actionInFlight)
}
