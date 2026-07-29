package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/spinner"
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
	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)
	require.NotNil(t, h.settingsOverlay)

	// Toggling a value persists it to config.json immediately, not on close.
	require.True(t, h.settingsOverlay.OpenAt("auto_attach"))
	_, _ = h.handleKeyPress(keyMsg(" "))
	assert.False(t, h.appConfig.GetAutoAttach())
	assert.False(t, config.LoadConfig().GetAutoAttach(),
		"a change must reach disk immediately so it survives a crash")

	// Esc is layered since the two-pane redesign: OpenAt focuses the rows pane, so the
	// first Esc backs out to the rail and only the second closes. This is the only place the
	// layering is observable end to end, so it is asserted rather than worked around — the
	// hint line says "esc back" and then "esc close" so the extra level is advertised
	// (spec §7/§15).
	_, _ = h.handleKeyPress(keyMsg("esc"))
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	require.NotNil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(keyMsg("esc"))
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.settingsOverlay)
}

func TestSettingsPanel_ThemeChangeAppliesLive(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)
	require.True(t, h.settingsOverlay.OpenAt("theme"))

	_, cmd := h.handleKeyPress(keyMsg("right"))
	assert.NotEqual(t, theme.DefaultThemeName, h.appConfig.Theme)
	assert.Equal(t, h.appConfig.Theme, theme.Current().Name,
		"the active theme must follow the config change without a restart")
	assert.Equal(t, h.appConfig.Theme, config.LoadConfig().Theme)
	assert.NotNil(t, cmd, "a repaint command must be issued for the new palette")
}

func TestSettingsPanel_AutoYesTogglePropagatesToHomeFlag(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(textMsg(","))
	require.True(t, h.settingsOverlay.OpenAt("auto_yes"))
	_, _ = h.handleKeyPress(keyMsg(" "))

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

	_, _ = h.handleKeyPress(textMsg(","))
	require.True(t, h.settingsOverlay.OpenAt("splash"))
	_, _ = h.handleKeyPress(keyMsg("right"))

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

	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)
	require.True(t, h.settingsOverlay.OpenAt("group_mode"))

	_, _ = h.handleKeyPress(keyMsg("right"))
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

	_, _ = h.handleKeyPress(textMsg(","))
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
		_, _ = h.handleKeyPress(textMsg(","))
		require.Equal(t, stateSettings, h.state)
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
	}
	// Both Escs go through handleKeyPress, not straight to the overlay: home only learns the
	// panel closed via handleSettingsState's `closed` return. Calling the overlay directly would
	// leave h.state == stateSettings, so the next ',' is routed INTO the still-open panel and the
	// overlay is never rebuilt.
	closePanel := func() {
		_, _ = h.handleKeyPress(keyMsg("esc")) // rows pane -> rail
		_, _ = h.handleKeyPress(keyMsg("esc")) // rail -> closed
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

		_, _ = h.handleKeyPress(textMsg(","))
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
		_, _ = h.handleKeyPress(keyMsg("right")) // off -> on
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

		_, _ = h.handleKeyPress(textMsg(","))
		require.True(t, h.settingsOverlay.OpenAt("group_mode"))
		require.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
			"off is not inert")

		_, _ = h.handleKeyPress(keyMsg("right")) // off -> on
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

	_, _ = h.handleKeyPress(textMsg(","))
	assert.NotEqual(t, 0, h.settingsOverlay.RailIndex(),
		"a fresh run must not land on All settings (spec §4)")
	require.True(t, h.settingsOverlay.OpenAt("agent_oom_margin")) // Advanced
	want := h.settingsOverlay.RailIndex()

	// Two Escs: OpenAt focused the rows pane, and Esc is layered.
	_, _ = h.handleKeyPress(keyMsg("esc"))
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	_, _ = h.handleKeyPress(keyMsg("esc"))
	require.Nil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(textMsg(","))
	assert.Equal(t, want, h.settingsOverlay.RailIndex(), "reopening returns to the last category")
}

// r routes through applySettingChange exactly as an edit does: the config is persisted and
// the live-apply hook runs. Pinned on theme because its hook is observable — theme.Set swaps
// the active palette, so a reset that persisted without live-applying would leave the running
// UI painted in a theme config.json no longer names.
func TestSettingsPanel_ResetPersistsAndLiveApplies(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)

	require.True(t, h.settingsOverlay.OpenAt("theme"))
	_, _ = h.handleKeyPress(keyMsg("right")) // off the default
	changed := h.appConfig.Theme
	require.NotEmpty(t, changed, "precondition: the theme is now explicitly set")
	require.Equal(t, changed, config.LoadConfig().Theme, "precondition: the edit reached disk")
	require.Equal(t, changed, theme.Current().Name, "precondition: and live-applied")

	_, cmd := h.handleKeyPress(textMsg("r"))

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
	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)

	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 1)
	_, _ = h.handleKeyPress(keyMsg("enter"))

	assert.Equal(t, stateAccounts, h.state, "Enter on Accounts opens the @ overlay")
	assert.NotNil(t, h.accountsOverlay)
	assert.Nil(t, h.settingsOverlay, "the settings panel closed to make way")

	// Closing accounts and reopening settings lands back on the entry we left.
	_, _ = h.handleKeyPress(keyMsg("esc"))
	_, _ = h.handleKeyPress(textMsg(","))
	require.NotNil(t, h.settingsOverlay)
	assert.Equal(t, h.settingsOverlay.RailEntryCount()-1, h.settingsOverlay.RailIndex())
}

// --- The Profiles editor (PR D) ---------------------------------------------

// profileNamesOf collapses a config's profile list to its names.
func profileNamesOf(cfg *config.Config) []string {
	out := make([]string, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		out[i] = p.Name
	}
	return out
}

// openProfilesEditor opens the settings panel and focuses the Profiles editor's pane.
func openProfilesEditor(t *testing.T, h *home) {
	t.Helper()
	_, _ = h.handleKeyPress(textMsg(","))
	require.Equal(t, stateSettings, h.state)
	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 2)
	_, _ = h.handleKeyPress(keyMsg("enter"))
}

// TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop pins the whole D path through home: the
// panel records a request, home turns it into a command rather than probing inline, and the
// result merges and persists.
//
// It stubs detectAgents — the same package var the startup agent check uses — which is what
// makes the TUI and `atrium profiles detect` share one detector.
func TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop(t *testing.T) {
	resetSettingsTestState(t)
	stubDetect(t, []config.Profile{
		{Name: "claude", Program: "/usr/local/bin/claude"},
		{Name: "codex", Program: "codex"},
	})
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude --model opus"}}
	h.appConfig.DefaultProgram = "claude"
	openProfilesEditor(t, h)

	_, cmd := h.handleKeyPress(textMsg("D"))
	require.NotNil(t, cmd, "D must produce a command rather than probing inline")
	msg := cmd()
	detected, ok := msg.(profilesDetectedMsg)
	require.Truef(t, ok, "expected a profilesDetectedMsg, got %T", msg)

	_, _ = h.Update(detected)

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program,
		"detection never modifies an existing profile")
	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(config.LoadConfig()),
		"the merge reached disk through applySettingChange")

	// A second run adds nothing, so it must not rewrite config.json at all.
	before := configFileModTime(t)
	_, _ = h.Update(profilesDetectedMsg{detected: []config.Profile{{Name: "codex", Program: "codex"}}})
	assert.Equal(t, before, configFileModTime(t),
		"a detection that added nothing must not persist, mirroring the CLI's early return")
}

// configFileModTime is the resolved config.json's mtime, for asserting a write did NOT happen.
func configFileModTime(t *testing.T) time.Time {
	t.Helper()
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	return info.ModTime()
}

// TestSettingsPanel_ProfileDetectAfterCloseStillMergesAndSaysSo. The probe takes long enough
// that the user can close the panel before it returns; the merge is what they asked for, so it
// happens, and home is the one that reports it.
//
// The alternative — dropping the result — made one set of keystrokes produce three different
// outcomes depending on how fast the user moved, including a silent config.json write.
func TestSettingsPanel_ProfileDetectAfterCloseStillMergesAndSaysSo(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude --model opus"}}
	require.Nil(t, h.settingsOverlay, "precondition: no panel")

	_, cmd := h.Update(profilesDetectedMsg{detected: []config.Profile{
		{Name: "claude", Program: "/usr/local/bin/claude"},
		{Name: "codex", Program: "codex"},
	}})

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program,
		"detection never modifies an existing profile, panel or no panel")
	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(config.LoadConfig()),
		"and it reached disk")
	// handleAgentNotice either shows the toast now (a cmd) or holds it over in
	// pendingAgentNotice when the hint row is busy. Both are "announced"; neither is silence.
	assert.True(t, cmd != nil || h.pendingAgentNotice != "",
		"the outcome must be announced, not swallowed")
	if h.pendingAgentNotice != "" {
		assert.Contains(t, h.pendingAgentNotice, "codex", "the held-over notice names what was added")
	}
}

// TestSettingsPanel_ProfileDetectWhileTheRailMovedAwayIsAnnounced is the silent-write guard at
// the app level: the pane is not showing the editor, so nothing in the panel can report the
// merge, and without the handback the user sees a rewritten config.json and no explanation.
func TestSettingsPanel_ProfileDetectWhileTheRailMovedAwayIsAnnounced(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	openProfilesEditor(t, h)

	_, _ = h.handleKeyPress(textMsg("D"))
	_, _ = h.handleKeyPress(keyMsg("esc")) // back to the rail, note cleared
	_, _ = h.handleKeyPress(keyMsg("up"))  // and off the Profiles entry

	_, cmd := h.Update(profilesDetectedMsg{detected: []config.Profile{{Name: "codex", Program: "codex"}}})

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.True(t, cmd != nil || h.pendingAgentNotice != "",
		"a merge the panel cannot report must still be announced")
}

// TestProfilesDetectedTextMirrorsTheCLI pins the wording against `atrium profiles detect`'s own
// output (main.go's profilesDetectCmd). Two surfaces for one operation should not describe it in
// two voices, and this is the assertion that keeps them together — the notice path itself is
// covered above, where the exact rendering depends on whether the hint row is free.
func TestProfilesDetectedTextMirrorsTheCLI(t *testing.T) {
	assert.Equal(t, "no new agents detected; profiles unchanged", profilesDetectedText(nil))
	assert.Equal(t, "added profiles: codex", profilesDetectedText([]string{"codex"}))
	assert.Equal(t, "added profiles: codex, gemini", profilesDetectedText([]string{"codex", "gemini"}))
}

// TestSettingsPanel_EditingTheDefaultProfileReResolvesTheLaunchCommand closes a live-apply gap
// the Profiles editor makes reachable.
//
// m.program is resolved once at launch and is the create form's fallback launch command
// whenever there is no variant picker — which is exactly the single-profile case below. Editing
// that profile's command without re-resolving leaves the form launching the previous one until
// the app is relaunched, which contradicts the whole point of guarding default_program.
func TestSettingsPanel_EditingTheDefaultProfileReResolvesTheLaunchCommand(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	h.appConfig.DefaultProgram = "claude"
	h.program = h.appConfig.GetProgram()
	require.Equal(t, "claude", h.program)

	openProfilesEditor(t, h)
	_, _ = h.handleKeyPress(textMsg("e"))
	_, _ = h.handleKeyPress(keyMsg("tab"))
	for _, r := range " --model opus" {
		_, _ = h.handleKeyPress(textMsg(string(r)))
	}
	_, _ = h.handleKeyPress(keyMsg("enter"))

	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program)
	assert.Equal(t, "claude --model opus", config.LoadConfig().Profiles[0].Program,
		"the edit reached disk through the panel's one writer")
	assert.Equal(t, "claude --model opus", h.program,
		"and the resolved launch command was re-derived, not left at launch-time")
}

// TestSettingsPanel_DefaultProgramReResolvesTheLaunchCommand is the same gap on the row that has
// always existed — cycling default_program. It is here because the fix covers both keys and a
// test for only one would let a later edit drop the other.
func TestSettingsPanel_DefaultProgramReResolvesTheLaunchCommand(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex --sandbox"},
	}
	h.appConfig.DefaultProgram = "claude"
	h.program = h.appConfig.GetProgram()

	_, _ = h.handleKeyPress(textMsg(","))
	require.True(t, h.settingsOverlay.OpenAt("default_program"))
	_, _ = h.handleKeyPress(keyMsg("right"))

	require.Equal(t, "codex", h.appConfig.DefaultProgram)
	assert.Equal(t, "codex --sandbox", h.program,
		"the launch command follows the setting rather than the launch-time snapshot")
}

// TestSettingsPanel_ProfileEditDropsAStashedDraft. A create form escaped with a dirty title is
// stashed whole and restored by the next bare n — including the []config.Profile it snapshotted
// at build time, which VariantPicker replays as launch commands verbatim. So a draft stashed
// before a profiles edit would offer a renamed-away profile and launch its OLD command.
//
// handleAccountsState already drops the stash for exactly this reason (app_accounts.go); this is
// the same line for the same hazard on the other record editor.
func TestSettingsPanel_ProfileEditDropsAStashedDraft(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	// TWO profiles: cycleEnum is a silent no-op on a single-option enum, so a one-profile
	// fixture would never reach applySettingChange and the test would pass for the wrong reason.
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	h.appConfig.DefaultProgram = "claude"
	h.stashedDraft = &overlay.TextInputOverlay{} // a draft pinned to the old profile list

	_, _ = h.handleKeyPress(textMsg(","))
	require.True(t, h.settingsOverlay.OpenAt("default_program"))
	_, _ = h.handleKeyPress(keyMsg("right"))
	require.Equal(t, "codex", h.appConfig.DefaultProgram, "precondition: the cycle landed")

	assert.Nil(t, h.stashedDraft, "a stale draft must not survive a change to what launches")
}
