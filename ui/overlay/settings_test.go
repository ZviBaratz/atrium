package overlay

import (
	"sort"
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

func TestSettingsOverlay_MaxSessionsEmptyMeansUnlimited(t *testing.T) {
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
	assert.Nil(t, cfg.MaxSessions, "an empty cap clears back to unlimited")

	// And the row displays "unlimited" rather than an empty value.
	o.SetSize(80, 40)
	assert.Contains(t, stripANSI(o.Render()), "unlimited")
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

	for _, want := range []string{"Settings", "General", "Appearance", "Behavior", "Theme", "esc close"} {
		assert.Contains(t, out, want)
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

func TestSettingsOverlay_ErrShownInRender(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 40)
	settingsAt(t, o, "daemon_poll_interval")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	o.HandleKeyPress(keyRunes("x"))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Contains(t, stripANSI(o.Render()), o.lastErr)
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
