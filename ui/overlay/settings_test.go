package overlay

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stripANSI removes escape sequences so assertions can match plain text.
func stripANSI(s string) string { return ansi.Strip(s) }

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// settingsAt moves the overlay cursor onto the row with the given key, failing
// the test if no such row exists.
func settingsAt(t *testing.T, o *SettingsOverlay, key string) {
	t.Helper()
	require.True(t, o.SelectRow(key), "settings panel should have a %q row", key)
}

// widestFooterRow returns the key of the row whose footer help is widest, so the
// footer-height guards test the actual worst case instead of a key that was the worst
// case when they were written. It measures settingRow.footerText — the composition the
// renderer itself uses — so the two cannot disagree.
func widestFooterRow(t *testing.T) string {
	t.Helper()
	key, widest := "", -1
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if w := ansi.StringWidth(r.footerText()); w > widest {
			key, widest = r.key, w
		}
	}
	require.NotEmpty(t, key, "the schema must declare at least one row")
	// A one-line footer would make the callers' capping assertions vacuous.
	require.Greater(t, widest, 60,
		"the widest footer (%q, %d cells) must exceed the 80-col inner width to wrap",
		key, widest)
	return key
}

// TestFooterTextFitsTwoLines caps the whole footer composition, not just the summary.
//
// TestSummaryFitsOneLine already holds summary to 74 cells, but the footer appends a
// caution and a timing note after it, so the rendered help can be far wider than any
// single field's cap — fast_forward_local_base's is 116 cells. Two wrapped lines at the
// 80-column floor is what the body budget is sized against; a third would silently take
// a row from the list above it. This is the guard a new caution has to clear.
func TestFooterTextFitsTwoLines(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24) // the project's degradation floor
	inner := o.innerWidth()

	for _, r := range newSettingRows(config.DefaultConfig()) {
		lines := strings.Split(ansi.Wrap(r.footerText(), inner, ""), "\n")
		assert.LessOrEqualf(t, len(lines), 2,
			"row %q wraps its footer to %d lines at inner width %d (%d cells); trim the "+
				"summary or the caution", r.key, len(lines), inner, ansi.StringWidth(r.footerText()))
	}
}

// TestEveryCautionReachesTheFooter pins that a row's caution is actually rendered,
// for every row that declares one.
//
// This guards a specific bug class rather than one string: help copy that lives in a
// field the renderer never reads is invisible, and a test that only pins the field's
// contents still passes. fast_forward_local_base's "modifies your local branch" note
// was rendered from applyNote before the taxonomy rewrite and was briefly lost that
// way — the caution moved into detail, which no render path reads in PR A.
func TestEveryCautionReachesTheFooter(t *testing.T) {
	cautions := 0
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.caution == "" {
			continue
		}
		cautions++
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(80, 40)
		settingsAt(t, o, r.key)
		footer := stripANSI(strings.Join(o.renderFooter(o.innerWidth()), " "))
		assert.Containsf(t, footer, r.caution,
			"row %q declares a caution the footer never renders", r.key)
	}
	// Without this the loop body could stop running and the test would still pass.
	require.Positive(t, cautions, "at least one row must declare a caution")
}

func TestSettingsOverlay_ToggleBool(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_attach")

	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.False(t, closed)
	assert.Equal(t, "auto_attach", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetAutoAttach(), "space flips the default-on field off")

	// Enter toggles bools too.
	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "auto_attach", changed)
	assert.True(t, cfg.GetAutoAttach())
}

// The focus-gate toggle is reachable from the panel (AC #4) and flips the
// default-off notify_when_focused: on = keep notifying while focused.
func TestSettingsOverlay_ToggleNotifyWhenFocused(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "notify_when_focused")

	require.False(t, cfg.GetNotifyWhenFocused(), "focus-gating is on by default (silent while focused)")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "notify_when_focused", changed, "a toggle must report its row key so home can persist")
	assert.True(t, cfg.GetNotifyWhenFocused(), "space turns notify-while-focused on")
}

// The notifications enum offers the SSH-friendly osc mode (AC #4).
func TestSettingsOverlay_NotificationsIncludesOSC(t *testing.T) {
	cfg := config.DefaultConfig()
	var row settingRow
	for _, r := range newSettingRows(cfg) {
		if r.key == "notifications" {
			row = r
		}
	}
	require.Equal(t, "notifications", row.key)
	assert.Contains(t, row.options(cfg), config.NotificationsOSC, "the notifications enum must offer osc")
}

// The finished-turn rung is reachable from the panel and offers ONLY the quieter rungs:
// admitting desktop or osc here would let the panel configure a finished turn that is
// louder than a session blocking on input, which is the one thing the ladder rules out.
func TestSettingsOverlay_FinishedTurnsOffersOnlyQuieterRungs(t *testing.T) {
	cfg := config.DefaultConfig()
	var row settingRow
	for _, r := range newSettingRows(cfg) {
		if r.key == "notifications_finished" {
			row = r
		}
	}
	require.Equal(t, "notifications_finished", row.key, "settings panel should have a notifications_finished row")
	require.Equal(t, []string{config.NotificationsSame, config.NotificationsOff, config.NotificationsBell}, row.options(cfg),
		"only rungs at or below every notification mode may be offered")
}

// Cycling the finished-turn rung writes the config field and reports the row key so home
// persists it.
func TestSettingsOverlay_CycleFinishedTurns(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "notifications_finished")

	require.Equal(t, config.NotificationsSame, cfg.GetNotificationsFinished(), "defaults to following notifications")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications_finished", changed, "a cycle must report its row key so home can persist")
	assert.Equal(t, config.NotificationsOff, cfg.GetNotificationsFinished(), "same → off")

	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.NotificationsBell, cfg.GetNotificationsFinished(), "off → bell")

	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.NotificationsSame, cfg.GetNotificationsFinished(), "bell wraps back to same")
}

// The mouse off-switch is reachable from the panel (not JSON-only) and toggles
// the default-on capture, so a user whose terminal's select-to-copy the capture
// breaks can turn it off without hand-editing config.json.
func TestSettingsOverlay_ToggleMouse(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "mouse")

	require.True(t, cfg.GetMouse(), "mouse defaults on")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "mouse", changed, "a toggle must report its row key so home can persist and live-apply")
	assert.False(t, cfg.GetMouse(), "space turns capture off")

	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "mouse", changed)
	assert.True(t, cfg.GetMouse(), "enter turns it back on")
}

func TestSettingsOverlay_ToggleTrustWorktreesRoot(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "trust_worktrees_root")

	require.False(t, cfg.GetTrustWorktreesRoot(), "trust must default off (opt-in)")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "trust_worktrees_root", changed)
	assert.True(t, cfg.GetTrustWorktreesRoot())
}

func TestSettingsOverlay_ToggleAutoYes(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_yes")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "auto_yes", changed)
	assert.True(t, cfg.AutoYes)
}

func TestSettingsOverlay_TogglePRCreateDraft(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "pr_create_draft")

	require.True(t, cfg.GetPRCreateDraft(), "PRs default to draft")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "pr_create_draft", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetPRCreateDraft(), "space flips the default-on draft field to ready-for-review")
}

func TestSettingsOverlay_ToggleShowReleaseNotesAfterUpdate(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "show_release_notes_after_update")

	require.True(t, cfg.GetShowReleaseNotesAfterUpdate(), "notes default on")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "show_release_notes_after_update", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetShowReleaseNotesAfterUpdate(), "space flips the default-on field off")
}

func TestSettingsOverlay_CycleThemeWraps(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "theme")

	names := theme.Names()
	sort.Strings(names)
	start := cfg.Theme

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "theme", changed)
	assert.NotEqual(t, start, cfg.Theme, "right must advance to the next theme")
	assert.Contains(t, names, cfg.Theme)

	// A full cycle returns to the starting theme (wrap-around).
	for i := 1; i < len(names); i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	}
	assert.Equal(t, start, cfg.Theme)

	// Left cycles backwards (and wraps too).
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, names[(indexOf(names, start)+len(names)-1)%len(names)], cfg.Theme)
}

// TestSettingsOverlay_CycleModelIndicator pins the model-chip enum: defaults
// to on, cycles on → off, and wraps back to on.
func TestSettingsOverlay_CycleModelIndicator(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "model_indicator")

	require.Equal(t, config.ModelIndicatorOn, cfg.GetModelIndicator(), "chip defaults to on")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "model_indicator", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.ModelIndicatorOff, cfg.GetModelIndicator())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.ModelIndicatorOn, cfg.GetModelIndicator(), "the enum wraps")
}

// TestSettingsOverlay_CycleEffortIndicator pins the effort-chip enum: defaults to on,
// cycles on → off, and wraps back to on. The reported key is what makes the chip toggle
// live-apply — app.applySettingChange switches on it, and the row is the only thing that
// can produce it, so without this row that branch is unreachable.
func TestSettingsOverlay_CycleEffortIndicator(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "effort_indicator")

	require.Equal(t, config.EffortIndicatorOn, cfg.GetEffortIndicator(), "chip defaults to on")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "effort_indicator", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.EffortIndicatorOff, cfg.GetEffortIndicator())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.EffortIndicatorOn, cfg.GetEffortIndicator(), "the enum wraps")
}

// TestSettingsOverlay_CycleSplash pins the splash enum: defaults to random,
// right steps into the named patterns, and a full cycle wraps back to random.
func TestSettingsOverlay_CycleSplash(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "splash")

	require.Equal(t, config.SplashRandom, cfg.GetSplash(), "splash defaults to random")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "splash", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.SplashVariants()[0], cfg.GetSplash())

	// Stepping over every named pattern wraps back to random.
	for range config.SplashVariants() {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	}
	assert.Equal(t, config.SplashRandom, cfg.GetSplash(), "the enum wraps")
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func TestSettingsOverlay_CycleDefaultProgramVisitsAllProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
		{Name: "gemini", Program: "gemini"},
	}
	cfg.DefaultProgram = "claude"
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "default_program")

	// Cycling right must walk the declared profile order — not the
	// GetProfiles() default-first reordering, which would ping-pong between
	// the first two profiles and never reach the third.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "codex", cfg.DefaultProgram)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "gemini", cfg.DefaultProgram)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "claude", cfg.DefaultProgram, "wraps back to the first profile")
}

// A hand-edited config can hold a raw command in default_program rather than a
// profile name (GetProgram passes it through). The enum must carry that value
// as a cycle option — otherwise the first ←/→/enter press would overwrite it
// with a profile name and persist-per-change would destroy it irrecoverably.
func TestSettingsOverlay_RawDefaultProgramSurvivesCycle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "gemini", Program: "gemini"},
	}
	cfg.DefaultProgram = "/home/user/launch-claude.sh" // not a profile name
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "default_program")

	// One press moves onto a profile…
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "claude", cfg.DefaultProgram)
	// …and a full cycle returns to the raw value: nothing is destroyed.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "/home/user/launch-claude.sh", cfg.DefaultProgram)
	// Cycling backwards from the raw value wraps onto the last profile.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "gemini", cfg.DefaultProgram)
}

func TestSettingsOverlay_SingleProfileCycleIsNoop(t *testing.T) {
	cfg := config.DefaultConfig() // no profiles → one synthesized from DefaultProgram
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "default_program")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Empty(t, changed, "cycling a single-option enum must not report a change")
	assert.Equal(t, "claude", cfg.DefaultProgram)
}

func TestSettingsOverlay_IntEditRejectsGarbageAndCommitsValid(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "daemon_poll_interval")

	// Enter starts an inline edit pre-filled with the current value.
	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, closed)
	assert.Empty(t, changed)
	assert.True(t, o.editing)

	o.HandleKeyPress(keyRunes("abc"))
	closed, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, closed)
	assert.Empty(t, changed, "an invalid value must not commit")
	assert.True(t, o.editing, "edit mode persists so the user can fix the value")
	assert.NotEmpty(t, o.lastErr)
	assert.Equal(t, 1000, cfg.DaemonPollInterval)

	// Esc abandons the edit without committing.
	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "esc during an edit cancels the edit, not the panel")
	assert.False(t, o.editing)

	// A valid value commits and reports the row key.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	for range "1000" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	o.HandleKeyPress(keyRunes("2000"))
	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "daemon_poll_interval", changed)
	assert.False(t, o.editing)
	assert.Equal(t, 2000, cfg.DaemonPollInterval)
}

func TestSettingsOverlay_PollIntervalClampedToFloor(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "daemon_poll_interval")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	for range "1000" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	o.HandleKeyPress(keyRunes("50"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, changed, "a sub-floor poll interval must be rejected")
	assert.NotEmpty(t, o.lastErr)
	assert.Equal(t, 1000, cfg.DaemonPollInterval)
}

// maxSessionsRow returns the rendered "Session limit" row line (not the summary
// footer, which also mentions the words — asserting on Render() as a whole would be
// satisfied by the summary and never test the value).
func maxSessionsRow(t *testing.T, o *SettingsOverlay) string {
	t.Helper()
	o.SetSize(80, 40)
	for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
		if strings.Contains(line, "Session limit") {
			return line
		}
	}
	t.Fatal("no \"Session limit\" row in the render")
	return ""
}

// Clearing the field to empty selects the host-derived "auto" default (nil), which
// the row shows as "auto (N)" — not "unlimited" (that is now the explicit 0).
func TestSettingsOverlay_MaxSessionsEmptyMeansAuto(t *testing.T) {
	cfg := config.DefaultConfig()
	five := 5
	cfg.MaxSessions = &five
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "max_sessions")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	for range "5" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "max_sessions", changed)
	assert.Nil(t, cfg.MaxSessions, "an empty cap selects the host-derived auto default")

	row := maxSessionsRow(t, o)
	assert.Contains(t, row, "auto", "the row shows the auto default")
	assert.NotContains(t, row, "unlimited", "auto is not unlimited")
}

// An explicit 0 is the "unlimited" escape hatch: it persists as a non-nil pointer
// (distinct from auto) and the row shows "unlimited".
func TestSettingsOverlay_MaxSessionsZeroMeansUnlimited(t *testing.T) {
	cfg := config.DefaultConfig() // MaxSessions nil (auto)
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "max_sessions")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // edit (pre-filled empty for auto)
	o.HandleKeyPress(keyRunes("0"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "max_sessions", changed)
	require.NotNil(t, cfg.MaxSessions, "explicit unlimited is a non-nil pointer, distinct from auto")
	assert.Equal(t, 0, *cfg.MaxSessions)

	row := maxSessionsRow(t, o)
	assert.Contains(t, row, "unlimited")
	assert.NotContains(t, row, "auto", "explicit unlimited is not the auto default")
}

func TestSettingsOverlay_TextEditCommits(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.BranchPrefix = "zvi/"
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "branch_prefix")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes("wip-"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "branch_prefix", changed)
	assert.Equal(t, "zvi/wip-", cfg.BranchPrefix)
}

func TestSettingsOverlay_EscCloses(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed)
}

func TestSettingsOverlay_NavigationClampsAtEnds(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	assert.Equal(t, 0, o.cursor)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, o.cursor, "up at the top clamps")

	for range o.rows {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, len(o.rows)-1, o.cursor, "down at the bottom clamps")

	// j/k vi keys navigate too.
	o.HandleKeyPress(keyRunes("k"))
	assert.Equal(t, len(o.rows)-2, o.cursor)
	o.HandleKeyPress(keyRunes("j"))
	assert.Equal(t, len(o.rows)-1, o.cursor)
}

func TestSettingsOverlay_RenderSmoke(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 40)
	out := stripANSI(o.Render())

	for _, want := range []string{"Settings", "Theme", "esc close"} {
		assert.Contains(t, out, want)
	}

	// Every category must reach the render as a section header. Derived from
	// allCategories() rather than a second hardcoded list, so the test cannot drift
	// from the vocabulary. The panel windows its body, so this needs a terminal tall
	// enough for all ten sections plus their rows.
	o.SetSize(80, 80)
	tall := stripANSI(o.Render())
	for _, c := range allCategories() {
		assert.Containsf(t, tall, c.label(), "category %q has no section header", c.label())
	}
}

func TestSettingsOverlay_RenderFitsWidth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TmuxConfigOverride = strings.Repeat("/very/long/path", 20)
	o := NewSettingsOverlay(cfg)
	o.SetSize(60, 40)
	for _, line := range strings.Split(o.Render(), "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), 60, "no rendered line may exceed the overlay width")
	}
}

func TestSettingsOverlay_ShortTerminalScrollsToCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 14) // far fewer lines than rows+headers need
	settingsAt(t, o, "tmux_config_override")
	out := stripANSI(o.Render())
	assert.Contains(t, out, "Tmux config override", "the selected row must be visible on short terminals")
}

// TestSettingsOverlay_LongSummaryShownInFull pins that a summary too wide for the box
// wraps and is shown in full rather than clipped to one line. The assertion is on the
// summary's tail *and* on its absence from the first footer line, so it cannot pass
// vacuously on a summary that simply fit.
//
// (Before the summary/detail split this test used group_mode's 443-char description;
// that prose now lives in detail, which PR B renders behind `?`. The phrase itself is
// pinned by TestDetailRetainsTheMovedProse.)
func TestSettingsOverlay_LongSummaryShownInFull(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(50, 40) // narrow box, tall terminal: the summary must wrap, not be capped
	settingsAt(t, o, "max_sessions")

	footer := o.renderFooter(o.innerWidth())
	require.Greater(t, len(footer), 2, "the summary must wrap onto more than one line")
	assert.NotContains(t, stripANSI(footer[0]), "host",
		"the tail must be on a wrapped line, or this test proves nothing")

	out := stripANSI(o.Render())
	assert.Contains(t, out, "host", "the summary's tail must survive wrapping")
	assert.Contains(t, out, "esc close", "the key hint stays visible")
}

// TestSettingsOverlay_FooterNeverClipsHint guards the regression that a
// variable-height (wrapped) footer could push the box past the terminal, making
// PlaceOverlay bottom-clip the pinned hint line. The rendered box height must
// stay within the terminal for any terminal >= 12 rows (below that it degrades
// like the pre-existing windowing).
//
// The sweep covers every height in the range, not just a few samples: the body
// budget (renderBody) and the description cap (renderFooter) are two separate
// formulas that must stay in numeric lockstep for the box to fit, so a dense
// sweep catches any future drift between them. It also exercises the height
// (12) at which the footer's full-width cut line trips the ellipsis hard-truncate
// branch — without which that line would soft-wrap in Render, grow the box, and clip
// the hint.
func TestSettingsOverlay_FooterNeverClipsHint(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	// The worst case is whichever row has the widest footer, so derive it rather than
	// naming one: group_mode held the role before the copy rewrite (443-char
	// description), update_base_on_create held it after, and adding one caution moved
	// it again. A hardcoded key silently stops testing the worst case.
	settingsAt(t, o, widestFooterRow(t))
	for h := 12; h <= 40; h++ {
		o.SetSize(80, h)
		out := o.Render()
		assert.LessOrEqualf(t, lipgloss.Height(out), h,
			"box height must fit terminal height %d", h)
		assert.Containsf(t, stripANSI(out), "esc close",
			"the hint must survive at terminal height %d", h)
	}
}

// TestSettingsOverlay_LongDescriptionCapsWithEllipsis pins that on a terminal too
// short to show the whole summary, it is capped with a trailing ellipsis and the hint
// still renders.
//
// Height 12 is the size the cap needs now: maxDescLines is height-11, and the widest
// footer text wraps to more than one line at inner width 60. The summary/detail split
// shortened every help string from as much as 443 chars to at most 74, so the heights
// this test used before (14/15) no longer reach the capping branch at all — they would
// pass whether or not the cap works.
func TestSettingsOverlay_LongDescriptionCapsWithEllipsis(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 12) // maxDescLines = 1, against a multi-line footer
	settingsAt(t, o, widestFooterRow(t))
	out := stripANSI(o.Render())
	assert.Contains(t, out, "…", "a short terminal caps the description with an ellipsis")
	assert.Contains(t, out, "esc close", "the hint must remain visible")
}

// TestSettingsOverlay_FooterCutLineStaysWithinInner pins the footer's inner
// defense directly: when the description is capped on a short terminal and the
// last kept line is already full-width, appending the ellipsis must not push it
// past the inner width. If it did, Render's lipgloss box would soft-wrap that
// line, add a row, and clip the pinned hint.
//
// 80x12 with update_base_on_create is the case that trips xansi.Truncate under the
// summary budget: its footer text wraps so that the first (and only kept) line is
// exactly 60 cells — one over the inner-1 threshold the branch guards.
//
// This row is named rather than derived, and it is deliberately *not* the widest
// footer — widest and worst-case are different selectors here. The widest row's first
// line happens to wrap well short of the threshold, so it never reaches the truncate
// branch at all; swapping widestFooterRow in here would quietly downgrade this to a
// test of the ellipsis alone. The precondition below is what holds the choice in place,
// and it measures `key` — the same row the cursor is on — so it fails both ways it can
// rot: a copy edit that moves the wrap point, and a swap to a row that never reaches
// the branch. Reading the key from a literal a second time would leave the two free to
// diverge, and the swap would pass.
func TestSettingsOverlay_FooterCutLineStaysWithinInner(t *testing.T) {
	const key = "update_base_on_create"

	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 12)
	settingsAt(t, o, key)
	inner := o.innerWidth()

	firstLine := strings.Split(
		ansi.Wrap(rowByKey(t, config.DefaultConfig(), key).footerText(), inner, ""), "\n")[0]
	require.Greater(t, ansi.StringWidth(firstLine), inner-1,
		"row %q's first wrapped line is %d cells, not over inner-1 (%d): the hard-truncate "+
			"branch never fires, so this test would only be checking the ellipsis",
		key, ansi.StringWidth(firstLine), inner-1)

	footer := o.renderFooter(inner)
	for i, line := range footer {
		assert.LessOrEqualf(t, ansi.StringWidth(line), inner,
			"footer line %d must stay within inner width %d after capping", i, inner)
	}
	// The ellipsis confirms the cap actually fired, so the width check above is
	// exercising the truncate path rather than a description that simply fit.
	assert.Contains(t, stripANSI(strings.Join(footer, "\n")), "…",
		"the capped description must end with an ellipsis")
}

func TestSettingsOverlay_ErrShownInRender(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 40)
	settingsAt(t, o, "daemon_poll_interval")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes("x"))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Contains(t, stripANSI(o.Render()), o.lastErr)
}

// TestSettingsOverlay_CycleAutoUpdate pins the auto-update enum: defaults to
// notify, cycles notify → auto → off and wraps back to notify.
func TestSettingsOverlay_CycleAutoUpdate(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_update")

	require.Equal(t, config.AutoUpdateNotify, cfg.GetAutoUpdateMode(), "defaults to notify")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "auto_update", changed, "must report its row key so home can persist")
	assert.Equal(t, config.AutoUpdateAuto, cfg.GetAutoUpdateMode())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.AutoUpdateOff, cfg.GetAutoUpdateMode())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.AutoUpdateNotify, cfg.GetAutoUpdateMode(), "enum wraps")
}

func TestSettingsOverlay_CycleSessionSort(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "session_sort")

	require.Equal(t, config.SessionSortCreation, cfg.GetSessionSort(), "defaults to creation")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "session_sort", changed, "must report its row key so home can persist")
	assert.Equal(t, config.SessionSortStatus, cfg.GetSessionSort())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.SessionSortCreation, cfg.GetSessionSort(), "enum wraps")
}

func TestSettingsOverlay_CycleGroupMode(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "group_mode")

	require.Equal(t, config.GroupModeRepo, cfg.GetGroupMode(), "defaults to repo")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "group_mode", changed, "must report its row key so home can persist")
	assert.Equal(t, config.GroupModeAccount, cfg.GetGroupMode())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, config.GroupModeRepo, cfg.GetGroupMode(), "enum wraps")
}

// The account-clustering row presents an off/on toggle while storing the
// repo/account config value underneath, so config.json (and a future third
// grouping axis) keep their vocabulary. The label names the added layer rather
// than implying account grouping replaces repo grouping.
func TestSettingsOverlay_AccountClusteringRowMapsOffOn(t *testing.T) {
	cfg := config.DefaultConfig()
	var row settingRow
	for _, r := range newSettingRows(cfg) {
		if r.key == "group_mode" {
			row = r
		}
	}
	require.Equal(t, "Account clustering", row.label)
	require.Equal(t, []string{"off", "on"}, row.options(cfg))

	require.Equal(t, "off", row.get(cfg), "repo (the default) displays as off")
	require.NoError(t, row.set(cfg, "on"))
	require.Equal(t, config.GroupModeAccount, cfg.GroupMode, "on stores account")
	require.Equal(t, "on", row.get(cfg))
	require.NoError(t, row.set(cfg, "off"))
	require.Equal(t, config.GroupModeRepo, cfg.GroupMode, "off stores repo")
}

func TestSettingsOverlay_CarryFilesRowExists(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	assert.True(t, o.SelectRow("carry_files"), "settings panel must have a carry_files row")
}

func TestSettingsOverlay_CarryFilesGetDisplaysDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 40)
	settingsAt(t, o, "carry_files")
	out := stripANSI(o.Render())
	// The default carry list is [".claude/settings.local.json"]; the row must
	// show it rather than "(none)".
	assert.Contains(t, out, ".claude/settings.local.json")
}

func TestSettingsOverlay_CarryFilesGetDisplaysNoneWhenEmpty(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{} // explicit empty opts out
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")
	row := o.rows[o.cursor]
	assert.Equal(t, "(none)", row.get(cfg))
}

func TestSettingsOverlay_CarryFilesEditCommitsSingleEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes(".env.local"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "carry_files", changed)
	assert.Equal(t, []string{".env.local"}, cfg.CarryFiles)
}

func TestSettingsOverlay_CarryFilesEditCommitsMultipleEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes(".env.local, .envrc , .secrets"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "carry_files", changed)
	assert.Equal(t, []string{".env.local", ".envrc", ".secrets"}, cfg.CarryFiles)
}

func TestSettingsOverlay_CarryFilesEditEmptyStringClearsList(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	// editGet pre-fills with the current raw list; clear it entirely.
	for range ".claude/settings.local.json" {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "carry_files", changed)
	assert.Empty(t, cfg.CarryFiles, "an empty field must set an explicit empty list (opt-out)")
}

func TestSettingsOverlay_CarryFilesEditGetReturnsRawList(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{".env", ".envrc"}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")
	row := o.rows[o.cursor]
	require.NotNil(t, row.editGet)
	assert.Equal(t, ".env, .envrc", row.editGet(cfg))
}

func TestSettingsOverlay_CarryFilesSetBlankEntriesOptOut(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{".env"}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")
	row := o.rows[o.cursor]

	// Comma- and whitespace-only input carries no real entries: it must
	// collapse to a non-nil empty slice (the explicit opt-out), never nil —
	// nil would make GetCarryFiles fall back to the default list.
	require.NoError(t, row.set(cfg, " , ,  "))
	assert.NotNil(t, cfg.CarryFiles, "opt-out must be an explicit empty slice, not nil")
	assert.Empty(t, cfg.CarryFiles)
}

func TestSettingsOverlay_LinkPathsRowExists(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	assert.True(t, o.SelectRow("link_paths"), "settings panel must have a link_paths row")
}

func TestSettingsOverlay_LinkPathsGetDisplaysNoneWhenUnset(t *testing.T) {
	cfg := config.DefaultConfig() // link_paths has no default: off until configured
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "link_paths")
	row := o.rows[o.cursor]
	assert.Equal(t, "(none)", row.get(cfg))
}

func TestSettingsOverlay_LinkPathsEditCommitsMultipleEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "link_paths")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes("node_modules, container/agent-runner/node_modules"))
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "link_paths", changed)
	assert.Equal(t, []string{"node_modules", "container/agent-runner/node_modules"}, cfg.LinkPaths)
}

func TestSettingsOverlay_LinkPathsSetBlankEntriesClearToNil(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LinkPaths = []string{"node_modules"}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "link_paths")
	row := o.rows[o.cursor]

	// Unlike carry_files there is no nil-vs-empty contract: nil and empty both
	// mean off, so nil is the honest "not configured" and keeps the key out of
	// the saved file (link_paths is omitempty).
	require.NoError(t, row.set(cfg, " , ,  "))
	assert.Nil(t, cfg.LinkPaths)
}

func TestSettingsOverlay_LinkPathsEditGetReturnsRawList(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LinkPaths = []string{"node_modules", ".husky/_"}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "link_paths")
	row := o.rows[o.cursor]
	require.NotNil(t, row.editGet)
	assert.Equal(t, "node_modules, .husky/_", row.editGet(cfg))
}

// --- Project-scan and smart-dispatch rows (#399 item 5) -----------------------
//
// These three keys were JSON-only: the README carried a "†" legend declaring
// them unreachable from the panel. They fit the widget kinds the panel already
// has (a comma-separated list like carry_files, a tri-state int like
// max_sessions, a bool), so the carve-out was accidental rather than principled.

func TestSettingsOverlay_ProjectScanAndSmartDispatchRowsExist(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, key := range []string{"project_search_roots", "project_search_depth", "smart_dispatch_auto"} {
		assert.Truef(t, o.SelectRow(key), "settings panel must have a %s row", key)
	}
}

// The roots row round-trips a comma-separated list, and clearing it restores the
// default rather than storing an explicit empty list — GetProjectSearchRoots
// treats nil and empty alike, so nil is the honest encoding of "no override".
func TestSettingsOverlay_ProjectSearchRootsRoundTrip(t *testing.T) {
	cfg := config.DefaultConfig()
	row := settingRowByKey(t, cfg, "project_search_roots")

	assert.Equal(t, "~", row.get(cfg), "the unset default displays as the home directory")
	require.NoError(t, row.set(cfg, " ~/src , ~/work "))
	assert.Equal(t, []string{"~/src", "~/work"}, cfg.ProjectSearchRoots, "entries are split and trimmed")
	assert.Equal(t, "~/src, ~/work", row.get(cfg))

	require.NoError(t, row.set(cfg, "  ,  "))
	assert.Nil(t, cfg.ProjectSearchRoots, "an all-blank entry clears the override")
	assert.Equal(t, "~", row.get(cfg), "and the default comes back")
}

// The depth row is the max_sessions tri-state: empty = built-in default, 0 = off,
// N = that depth.
func TestSettingsOverlay_ProjectSearchDepthTriState(t *testing.T) {
	cfg := config.DefaultConfig()
	row := settingRowByKey(t, cfg, "project_search_depth")

	assert.Contains(t, row.get(cfg), "default", "an unset depth names the value it stands in for")
	assert.Contains(t, row.get(cfg), strconv.Itoa(config.DefaultProjectSearchDepth()))
	assert.Equal(t, "", row.editGet(cfg), "and edits as empty, so re-saving keeps it unset")

	require.NoError(t, row.set(cfg, "0"))
	assert.Equal(t, "off", row.get(cfg), "0 disables the scan and says so")
	assert.Equal(t, "0", row.editGet(cfg))

	require.NoError(t, row.set(cfg, "5"))
	assert.Equal(t, "5", row.get(cfg))
	assert.Equal(t, 5, cfg.GetProjectSearchDepth())

	require.NoError(t, row.set(cfg, ""))
	assert.Nil(t, cfg.ProjectSearchDepth, "empty returns the key to unset")
}

// A depth past the accessor's clamp is refused rather than accepted and silently
// rewritten — echoing back a number GetProjectSearchDepth ignores would be a lie.
func TestSettingsOverlay_ProjectSearchDepthRefusesPastTheClamp(t *testing.T) {
	cfg := config.DefaultConfig()
	row := settingRowByKey(t, cfg, "project_search_depth")

	err := row.set(cfg, strconv.Itoa(config.MaxProjectSearchDepth()+1))
	require.Error(t, err, "a value the accessor would clamp must be refused, not stored")
	assert.Contains(t, err.Error(), strconv.Itoa(config.MaxProjectSearchDepth()), "the error names the ceiling")
	assert.Nil(t, cfg.ProjectSearchDepth, "and nothing is written")

	require.Error(t, row.set(cfg, "-1"), "a negative depth is not the off switch; 0 is")
	require.Error(t, row.set(cfg, "three"))
}

// settingRowByKey returns the declared row for key, failing the test if absent.
func settingRowByKey(t *testing.T, cfg *config.Config, key string) settingRow {
	t.Helper()
	for _, r := range newSettingRows(cfg) {
		if r.key == key {
			return r
		}
	}
	t.Fatalf("no settings row for %q", key)
	return settingRow{}
}

// The README's configuration reference claims which keys the panel cannot edit.
// That claim is hand-maintained prose, and it went stale the moment these three
// rows landed — nothing pinned it to the schema. This is that pin: every key in
// the README table either has a row, or is one of the documented exceptions, and
// the exception list itself must not name a key that does have a row.
func TestReadmeSettingsExceptionsMatchTheRowSchema(t *testing.T) {
	// Keys the panel deliberately cannot edit: three lists *of records* (one
	// value per row cannot express them) and one deprecated key superseded by
	// glyph_set. Keep in sync with the legend under "#### Configuration reference".
	exceptions := map[string]bool{
		"profiles": true, "claude_accounts": true, "gh_accounts": true, "agy_accounts": true, "nerd_font": true,
	}

	rows := map[string]bool{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		rows[r.key] = true
	}

	readme := moduleFile(t, "README.md")
	start := strings.Index(readme, "#### Configuration reference")
	require.GreaterOrEqual(t, start, 0)
	section := readme[start:]
	if end := strings.Index(section, "### FAQs"); end > 0 {
		section = section[:end]
	}

	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)`").FindAllStringSubmatch(section, -1) {
		key := m[1]
		if exceptions[key] {
			assert.Falsef(t, rows[key], "%s is listed as a panel exception but has a settings row", key)
			continue
		}
		assert.Truef(t, rows[key], "%s is documented as panel-editable but has no settings row", key)
	}

	for key := range exceptions {
		assert.Containsf(t, section, "`"+key+"`", "exception %s must appear in the README table", key)
	}
}

// moduleFile walks up from the test's working directory to the module root and
// reads the named file (see the identical helper in packages config and keys).
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			b, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			return string(b)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "reached filesystem root without finding go.mod (looking for %s)", name)
		dir = parent
	}
}
