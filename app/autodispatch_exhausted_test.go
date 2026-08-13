package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #483: smart auto-dispatch called startNewSession directly and never ran
// gateAllExhausted, so a fully rate-limited pool spawned silently on the rotation
// cursor's member instead of raising the confirm and pinning the soonest-to-reset
// one. Every gate test that existed drove the create form, which is exactly why the
// other spawn path could diverge for a whole release without a red test.
//
// newSmartHome deliberately configures no claude_accounts, so these build the pool
// on top of it — an auto-dispatch home that can actually reach the gate. The "box"
// session is what makes "Review box#123" a *confident* local match: candidates come
// from existing sessions' repos, and without one the line is unmatched and opens the
// form instead of ever reaching autoDispatch.
func autoDispatchPoolHome(t *testing.T) *home {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newSmartHome(t)
	on := true
	h.appConfig.SmartDispatchAuto = &on
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	addDirectInstance(t, h, "other", mkNamedDir(t, "box"))
	return h
}

func TestAutoDispatch_AllExhaustedAsksConfirm(t *testing.T) {
	h := autoDispatchPoolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")

	assert.Equal(t, stateConfirm, h.state,
		"opting out of the form is not opting out of the account gates")
	require.NotNil(t, h.pendingExhausted, "the plan is staged behind the confirm")
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawned yet")
}

// stateConfirm being set is not the same as the dialog being on screen, and this
// entry into it is new: every other confirm arrives from a create form, this one
// from statePrompt with a smart-dispatch overlay open. So render the frame and read
// what it says — the reason and the member accepting would use, which is the whole
// content the user makes the decision on.
//
// Deliberately *not* asserted: that the dispatch overlay does not render underneath.
// View() selects exactly one overlay by m.state, so in stateConfirm a stale
// textInputOverlay pointer cannot reach the frame — an assertion about it would pass
// on any input, including one where confirmAllExhausted stopped clearing it (checked
// by mutation, it did).
func TestAutoDispatch_AllExhaustedConfirmActuallyRenders(t *testing.T) {
	h := autoDispatchPoolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})

	h.handleSmartDispatchSubmit("Review box#123")
	require.Equal(t, stateConfirm, h.state)

	view := h.View().Content
	assert.Contains(t, view, "all work accounts are rate-limited",
		"the dialog states why it is asking")
	assert.Contains(t, view, "create anyway on work-1?",
		"and which member accepting would use")
}

// The divergence had two halves — no confirm, and a different member — so the
// fixture forces them apart: the cursor sits on work-1 (the pre-fix answer) while
// work-2 resets soonest (the confirm's answer). A fix that raised the confirm but
// kept the cursor's member would pass a same-member fixture.
func TestAutoDispatch_ExhaustedAcceptSpawnsSoonest(t *testing.T) {
	h := autoDispatchPoolHome(t)
	later := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	require.NoError(t, h.appState.SetAccountLimited("work-1", later))
	require.NoError(t, h.appState.SetAccountLimited("work-2", soon))
	require.Equal(t, 0, h.appState.GetAccountRotation("work"), "cursor is on work-1")
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.Update(textMsg("y"))
	require.NotNil(t, cmd, "accepting must return the staged spawn command")
	h.Update(cmd())

	require.Equal(t, before+1, h.list.NumInstances(), "accepting spawns the staged session")
	assert.Nil(t, h.pendingExhausted, "the stage is consumed")
	inst := h.list.GetInstances()[h.list.NumInstances()-1]
	assert.Equal(t, "work-2", inst.ClaudeAccountName(),
		"pinned to the soonest-to-reset member, not the rotation cursor's")
}

func TestAutoDispatch_ExhaustedDeclineCreatesNothing(t *testing.T) {
	h := autoDispatchPoolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")
	require.Equal(t, stateConfirm, h.state)

	h.Update(textMsg("n"))

	assert.Equal(t, before, h.list.NumInstances(), "declining creates nothing")
	assert.Equal(t, stateDefault, h.state)
}

// A pool of one has nothing to rotate to, so being limited is not "exhausted" in
// the sense the confirm is about — gateAllExhausted exempts it, and auto-dispatch
// must inherit that exemption rather than start asking about every single-account
// config. This is the same exemption startNewSession's fail-closed refusal carries.
func TestAutoDispatch_SingletonLimitedPoolStillSpawns(t *testing.T) {
	h := autoDispatchPoolHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{{Name: "solo", ConfigDir: "~/.claude-solo"}}
	require.NoError(t, h.appState.SetAccountLimited("solo", ""))
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")

	assert.Equal(t, stateDefault, h.state, "a pool of one has nothing to rotate to")
	assert.Nil(t, h.pendingExhausted)
	assert.Equal(t, before+1, h.list.NumInstances())
}

// A hard cap must never be answerable with the exhausted confirm, whose accept path
// spawns without re-checking the cap — the same safeguard bypass
// TestCreateForm_HardCapBeatsExhaustedGate pins on the form. On this path the
// pre-route guard in handleSmartDispatchSubmit is what refuses first, so this is an
// outcome guard rather than an ordering one: autoDispatch's own capBlock branch is
// the backstop behind it, unreachable while that guard stands.
func TestAutoDispatch_HardCapNeverOpensTheExhaustedConfirm(t *testing.T) {
	h := autoDispatchPoolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	maxN := 1
	h.appConfig.MaxSessions = &maxN // already at the cap: the box session occupies it
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")

	assert.NotEqual(t, stateConfirm, h.state, "a hard cap refuses; it does not open the exhausted confirm")
	assert.Nil(t, h.pendingExhausted)
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawned past the hard cap")
}

// And the other end of the order: the exhausted confirm must be staged ahead of the
// soft-cap one. Accepting a soft-cap confirm goes straight to spawnVariants, which
// re-runs no gate — so if the soft cap were asked first, saying yes would spawn on
// a fully rate-limited pool having never been asked about it.
func TestAutoDispatch_ExhaustedBeatsSoftCap(t *testing.T) {
	h := autoDispatchPoolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	h.appConfig.MaxSessions = nil // host-derived soft cap, already reached
	h.hostCap = 1

	h.handleSmartDispatchSubmit("Review box#123")

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.pendingExhausted, "the exhausted confirm is the one staged")
	assert.Nil(t, h.pendingOverCap, "the soft-cap confirm must not have won the race")
}

// Both callers gate before reaching startNewSession, so this is the backstop: the
// branch that used to spawn silently on the cursor's member now refuses. "Should be
// impossible" is exactly what it quietly assumed while auto-dispatch skipped the
// gate, so a third caller that forgets gets an error it has to handle rather than a
// session on an account the user marked unusable.
func TestStartNewSession_RefusesUnpinnedAllLimitedPool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	before := h.list.NumInstances()

	_, err := h.startNewSession("s", t.TempDir(), true, false, "echo", "", "", nil, false, nil)

	require.Error(t, err, "an unpinned spawn onto a fully-limited pool must not be silent")
	assert.Contains(t, err.Error(), "work", "the refusal names the pool")
	assert.Equal(t, before, h.list.NumInstances(), "nothing is created by a refused spawn")
}

// The exemption that keeps the backstop from becoming a new bug. SelectPoolMember
// reports allLimited for a singleton pool whose one account is limited, but a pool
// of one has nothing to rotate to — gateAllExhausted exempts exactly that case, and
// refusing here instead would break every single-account config the moment its
// owner pressed `l`.
func TestStartNewSession_SingletonLimitedAccountStillSpawns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{{Name: "solo", ConfigDir: "~/.claude-solo"}}
	require.NoError(t, h.appState.SetAccountLimited("solo", ""))

	inst := startDirect(t, h, nil)

	assert.Equal(t, "solo", inst.ClaudeAccountName(), "a lone limited account is still the only choice")
}

// A deliberate member pin already bypasses availability, and the confirm's own
// accept path is a pin — so the backstop must not refuse it, or accepting the
// confirm this PR adds would fail to spawn.
func TestStartNewSession_PinnedMemberBypassesTheBackstop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	pinned := h.appConfig.ClaudeAccounts[1]

	inst := startDirect(t, h, &overlay.AccountSelection{Pool: "work", Member: &pinned})

	assert.Equal(t, "work-2", inst.ClaudeAccountName())
}
