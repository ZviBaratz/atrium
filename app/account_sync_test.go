package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renamedPoolConfig is the shape that produced #470: a work account that was
// renamed (work -> zvi.baratz) and moved into a pool, a second member of that
// pool, and an ungrouped catch-all. Dirs use ~ so they resolve under the
// sandboxed HOME, exactly as a real config.json does.
func renamedPoolConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.GroupMode = config.GroupModeAccount
	cfg.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "zvi.baratz", ConfigDir: "~/.claude-work", RemoteMatches: []string{"quantivly"}, Pool: "quantivly"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
		{Name: "zvi.baratz2", ConfigDir: "~/.claude-work2", RemoteMatches: []string{"quantivly"}, Pool: "quantivly"},
	}
	return cfg
}

// homeDir is the sandboxed HOME the suite runs under (see TestMain), the root the
// ~-prefixed config dirs above expand to.
func homeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return home
}

// stampedInstance builds a session carrying the account identity a past launch
// stamped on it — the persisted state a rename leaves behind.
func stampedInstance(t *testing.T, repo, account, configDir, pool string, isDefault bool) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: repo, Path: "/tmp/" + repo, Program: "echo",
	})
	require.NoError(t, err)
	inst.SetClaudeAccount(account, configDir, isDefault)
	inst.SetClaudeAccountPool(pool)
	return inst
}

// loadStamped rehydrates paused sessions through the real load path, so they come
// back marked started — the only sessions SaveInstances writes again, and therefore
// the only shape a test about self-healing state.json can use. Paused rows reattach
// without touching tmux or git.
func loadStamped(t *testing.T, st *config.State, rows []session.InstanceData) (*session.Storage, []*session.Instance) {
	t.Helper()
	for i := range rows {
		rows[i].Status = session.Paused
		rows[i].Program = "echo"
		rows[i].Path = "/nonexistent/" + rows[i].Title
		rows[i].Worktree = session.GitWorktreeData{
			RepoPath:     "/nonexistent/" + rows[i].Title,
			WorktreePath: "/nonexistent/wt-" + rows[i].Title,
			SessionName:  rows[i].Title, BranchName: "zvi/" + rows[i].Title,
		}
	}
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NoError(t, st.SaveInstances(raw))

	storage, err := session.NewStorage(st)
	require.NoError(t, err)
	instances, err := storage.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, instances, len(rows))
	return storage, instances
}

// The reported failure (#470): the work account was renamed and moved into pool
// quantivly, so its existing sessions — still stamped with the old name and no
// pool — clustered under a `work` divider config no longer has, beside a second
// `quantivly` divider holding only the newly created session.
//
// Driven through the real startup path (assembleHome) because the fix has to land
// before the first cluster build, and because the stale cluster ORDER is half the
// bug: healing the stamps alone turns the leading `work` key into an unlisted
// `quantivly` one, which clusterByAccount appends last — flipping the user's
// chosen slot from top to bottom.
func TestAccountSync_RenamedPoolAbsorbsStaleSessions(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	st := config.DefaultState()
	st.AccountOrder = []string{"work", "personal"} // the slot [ / ] gave the work cluster

	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	// Creation order interleaves the accounts so the assertion on cluster order
	// cannot pass by accident.
	stale1 := stampedInstance(t, "hub", "work", filepath.Join(home, ".claude-work"), "", false)
	stale2 := stampedInstance(t, "platform", "work", filepath.Join(home, ".claude-work"), "", false)
	fresh := stampedInstance(t, "iqa", "zvi.baratz2", filepath.Join(home, ".claude-work2"), "quantivly", false)
	mine := stampedInstance(t, "atrium", "personal", filepath.Join(home, ".claude-personal"), "", true)
	instances := []*session.Instance{stale1, stale2, fresh, mine}

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)

	for _, inst := range []*session.Instance{stale1, stale2} {
		assert.Equal(t, "zvi.baratz", inst.ClaudeAccountName(),
			"the badge must name the account config calls this login now")
		assert.Equal(t, "quantivly", inst.ClaudeAccountPool(),
			"a session whose account moved into a pool clusters under that pool")
		assert.Equal(t, filepath.Join(home, ".claude-work"), inst.ClaudeConfigDir(),
			"the injected CLAUDE_CONFIG_DIR is the anchor and must never be rewritten")
	}

	assert.Equal(t, []string{"quantivly", "personal"}, h.list.AccountOrder(),
		"the renamed cluster keeps the slot its old key held, rather than falling to the bottom")

	got := h.list.GetInstances()
	require.Len(t, got, 4)
	titles := make([]string, 0, len(got))
	for _, inst := range got {
		titles = append(titles, inst.Title)
	}
	assert.Equal(t, []string{"hub", "platform", "iqa", "atrium"}, titles,
		"all three quantivly sessions render as one leading cluster")
}

// A rename must heal; a DELETION must not blank a badge. With the account gone from
// config there is nothing to re-derive from, so the last-known stamp stands — it is
// still the truth about the login the session runs under.
func TestAccountSync_KeepsStampWhenAccountDeleted(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	inst := stampedInstance(t, "old", "retired", filepath.Join(home, ".claude-retired"), "legacy", false)

	sync := syncAccountStamps(cfg, []*session.Instance{inst})

	assert.False(t, sync.changed())
	assert.Equal(t, "retired", inst.ClaudeAccountName())
	assert.Equal(t, "legacy", inst.ClaudeAccountPool())
}

// The pass must be inert for a user who configures no accounts at all — that is the
// dormancy promise the pools feature was built on.
func TestAccountSync_DormantWithoutAccounts(t *testing.T) {
	inst := stampedInstance(t, "solo", "", "", "", false)
	assert.False(t, syncAccountStamps(config.DefaultConfig(), []*session.Instance{inst}).changed())
	assert.False(t, syncAccountStamps(nil, []*session.Instance{inst}).changed())
}

// Removing an account from its pool is a config edit too: the pool empties and the
// cluster key falls back to the account name, carrying its order slot the same way.
func TestAccountSync_UnpoolingFallsBackToName(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	cfg.ClaudeAccounts[0].Pool = "" // zvi.baratz leaves the pool
	inst := stampedInstance(t, "hub", "zvi.baratz", filepath.Join(home, ".claude-work"), "quantivly", false)

	sync := syncAccountStamps(cfg, []*session.Instance{inst})

	assert.Equal(t, "zvi.baratz", inst.AccountClusterKey())
	assert.Equal(t, map[string]string{"quantivly": "zvi.baratz"}, sync.clusterMoves)
}

// Re-running the pass changes nothing: the anchor it reads is never one of the
// fields it writes, which is what makes healing safe to persist.
func TestAccountSync_Idempotent(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	inst := stampedInstance(t, "hub", "work", filepath.Join(home, ".claude-work"), "", false)

	require.True(t, syncAccountStamps(cfg, []*session.Instance{inst}).changed())
	assert.False(t, syncAccountStamps(cfg, []*session.Instance{inst}).changed(),
		"a second pass has nothing left to move")
}

// A cluster key that still holds a session has NOT vanished, so its slot in the
// stored order is not free to carry — here one `work` session heals into the pool
// while a second (its dir gone from config) stays behind under `work`.
func TestAccountSync_PopulatedOldClusterKeepsItsSlot(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	healed := stampedInstance(t, "hub", "work", filepath.Join(home, ".claude-work"), "", false)
	stranded := stampedInstance(t, "legacy", "work", filepath.Join(home, ".claude-gone"), "", false)

	sync := syncAccountStamps(cfg, []*session.Instance{healed, stranded})

	require.Equal(t, "quantivly", healed.AccountClusterKey())
	require.Equal(t, "work", stranded.AccountClusterKey())
	assert.Empty(t, sync.clusterMoves, "work still has a session, so nothing may take its slot")

	order, moved := remapAccountOrder([]string{"work", "personal"}, sync.clusterMoves)
	assert.False(t, moved)
	assert.Equal(t, []string{"work", "personal"}, order)
}

func TestRemapAccountOrder(t *testing.T) {
	for _, tc := range []struct {
		desc  string
		order []string
		moves map[string]string
		want  []string
		moved bool
	}{
		{"a renamed cluster keeps its slot",
			[]string{"work", "personal"}, map[string]string{"work": "quantivly"},
			[]string{"quantivly", "personal"}, true},
		{"the earlier slot wins and the duplicate is dropped",
			[]string{"quantivly", "personal", "work"}, map[string]string{"work": "quantivly"},
			[]string{"quantivly", "personal"}, true},
		{"an account with no live sessions keeps its slot for when it returns",
			[]string{"ghost", "work"}, map[string]string{"work": "quantivly"},
			[]string{"ghost", "quantivly"}, true},
		{"nothing moved",
			[]string{"work", "personal"}, nil,
			[]string{"work", "personal"}, false},
		{"no stored order to carry",
			nil, map[string]string{"work": "quantivly"},
			nil, false},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			got, moved := remapAccountOrder(tc.order, tc.moves)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.moved, moved)
		})
	}
}

// state.json must self-heal on the first launch after a rename: `atrium ls` and the
// autoyes daemon read the stored rows raw, from another process, and never
// re-derive — so the healed identities have to reach disk, not just the list.
func TestAccountSync_FlushPersistsHealedRows(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	st := config.DefaultState()
	st.AccountOrder = []string{"work", "personal"}
	storage, instances := loadStamped(t, st, []session.InstanceData{
		{Title: "hub", ClaudeAccount: "work", ClaudeConfigDir: filepath.Join(home, ".claude-work")},
	})

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)
	cs := withCapturingStore(t, h)

	h.flushAccountStamps()

	require.Equal(t, 1, cs.saves, "the healed rows are written once")
	var rows []session.InstanceData
	require.NoError(t, json.Unmarshal(cs.last, &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "zvi.baratz", rows[0].ClaudeAccount)
	assert.Equal(t, "quantivly", rows[0].ClaudeAccountPool)
	assert.Equal(t, filepath.Join(home, ".claude-work"), rows[0].ClaudeConfigDir)
	assert.Equal(t, []string{"quantivly", "personal"}, st.GetAccountOrder())

	h.flushAccountStamps()
	assert.Equal(t, 1, cs.saves, "the flush is one-shot")
}

// A clean launch (config and stamps already agree) must write nothing — otherwise
// every startup would rewrite state.json for no reason.
func TestAccountSync_FlushSilentWhenNothingHealed(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	st := config.DefaultState()
	storage, instances := loadStamped(t, st, []session.InstanceData{
		{Title: "iqa", ClaudeAccount: "zvi.baratz2", ClaudeConfigDir: filepath.Join(home, ".claude-work2"),
			ClaudeAccountPool: "quantivly"},
	})

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)
	cs := withCapturingStore(t, h)

	h.flushAccountStamps()

	assert.Equal(t, 0, cs.saves)
}

// The payoff of the live path: renaming an account in the @ panel regroups the open
// sessions immediately, with a notice explaining the rearrangement, rather than
// leaving the account split across two headers until the next launch.
func TestAccountsPanel_RenameRegroupsOpenSessions(t *testing.T) {
	resetSettingsTestState(t)
	home := homeDir(t)
	h := newSettingsTestHome()
	h.appConfig.GroupMode = config.GroupModeAccount
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}
	for _, spec := range []struct{ repo, acct, dir string }{
		{"api", "work", filepath.Join(home, ".claude-work")},
		{"sideproj", "personal", filepath.Join(home, ".claude-personal")},
	} {
		h.list.AddInstance(stampedInstance(t, spec.repo, spec.acct, spec.dir, "", false))()
	}
	h.list.SetGroupMode("account")
	cs := withCapturingStore(t, h)

	// The user edits the work account: new name, and a pool. Driven through the real
	// @-panel key path, so the wiring in handleAccountsState is covered too.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	require.Equal(t, stateAccounts, h.state)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}) // edit row 0
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})                     // focus starts on Name
	for _, r := range "zvi.baratz" {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	for i := 0; i < 4; i++ { // Name → ConfigDir → remote → path → Pool
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	}
	for _, r := range "quantivly" {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // commit
	require.False(t, h.menu.HasNotice(), "the notice waits: the panel still covers the list")

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // close the panel
	require.Equal(t, stateDefault, h.state)

	renamed := h.list.GetInstances()
	require.Len(t, renamed, 2)
	var work *session.Instance
	for _, inst := range renamed {
		if inst.ClaudeConfigDir() == filepath.Join(home, ".claude-work") {
			work = inst
		}
	}
	require.NotNil(t, work)
	assert.Equal(t, "zvi.baratz", work.ClaudeAccountName())
	assert.Equal(t, "quantivly", work.ClaudeAccountPool())
	assert.Equal(t, 1, cs.saves, "the healed rows are persisted once, not per session")
	require.NotNil(t, cmd, "closing the panel carries the held-over notice")
	assert.Contains(t, h.menu.String(), "quantivly",
		"the notice names the cluster they landed in, once the list is visible again")
}

func TestAccountSyncNotice(t *testing.T) {
	assert.Equal(t, `3 sessions regrouped under "quantivly"`,
		accountSyncNotice(accountStampSync{restamped: 3, regrouped: 3,
			clusterMoves: map[string]string{"work": "quantivly"},
			destinations: map[string]bool{"quantivly": true}}))
	assert.Equal(t, "1 session regrouped under \"quantivly\"",
		accountSyncNotice(accountStampSync{restamped: 1, regrouped: 1,
			clusterMoves: map[string]string{"work": "quantivly"},
			destinations: map[string]bool{"quantivly": true}}))
	assert.Equal(t, "4 sessions regrouped to match the renamed accounts",
		accountSyncNotice(accountStampSync{restamped: 4, regrouped: 4,
			clusterMoves: map[string]string{"work": "quantivly", "old": "personal"},
			destinations: map[string]bool{"quantivly": true, "personal": true}}))
	// A cluster the sessions LEFT can still hold a session that could not be healed,
	// which drops it from clusterMoves — but they all landed in one place, so the
	// notice can still name it.
	assert.Equal(t, `1 session regrouped under "quantivly"`,
		accountSyncNotice(accountStampSync{restamped: 1, regrouped: 1,
			destinations: map[string]bool{"quantivly": true}}))
	// Renaming a POOLED account moves no cluster — only the badges change, and the
	// notice must not claim a regrouping the user cannot see.
	assert.Equal(t, "2 badges renamed to match the accounts config",
		accountSyncNotice(accountStampSync{restamped: 2}))
}

// A stale cluster key can fan OUT: two sessions stamped with the same pool whose
// accounts were later split into different pools both leave "work", but for
// different destinations. clusterMoves keeps only the first — one persisted slot
// cannot be split — so it is not a trustworthy count of where sessions landed, and
// the notice must not name one of two landing spots as if it were the only one.
func TestSyncAccountStamps_FanOutKeepsOneSlotAndStaysGeneric(t *testing.T) {
	home := homeDir(t)
	cfg := config.DefaultConfig()
	cfg.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "a", ConfigDir: "~/.claude-a", Pool: "alpha"},
		{Name: "b", ConfigDir: "~/.claude-b", Pool: "beta"},
	}
	instances := []*session.Instance{
		stampedInstance(t, "api", "a", filepath.Join(home, ".claude-a"), "work", false),
		stampedInstance(t, "web", "b", filepath.Join(home, ".claude-b"), "work", false),
	}

	sync := syncAccountStamps(cfg, instances)

	assert.Equal(t, 2, sync.regrouped, "both sessions left the stale 'work' cluster")
	assert.Equal(t, map[string]bool{"alpha": true, "beta": true}, sync.destinations,
		"they landed in two different clusters")
	require.Len(t, sync.clusterMoves, 1,
		"one vanished key carries one slot; the second destination appends instead")
	assert.Equal(t, "alpha", sync.clusterMoves["work"], "first session's cluster keeps the slot")
	assert.Equal(t, "2 sessions regrouped to match the renamed accounts",
		accountSyncNotice(sync), "naming just one of two destinations would be a lie")
}

// Committing two edits without closing the panel must announce BOTH. Each commit
// re-syncs from the already-healed list, so the second pass only knows about its own
// rename: holding the raw text would let it overwrite the first, and the panel is
// exactly the place where a dropped notice is never seen (it fires behind a modal).
func TestAccountsPanel_TwoRenamesInOneVisitAnnounceBoth(t *testing.T) {
	resetSettingsTestState(t)
	home := homeDir(t)
	h := newSettingsTestHome()
	h.appConfig.GroupMode = config.GroupModeAccount
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}
	for _, spec := range []struct{ repo, acct, dir string }{
		{"api", "work", filepath.Join(home, ".claude-work")},
		{"sideproj", "personal", filepath.Join(home, ".claude-personal")},
	} {
		h.list.AddInstance(stampedInstance(t, spec.repo, spec.acct, spec.dir, "", false))()
	}
	h.list.SetGroupMode("account")
	withCapturingStore(t, h)

	renameRow := func(row int, to string) {
		for i := 0; i < row; i++ {
			_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		}
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU}) // focus starts on Name
		for _, r := range to {
			_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	}

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@")})
	require.Equal(t, stateAccounts, h.state)
	renameRow(0, "zvi.baratz")
	renameRow(1, "zvi.personal")
	require.False(t, h.menu.HasNotice(), "both notices wait behind the panel")

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateDefault, h.state)
	require.NotNil(t, cmd, "closing the panel carries the held-over notice")

	assert.Contains(t, h.menu.String(), "2 sessions regrouped",
		"the held notice counts both renames, not just the last one")
}

// failingOrderState is the real State with a broken account-order write, to pin what
// the label write does when the carry it depends on could not land.
type failingOrderState struct {
	*config.State
	calls int
}

func (s *failingOrderState) SetAccountOrder([]string) error {
	s.calls++
	return errors.New("state.json is not writable")
}

// The order carry is the half of the flush with no second chance: clusterMoves is a
// diff between the stamp on disk and live config, so once healed labels land nothing
// disagrees and no later pass can recompute it. A failed order write must therefore
// NOT be followed by the label write — leaving both halves stale is what lets the
// next launch retry the pair, instead of stranding the user's [ / ] slot behind
// labels that will never disagree again.
func TestAccountSync_FailedOrderWriteLeavesLabelsUnpersisted(t *testing.T) {
	home := homeDir(t)
	cfg := renamedPoolConfig()
	st := config.DefaultState()
	st.AccountOrder = []string{"work", "personal"}
	storage, instances := loadStamped(t, st, []session.InstanceData{
		{Title: "hub", ClaudeAccount: "work", ClaudeConfigDir: filepath.Join(home, ".claude-work")},
	})

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)
	cs := withCapturingStore(t, h)
	failing := &failingOrderState{State: st}
	h.appState = failing

	h.flushAccountStamps()

	require.Equal(t, 1, failing.calls, "the order carry is attempted before the labels")
	assert.Equal(t, 0, cs.saves,
		"labels stay stale on disk so the next launch recomputes the move and retries both")
}
