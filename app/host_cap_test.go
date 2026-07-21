package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capVerdict is the single decision behind every creation site: an unlimited cap
// (Limit ≤ 0) always allows; a count that stays within Limit allows; going over a
// soft cap asks for confirmation; going over a hard cap is refused.
func TestCapVerdict(t *testing.T) {
	soft := func(n int) config.SessionCap { return config.SessionCap{Limit: n, Soft: true} }
	hard := func(n int) config.SessionCap { return config.SessionCap{Limit: n, Soft: false} }
	cases := []struct {
		name          string
		sc            config.SessionCap
		count, adding int
		want          capOutcome
	}{
		{"unlimited soft", soft(0), 100, 5, capAllow},
		{"explicit unlimited", hard(0), 100, 5, capAllow},
		{"soft under", soft(4), 2, 1, capAllow},
		{"soft at boundary", soft(4), 3, 1, capAllow}, // 3+1 == 4, not over
		{"soft over by one", soft(4), 4, 1, capConfirm},
		{"soft batch over", soft(4), 2, 3, capConfirm}, // 5 > 4
		{"hard under", hard(2), 0, 1, capAllow},
		{"hard at boundary", hard(2), 1, 1, capAllow},
		{"hard over", hard(2), 2, 1, capBlock},
		{"hard batch over", hard(2), 0, 3, capBlock},
	}
	for _, tc := range cases {
		if got := capVerdict(tc.sc, tc.count, tc.adding); got != tc.want {
			t.Errorf("%s: capVerdict(%+v, %d, %d) = %v, want %v",
				tc.name, tc.sc, tc.count, tc.adding, got, tc.want)
		}
	}
}

// With max_sessions unset, crossing the host-derived cap opens a confirmation
// (not a hard block): the create is staged, nothing spawns yet, and the form is
// dismissed behind the confirm.
func TestCreateForm_SoftCapOverAsksToConfirm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2                 // pin the derived cap so the test is machine-independent
	h.appConfig.MaxSessions = nil // unset → host-derived soft cap
	addStubInstances(t, h, 2)     // two live sessions == the cap
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h) // the third session is one over the soft cap of 2

	assert.Equal(t, stateConfirm, h.state, "crossing the soft cap confirms, it does not block")
	require.NotNil(t, h.pendingOverCap, "the create must be staged for the confirm")
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawns until confirmed")
	assert.Nil(t, h.textInputOverlay, "the form is dismissed behind the confirm")
}

// Confirming the host-capacity prompt spawns the staged session and consumes the
// stage.
func TestConfirmOverCap_SpawnsStagedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2)
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state)

	// Press y to confirm; the staged action yields proceedOverCapMsg, which Update
	// then spawns.
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.NotNil(t, cmd, "confirming must return the staged spawn command")
	h.Update(cmd())

	assert.Equal(t, before+1, h.list.NumInstances(), "confirming spawns the staged session")
	assert.Nil(t, h.pendingOverCap, "the stage is consumed")
	assert.Equal(t, stateDefault, h.state)
}

// Declining the host-capacity prompt spawns nothing and drops the stage.
func TestDeclineOverCap_SpawnsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2)
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}) // decline

	assert.Equal(t, before, h.list.NumInstances(), "declining spawns nothing")
	assert.NotEqual(t, stateConfirm, h.state, "the confirm closes")
}

// Paused sessions impose no host load, so they do not count toward the soft cap:
// two live + two paused under a derived cap of 3 still has room for one more
// without a confirmation. (A hard cap, by contrast, counts every session.)
func TestSoftCap_PausedSessionsDoNotCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 3
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2) // two live
	for _, inst := range h.list.GetInstances() {
		inst.SetStatus(session.Paused) // park the two stubs...
	}
	addStubInstances(t, h, 2) // ...then add two more live ones
	before := h.list.NumInstances()
	require.Equal(t, 4, before, "4 total: 2 paused + 2 live")

	typeString(h, "race")
	ctrlS(h) // 2 live + 1 new == cap 3

	assert.Equal(t, stateDefault, h.state, "paused sessions don't count, so this fits without a confirm")
	assert.Equal(t, before+1, h.list.NumInstances(), "the session spawns silently")
}

// An explicit max_sessions of 0 is the escape hatch: unlimited, and being
// explicit it never opens the host-capacity confirmation even far over the
// derived cap.
func TestCreateForm_ExplicitUnlimitedSkipsConfirm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	zero := 0
	h.appConfig.MaxSessions = &zero // explicit unlimited
	addStubInstances(t, h, 5)       // well over the derived cap
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h)

	assert.Equal(t, stateDefault, h.state, "explicit unlimited never confirms")
	assert.Nil(t, h.pendingOverCap)
	assert.Equal(t, before+1, h.list.NumInstances(), "the session spawns silently")
}
