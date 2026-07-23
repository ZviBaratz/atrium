package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poolHome builds a create-form home routed to a two-member "work" pool where
// work-1 is the catch-all (no rules), so ResolveClaudePool always lands on it.
func poolHome(t *testing.T) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	return h
}

// startDirect spawns a direct (non-git) session through startNewSession with the
// given account selection and returns the resulting instance so the caller can read
// back the pinned account/pool. It asserts exactly one session was created.
func startDirect(t *testing.T, h *home, sel *overlay.AccountSelection) *session.Instance {
	t.Helper()
	before := h.list.NumInstances()
	_, err := h.startNewSession("s", t.TempDir(), true, "echo", "", "", sel, false)
	require.NoError(t, err)
	require.Equal(t, before+1, h.list.NumInstances())
	return h.list.GetInstances()[h.list.NumInstances()-1]
}

func TestStartNewSession_RotatesAndAdvances(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)

	first := startDirect(t, h, nil)
	assert.Equal(t, "work-1", first.ClaudeAccountName())
	assert.Equal(t, "work", first.ClaudeAccountPool())

	second := startDirect(t, h, nil)
	assert.Equal(t, "work-2", second.ClaudeAccountName(), "cursor advanced to the sibling")
	assert.Equal(t, "work", second.ClaudeAccountPool())
}

func TestStartNewSession_SkipsLimited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", "")) // indefinite

	inst := startDirect(t, h, nil)
	assert.Equal(t, "work-2", inst.ClaudeAccountName(), "limited work-1 skipped")
}

func TestStartNewSession_PinnedBypassesAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", "")) // even limited

	pin := &overlay.AccountSelection{Pool: "work", Member: &config.ClaudeAccount{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"}}
	inst := startDirect(t, h, pin)
	assert.Equal(t, "work-1", inst.ClaudeAccountName(), "a deliberate pin runs even on a limited account")
	assert.Equal(t, "work", inst.ClaudeAccountPool())
}

func TestSoonestResetMember(t *testing.T) {
	now := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	members := []config.ClaudeAccount{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	avail := map[string]config.AccountAvailability{
		"a": {Limited: true, Until: "2026-07-23T18:00:00Z"},
		"b": {Limited: true, Until: "2026-07-23T17:00:00Z"},
		"c": {Limited: true}, // indefinite sorts last
	}
	assert.Equal(t, 1, soonestResetMember(members, avail, now), "b resets soonest")

	allIndef := map[string]config.AccountAvailability{"a": {Limited: true}, "b": {Limited: true}, "c": {Limited: true}}
	assert.Equal(t, 0, soonestResetMember(members, allIndef, now), "all indefinite -> fallback 0")
}

func TestCreateForm_AllExhaustedAsksConfirm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	before := h.list.NumInstances()

	typeString(h, "doomed")
	ctrlS(h)

	assert.Equal(t, stateConfirm, h.state, "a fully-limited pool asks before spawning")
	require.NotNil(t, h.pendingExhausted, "the plan is staged behind the confirm")
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawned yet")
	assert.Nil(t, h.textInputOverlay, "form dismissed (stashed as restorable draft)")
}
