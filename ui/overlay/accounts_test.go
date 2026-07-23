package overlay

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func twoTabCfg() *config.Config {
	return &config.Config{
		ClaudeAccounts: []config.ClaudeAccount{
			{Name: "work", ConfigDir: "~/.claude-work", RemoteMatches: []string{"github.com/acme"}},
			{Name: "personal", ConfigDir: "~/.claude"},
		},
		GHAccounts: []config.GHAccount{
			{Name: "gh-work", ConfigDir: "~/.config/gh-work", RemoteMatches: []string{"github.com/acme"}},
		},
	}
}

func TestAccountsOverlay_NavAndTabSwitchClampsCursor(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	require.Equal(t, tabClaude, o.tab)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 1, o.cursorIndex())

	// Claude tab has 2 rows, cursor=1; GitHub tab has 1 row → cursor must clamp to 0.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, tabGH, o.tab)
	assert.Equal(t, 0, o.cursorIndex(), "cursor clamped into the shorter tab (no panic later)")
}

func TestAccountsOverlay_EmptyTabIsSafe(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	// No accounts on either tab; nav/tab/render must not panic.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, 0, o.cursorIndex())
	assert.Contains(t, o.Render(), "No GitHub accounts")
}

func TestAccountsOverlay_EscCloses(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	closed, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed)
	assert.False(t, dirty)
}

func TestAccountsOverlay_BadgesMarkCatchAllAndUnreachable(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a"}, // first rule-less → default
		{Name: "b"}, // second rule-less → unreachable
		{Name: "c", RemoteMatches: []string{"github.com/x"}}, // routed
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	out := o.Render()
	assert.Contains(t, out, "default")
	assert.Contains(t, out, "unreachable")
	assert.Contains(t, out, "routed")
}

// typeInto sends each rune of s to the overlay as individual key messages.
func typeInto(o *AccountsOverlay, s string) {
	for _, r := range s {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestAccountsOverlay_AddAppendsOnCommit(t *testing.T) {
	cfg := &config.Config{}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}) // new
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "work")                            // Name
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // → Config dir
	typeInto(o, "~/.claude-work")
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // commit

	assert.True(t, dirty)
	assert.Equal(t, modeList, o.mode)
	require.Len(t, cfg.ClaudeAccounts, 1)
	assert.Equal(t, "work", cfg.ClaudeAccounts[0].Name)
	assert.Equal(t, "~/.claude-work", cfg.ClaudeAccounts[0].ConfigDir)
}

func TestAccountsOverlay_ValidationRejectsEmptyAndDuplicateName(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "work"}}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // empty name
	assert.False(t, dirty)
	assert.Equal(t, modeEdit, o.mode, "stays in edit on validation error")
	assert.NotEmpty(t, o.lastErr)
	assert.Len(t, cfg.ClaudeAccounts, 1, "config not mutated")

	typeInto(o, "work") // duplicate of the existing account
	_, dirty = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, dirty)
	assert.Equal(t, modeEdit, o.mode)
	assert.Len(t, cfg.ClaudeAccounts, 1)
}

func TestAccountsOverlay_CancelDiscardsEdits(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", RemoteMatches: []string{"github.com/acme"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}) // edit row 0
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "-extra")                          // mutate the Name field
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // cancel

	assert.Equal(t, modeList, o.mode)
	assert.Equal(t, "work", cfg.ClaudeAccounts[0].Name, "esc discards edits")
	assert.Equal(t, []string{"github.com/acme"}, cfg.ClaudeAccounts[0].RemoteMatches)
}

// TestAccountsOverlay_EditInPlaceUnrenamedCommits covers the gap left by every
// other committing test using 'n' (new, editIndex == -1): editing an EXISTING
// account without renaming it must (a) let validate's self-exclusion
// (`i != o.editIndex`) accept the unrenamed name instead of flagging it as a
// dup of itself, and (b) commit via the replace-at-index branch
// (`o.cfg.ClaudeAccounts[o.editIndex] = a`), not append.
func TestAccountsOverlay_EditInPlaceUnrenamedCommits(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}) // edit row 0
	require.Equal(t, modeEdit, o.mode)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // Name → Config dir (cursor lands at end)
	typeInto(o, "-2")                              // appends: "~/.claude-work" + "-2"

	// Capture the commit keypress's dirty return directly (typeInto only sends
	// rune keys, not Enter).
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, dirty)
	require.Len(t, cfg.ClaudeAccounts, 1, "replaced in place, not appended")
	assert.Equal(t, "work", cfg.ClaudeAccounts[0].Name, "unrenamed edit accepted by validate's self-exclusion")
	assert.Equal(t, "~/.claude-work-2", cfg.ClaudeAccounts[0].ConfigDir)
	assert.Equal(t, modeList, o.mode)
}

// TestAccountsOverlay_EditRenameToDuplicateRejected covers the other half of
// validate's self-exclusion: renaming the account being edited TO a different
// existing account's name must still be rejected (self-exclusion only forgives
// the row's OWN prior name, not other rows). The Name field starts focused
// with the seeded value ("work") and the cursor at the end; ctrl+u
// (bubbles' DeleteBeforeCursor) clears everything before the cursor, i.e. the
// whole field, so retyping doesn't concatenate into "workpersonal". Each step
// asserts the in-progress form value directly (o.form.Name(), reachable since
// this test lives in package overlay) so the test can't pass by accident if
// clearing silently no-ops and the two names concatenate into something novel.
func TestAccountsOverlay_EditRenameToDuplicateRejected(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work"}, {Name: "personal"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}) // edit row 0 ("work")
	require.Equal(t, modeEdit, o.mode)
	require.Equal(t, "work", o.form.Name(), "seeded from row 0")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})
	require.Equal(t, "", o.form.Name(), "field genuinely cleared, not a no-op")

	typeInto(o, "personal")
	require.Equal(t, "personal", o.form.Name(), "renamed to the OTHER account's name, not concatenated")

	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.False(t, dirty)
	assert.Equal(t, modeEdit, o.mode, "stays in edit on validation error")
	assert.NotEmpty(t, o.lastErr)
	assert.Equal(t, "work", cfg.ClaudeAccounts[0].Name, "rename to a duplicate rejected; original row untouched")
}

func TestAccountsOverlay_DeleteWithConfirm(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "a"}, {Name: "b"}}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.cursor = 1

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	require.Equal(t, modeConfirmDelete, o.mode)
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	assert.True(t, dirty)
	require.Len(t, cfg.ClaudeAccounts, 1)
	assert.Equal(t, "a", cfg.ClaudeAccounts[0].Name)
	assert.Equal(t, 0, o.cursor, "cursor clamped after delete")
}

func TestAccountsOverlay_GHCommitIncludesTokenEnv(t *testing.T) {
	cfg := &config.Config{}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabGH)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	typeInto(o, "gh-work")
	// jump to the Token env field (index fldToken) via tab presses
	for i := 0; i < fldToken; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	}
	typeInto(o, "GH_TOKEN")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.Len(t, cfg.GHAccounts, 1)
	assert.Equal(t, []string{"GH_TOKEN"}, cfg.GHAccounts[0].TokenEnv)
}

// Tab cycles Claude → GitHub → Antigravity → Claude, and the Antigravity tab is
// backed by AgyAccounts.
func TestAccountsOverlay_TabCyclesThroughAgy(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	require.Equal(t, tabClaude, o.tab)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, tabGH, o.tab)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, tabAgy, o.tab, "third tab is Antigravity")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, tabClaude, o.tab, "wraps back to Claude")

	// shift+tab goes backward.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyShiftTab})
	require.Equal(t, tabAgy, o.tab, "shift+tab wraps backward to Antigravity")
}

// The Antigravity tab surfaces its own empty state and add/commit path, writing to
// AgyAccounts (no token field, unlike GitHub).
func TestAccountsOverlay_AgyAddAppendsToAgyAccounts(t *testing.T) {
	cfg := &config.Config{}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabAgy)
	assert.Contains(t, o.Render(), "No Antigravity accounts")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "work")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // → Config dir
	typeInto(o, "~/.agy-work")
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, dirty)
	require.Len(t, cfg.AgyAccounts, 1)
	assert.Equal(t, "work", cfg.AgyAccounts[0].Name)
	assert.Equal(t, "~/.agy-work", cfg.AgyAccounts[0].ConfigDir)
	// The agy form must not expose the GitHub-only token field.
	assert.False(t, o.showToken())
}

// Deleting on the Antigravity tab removes from AgyAccounts, not another section.
func TestAccountsOverlay_AgyDeleteRemovesFromAgyAccounts(t *testing.T) {
	cfg := &config.Config{AgyAccounts: []config.AgyAccount{
		{Name: "work", ConfigDir: "~/.agy-work"},
		{Name: "personal", ConfigDir: "~/.agy"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabAgy)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	require.Equal(t, modeConfirmDelete, o.mode)
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	assert.True(t, dirty)
	require.Len(t, cfg.AgyAccounts, 1)
	assert.Equal(t, "personal", cfg.AgyAccounts[0].Name)
}

// The routing preview resolves and displays the Antigravity account alongside
// Claude and GitHub.
func TestAccountsOverlay_PreviewShowsAgy(t *testing.T) {
	cfg := &config.Config{AgyAccounts: []config.AgyAccount{
		{Name: "acme", ConfigDir: "~/.agy-acme", RemoteMatches: []string{"github.com/acme"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/acme/widgets")
	out := o.renderPreview()
	assert.Contains(t, out, "Antigravity → ")
	// ResolvedConfigDir expands ~, so the rendered dir is absolute.
	assert.Contains(t, out, "acme (", "routed agy account shows its name")
	assert.Contains(t, out, ".agy-acme)", "routed agy account shows its config dir")
}

func TestAccountsOverlay_PreviewResolves(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work", RemoteMatches: []string{"github.com/acme"}},
		{Name: "personal", ConfigDir: "~/.claude"}, // catch-all
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	require.Equal(t, modePreview, o.mode)
	typeInto(o, "github.com/acme/widgets")
	assert.Contains(t, o.renderPreview(), "work", "remote matches the work account")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, modeList, o.mode)
}

func TestAccountsOverlay_PreviewEmptyAndRuleOnlyInheritAmbient(t *testing.T) {
	// 0 accounts
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/acme")
	out := o.renderPreview()
	assert.Contains(t, out, "inherit")
	assert.NotContains(t, out, "Claude → \n", "no blank name")

	// rule-only (no catch-all), unmatched input
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", RemoteMatches: []string{"github.com/acme"}},
	}}
	o2 := NewAccountsOverlay(cfg, config.DefaultState())
	o2.SetSize(80, 24)
	o2.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o2, "github.com/other")
	out2 := o2.renderPreview()
	assert.Contains(t, out2, "inherit", "no-match with no catch-all inherits ambient")
	// The synthetic sentinel must render as the bare "inherit ambient env"
	// line, never as if "default" were a real account name — a broken guard
	// that dropped this distinction would still pass the Contains check above.
	assert.NotContains(t, out2, "default (", "synthetic sentinel must not render as a named account")
}

// TestAccountsOverlay_PreviewCatchAllNamedShowsName protects the
// show-the-name direction of the isDefault-aware guard: a real catch-all
// account (no rules) with an empty config dir must still render its own
// name, not collapse into the bare "inherit ambient env" sentinel line
// (which renderPreview reserves for the synthetic no-catch-all case).
func TestAccountsOverlay_PreviewCatchAllNamedShowsName(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal"}, // catch-all: no rules, empty ConfigDir
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/unmatched")
	assert.Contains(t, o.renderPreview(), "personal (inherit ambient env)")
}

// TestAccountsOverlay_PreviewRuleMatchedNamedDefaultShowsName is the case
// Fix 1 corrects: a rule (not catch-all) matched an account that happens to
// be named "default" with an empty config dir. ResolveClaudeAccount returns
// isDefault=false here, distinguishing it from the synthetic sentinel, so
// renderPreview must show the account's name rather than collapsing to the
// bare "inherit ambient env" line.
func TestAccountsOverlay_PreviewRuleMatchedNamedDefaultShowsName(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "default", RemoteMatches: []string{"github.com/acme"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/acme/x")
	assert.Contains(t, o.renderPreview(), "default (inherit ambient env)")
}

// TestAccountsOverlay_PreviewPathFieldRoutes confirms tab-switch and the
// Path input are wired into resolution, not just the Remote field.
func TestAccountsOverlay_PreviewPathFieldRoutes(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "pathacct", ConfigDir: "~/.claude-path", PathMatches: []string{"~/work/"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // focus: remote → path
	typeInto(o, "~/work/x")
	assert.Contains(t, o.renderPreview(), "pathacct", "typing into Path drives resolution")
}

// TestAccountsOverlay_PreviewGHMatchShowsDirAndToken covers the GH
// real-match render branch (previously exercised only by the "no accounts"
// and "0 accounts" paths, never an actual match).
func TestAccountsOverlay_PreviewGHMatchShowsDirAndToken(t *testing.T) {
	cfg := &config.Config{GHAccounts: []config.GHAccount{
		{Name: "gh", ConfigDir: "~/.config/gh-work", RemoteMatches: []string{"github.com/acme"}, TokenEnv: []string{"GH_TOKEN"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/acme")
	out := o.renderPreview()
	assert.Contains(t, out, "gh-work", "GitHub line shows the resolved config dir")
	assert.Contains(t, out, "[GH_TOKEN]", "GitHub line shows the token env")
}

// A matched GH account can set TokenEnv without a config dir; the preview must
// still surface the token names rather than collapsing to a bare
// "inherit ambient env" line.
func TestAccountsOverlay_PreviewGHTokenWithoutDirSurfacesToken(t *testing.T) {
	cfg := &config.Config{GHAccounts: []config.GHAccount{
		{Name: "gh", RemoteMatches: []string{"github.com/acme"}, TokenEnv: []string{"GH_TOKEN"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	typeInto(o, "github.com/acme")
	assert.Contains(t, o.renderPreview(), "inherit ambient env [GH_TOKEN]",
		"token env surfaces even when the account sets no config dir")
}

// A long account list must window its rows to the terminal height (cursor kept
// in view) rather than overflowing past the bottom, mirroring SettingsOverlay.
func TestAccountsOverlay_ListWindowsRowsOnShortTerminal(t *testing.T) {
	cfg := &config.Config{}
	for i := 0; i < 30; i++ {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name:          fmt.Sprintf("acct%02d", i),
			ConfigDir:     "~/.claude",
			RemoteMatches: []string{fmt.Sprintf("github.com/org%02d", i)},
		})
	}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	for i := 0; i < 25; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, 25, o.cursorIndex())

	out := o.Render()
	assert.LessOrEqual(t, strings.Count(out, "\n")+1, 24, "overlay fits within the terminal height")
	assert.Contains(t, out, "acct25", "the selected row stays visible")
	assert.NotContains(t, out, "acct00", "rows above the window scroll off")
}

// Catch-all badges are order-dependent (first rule-less account = "default",
// later ones = "unreachable"). When the list is windowed and the first rule-less
// account has scrolled off the top, a later rule-less account still in view must
// keep reading "catch-all (unreachable)" — never "default". This guards the
// pre-scan that carries badge state across rows above the window.
func TestAccountsOverlay_CatchAllBadgeSurvivesWindowScroll(t *testing.T) {
	cfg := &config.Config{}
	for i := 0; i < 30; i++ {
		acct := config.ClaudeAccount{
			Name:          fmt.Sprintf("acct%02d", i),
			ConfigDir:     "~/.claude",
			RemoteMatches: []string{fmt.Sprintf("github.com/org%02d", i)},
		}
		switch i {
		case 0:
			acct = config.ClaudeAccount{Name: "firstcatch", ConfigDir: "~/.claude"} // rule-less → "default"
		case 15:
			acct = config.ClaudeAccount{Name: "latercatch", ConfigDir: "~/.claude"} // rule-less → "unreachable"
		}
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, acct)
	}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24) // budget 12 rows

	for i := 0; i < 20; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, 20, o.cursorIndex())

	// Window is [9,21): the first catch-all (index 0) scrolled off; the later one
	// (index 15) is still visible.
	out := o.Render()
	require.NotContains(t, out, "firstcatch", "the first catch-all scrolled off the top")
	require.Contains(t, out, "latercatch", "the later catch-all is in the window")
	assert.Contains(t, out, "unreachable", "the later rule-less account still reads unreachable")
	assert.NotContains(t, out, "default",
		"the only 'default' badge belonged to the scrolled-off first catch-all; a broken "+
			"pre-scan would wrongly render the visible later catch-all as 'default'")
}

// TestAccountsOverlay_ToggleAvailability covers the 'l' key: it flags the
// cursored Claude account rate-limited via state.SetAccountLimited and clears
// it back via state.ClearAccountLimited on a second press. Off the Claude tab
// (or with no accounts) the key must no-op, which the sibling tab-scoped tests
// below cover.
func TestAccountsOverlay_ToggleAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	st := config.DefaultState()
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	// Cursor on work-1; 'l' flags it limited.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	assert.True(t, st.GetAccountAvailability()["work-1"].Limited, "l flags the cursored account limited")

	// 'l' again clears it.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	assert.Empty(t, st.GetAccountAvailability(), "l again clears the flag")
}

// TestAccountsOverlay_RendersPoolAndAvailability covers row rendering: the
// pool name and a "limited" marker must both appear for an account flagged
// unavailable.
func TestAccountsOverlay_RendersPoolAndAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"}}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	view := o.Render()
	assert.Contains(t, view, "pool:work")
	assert.Contains(t, view, "limited")
}

// TestAccountsOverlay_ToggleIgnoredOnGHTab guards the o.tab == tabClaude gate:
// the 'l' key must not panic or mutate state when the GH tab (whose rows are
// GHAccount, not ClaudeAccount) is active.
func TestAccountsOverlay_ToggleIgnoredOnGHTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoTabCfg()
	st := config.DefaultState()
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	o.selectTab(tabGH)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	assert.Empty(t, st.GetAccountAvailability(), "l on the GH tab must not flag anything")
}

// TestAccountForm_ClaudePoolRoundTrips covers the shared-form gating: a Claude
// edit seeds and commits the Pool field, and the field is entirely absent
// (showPool false) on a GH edit so it can never leak into a GHAccount.
func TestAccountForm_ClaudePoolRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}) // edit row 0
	require.Equal(t, modeEdit, o.mode)
	require.True(t, o.form.showPool, "Claude edit shows the Pool field")
	assert.Equal(t, "work", o.form.Pool(), "seeded from the account")

	// Focus starts on Name; tab forward to the Pool field (index fldPool),
	// clear it, and retype a new value.
	for i := 0; i < fldPool; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	}
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeInto(o, "otherpool")
	_, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, dirty)
	assert.Equal(t, "otherpool", cfg.ClaudeAccounts[0].Pool, "commit writes the edited Pool")

	// GH tab: the field must not exist at all.
	o.selectTab(tabGH)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	assert.False(t, o.form.showPool, "GH form never shows Pool")
	assert.Equal(t, "", o.form.Pool(), "Pool is empty on a GH form regardless of input contents")
}

// TestAccountsOverlay_AgyFormHasNoPool guards the openForm wiring: the Antigravity
// tab must build its form with showPool=false. It once derived showPool as
// !showToken, which — because the agy tab also passes showToken=false — wrongly
// grew a Claude-only Pool field on the agy account form.
func TestAccountsOverlay_AgyFormHasNoPool(t *testing.T) {
	cfg := &config.Config{AgyAccounts: []config.AgyAccount{
		{Name: "agy-work", ConfigDir: "~/.antigravity"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabAgy)

	// New agy account.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	require.Equal(t, modeEdit, o.mode)
	assert.False(t, o.form.showPool, "a new agy account form must not show the Pool field")

	// Edit an existing agy account.
	o.form = nil
	o.mode = modeList
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	require.Equal(t, modeEdit, o.mode)
	assert.False(t, o.form.showPool, "an agy account edit form must not show the Pool field")
}
