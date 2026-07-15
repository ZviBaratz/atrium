package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [ / ] are the unshifted twins of { / }, completing the reorder ladder:
// J/K (session) → {/} (repo group) → [/] (account cluster).
func TestKeymap_AccountReorderBrackets(t *testing.T) {
	require.Equal(t, keys.KeyMoveAccountUp, keys.GlobalKeyStringsMap["["])
	require.Equal(t, keys.KeyMoveAccountDown, keys.GlobalKeyStringsMap["]"])
}

// ] moves the selected session's whole account cluster down and persists the chosen
// order to state — end-to-end through handleKeyPress.
func TestAccountReorder_BracketMovesClusterAndPersists(t *testing.T) {
	h := accountGroupedHome(t) // api|work, infra|personal (one block each)
	h.state = stateDefault
	h.list.SetSelectedInstance(0) // api|work, the leading cluster

	pressKey(h, ']') // KeyMoveAccountDown

	require.Equal(t, []string{"personal", "work"}, h.list.AccountOrder(),
		"the work cluster moved below personal")
	require.Equal(t, []string{"personal", "work"}, h.appState.GetAccountOrder(),
		"the chosen order is persisted to state, not the instance array")
}

// [ mirrors ] in the opposite direction.
func TestAccountReorder_BracketMovesClusterUp(t *testing.T) {
	h := accountGroupedHome(t)
	h.state = stateDefault
	h.list.SetSelectedInstance(1) // infra|personal, the trailing cluster

	pressKey(h, '[') // KeyMoveAccountUp

	require.Equal(t, []string{"personal", "work"}, h.appState.GetAccountOrder())
}

// A cluster move rewrites only the stored order — the canonical session order (what is
// persisted as instances, and what repo mode renders) must be untouched.
func TestAccountReorder_LeavesSessionOrderUntouched(t *testing.T) {
	h := accountGroupedHome(t)
	h.state = stateDefault
	before := append([]string(nil), instanceTitles(h)...)
	h.list.SetSelectedInstance(0)

	pressKey(h, ']')

	assert.Equal(t, before, instanceTitles(h), "an account move must not reorder sessions")
}

// Without account grouping there are no clusters to reorder, so [ / ] must explain
// itself rather than silently doing nothing.
func TestAccountReorder_ExplainsWhenNotAccountGrouped(t *testing.T) {
	h := accountGroupedHome(t)
	h.list.SetGroupMode("repo") // two accounts present, but nothing clusters them
	h.state = stateDefault
	h.list.SetSelectedInstance(0)

	pressKey(h, ']')

	require.True(t, h.menu.HasNotice(), "a cluster move with no clustering must explain itself")
	assert.Contains(t, h.menu.String(), "account grouping")
	assert.Empty(t, h.list.AccountOrder(), "a refused move records no order")
}

// The payoff of holding the order in state: a restart restores the chosen cluster
// order rather than falling back to creation order. Driven through the real startup
// path (assembleHome) so the restore wiring is covered, not just the list primitive.
func TestAccountReorder_OrderSurvivesRestart(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.GroupMode = config.GroupModeAccount
	st := config.DefaultState()
	// The order a previous run chose: personal ahead of work, plus an account whose
	// sessions are all gone — it must not disturb the view, but must keep its slot.
	st.AccountOrder = []string{"personal", "ghost", "work"}

	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	newInst := func(repo, account string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: repo, Path: "/tmp/" + repo, Program: "echo",
		})
		require.NoError(t, err)
		inst.SetClaudeAccount(account, "", false)
		return inst
	}
	// Canonical (creation) order puts work first — only the stored order flips it.
	instances := []*session.Instance{newInst("api", "work"), newInst("sideproj", "personal")}

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)

	got := h.list.GetInstances()
	require.Len(t, got, 2)
	require.Equal(t, "sideproj", filepath.Base(got[0].Path),
		"the restored order must lead with personal, not creation order")
	require.Equal(t, "api", filepath.Base(got[1].Path))
	require.Equal(t, []string{"personal", "ghost", "work"}, h.list.AccountOrder(),
		"an account with no live sessions keeps its slot for when it returns")
}

// A mixed-account repo renders as one cluster despite holding two accounts. [ / ] cannot
// move it, and the user must be told so — the failure this guards is a dead key: the move
// refuses, no notice fires, and nothing on screen explains the silence.
func TestAccountReorder_ExplainsWhenOnlyOneCluster(t *testing.T) {
	h := newCreateFormHome(t)
	h.appState = config.DefaultState()
	// Both sessions live in ONE repo, so they form a single block whose anchor (work)
	// names the sole cluster — even though personal is also present.
	for _, acct := range []string{"work", "personal"} {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s-" + acct, Path: "/tmp/shared", Program: "echo",
		})
		require.NoError(t, err)
		inst.SetClaudeAccount(acct, "", false)
		h.list.AddInstance(inst)
	}
	h.list.SetGroupMode("account")
	h.state = stateDefault
	h.list.SetSelectedInstance(0)

	pressKey(h, ']')

	require.True(t, h.menu.HasNotice(), "a refused cluster move must explain itself, not go silent")
	assert.Contains(t, h.menu.String(), "one account cluster")
	assert.Empty(t, h.list.AccountOrder(), "a refused move records no order")
}

// instanceTitles returns the canonical (persisted) session order by title.
func instanceTitles(h *home) []string {
	out := make([]string, 0)
	for _, inst := range h.list.InstancesForPersist() {
		out = append(out, inst.Title)
	}
	return out
}
