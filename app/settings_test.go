package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSettingsTestHome builds the minimal home model the settings paths touch.
// HOME is sandboxed by TestMain, so config persistence stays hermetic. The list
// and state are empty but present: an accounts edit re-derives every open
// session's account labels from the new config (#470), which asks the list for
// them.
func newSettingsTestHome() *home {
	s := spinner.New()
	st := config.DefaultState()
	storage, _ := session.NewStorage(st)
	return &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		appState:  st,
		storage:   storage,
		list:      ui.NewList(&s),
		menu:      ui.NewMenu(),
	}
}

// resetSettingsTestState restores the on-disk config and active theme that
// settings tests mutate, so sibling tests in the package see defaults.
func resetSettingsTestState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = config.SaveConfig(config.DefaultConfig())
		theme.Set(theme.DefaultThemeName)
	})
}

func TestSettingsPanel_OpenEditPersistClose(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	// ',' opens the settings panel.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	require.NotNil(t, h.settingsOverlay)

	// Toggling a value persists it to config.json immediately, not on close.
	require.True(t, h.settingsOverlay.OpenAt("auto_attach"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.False(t, h.appConfig.GetAutoAttach())
	assert.False(t, config.LoadConfig().GetAutoAttach(),
		"a change must reach disk immediately so it survives a crash")

	// Esc is layered since the two-pane redesign: OpenAt focuses the rows pane, so the
	// first Esc backs out to the rail and only the second closes. This is the only place the
	// layering is observable end to end, so it is asserted rather than worked around — the
	// hint line says "esc back" and then "esc close" so the extra level is advertised
	// (spec §7/§15).
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	require.NotNil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.settingsOverlay)
}

func TestSettingsPanel_ThemeChangeAppliesLive(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	require.True(t, h.settingsOverlay.OpenAt("theme"))

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.NotEqual(t, theme.DefaultThemeName, h.appConfig.Theme)
	assert.Equal(t, h.appConfig.Theme, theme.Current().Name,
		"the active theme must follow the config change without a restart")
	assert.Equal(t, h.appConfig.Theme, config.LoadConfig().Theme)
	assert.NotNil(t, cmd, "a repaint command must be issued for the new palette")
}

func TestSettingsPanel_AutoYesTogglePropagatesToHomeFlag(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.OpenAt("auto_yes"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeySpace})

	assert.True(t, h.autoYes, "the home flag gates AutoYes on newly created instances")
	assert.True(t, config.LoadConfig().AutoYes,
		"the persisted flag is what the exit-time daemon decision reads")
}

// TestSettingsPanel_SplashChangePersists proves the "splash" settings-changed
// case reaches disk end-to-end (the live ui.SetSplashVariant side is pinned in
// ui's own tests; its rendering effect is shielded here by the env override
// TestMain sets).
func TestSettingsPanel_SplashChangePersists(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.OpenAt("splash"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})

	want := config.SplashVariants()[0]
	assert.Equal(t, want, h.appConfig.GetSplash())
	assert.Equal(t, want, config.LoadConfig().GetSplash(),
		"the change must reach disk immediately so it survives a crash")
}

// TestGroupModeChange_ClustersList proves the "group_mode" settings-changed case
// (app_layout.go) reaches the live list end-to-end: opening the panel, cycling
// the row to "account", and reading the list back out. Mirrors
// TestSettingsPanel_ThemeChangeAppliesLive's open/select/KeyRight dispatch, but
// builds the home via assembleHome (see TestAssembleHomeWiring) so the list
// carries real instances to cluster, rather than newSettingsTestHome's list-less
// shell.
func TestGroupModeChange_ClustersList(t *testing.T) {
	resetSettingsTestState(t)

	cfg := config.DefaultConfig()
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)

	newInst := func(repoBase, account string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: repoBase + "-" + account, Path: "/tmp/" + repoBase, Program: "echo",
		})
		require.NoError(t, err)
		if account != "" {
			inst.SetClaudeAccount(account, "", false)
		}
		return inst
	}
	// Interleaved input: work, personal, work — two repos share the "work"
	// account and must end up adjacent once account-clustering applies.
	instances := []*session.Instance{
		newInst("api", "work"),
		newInst("sideproj", "personal"),
		newInst("infra", "work"),
	}

	h := assembleHome(context.Background(), "claude", false, "v", "atr", cfg, st, storage, instances)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	require.True(t, h.settingsOverlay.OpenAt("group_mode"))

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.GroupModeAccount, h.appConfig.GetGroupMode(),
		"must report its row key so home can persist")

	got := h.list.GetInstances()
	require.Len(t, got, 3)
	repos := make([]string, len(got))
	for i, inst := range got {
		repos[i] = filepath.Base(inst.Path)
	}
	assert.Equal(t, []string{"api", "infra", "sideproj"}, repos,
		"the two work-account repos (api, infra) must be adjacent after clustering")
}

// accountGroupedHome builds a home whose list is in account mode with two distinct
// accounts, so account grouping and its reorder guards are live. The menu is visible
// in stateDefault (the hint bar defaults on), so reorder hints land on it.
func accountGroupedHome(t *testing.T) *home {
	t.Helper()
	h := newCreateFormHome(t)
	// A working in-memory storage so a performed (not just hinted) reorder can persist.
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)
	h.appState = st
	h.storage = storage
	for _, spec := range []struct{ repo, acct string }{{"api", "work"}, {"infra", "personal"}} {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: spec.repo, Path: "/tmp/" + spec.repo, Program: "echo",
		})
		require.NoError(t, err)
		inst.SetClaudeAccount(spec.acct, "", false)
		h.list.AddInstance(inst)
	}
	h.list.SetGroupMode("account")
	return h
}

// Pressing { (whole-group move) toward a different account's cluster must explain
// that group reordering stays within an account, rather than silently no-op'ing.
func TestGroupMode_GroupMoveAcrossAccountBoundaryExplains(t *testing.T) {
	h := accountGroupedHome(t) // api|work, infra|personal (clustered, one block each)
	before := append([]*session.Instance(nil), h.list.GetInstances()...)
	h.list.SetSelectedInstance(1) // infra|personal, whose neighbor above is work

	pressKey(h, '{') // KeyMoveGroupUp — would cross into the work cluster

	require.True(t, h.menu.HasNotice(), "a cross-account group move must explain itself")
	assert.Contains(t, h.menu.String(), "within an account")
	assert.Equal(t, before, h.list.GetInstances(), "the cross-account move stays a no-op")
}

// A whole-group move within an account cluster is performed (and persisted), with no
// hint — account grouping no longer disables group reordering outright.
func TestGroupMode_GroupMoveWithinClusterPerformsMove(t *testing.T) {
	h := accountGroupedHome(t)
	// Add a second work repo so the work cluster has two blocks to reorder.
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "infra2", Path: "/tmp/infra2", Program: "echo",
	})
	require.NoError(t, err)
	inst.SetClaudeAccount("work", "", false)
	h.list.AddInstance(inst)
	// Clustered display now leads with the two work blocks: api, infra2, then personal.
	h.list.SetSelectedInstance(0) // api|work

	pressKey(h, '}') // KeyMoveGroupDown within the work cluster

	assert.False(t, h.menu.HasNotice(), "a within-cluster move needs no explanation")
	got := h.list.GetInstances()
	require.Len(t, got, 3)
	assert.Equal(t, "infra2", filepath.Base(got[0].Path), "api and infra2 swapped within the work cluster")
	assert.Equal(t, "api", filepath.Base(got[1].Path))
}

// J/K within-group reordering works while account-grouped (no status sort), so
// pressing K performs the swap rather than emitting a hint.
func TestGroupMode_SessionMoveWorksWhileAccountGrouped(t *testing.T) {
	h := accountGroupedHome(t)
	// Two work sessions in one repo so there is a sibling to swap with.
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "api2", Path: "/tmp/api", Program: "echo",
	})
	require.NoError(t, err)
	inst.SetClaudeAccount("work", "", false)
	h.list.AddInstance(inst)
	h.list.SetSelectedInstance(1) // the second api session

	pressKey(h, 'K') // KeyMoveUp — within the api repo

	assert.False(t, h.menu.HasNotice(), "J/K is available under account grouping")
	assert.Equal(t, "api2", h.list.GetSelectedInstance().Title, "the second api session moved up")
	assert.Equal(t, 0, indexOfTitle(h.list, "api2"), "and now leads its repo")
}

// indexOfTitle returns the position of the instance with the given title in the
// displayed order, or -1.
func indexOfTitle(l *ui.List, title string) int {
	for i, it := range l.GetInstances() {
		if it.Title == title {
			return i
		}
	}
	return -1
}

func TestSettingsPanel_HidesHintBarLikeOtherModals(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	assert.False(t, h.menuVisible(), "the panel renders its own key hints; the bar would be redundant")
}

// TestSettingsPanel_GroupModeChipFollowsTheLiveList pins the wiring that carries the
// session-derived gate into the panel. It is an app-level test because neither package can answer
// it alone: ui.List owns the count and ui/overlay owns the chip.
func TestSettingsPanel_GroupModeChipFollowsTheLiveList(t *testing.T) {
	resetSettingsTestState(t)
	h := accountGroupedHome(t) // two distinct accounts, grouping on
	// accountGroupedHome sets the LIST's mode, not the config's. The chip needs both: the panel
	// reads the setting from config and the gate from the list.
	h.appConfig.GroupMode = config.GroupModeAccount
	require.True(t, h.list.AccountClusteringVisible(), "the fixture must actually cluster")

	openPanel := func() {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
		require.Equal(t, stateSettings, h.state)
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
	}
	// Both Escs go through handleKeyPress, not straight to the overlay: home only learns the
	// panel closed via handleSettingsState's `closed` return. Calling the overlay directly would
	// leave h.state == stateSettings, so the next ',' is routed INTO the still-open panel and the
	// overlay is never rebuilt.
	closePanel := func() {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // rows pane -> rail
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // rail -> closed
		require.Nil(t, h.settingsOverlay)
	}

	openPanel()
	assert.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"two clusters are visible, so the row is not inert")
	closePanel()

	// Collapse to one cluster: the chip must appear on the next open.
	for _, inst := range h.list.GetInstances() {
		inst.SetClaudeAccount("work", "", false)
	}
	require.False(t, h.list.AccountClusteringVisible())

	openPanel()
	assert.Contains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"one cluster means the setting is on but doing nothing")
}

// TestSettingsPanel_GroupModeChipTracksTheListInTheSameFrame pins the refresh in
// applySettingChange, which the reopen-based test above cannot see.
//
// The direction matters, and getting it wrong is why a first draft of this test passed with the
// refresh deleted. SetGroupMode changes accountGrouped(), which is HALF the gate — so the value
// the panel holds has to be recomputed, not merely re-read. With two distinct accounts starting
// in repo mode, the gate is false at construction (not grouped) and true the instant clustering
// is switched on; a stale false would put "nothing to cluster" on a row that is, in fact,
// clustering two accounts.
//
// The one-account direction is asserted too, but on its own it proves nothing about the refresh:
// there the gate is false before and after, so a stale value is accidentally correct.
func TestSettingsPanel_GroupModeChipTracksTheListInTheSameFrame(t *testing.T) {
	t.Run("two accounts: turning it on must clear the chip", func(t *testing.T) {
		resetSettingsTestState(t)
		h := accountGroupedHome(t) // api|work, infra|personal
		h.appConfig.GroupMode = config.GroupModeRepo
		h.list.SetGroupMode(config.GroupModeRepo)
		require.False(t, h.list.AccountClusteringVisible(), "repo mode clusters nothing")

		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // off -> on
		require.Equal(t, config.GroupModeAccount, h.appConfig.GetGroupMode())
		require.True(t, h.list.AccountClusteringVisible(), "two accounts now cluster")

		assert.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
			"the panel must recompute the gate, not keep the false it was built with")
	})

	t.Run("one account: turning it on must show the chip", func(t *testing.T) {
		resetSettingsTestState(t)
		h := accountGroupedHome(t)
		for _, inst := range h.list.GetInstances() {
			inst.SetClaudeAccount("work", "", false) // collapse to one cluster
		}
		h.appConfig.GroupMode = config.GroupModeRepo
		h.list.SetGroupMode(config.GroupModeRepo)

		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
		require.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
			"off is not inert")

		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // off -> on
		require.Equal(t, config.GroupModeAccount, h.appConfig.GetGroupMode())
		assert.Contains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
			"the chip must appear without reopening the panel")
	})
}

// TestSettingsPanel_RemembersTheCategoryAcrossOpens pins spec §7's in-memory rail memory — and
// that a FIRST open still lands on the default category rather than on All settings, which a
// zero-valued int would have produced.
func TestSettingsPanel_RemembersTheCategoryAcrossOpens(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.NotEqual(t, 0, h.settingsOverlay.RailIndex(),
		"a fresh run must not land on All settings (spec §4)")
	require.True(t, h.settingsOverlay.OpenAt("agent_oom_margin")) // Advanced
	want := h.settingsOverlay.RailIndex()

	// Two Escs: OpenAt focused the rows pane, and Esc is layered.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Nil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.Equal(t, want, h.settingsOverlay.RailIndex(), "reopening returns to the last category")
}

// r routes through applySettingChange exactly as an edit does: the config is persisted and
// the live-apply hook runs. Pinned on theme because its hook is observable — theme.Set swaps
// the active palette, so a reset that persisted without live-applying would leave the running
// UI painted in a theme config.json no longer names.
func TestSettingsPanel_ResetPersistsAndLiveApplies(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)

	require.True(t, h.settingsOverlay.OpenAt("theme"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // off the default
	changed := h.appConfig.Theme
	require.NotEmpty(t, changed, "precondition: the theme is now explicitly set")
	require.Equal(t, changed, config.LoadConfig().Theme, "precondition: the edit reached disk")
	require.Equal(t, changed, theme.Current().Name, "precondition: and live-applied")

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})

	assert.Empty(t, h.appConfig.Theme, "r cleared the explicit theme")
	assert.Empty(t, config.LoadConfig().Theme, "r persisted, like an edit")
	assert.Equal(t, theme.DefaultThemeName, theme.Current().Name,
		"r live-applied: the running UI repainted in the default palette")
	assert.NotNil(t, cmd, "a repaint command must be issued, as for an edit")
}

// The rail's Accounts entry actually opens the accounts overlay: the panel closes, the @
// overlay opens in its place, and the remembered rail brings the user back to Accounts on the
// next ','. Home-level wiring, because an overlay cannot open a sibling.
func TestSettingsPanel_AccountsEntryOpensTheAccountsOverlay(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)

	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 1)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateAccounts, h.state, "Enter on Accounts opens the @ overlay")
	assert.NotNil(t, h.accountsOverlay)
	assert.Nil(t, h.settingsOverlay, "the settings panel closed to make way")

	// Closing accounts and reopening settings lands back on the entry we left.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.NotNil(t, h.settingsOverlay)
	assert.Equal(t, h.settingsOverlay.RailEntryCount()-1, h.settingsOverlay.RailIndex())
}
