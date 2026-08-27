package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/keys"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/stretchr/testify/require"
)

// synthKeyMsg must produce a message that stringifies back to the key it was
// given: the dispatch path keys off msg.String(), so a mismatch would fire the
// wrong action (or none). A hand-picked spot check on the shapes that differ
// most; TestEveryBarKeyIsSynthesizable below is the exhaustive one.
func TestSynthKeyMsg_RoundTrips(t *testing.T) {
	for _, k := range []string{"enter", "esc", "space", "ctrl+x", "shift+up", "shift+down", "n", "?", "q", "s"} {
		msg, ok := synthKeyMsg(k)
		require.True(t, ok, "synthKeyMsg(%q) should succeed", k)
		require.Equal(t, k, msg.String(), "synthesized key must stringify back to %q", k)
	}
	// A range/compound label maps to no single key, so it is not synthesizable —
	// the click is a no-op rather than a wrong action.
	_, ok := synthKeyMsg("a–z")
	require.False(t, ok)
}

// hintBarClickState is per-state code the surface registry deliberately does
// not cover — clickability is judged from what the bar renders, not from
// barVisible — so this walk over the whole enum is what forces a new state to
// be classified here instead of landing in neither of two hand-kept lists,
// which is how stateDiffComment's zone-marked bar came to click dead (#852).
func TestHintBarClickState(t *testing.T) {
	// Every state appears. The default reason a state refuses clicks is that an
	// overlay owns the screen (or the bar shows non-key progress); rows where
	// the answer is anything less obvious say so.
	clickable := map[state]bool{
		stateDefault: true, // the hint line's entries are the click targets
		stateFilter:  true, // mode bar with live, zone-marked entries
		stateHints:   true, // mode bar with live, zone-marked entries
		stateVisual:  true, // mode bar with live, zone-marked entries
		// The fourth mode bar renders the same zone-marked entries as the three
		// above, but the gate refuses it and its clicks are dead. This row pins
		// the current behavior so #852's fix is a deliberate flip, not drift.
		stateDiffComment:    false,
		statePrompt:         false,
		stateHelp:           false,
		stateConfirm:        false,
		stateRename:         false,
		stateQueue:          false,
		stateCmdLog:         false,
		stateInfo:           false,
		stateSettings:       false,
		stateWelcome:        false,
		stateAccounts:       false,
		stateScreensaver:    false, // the splash owns the whole frame
		stateHistory:        false,
		stateCommandPalette: false,
		stateCustomCommands: false,
		stateCheckpoints:    false,
		stateImagePreview:   false,
	}
	h := &home{}
	for st := stateDefault; st < numStates; st++ {
		want, classified := clickable[st]
		require.Truef(t, classified,
			"state %d is unclassified — decide whether its bar accepts clicks and add the row", int(st))
		h.state = st
		require.Equalf(t, want, h.hintBarClickState(),
			"state %d: hintBarClickState disagrees with the classification here", int(st))
	}
	// The walk runs first so an added state fails on its own message above; once
	// it passes, every live state has a row, and a size mismatch can only be
	// surplus rows for states the enum no longer holds.
	require.Len(t, clickable, int(numStates),
		"the classification holds rows for states the enum does not — a removed state left a stale row")
}

// A left-click on a hint-bar entry performs the same action as pressing its
// key: clicking "? help" on the empty bar opens the help overlay, exactly like
// pressing ?. This drives the whole path — handleMouse → Menu.KeyAtZone →
// synthKeyMsg → handleKeyPress.
func TestHintBarClick_MirrorsKeyPress(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.menu.SetInstance(nil) // empty bar: n / ? / q

	// The bar marks each entry as zone "hintbar:<key>" (ui.hintZoneID). View()
	// scans the composed frame itself (app.go), so just render until the ? entry's
	// bounds register — scanning its stripped output again would corrupt them.
	const helpZone = "hintbar:?"
	// Collapse zone-get and click into one Eventually (issue #434). A non-zero
	// zone is not necessarily *this* frame's: Scan hands bounds to an async
	// worker, and the manager is package-global, so Get can still be serving
	// bounds an earlier test's differently-sized frame registered. Clicking those
	// misses. Folding the click in means a miss just re-renders and retries.
	require.Eventually(t, func() bool {
		_ = h.View().Content
		zi := zone.Get(helpZone)
		if zi.IsZero() {
			return false
		}
		h.Update(testutil.MouseClick(zi.StartX, zi.StartY, tea.MouseLeft))
		return h.state == stateHelp
	}, testutil.ZoneClickTimeout, testutil.ZoneClickPoll, "clicking the ? hint must open help, like pressing ?")
}

// A click that lands on no hint-bar entry falls through to the normal row/tab
// handling and changes no state — the bar's zones don't swallow stray clicks.
func TestHintBarClick_MissIsInert(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.menu.SetInstance(nil)
	_ = h.View().Content // internally scans the frame's zones

	h.Update(testutil.MouseClick(0, 0, tea.MouseLeft))
	require.Equal(t, stateDefault, h.state, "a click off every hint entry must not open an overlay")
}

// Every key a bar can click through must be synthesizable, and this is the
// direction nothing used to assert.
//
// A hint-bar click does not dispatch the action directly: it re-injects the
// entry's key string and lets handleKeyPress route it (ui/menu.go's
// primaryDispatchKey picks the string, app_msgs.go's synthKeyMsg builds the
// message). So a key synthKeyMsg cannot spell is a bar entry that renders live
// and does nothing when clicked — silent, and invisible to every other guard
// here, because TestMenuBars_KeysExistInRegistry only checks the reverse
// direction: that a bar names a key the registry has.
//
// Sourcing the set from the dispatch map rather than from a list is the point.
// The bars can only name registry keys, so this covers them by construction and
// keeps covering them when the keymap changes — including when a user's config
// rebinds one, since the dispatch map is what an override rewrites.
func TestEveryBarKeyIsSynthesizable(t *testing.T) {
	check := func(k string) {
		t.Helper()
		msg, ok := synthKeyMsg(k)
		if !ok {
			t.Errorf("synthKeyMsg(%q) failed — a bar entry on that key would click dead", k)
			return
		}
		if got := msg.String(); got != k {
			t.Errorf("synthKeyMsg(%q) stringifies as %q — a click would fire the wrong action", k, got)
		}
	}
	for k := range keys.GlobalKeyStringsMap {
		check(k)
	}
	// The mode bars and the pane-focus variant render from their own tables
	// and never enter the dispatch map, so the loop above cannot see them.
	// Sourced from keys.ModeHintTables so a new table is swept without this
	// list knowing about it. Entries carrying no key are label-only ranges
	// like "a–z" that map to no single action and stay inert by design.
	for _, table := range keys.ModeHintTables() {
		for _, b := range table {
			for _, k := range b.Keys() {
				check(k)
			}
		}
	}
}
