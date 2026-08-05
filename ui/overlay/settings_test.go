package overlay

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stripANSI removes escape sequences so assertions can match plain text.
func stripANSI(s string) string { return ansi.Strip(s) }

func keyRunes(s string) tea.KeyPressMsg {
	return textMsg(s)
}

// settingsAt moves the overlay cursor onto the row with the given key, failing
// the test if no such row exists.
func settingsAt(t *testing.T, o *SettingsOverlay, key string) {
	t.Helper()
	require.True(t, o.OpenAt(key), "settings panel should have a %q row", key)
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
	// A one-line footer would make the callers' capping assertions vacuous. The bound is the
	// panel's own inner width at the 80-column floor — 74 since PR B widened the box from a
	// fixed 64 — read from the overlay rather than restated as a literal, so a further width
	// change cannot leave a stale number here.
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	require.Greater(t, widest, o.innerWidth(),
		"the widest footer (%q, %d cells) must exceed the inner width %d to wrap",
		key, widest, o.innerWidth())
	return key
}

// worstExpandedHelpRow returns the key of the row whose `?` view wraps to the most lines at
// w×h, for the same reason widestFooterRow exists: a key that was the worst case when a test
// was written stops being it, and the test then guards nothing while still passing.
//
// It measures expandedHelpWrapped — what the renderer itself windows — under
// config.DefaultConfig(), so the callers do not restate the composition. The answer varies with
// the WIDTH (that is what innerWidth wraps against; boxWidth ignores the height), and the
// callers differ on which size they care about — at 60x20 it is not the row with the longest
// detail literal. h is taken so a caller passes its own size rather than a width alone, and so
// the helper keeps working if the box's height ever reaches the wrap.
//
// Unlike widestFooterRow it does NOT assert its answer overflows, because at one caller's size
// it legitimately does not: at 100x32 the tallest ? view is splash's 21 lines against a 25-line
// budget, and TestExpandedHelpDoesNotChangeTheBoxHeight wants that non-overflowing case to
// exercise the padding half of expandedHelpLines. A caller that needs overflow asserts it
// itself — TestExpandedHelpScrolls requires a positive maxHelpScroll before it scrolls.
func worstExpandedHelpRow(t *testing.T, w, h int) string {
	t.Helper()
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(w, h)
	key, tallest := "", -1
	for i, r := range o.rows {
		o.cursor = i
		if n := len(o.expandedHelpWrapped()); n > tallest {
			key, tallest = r.key, n
		}
	}
	require.NotEmpty(t, key, "the schema must declare at least one row")
	require.Positive(t, tallest, "the tallest ? view must have lines, or the callers prove nothing")
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

	wrapped := 0
	for _, r := range newSettingRows(config.DefaultConfig()) {
		lines := strings.Split(ansi.Wrap(r.footerText(), inner, ""), "\n")
		if len(lines) == 2 {
			wrapped++
		}
		assert.LessOrEqualf(t, len(lines), 2,
			"row %q wraps its footer to %d lines at inner width %d (%d cells); trim the "+
				"summary or the caution", r.key, len(lines), inner, ansi.StringWidth(r.footerText()))
	}
	// PR B widened the box from inner 60 to inner 74, so without this the cap could pass with
	// every footer on one line — proving nothing about the two-line budget the help pane's prose
	// is sized against.
	require.Positive(t, wrapped,
		"at least one footer must actually need two lines at inner width %d, or this cap is vacuous",
		inner)
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
		o.SetSize(100, 32) // wide and tall, so the help pane is at its full three lines
		settingsAt(t, o, r.key)
		help := stripANSI(strings.Join(o.helpLines(), " "))
		assert.Containsf(t, help, r.caution,
			"row %q declares a caution the help pane never renders", r.key)
	}
	// Without this the loop body could stop running and the test would still pass.
	require.Positive(t, cautions, "at least one row must declare a caution")
}

func TestSettingsOverlay_ToggleBool(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_attach")

	closed, changed := o.HandleKeyPress(keyMsg(" "))
	assert.False(t, closed)
	assert.Equal(t, "auto_attach", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetAutoAttach(), "space flips the default-on field off")

	// Enter toggles bools too.
	_, changed = o.HandleKeyPress(keyMsg("enter"))
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
	_, changed := o.HandleKeyPress(keyMsg(" "))
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
	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "notifications_finished", changed, "a cycle must report its row key so home can persist")
	assert.Equal(t, config.NotificationsOff, cfg.GetNotificationsFinished(), "same → off")

	_, _ = o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.NotificationsBell, cfg.GetNotificationsFinished(), "off → bell")

	_, _ = o.HandleKeyPress(keyMsg("right"))
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
	_, changed := o.HandleKeyPress(keyMsg(" "))
	assert.Equal(t, "mouse", changed, "a toggle must report its row key so home can persist and live-apply")
	assert.False(t, cfg.GetMouse(), "space turns capture off")

	_, changed = o.HandleKeyPress(keyMsg("enter"))
	assert.Equal(t, "mouse", changed)
	assert.True(t, cfg.GetMouse(), "enter turns it back on")
}

func TestSettingsOverlay_ToggleTrustWorktreesRoot(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "trust_worktrees_root")

	require.False(t, cfg.GetTrustWorktreesRoot(), "trust must default off (opt-in)")
	_, changed := o.HandleKeyPress(keyMsg(" "))
	assert.Equal(t, "trust_worktrees_root", changed)
	assert.True(t, cfg.GetTrustWorktreesRoot())
}

func TestSettingsOverlay_ToggleAutoYes(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_yes")

	_, changed := o.HandleKeyPress(keyMsg(" "))
	assert.Equal(t, "auto_yes", changed)
	assert.True(t, cfg.AutoYes)
}

func TestSettingsOverlay_TogglePRCreateDraft(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "pr_create_draft")

	require.True(t, cfg.GetPRCreateDraft(), "PRs default to draft")
	_, changed := o.HandleKeyPress(keyMsg(" "))
	assert.Equal(t, "pr_create_draft", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetPRCreateDraft(), "space flips the default-on draft field to ready-for-review")
}

func TestSettingsOverlay_ToggleShowReleaseNotesAfterUpdate(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "show_release_notes_after_update")

	require.True(t, cfg.GetShowReleaseNotesAfterUpdate(), "notes default on")
	_, changed := o.HandleKeyPress(keyMsg(" "))
	assert.Equal(t, "show_release_notes_after_update", changed, "a toggle must report its row key so home can persist")
	assert.False(t, cfg.GetShowReleaseNotesAfterUpdate(), "space flips the default-on field off")
}

func TestSettingsOverlay_CycleThemeWraps(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "theme")

	// SelectableNames, not Names: the picker offers the reserved `auto` value too,
	// so a cycle of len(Names()) would no longer close.
	names := theme.SelectableNames()
	start := cfg.Theme

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "theme", changed)
	assert.NotEqual(t, start, cfg.Theme, "right must advance to the next theme")
	assert.Contains(t, names, cfg.Theme)

	// A full cycle returns to the starting theme (wrap-around).
	for i := 1; i < len(names); i++ {
		o.HandleKeyPress(keyMsg("right"))
	}
	assert.Equal(t, start, cfg.Theme)

	// Left cycles backwards (and wraps too).
	o.HandleKeyPress(keyMsg("left"))
	assert.Equal(t, names[(indexOf(names, start)+len(names)-1)%len(names)], cfg.Theme)
}

// TestSettingsOverlay_CycleModelIndicator pins the model-chip enum: defaults
// to on, cycles on → off, and wraps back to on.
func TestSettingsOverlay_CycleModelIndicator(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "model_indicator")

	require.Equal(t, config.ModelIndicatorOn, cfg.GetModelIndicator(), "chip defaults to on")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "model_indicator", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.ModelIndicatorOff, cfg.GetModelIndicator())

	o.HandleKeyPress(keyMsg("right"))
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

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "effort_indicator", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.EffortIndicatorOff, cfg.GetEffortIndicator())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.EffortIndicatorOn, cfg.GetEffortIndicator(), "the enum wraps")
}

// TestSettingsOverlay_CycleSplash pins the splash enum: defaults to random,
// right steps into the named patterns, off is the last stop, and a full lap
// wraps back to random.
//
// The lap length is derived from the row's own options rather than written out,
// so adding a pattern (or another mode) does not silently turn "a full cycle"
// into "most of one" — the shape this test asserts is the wrap, not the count.
func TestSettingsOverlay_CycleSplash(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "splash")
	options := rowByKey(t, cfg, "splash").options(cfg)

	require.Equal(t, config.SplashRandom, cfg.GetSplash(), "splash defaults to random")
	require.Equal(t, config.SplashOff, options[len(options)-1], "off is the last option offered")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "splash", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.SplashVariants()[0], cfg.GetSplash())

	// One right short of a full lap lands on off — the rung #316 added, and the
	// only value that reaches config as something other than a pattern name.
	for i := 0; i < len(options)-2; i++ {
		o.HandleKeyPress(keyMsg("right"))
	}
	assert.Equal(t, config.SplashOff, cfg.GetSplash(), "off must be reachable by cycling")
	assert.False(t, cfg.SplashEnabled(), "picking off must disable the splash")

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.SplashRandom, cfg.GetSplash(), "the enum wraps")
	assert.True(t, cfg.SplashEnabled(), "cycling past off re-enables the splash")
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
	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "codex", cfg.DefaultProgram)
	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "gemini", cfg.DefaultProgram)
	o.HandleKeyPress(keyMsg("right"))
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
	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "claude", cfg.DefaultProgram)
	// …and a full cycle returns to the raw value: nothing is destroyed.
	o.HandleKeyPress(keyMsg("right"))
	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "/home/user/launch-claude.sh", cfg.DefaultProgram)
	// Cycling backwards from the raw value wraps onto the last profile.
	o.HandleKeyPress(keyMsg("left"))
	assert.Equal(t, "gemini", cfg.DefaultProgram)
}

func TestSettingsOverlay_SingleProfileCycleIsNoop(t *testing.T) {
	cfg := config.DefaultConfig() // no profiles → one synthesized from DefaultProgram
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "default_program")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Empty(t, changed, "cycling a single-option enum must not report a change")
	assert.Equal(t, "claude", cfg.DefaultProgram)
}

func TestSettingsOverlay_IntEditRejectsGarbageAndCommitsValid(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "daemon_poll_interval")

	// Enter starts an inline edit pre-filled with the current value.
	closed, changed := o.HandleKeyPress(keyMsg("enter"))
	assert.False(t, closed)
	assert.Empty(t, changed)
	assert.True(t, o.editing)

	o.HandleKeyPress(keyRunes("abc"))
	closed, changed = o.HandleKeyPress(keyMsg("enter"))
	assert.False(t, closed)
	assert.Empty(t, changed, "an invalid value must not commit")
	assert.True(t, o.editing, "edit mode persists so the user can fix the value")
	assert.NotEmpty(t, o.lastErr)
	assert.Equal(t, 1000, cfg.DaemonPollInterval)

	// Esc abandons the edit without committing.
	closed, _ = o.HandleKeyPress(keyMsg("esc"))
	assert.False(t, closed, "esc during an edit cancels the edit, not the panel")
	assert.False(t, o.editing)

	// A valid value commits and reports the row key.
	o.HandleKeyPress(keyMsg("enter"))
	for range "1000" {
		o.HandleKeyPress(keyMsg("backspace"))
	}
	o.HandleKeyPress(keyRunes("2000"))
	_, changed = o.HandleKeyPress(keyMsg("enter"))
	assert.Equal(t, "daemon_poll_interval", changed)
	assert.False(t, o.editing)
	assert.Equal(t, 2000, cfg.DaemonPollInterval)
}

func TestSettingsOverlay_PollIntervalClampedToFloor(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "daemon_poll_interval")

	o.HandleKeyPress(keyMsg("enter"))
	for range "1000" {
		o.HandleKeyPress(keyMsg("backspace"))
	}
	o.HandleKeyPress(keyRunes("50"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
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

	o.HandleKeyPress(keyMsg("enter"))
	for range "5" {
		o.HandleKeyPress(keyMsg("backspace"))
	}
	_, changed := o.HandleKeyPress(keyMsg("enter"))
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

	o.HandleKeyPress(keyMsg("enter")) // edit (pre-filled empty for auto)
	o.HandleKeyPress(keyRunes("0"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
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

	o.HandleKeyPress(keyMsg("enter"))
	o.HandleKeyPress(keyRunes("wip-"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
	assert.Equal(t, "branch_prefix", changed)
	assert.Equal(t, "zvi/wip-", cfg.BranchPrefix)
}

func TestSettingsOverlay_EscCloses(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	closed, _ := o.HandleKeyPress(keyMsg("esc"))
	assert.True(t, closed)
}

// Navigation clamping now lives in settings_nav_test.go, split by pane:
// TestRailNavigationClampsAtEnds and TestRowNavigationStaysWithinTheCategory (plus
// TestPagingKeysStayWithinTheCategory for PgUp/PgDn/Home/End). A single test cannot cover
// both any more — ↑/↓ mean "category" on the rail and "row" in the rows pane, and a fresh
// overlay opens on the rail (TestPanelOpensOnTheRail).

func TestSettingsOverlay_RenderSmoke(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	out := stripANSI(o.Render())

	assert.Contains(t, out, "Settings")
	assert.Contains(t, out, "Session limit", "the landing category's rows are visible")
	assert.Contains(t, out, "esc close", "the rail's hint")

	// Every rail entry is visible at once — the two-pane rail is the orientation the old
	// single column lacked (D2), so this no longer needs an artificially tall terminal.
	// Derived from railEntries() rather than a second hardcoded list.
	for _, e := range railEntries() {
		assert.Containsf(t, out, e.label, "rail entry %q is not rendered", e.label)
	}

	// A row from another category becomes visible once selected, and the hint changes with
	// the focus (spec §15).
	settingsAt(t, o, "theme")
	selected := stripANSI(o.Render())
	assert.Contains(t, selected, "Theme")
	assert.Contains(t, selected, "esc back", "the rows pane advertises a different esc")
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
	o.SetSize(80, 14)
	settingsAt(t, o, "tmux_config_override")

	// The flat view, not Advanced. OpenAt syncs the rail to the row's category, and
	// Advanced has three rows — which fit any pane — so asserting there would test nothing about
	// windowing. All settings is 57 lines into a pane of three.
	o.railCursor = 0
	o.syncCursorToRail()
	require.Greater(t, len(o.rowsPaneContent(o.rowsPaneWidth())), o.paneHeight(),
		"the flat view must overflow the pane, or windowing is untested")

	assert.Contains(t, stripANSI(o.Render()), "Tmux config override",
		"the selected row must be visible on a short terminal")
}

// TestSettingsOverlay_LongSummaryWrapsWithinTheHelpPane pins that a summary too wide for one
// line is wrapped and shown whole inside the fixed-height help pane, rather than clipped. The
// assertion is on the tail *and* on its absence from the first line, so it cannot pass on a
// summary that simply fit.
//
// (Before PR B the footer grew to fit and this test asserted on renderFooter's line count. The
// pane is now fixed at three lines — that is the D5 fix — so what is pinned here is that the
// text still arrives, not that the pane resized. The prose that used to be asserted on is
// group_mode's detail, pinned by TestDetailRetainsTheMovedProse.)
func TestSettingsOverlay_LongSummaryWrapsWithinTheHelpPane(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(56, 40) // narrow: single-pane, so the summary must wrap
	settingsAt(t, o, "max_sessions")

	help := o.helpLines()
	require.Len(t, help, o.helpHeight(), "the pane is exactly helpHeight() lines")
	assert.NotContains(t, stripANSI(help[0]), "host",
		"the tail must be on a wrapped line, or this test proves nothing")
	assert.Contains(t, stripANSI(strings.Join(help, "\n")), "host",
		"the summary's tail must survive wrapping")
	assert.Contains(t, stripANSI(o.Render()), "esc back", "the key hint stays visible")
}

// The height sweep that used to live here is now TestBoxNeverOutgrowsTheTerminal in
// settings_render_test.go, which also sweeps WIDTH. The mechanism it guards is unchanged and
// more important than before — paneHeight and helpHeight are two separate formulas that must
// stay in numeric lockstep, and PR B adds a second way to overflow (a body line too wide for
// the box soft-wraps rather than degrading, growing the box a row at a time).

// TestSettingsOverlay_HelpPaneCapsWithEllipsis pins that help too long for the fixed pane is
// capped with a trailing ellipsis and the pane stays exactly its budgeted height.
//
// The width matters and is the reason the old heights (14/15, then 12) no longer work: this
// used to test renderFooter's maxDescLines, which scaled with terminal HEIGHT. The pane is now
// a fixed three lines, so the cap is reached by making the box NARROW instead — at the
// 80-column inner width of 74 the widest footer wraps to two lines and never reaches it.
func TestSettingsOverlay_HelpPaneCapsWithEllipsis(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(40, 24)
	settingsAt(t, o, widestFooterRow(t))

	help := o.helpLines()
	require.Len(t, help, o.helpHeight())
	// Precondition: the prose must actually exceed the budget the pane leaves it, or the
	// ellipsis below proves nothing. The context line claims one row, so the prose gets
	// helpHeight()-1.
	proseBudget := o.helpHeight() - 1
	wrapped := strings.Split(ansi.Wrap(o.selectedRow().footerText(), o.innerWidth(), ""), "\n")
	require.Greater(t, len(wrapped), proseBudget,
		"the footer must need more than %d lines at inner width %d", proseBudget, o.innerWidth())

	assert.Contains(t, stripANSI(strings.Join(help, "\n")), "…",
		"the capped help ends with an ellipsis")
	assert.Contains(t, stripANSI(o.Render()), "esc back", "the hint must remain visible")
}

// TestSettingsOverlay_HelpCutLineStaysWithinInner pins the help pane's inner defense
// directly: when the prose is capped to the pane's height and the last kept line is already
// full-width, appending the ellipsis must not push it past the inner width. If it did, the
// lipgloss box would soft-wrap that line, add a row, and clip the pinned hint.
//
// The case is DERIVED rather than named. The pre-PR-B version hardcoded update_base_on_create
// at 80x12 because its first wrapped line was exactly 60 cells at the old inner width of 60 —
// a fact PR B invalidates by widening the box and by capping against a prose budget that
// reserves a row for the context line. Searching for a real case keeps the test honest across
// the next copy edit; if no case exists the test says so rather than quietly checking nothing.
func TestSettingsOverlay_HelpCutLineStaysWithinInner(t *testing.T) {
	type hit struct {
		key           string
		width, height int
	}
	var found []hit
	for _, size := range []struct{ w, h int }{{40, 24}, {36, 20}, {32, 16}, {50, 13}, {44, 12}} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(size.w, size.h)
		for _, r := range newSettingRows(config.DefaultConfig()) {
			settingsAt(t, o, r.key)
			if strings.Contains(stripANSI(strings.Join(o.helpLines(), "\n")), "…") {
				found = append(found, hit{r.key, size.w, size.h})
			}
		}
	}
	require.NotEmpty(t, found,
		"no row's help is capped at any tested size, so the ellipsis-append branch is "+
			"unreachable under the current copy — report this rather than weakening the test")

	for _, f := range found {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(f.width, f.height)
		settingsAt(t, o, f.key)
		inner := o.innerWidth()
		for i, line := range o.helpLines() {
			assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), inner,
				"help line %d must stay within inner width %d after capping (row %q at %dx%d)",
				i, inner, f.key, f.width, f.height)
		}
		// The box must not have grown either: a soft-wrapped help line is exactly how the
		// pinned hint used to get clipped.
		assert.LessOrEqualf(t, lipgloss.Height(o.Render()), f.height,
			"row %q at %dx%d grew the box past the terminal", f.key, f.width, f.height)
	}
}

func TestSettingsOverlay_ErrShownInRender(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 40)
	settingsAt(t, o, "daemon_poll_interval")
	o.HandleKeyPress(keyMsg("enter"))
	o.HandleKeyPress(keyRunes("x"))
	o.HandleKeyPress(keyMsg("enter"))
	assert.Contains(t, stripANSI(o.Render()), o.lastErr)
}

// TestSettingsOverlay_CycleAutoUpdate pins the auto-update enum: defaults to
// notify, cycles notify → auto → off and wraps back to notify.
func TestSettingsOverlay_CycleAutoUpdate(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "auto_update")

	require.Equal(t, config.AutoUpdateNotify, cfg.GetAutoUpdateMode(), "defaults to notify")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "auto_update", changed, "must report its row key so home can persist")
	assert.Equal(t, config.AutoUpdateAuto, cfg.GetAutoUpdateMode())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.AutoUpdateOff, cfg.GetAutoUpdateMode())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.AutoUpdateNotify, cfg.GetAutoUpdateMode(), "enum wraps")
}

func TestSettingsOverlay_CycleSessionSort(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "session_sort")

	require.Equal(t, config.SessionSortCreation, cfg.GetSessionSort(), "defaults to creation")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "session_sort", changed, "must report its row key so home can persist")
	assert.Equal(t, config.SessionSortStatus, cfg.GetSessionSort())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.SessionSortCreation, cfg.GetSessionSort(), "enum wraps")
}

func TestSettingsOverlay_CycleGroupMode(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "group_mode")

	require.Equal(t, config.GroupModeRepo, cfg.GetGroupMode(), "defaults to repo")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "group_mode", changed, "must report its row key so home can persist")
	assert.Equal(t, config.GroupModeAccount, cfg.GetGroupMode())

	o.HandleKeyPress(keyMsg("right"))
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
	assert.True(t, o.OpenAt("carry_files"), "settings panel must have a carry_files row")
}

func TestSettingsOverlay_CarryFilesGetDisplaysDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	settingsAt(t, o, "carry_files")

	// The default carry list is [".claude/settings.local.json"]; the row must show it rather
	// than "(none)". At 80 columns it does not fit the Worktrees rows pane — 27 cells of value
	// against the slack a 23-cell label leaves — so it is truncated on the row and shown in full
	// in the help pane. Spec §10 requires exactly that, so asserting WHERE it appears turns this
	// into a free check on the requirement; asserting on Render() as a whole would pass either
	// way and say nothing about it.
	require.True(t, o.valueWasTruncated(),
		"the default must not fit an 80-col rows pane (%d cells), or this test proves nothing "+
			"about the help pane's obligation", o.rowsPaneWidth())
	assert.Contains(t, stripANSI(strings.Join(o.helpLines(), "\n")), ".claude/settings.local.json",
		"a truncated value must be recoverable from the help pane")

	// Given room, it renders on the row itself.
	o.SetSize(120, 32)
	require.False(t, o.valueWasTruncated())
	assert.Contains(t, stripANSI(o.Render()), ".claude/settings.local.json")
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

	o.HandleKeyPress(keyMsg("enter"))
	o.HandleKeyPress(keyRunes(".env.local"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
	assert.Equal(t, "carry_files", changed)
	assert.Equal(t, []string{".env.local"}, cfg.CarryFiles)
}

func TestSettingsOverlay_CarryFilesEditCommitsMultipleEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CarryFiles = []string{}
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")

	o.HandleKeyPress(keyMsg("enter"))
	o.HandleKeyPress(keyRunes(".env.local, .envrc , .secrets"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
	assert.Equal(t, "carry_files", changed)
	assert.Equal(t, []string{".env.local", ".envrc", ".secrets"}, cfg.CarryFiles)
}

func TestSettingsOverlay_CarryFilesEditEmptyStringClearsList(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "carry_files")

	o.HandleKeyPress(keyMsg("enter"))
	// editGet pre-fills with the current raw list; clear it entirely.
	for range ".claude/settings.local.json" {
		o.HandleKeyPress(keyMsg("backspace"))
	}
	_, changed := o.HandleKeyPress(keyMsg("enter"))
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
	assert.True(t, o.OpenAt("link_paths"), "settings panel must have a link_paths row")
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

	o.HandleKeyPress(keyMsg("enter"))
	o.HandleKeyPress(keyRunes("node_modules, container/agent-runner/node_modules"))
	_, changed := o.HandleKeyPress(keyMsg("enter"))
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
		assert.Truef(t, o.OpenAt(key), "settings panel must have a %s row", key)
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
	// Keys with no settingRow: five lists *of records* — `profiles`, the three
	// account lists, and `custom_commands` — which one value per row cannot
	// express, plus the deprecated `nerd_font` that `glyph_set` supersedes. Not
	// all are unreachable: `profiles` has its own record editor on the rail, and
	// the account lists have the Accounts overlay. Only `custom_commands` is
	// config.json-only. Keep in sync with the legend under "#### Configuration
	// reference".
	exceptions := map[string]bool{
		"profiles": true, "claude_accounts": true, "gh_accounts": true, "agy_accounts": true,
		"custom_commands": true, "nerd_font": true,
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

// TestSettingsOverlay_RawDefaultProgramSurvivesAProfilesEdit is spec §9's third obligation and
// guard 12's last clause. TestSettingsOverlay_RawDefaultProgramSurvivesCycle pins that a
// hand-edited raw command stays a cycle option across a CYCLE; this pins that it stays one
// across a PROFILES edit, which is the new way to reach it.
//
// The mechanism is a lexical capture taken once in newSettingRows. It survives precisely because
// s.rows is built once and never rebuilt: a refresh would recompute the capture from the
// CURRENT DefaultProgram — by then a profile name — and the raw command would be gone with no
// way back. So this test is also the guard against a well-meaning "re-read the row after a
// profiles edit" change.
func TestSettingsOverlay_RawDefaultProgramSurvivesAProfilesEdit(t *testing.T) {
	const raw = "/home/user/launch-claude.sh"
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = raw
	cfg.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	settingsAt(t, o, "default_program")
	row := o.rows[o.cursor]
	require.Contains(t, row.options(cfg), raw, "precondition: the raw command is a cycle option")

	// Cycle off it, so the live config no longer holds the raw string anywhere.
	_, _ = o.HandleKeyPress(keyMsg("right"))
	require.NotEqual(t, raw, cfg.DefaultProgram, "precondition: the raw value is only in the capture now")

	// Now edit the profile list from the editor: add one, then delete it again.
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))
	typeProfile(o, "codex")
	_, _ = o.HandleKeyPress(keyMsg("tab"))
	typeProfile(o, "codex")
	_, _ = o.HandleKeyPress(keyMsg("enter"))
	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, _ = o.HandleKeyPress(keyRunes("y"))

	settingsAt(t, o, "default_program")
	opts := o.rows[o.cursor].options(cfg)
	assert.Contains(t, opts, raw, "a profiles edit must not destroy the captured raw command")
	assert.Contains(t, opts, "claude", "and the live profile names are still there")
}

// TestSettingsOverlay_NewProfileBecomesACycleOption is the other direction: options() walks
// cfg.Profiles live, so a record added by the editor is immediately cyclable — no row rebuild
// needed, and none wanted (see the sibling above).
func TestSettingsOverlay_NewProfileBecomesACycleOption(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = "claude"
	cfg.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	settingsAt(t, o, "default_program")
	require.NotContains(t, o.rows[o.cursor].options(cfg), "gemini")

	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))
	typeProfile(o, "gemini")
	_, _ = o.HandleKeyPress(keyMsg("tab"))
	typeProfile(o, "gemini")
	_, _ = o.HandleKeyPress(keyMsg("enter"))

	settingsAt(t, o, "default_program")
	assert.Contains(t, o.rows[o.cursor].options(cfg), "gemini",
		"the enum reads cfg.Profiles live, so a new record is cyclable at once")
}

// TestSettingsOverlay_CycleContextIndicator pins the context-chip enum. Unlike
// the three on/off chip rows above it has four modes, so the cycle is worth
// walking in full: an options list that silently dropped a mode would still pass
// a "cycles and wraps" check that only looked at the first and last entry.
//
// The offered order leads with the default rather than with "off", so a user who
// arrows one step from a fresh config lands on a different *shape* of the chip
// instead of turning it off by accident.
func TestSettingsOverlay_CycleContextIndicator(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "context_indicator")

	require.Equal(t, config.ContextIndicatorPercent, cfg.GetContextIndicator(), "chip defaults to percent")

	_, changed := o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, "context_indicator", changed, "the cycle must report its row key so home can persist")
	assert.Equal(t, config.ContextIndicatorCount, cfg.GetContextIndicator())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.ContextIndicatorBar, cfg.GetContextIndicator())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.ContextIndicatorOff, cfg.GetContextIndicator())

	o.HandleKeyPress(keyMsg("right"))
	assert.Equal(t, config.ContextIndicatorPercent, cfg.GetContextIndicator(), "the enum wraps")

	o.HandleKeyPress(keyMsg("left"))
	assert.Equal(t, config.ContextIndicatorOff, cfg.GetContextIndicator(), "left wraps backwards too")
}
