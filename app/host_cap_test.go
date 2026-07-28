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

// resumeCapConfirm is capVerdict read from the resume side (#463). Two things differ
// from a creation, and the table pins both: a resume grows only the *live* population,
// so it is measured against the live count; and it is measured against the *soft* cap
// alone, because a hard cap counts every session (paused included) and so cannot be
// crossed by restoring sessions that already count. It never blocks.
func TestResumeCapConfirm(t *testing.T) {
	soft := func(n int) config.SessionCap { return config.SessionCap{Limit: n, Soft: true} }
	hard := func(n int) config.SessionCap { return config.SessionCap{Limit: n, Soft: false} }
	cases := []struct {
		name    string
		sc      config.SessionCap
		live, n int
		want    bool
	}{
		{"soft under", soft(4), 1, 2, false},
		{"soft at boundary", soft(4), 3, 1, false}, // 3+1 == 4, not over
		{"soft over by one", soft(4), 4, 1, true},
		{"soft batch over from nothing live", soft(2), 0, 3, true},
		{"soft unlimited", soft(0), 5, 5, false},
		{"hard cap: resume adds no counted session", hard(2), 2, 3, false},
		{"hard cap already exceeded by total", hard(2), 2, 1, false},
		{"explicit unlimited", hard(0), 9, 9, false},
	}
	for _, tc := range cases {
		if got := resumeCapConfirm(tc.sc, tc.live, tc.n); got != tc.want {
			t.Errorf("%s: resumeCapConfirm(%+v, %d, %d) = %v, want %v",
				tc.name, tc.sc, tc.live, tc.n, got, tc.want)
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
	assert.Nil(t, h.stashedDraft, "a committed create clears the draft stashed behind the confirm")
	assert.Nil(t, config.LoadState().GetDraft(), "the on-disk draft is cleared on commit")
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

// Declining the host-capacity prompt must not discard the typed form. The soft cap
// is the friendlier alternative to a hard-cap block (which keeps the form open), so
// it must not lose more than the block it replaces: the title and prompt are stashed
// in memory and mirrored to disk, exactly as a non-destructive Escape would.
func TestDeclineOverCap_PreservesDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2)

	typeString(h, "race")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}) // decline

	require.NotNil(t, h.stashedDraft, "declining must stash the dirty form, not discard it")
	assert.Equal(t, "race", h.stashedDraft.GetTitle(), "the typed title survives the decline")
	if d := config.LoadState().GetDraft(); assert.NotNil(t, d, "the draft is mirrored to disk") {
		assert.Equal(t, "race", d.Title, "the persisted draft holds the typed title")
	}
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

// The over-cap dialog's ',' is a real deep link: it cancels the create, opens the settings
// panel, and lands on the Session limit row — the setting the message just named. Without the
// landing it would be a shortcut to a 13-entry rail, which is roughly what the old
// "set max_sessions in config.json" tail already amounted to.
func TestOverCapDialogCommaOpensTheSessionLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2)
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})

	assert.Equal(t, stateSettings, h.state, "',' leaves the dialog for the settings panel")
	require.NotNil(t, h.settingsOverlay)
	assert.Equal(t, "max_sessions", h.settingsOverlay.SelectedRowKey(),
		"and lands on the row the message named")
	assert.Nil(t, h.confirmationOverlay, "the dialog is dismissed")
	assert.Equal(t, before, h.list.NumInstances(), "',' spawns nothing — it is a cancel")
}

// ',' is armed by the cap dialog and nothing else: an unrelated confirmation must not have a
// key that silently cancels it and opens a panel.
func TestCommaIsInertInAnUnarmedConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.confirmAction("Do the thing?", instantAction, func() tea.Msg { return nil })
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.Equal(t, stateConfirm, h.state, "an unarmed dialog ignores ','")
	assert.NotNil(t, h.confirmationOverlay)
}
