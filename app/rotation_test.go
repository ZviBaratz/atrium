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

// TestStartNewSession_NoPoolStaysDormant is the dormancy guarantee: a user with
// claude_accounts but NO pool on any of them must gain no new state keys. The
// singleton "solo" account routes as the catch-all, so without the Fix-1 guards it
// would stamp claude_account_pool="solo" and write account_rotation["solo"]=1 (both
// functionally invisible, since a singleton pool name equals the account name — but
// they add keys to state.json). With the guards the pool stamp is empty (accountKey
// falls back to the account name), and neither a rotation cursor nor an availability
// entry is persisted — byte-for-byte pre-feature behavior.
func TestStartNewSession_NoPoolStaysDormant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "solo", ConfigDir: "~/.claude"}, // no Pool, no rules → the catch-all
	}

	inst := startDirect(t, h, nil)

	assert.Equal(t, "solo", inst.ClaudeAccountName(), "the catch-all account still routes")
	assert.Equal(t, "", inst.ClaudeAccountPool(), "no real pool → no cluster-pool stamp (dormant)")
	assert.Equal(t, 0, h.appState.GetAccountRotation("solo"), "no rotation cursor persisted for a singleton")
	assert.Len(t, h.appState.GetAccountAvailability(), 0, "no availability entry persisted")
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

// A fan-out batch over an explicit HARD max_sessions must be hard-refused even when
// the routed pool is fully rate-limited: the session cap is evaluated before the
// all-exhausted gate, so the exhausted confirm never opens (accepting it would spawn
// past the user's explicit limit — spawnVariants does not re-check the cap). Without
// that ordering the identical batch would be blocked in the non-exhausted case but
// slip through here. Regression guard for the safeguard bypass.
func TestCreateForm_HardCapBeatsExhaustedGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	require.NoError(t, h.appState.SetAccountLimited("work-1", "")) // whole pool exhausted
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	maxN := 2
	h.appConfig.MaxSessions = &maxN // explicit HARD cap
	addStubInstances(t, h, 2)       // already at the hard cap
	before := h.list.NumInstances()

	typeString(h, "doomed")
	h.textInputOverlay.FocusVariants()
	plusKey(h) // claude 1 -> 2: a batch of 2, so 2 existing + 2 == 4 > cap 2
	ctrlS(h)

	assert.NotEqual(t, stateConfirm, h.state, "a hard cap must refuse, not open the exhausted confirm")
	assert.Nil(t, h.pendingExhausted, "nothing is staged behind an exhausted confirm")
	assert.Equal(t, before, h.list.NumInstances(), "nothing is spawned past the hard cap")
	require.NotNil(t, h.textInputOverlay, "the form stays open on a hard-cap refusal")
	assert.Contains(t, h.textInputOverlay.VariantError(), "max_sessions")
}

// Accepting the all-exhausted confirm spawns the staged session pinned to the
// soonest-to-reset member. work-2 resets before work-1, so the batch must land on
// work-2. Mirrors the over-cap accept in host_cap_test.go (press y → run the staged
// command → feed its message through Update).
func TestCreateForm_ExhaustedAcceptSpawnsSoonest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	// Both limited into the future (so the pool is exhausted now), with work-2 the
	// soonest to reset.
	later := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	require.NoError(t, h.appState.SetAccountLimited("work-1", later))
	require.NoError(t, h.appState.SetAccountLimited("work-2", soon))
	before := h.list.NumInstances()

	typeString(h, "doomed")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state, "a fully-limited pool asks before spawning")
	require.NotNil(t, h.pendingExhausted, "the plan is staged behind the confirm")

	// Accept: y yields the staged action, whose message spawns via Update.
	_, cmd := h.Update(textMsg("y"))
	require.NotNil(t, cmd, "accepting must return the staged spawn command")
	h.Update(cmd())

	assert.Equal(t, before+1, h.list.NumInstances(), "accepting spawns the staged session")
	assert.Nil(t, h.pendingExhausted, "the stage is consumed")
	inst := h.list.GetInstances()[h.list.NumInstances()-1]
	assert.Equal(t, "work-2", inst.ClaudeAccountName(), "pinned to the soonest-to-reset member")
	assert.Equal(t, "work", inst.ClaudeAccountPool())
}
