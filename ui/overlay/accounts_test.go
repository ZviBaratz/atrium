package overlay

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
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

// claudeNames returns Claude account names in config order (== routing precedence).
func claudeNames(cfg *config.Config) []string {
	names := make([]string, len(cfg.ClaudeAccounts))
	for i, a := range cfg.ClaudeAccounts {
		names[i] = a.Name
	}
	return names
}

// ghNames returns GitHub account names in config order.
func ghNames(cfg *config.Config) []string {
	names := make([]string, len(cfg.GHAccounts))
	for i, a := range cfg.GHAccounts {
		names[i] = a.Name
	}
	return names
}

// agyNames returns Antigravity account names in config order.
func agyNames(cfg *config.Config) []string {
	names := make([]string, len(cfg.AgyAccounts))
	for i, a := range cfg.AgyAccounts {
		names[i] = a.Name
	}
	return names
}

func TestAccountsOverlay_NavAndTabSwitchClampsCursor(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	require.Equal(t, tabClaude, o.tab)

	o.HandleKeyPress(keyMsg("down"))
	assert.Equal(t, 1, o.cursorIndex())

	// Claude tab has 2 rows, cursor=1; GitHub tab has 1 row → cursor must clamp to 0.
	o.HandleKeyPress(keyMsg("tab"))
	assert.Equal(t, tabGH, o.tab)
	assert.Equal(t, 0, o.cursorIndex(), "cursor clamped into the shorter tab (no panic later)")
}

func TestAccountsOverlay_EmptyTabIsSafe(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	// No accounts on either tab; nav/tab/render must not panic.
	o.HandleKeyPress(keyMsg("down"))
	o.HandleKeyPress(keyMsg("tab"))
	assert.Equal(t, 0, o.cursorIndex())
	assert.Contains(t, o.Render(), "No GitHub accounts")
}

func TestAccountsOverlay_EscCloses(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	closed, dirty := o.HandleKeyPress(keyMsg("esc"))
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
	// Exact, not merely "contains unreachable": the badge used to read
	// "catch-all (unreachable)", which satisfies that substring too, so an assertion
	// without this one passes on both sides of the rename it is supposed to pin.
	// 23 cells for a badge is what pushed the row past the box in #478.
	assert.NotContains(t, out, "catch-all", "the badge is `unreachable`, not `catch-all (unreachable)`")
}

// typeInto sends each rune of s to the overlay as individual key messages.
func typeInto(o *AccountsOverlay, s string) {
	for _, r := range s {
		o.HandleKeyPress(textMsg(string(r)))
	}
}

func TestAccountsOverlay_AddAppendsOnCommit(t *testing.T) {
	cfg := &config.Config{}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("n")) // new
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "work")             // Name
	o.HandleKeyPress(keyMsg("tab")) // → Config dir
	typeInto(o, "~/.claude-work")
	_, dirty := o.HandleKeyPress(keyMsg("enter")) // commit

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

	o.HandleKeyPress(textMsg("n"))
	_, dirty := o.HandleKeyPress(keyMsg("enter")) // empty name
	assert.False(t, dirty)
	assert.Equal(t, modeEdit, o.mode, "stays in edit on validation error")
	assert.NotEmpty(t, o.lastErr)
	assert.Len(t, cfg.ClaudeAccounts, 1, "config not mutated")

	typeInto(o, "work") // duplicate of the existing account
	_, dirty = o.HandleKeyPress(keyMsg("enter"))
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

	o.HandleKeyPress(textMsg("e")) // edit row 0
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "-extra")           // mutate the Name field
	o.HandleKeyPress(keyMsg("esc")) // cancel

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

	o.HandleKeyPress(textMsg("e")) // edit row 0
	require.Equal(t, modeEdit, o.mode)

	o.HandleKeyPress(keyMsg("tab")) // Name → Config dir (cursor lands at end)
	typeInto(o, "-2")               // appends: "~/.claude-work" + "-2"

	// Capture the commit keypress's dirty return directly (typeInto only sends
	// rune keys, not Enter).
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

	assert.True(t, dirty)
	require.Len(t, cfg.ClaudeAccounts, 1, "replaced in place, not appended")
	assert.Equal(t, "work", cfg.ClaudeAccounts[0].Name, "unrenamed edit accepted by validate's self-exclusion")
	assert.Equal(t, "~/.claude-work-2", cfg.ClaudeAccounts[0].ConfigDir)
	assert.Equal(t, modeList, o.mode)
}

// expect_account has no field on this form, and commit() replaces the whole struct
// at the edited index — so without an explicit carry, any edit at all silently
// unpins the account. That is the worst possible moment to lose it: the pin exists
// to stop sessions billing the wrong login, and it would vanish exactly when
// someone was paying the account enough attention to open it.
//
// The edit here renames, so the assertion cannot pass by the row being left alone.
func TestAccountsOverlay_EditPreservesExpectAccount(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude-personal", ExpectAccount: "me@example.com"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("e")) // edit row 0
	require.Equal(t, modeEdit, o.mode)

	// Name starts focused with the cursor at the end; ctrl+u clears it (see
	// TestAccountsOverlay_EditRenameToDuplicateRejected).
	o.HandleKeyPress(keyMsg("ctrl+u"))
	typeInto(o, "home")
	require.Equal(t, "home", o.form.Name(), "rename did not take; the carry is untested")

	o.HandleKeyPress(keyMsg("enter"))

	require.Len(t, cfg.ClaudeAccounts, 1)
	assert.Equal(t, "home", cfg.ClaudeAccounts[0].Name)
	assert.Equal(t, "me@example.com", cfg.ClaudeAccounts[0].ExpectAccount,
		"editing an account wiped its expect_account pin")
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

	o.HandleKeyPress(textMsg("e")) // edit row 0 ("work")
	require.Equal(t, modeEdit, o.mode)
	require.Equal(t, "work", o.form.Name(), "seeded from row 0")

	o.HandleKeyPress(keyMsg("ctrl+u"))
	require.Equal(t, "", o.form.Name(), "field genuinely cleared, not a no-op")

	typeInto(o, "personal")
	require.Equal(t, "personal", o.form.Name(), "renamed to the OTHER account's name, not concatenated")

	_, dirty := o.HandleKeyPress(keyMsg("enter"))

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

	o.HandleKeyPress(textMsg("d"))
	require.Equal(t, modeConfirmDelete, o.mode)
	_, dirty := o.HandleKeyPress(textMsg("y"))
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

	o.HandleKeyPress(textMsg("n"))
	typeInto(o, "gh-work")
	// jump to the Token env field (index fldToken) via tab presses
	for i := 0; i < fldToken; i++ {
		o.HandleKeyPress(keyMsg("tab"))
	}
	typeInto(o, "GH_TOKEN")
	o.HandleKeyPress(keyMsg("enter"))

	require.Len(t, cfg.GHAccounts, 1)
	assert.Equal(t, []string{"GH_TOKEN"}, cfg.GHAccounts[0].TokenEnv)
}

// Tab cycles Claude → GitHub → Antigravity → Claude, and the Antigravity tab is
// backed by AgyAccounts.
func TestAccountsOverlay_TabCyclesThroughAgy(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)
	require.Equal(t, tabClaude, o.tab)

	o.HandleKeyPress(keyMsg("tab"))
	require.Equal(t, tabGH, o.tab)
	o.HandleKeyPress(keyMsg("tab"))
	require.Equal(t, tabAgy, o.tab, "third tab is Antigravity")
	o.HandleKeyPress(keyMsg("tab"))
	require.Equal(t, tabClaude, o.tab, "wraps back to Claude")

	// shift+tab goes backward.
	o.HandleKeyPress(keyMsg("shift+tab"))
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

	o.HandleKeyPress(textMsg("n"))
	require.Equal(t, modeEdit, o.mode)
	typeInto(o, "work")
	o.HandleKeyPress(keyMsg("tab")) // → Config dir
	typeInto(o, "~/.agy-work")
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

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

	o.HandleKeyPress(textMsg("d"))
	require.Equal(t, modeConfirmDelete, o.mode)
	_, dirty := o.HandleKeyPress(textMsg("y"))

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
	o.HandleKeyPress(textMsg("t"))
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

	o.HandleKeyPress(textMsg("t"))
	require.Equal(t, modePreview, o.mode)
	typeInto(o, "github.com/acme/widgets")
	assert.Contains(t, o.renderPreview(), "work", "remote matches the work account")

	o.HandleKeyPress(keyMsg("esc"))
	assert.Equal(t, modeList, o.mode)
}

func TestAccountsOverlay_PreviewEmptyAndRuleOnlyInheritAmbient(t *testing.T) {
	// 0 accounts
	o := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	o.SetSize(80, 24)
	o.HandleKeyPress(textMsg("t"))
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
	o2.HandleKeyPress(textMsg("t"))
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
	o.HandleKeyPress(textMsg("t"))
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
	o.HandleKeyPress(textMsg("t"))
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
	o.HandleKeyPress(textMsg("t"))
	o.HandleKeyPress(keyMsg("tab")) // focus: remote → path
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
	o.HandleKeyPress(textMsg("t"))
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
	o.HandleKeyPress(textMsg("t"))
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
		o.HandleKeyPress(keyMsg("down"))
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
// keep reading "unreachable" — never "default". This guards the ordering carried
// across rows above the window: it used to be a dedicated pre-scan, and is now the
// single whole-list walk rowTails makes to build every row's badge and measure its
// tail (accounts_layout.go).
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
	o.SetSize(80, 24) // budget 12 rows: no pool anywhere in this fixture, so chrome stays 12

	for i := 0; i < 20; i++ {
		o.HandleKeyPress(keyMsg("down"))
	}
	require.Equal(t, 20, o.cursorIndex())

	// Window is [9,21): the first catch-all (index 0) scrolled off; the later one
	// (index 15) is still visible.
	out := o.Render()
	require.NotContains(t, out, "firstcatch", "the first catch-all scrolled off the top")
	require.Contains(t, out, "latercatch", "the later catch-all is in the window")
	assert.Contains(t, out, "unreachable", "the later rule-less account still reads unreachable")
	assert.NotContains(t, out, "catch-all", "and reads it as the whole badge, not as a suffix")
	assert.NotContains(t, out, "default",
		"the only 'default' badge belonged to the scrolled-off first catch-all; a broken "+
			"pre-scan would wrongly render the visible later catch-all as 'default'")
}

// TestAccountsOverlay_RowWindowChromeConditionalOnSplitPoolNote pins the dormancy
// fix: rowWindow's chrome allowance for the split-pool note must be conditional on
// that note actually being able to render (Claude tab with a genuinely split pool),
// not a static worst case charged to every config. A pool-free config keeps the
// full pre-existing 12-row budget; a config with a split pool loses exactly the one
// row its own note occupies — never more, and never for a config that has no
// pools at all.
func TestAccountsOverlay_RowWindowChromeConditionalOnSplitPoolNote(t *testing.T) {
	mk := func(withSplitPool bool) *config.Config {
		cfg := &config.Config{}
		for i := 0; i < 30; i++ {
			cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
				Name: fmt.Sprintf("acct%02d", i), ConfigDir: "~/.claude",
			})
		}
		if withSplitPool {
			// Non-adjacent members of the same pool: splitPools flags this "work".
			cfg.ClaudeAccounts[0].Pool = "work"
			cfg.ClaudeAccounts[2].Pool = "work"
		}
		return cfg
	}

	flat := NewAccountsOverlay(mk(false), config.DefaultState())
	flat.SetSize(80, 24)
	start, end := flat.rowWindow(flat.activeLen(), flat.listNotes())
	assert.Equal(t, 12, end-start, "a pool-free config gets the full 12-row budget")

	split := NewAccountsOverlay(mk(true), config.DefaultState())
	split.SetSize(80, 24)
	start, end = split.rowWindow(split.activeLen(), split.listNotes())
	assert.Equal(t, 11, end-start, "a split pool costs exactly the one row its own note occupies")
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
	o.HandleKeyPress(textMsg("l"))
	assert.True(t, st.GetAccountAvailability()["work-1"].Limited, "l flags the cursored account limited")

	// 'l' again clears it.
	o.HandleKeyPress(textMsg("l"))
	assert.Empty(t, st.GetAccountAvailability(), "l again clears the flag")
}

// TestAccountsOverlay_RendersPoolAndAvailability covers row rendering: the pool name
// and the limited mark must both appear on the row of an account flagged
// unavailable.
//
// Scoped to the ROW, not the whole view. The rows stopped spelling
// "available"/"limited" in #478 — the words cost 13 columns a 96-cell row could not
// spare — and the legend line still says "l limited", so a whole-view assertion on
// that word now passes no matter what the row renders.
func TestAccountsOverlay_RendersPoolAndAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	g := theme.Current().Glyphs
	limited := rowLine(t, o.renderList(), "work-1")
	assert.Contains(t, limited, "pool:work")
	assert.Contains(t, limited, g.AcctLimited, "the flagged account carries the limited mark")
	assert.NotContains(t, limited, g.AcctAvailable, "and not the available one")

	available := rowLine(t, o.renderList(), "work-2")
	assert.Contains(t, available, g.AcctAvailable, "its unflagged pool-mate carries the available mark")
	assert.NotContains(t, available, g.AcctLimited)
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

	o.HandleKeyPress(textMsg("l"))
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

	o.HandleKeyPress(textMsg("e")) // edit row 0
	require.Equal(t, modeEdit, o.mode)
	require.True(t, o.form.showPool, "Claude edit shows the Pool field")
	assert.Equal(t, "work", o.form.Pool(), "seeded from the account")

	// Focus starts on Name; tab forward to the Pool field (index fldPool),
	// clear it, and retype a new value.
	for i := 0; i < fldPool; i++ {
		o.HandleKeyPress(keyMsg("tab"))
	}
	o.HandleKeyPress(keyMsg("ctrl+u"))
	typeInto(o, "otherpool")
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

	assert.True(t, dirty)
	assert.Equal(t, "otherpool", cfg.ClaudeAccounts[0].Pool, "commit writes the edited Pool")

	// GH tab: the field must not exist at all.
	o.selectTab(tabGH)
	o.HandleKeyPress(textMsg("n"))
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
	o.HandleKeyPress(textMsg("n"))
	require.Equal(t, modeEdit, o.mode)
	assert.False(t, o.form.showPool, "a new agy account form must not show the Pool field")

	// Edit an existing agy account.
	o.form = nil
	o.mode = modeList
	o.HandleKeyPress(textMsg("e"))
	require.Equal(t, modeEdit, o.mode)
	assert.False(t, o.form.showPool, "an agy account edit form must not show the Pool field")
}

// A rate-limit flag is keyed by account NAME, so a rename must carry it — otherwise
// renaming an exhausted account silently reports it as available again, and rotation
// hands it the next session (#470).
func TestAccountsOverlay_RenameCarriesAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work", Pool: "quantivly"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work", "2026-07-25T12:00:00Z"))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("e")) // edit row 0
	require.Equal(t, modeEdit, o.mode)
	o.HandleKeyPress(keyMsg("ctrl+u")) // focus starts on Name
	typeInto(o, "zvi.baratz")
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

	require.True(t, dirty)
	avail := st.GetAccountAvailability()
	assert.True(t, avail["zvi.baratz"].Limited, "the flag follows the account to its new name")
	assert.Equal(t, "2026-07-25T12:00:00Z", avail["zvi.baratz"].Until, "including when it lifts")
	assert.NotContains(t, avail, "work", "the old key is not left behind as an orphan")
}

// Editing anything OTHER than the name must leave availability exactly as it was —
// the carry is keyed off a name change, not off committing the form.
func TestAccountsOverlay_EditKeepingNameLeavesAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work", Pool: "quantivly"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("e"))
	for i := 0; i < fldPool; i++ {
		o.HandleKeyPress(keyMsg("tab"))
	}
	o.HandleKeyPress(keyMsg("ctrl+u"))
	typeInto(o, "renamed-pool")
	o.HandleKeyPress(keyMsg("enter"))

	assert.True(t, st.GetAccountAvailability()["work"].Limited)
}

// Deleting an account drops its rate-limit flag: state keys availability by name, so
// a leftover entry would silently apply to a future account that reuses the name.
func TestAccountsOverlay_DeleteClearsAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("d"))
	require.Equal(t, modeConfirmDelete, o.mode)
	_, dirty := o.HandleKeyPress(textMsg("y"))

	require.True(t, dirty)
	assert.Empty(t, st.GetAccountAvailability())
}

// An availability entry outlives the account it belonged to whenever one is deleted
// or renamed away from — exactly the orphaned keys `atrium doctor` reports. validate()
// only rejects a name a LIVE account holds, so a rename can land squarely on one.
// The renamed account is the authority on its own new name: its flag must win, not be
// swallowed by the stale entry sitting there.
func TestAccountsOverlay_RenameOntoOrphanedAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work", "2026-07-25T12:00:00Z"))
	// No account is named zvi.baratz — a leftover from an earlier rename or delete.
	require.NoError(t, st.SetAccountLimited("zvi.baratz", "2019-01-01T00:00:00Z"))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("e"))
	o.HandleKeyPress(keyMsg("ctrl+u"))
	typeInto(o, "zvi.baratz")
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

	require.True(t, dirty)
	avail := st.GetAccountAvailability()
	assert.True(t, avail["zvi.baratz"].Limited, "the renamed account is still rate-limited")
	assert.Equal(t, "2026-07-25T12:00:00Z", avail["zvi.baratz"].Until,
		"the carried entry overwrites the orphan rather than losing to it")
	assert.NotContains(t, avail, "work", "the old key is not left behind")
}

// The mirror case: an account with NO flag renamed onto an orphaned entry. Carrying
// nothing must also mean leaving nothing — otherwise the stale flag comes back to
// life under a name that is now live, and rotation skips a perfectly usable login.
func TestAccountsOverlay_RenameOntoOrphanClearsStaleFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("zvi.baratz", "")) // orphan; work has no flag
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	o.HandleKeyPress(textMsg("e"))
	o.HandleKeyPress(keyMsg("ctrl+u"))
	typeInto(o, "zvi.baratz")
	_, dirty := o.HandleKeyPress(keyMsg("enter"))

	require.True(t, dirty)
	assert.Empty(t, st.GetAccountAvailability(),
		"an unlimited account must not inherit an orphan's exhaustion")
}

// TestAccountsOverlay_ReorderSwapsAndFollowsCursor: J moves the cursored account
// down one slot in config order (which IS routing precedence) and the cursor
// tracks the account, not the position, so a second J keeps moving the same one.
func TestAccountsOverlay_ReorderSwapsAndFollowsCursor(t *testing.T) {
	cfg := twoTabCfg() // Claude: work, personal
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	closed, dirty := o.HandleKeyPress(textMsg("J"))
	assert.False(t, closed)
	assert.True(t, dirty, "reorder mutates config, so the app must persist it")
	assert.Equal(t, []string{"personal", "work"}, claudeNames(cfg))
	assert.Equal(t, 1, o.cursorIndex(), "cursor follows the moved account")

	// K moves it back.
	_, dirty = o.HandleKeyPress(textMsg("K"))
	assert.True(t, dirty)
	assert.Equal(t, []string{"work", "personal"}, claudeNames(cfg))
	assert.Equal(t, 0, o.cursorIndex())
}

// Boundary presses must not report dirty — a no-op must not trigger a config write.
func TestAccountsOverlay_ReorderAtBoundsIsNoOp(t *testing.T) {
	cfg := twoTabCfg() // Claude: work, personal
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	// Cursor starts at row 0 — K (up) is already at the top boundary.
	_, dirty := o.HandleKeyPress(textMsg("K"))
	assert.False(t, dirty, "K at row 0 is a no-op")
	assert.Equal(t, []string{"work", "personal"}, claudeNames(cfg))
	assert.Equal(t, 0, o.cursorIndex())

	// Move to the last row: J (down) is now at the bottom boundary.
	o.HandleKeyPress(keyMsg("down"))
	require.Equal(t, 1, o.cursorIndex())
	_, dirty = o.HandleKeyPress(textMsg("J"))
	assert.False(t, dirty, "J at the last row is a no-op")
	assert.Equal(t, []string{"work", "personal"}, claudeNames(cfg))
	assert.Equal(t, 1, o.cursorIndex())
}

// Order is first-match precedence in every section, so reorder works on all tabs.
func TestAccountsOverlay_ReorderWorksOnGHAndAgyTabs(t *testing.T) {
	cfg := &config.Config{
		GHAccounts: []config.GHAccount{
			{Name: "gh-work", ConfigDir: "~/.config/gh-work"},
			{Name: "gh-personal", ConfigDir: "~/.config/gh-personal"},
		},
		AgyAccounts: []config.AgyAccount{
			{Name: "agy-work", ConfigDir: "~/.agy-work"},
			{Name: "agy-personal", ConfigDir: "~/.agy-personal"},
		},
	}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	o.selectTab(tabGH)
	_, dirty := o.HandleKeyPress(textMsg("J"))
	assert.True(t, dirty)
	assert.Equal(t, []string{"gh-personal", "gh-work"}, ghNames(cfg))

	o.selectTab(tabAgy)
	o.cursor = 0 // selectTab only clamps; it does not reset the cursor across tabs
	_, dirty = o.HandleKeyPress(textMsg("J"))
	assert.True(t, dirty)
	assert.Equal(t, []string{"agy-personal", "agy-work"}, agyNames(cfg))
}

// The shift+arrow aliases must behave identically to J/K.
func TestAccountsOverlay_ReorderShiftArrowAliases(t *testing.T) {
	cfg := twoTabCfg() // Claude: work, personal
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	_, dirty := o.HandleKeyPress(keyMsg("shift+down"))
	assert.True(t, dirty)
	assert.Equal(t, []string{"personal", "work"}, claudeNames(cfg))
	assert.Equal(t, 1, o.cursorIndex())

	_, dirty = o.HandleKeyPress(keyMsg("shift+up"))
	assert.True(t, dirty)
	assert.Equal(t, []string{"work", "personal"}, claudeNames(cfg))
	assert.Equal(t, 0, o.cursorIndex())
}

// Dormancy: a single-account tab cannot reorder and must report no change.
func TestAccountsOverlay_ReorderSingleAccountIsInert(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "solo"}}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	_, dirty := o.HandleKeyPress(textMsg("J"))
	assert.False(t, dirty, "J with a single account is a no-op")
	_, dirty = o.HandleKeyPress(textMsg("K"))
	assert.False(t, dirty, "K with a single account is a no-op")
	assert.Equal(t, []string{"solo"}, claudeNames(cfg))
	assert.Equal(t, 0, o.cursorIndex())
}

// rowLine returns the single rendered line containing name, so a test can assert
// which row a badge landed on rather than merely that the badge exists somewhere in
// the whole view — an assert.Contains over the entire view is order-blind and can't
// tell rows apart.
func rowLine(t *testing.T, view, name string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	t.Fatalf("no rendered line contains %q; full view:\n%s", name, view)
	return ""
}

// The point of reordering: order is first-match precedence and the FIRST rule-less
// account is the catch-all, so moving a rule-less account to the top changes which
// account an unmatched repo resolves to — and the rendered badges follow.
func TestAccountsOverlay_ReorderChangesCatchAll(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "alpha"}, // first rule-less → the catch-all
		{Name: "bravo"}, // rule-less but later → unreachable
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	name, _, isDefault := cfg.ResolveClaudeAccount("", "/tmp/anything")
	require.Equal(t, "alpha", name)
	require.True(t, isDefault)
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "default")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "unreachable")

	o.HandleKeyPress(textMsg("J")) // alpha down

	name, _, _ = cfg.ResolveClaudeAccount("", "/tmp/anything")
	assert.Equal(t, "bravo", name, "the new first rule-less account is the catch-all")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "default")
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "unreachable")
}

// Same for the rule-matching case: two accounts whose rules both match a remote
// resolve to whichever is first, so reorder flips the winner. GH tab, to pin that
// the GH section's order is load-bearing too. Both rows carry a rule (neither is a
// catch-all), so their badges both read "routed" before and after — reordering
// doesn't touch the badge in this case, only which resolved dir wins the match —
// so the rowLine checks here guard against a broken reorder scrambling a row's
// fields (e.g. dropping RemoteMatches), while the config-level dir flip below is
// what actually pins the precedence change.
func TestAccountsOverlay_ReorderChangesGHMatchPriority(t *testing.T) {
	cfg := &config.Config{GHAccounts: []config.GHAccount{
		{Name: "alpha", ConfigDir: "/cfg/alpha", RemoteMatches: []string{"github.com/acme"}},
		{Name: "bravo", ConfigDir: "/cfg/bravo", RemoteMatches: []string{"github.com/acme"}},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabGH)

	dir := cfg.ResolveGHConfigDir("github.com/acme/widgets", "")
	require.Equal(t, "/cfg/alpha", dir, "first match wins")
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "routed")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "routed")

	o.HandleKeyPress(textMsg("J")) // alpha down, cursor starts on row 0

	dir = cfg.ResolveGHConfigDir("github.com/acme/widgets", "")
	assert.Equal(t, "/cfg/bravo", dir, "the newly-first account now wins the match")
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "routed")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "routed")
}

// TestAccountsOverlay_LegendAdvertisesReorder pins the "select" vs "move" split
// (cursor keys read "select" now that J/K owns "move" as its own verb) and
// gates the reorder hint on there being a second row to swap with —
// advertising J/K on a tab where it's a no-op would violate the "never name a
// dead key" convention this legend already follows for "l limited".
func TestAccountsOverlay_LegendAdvertisesReorder(t *testing.T) {
	cfg := twoTabCfg() // Claude: 2 accounts (reorder live); GH: 1 account (reorder dead)
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	out := o.Render()
	assert.Contains(t, out, "↑/↓ select")
	assert.NotContains(t, out, "↑/↓ move")
	assert.Contains(t, out, "J/K reorder", "2 Claude accounts: reorder is live")

	// A single account: J/K can't move anything, so it must not be advertised.
	solo := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "solo"}}}
	oSolo := NewAccountsOverlay(solo, config.DefaultState())
	oSolo.SetSize(80, 24)
	assert.NotContains(t, oSolo.Render(), "J/K reorder", "1 account: reorder is dead")

	// GH tab of twoTabCfg also has only 1 row.
	o.selectTab(tabGH)
	out = o.Render()
	assert.NotContains(t, out, "J/K reorder", "GH tab has 1 row: reorder is dead")
	assert.Contains(t, out, "t test routing")

	// 0 accounts: rows() is empty, so the key is equally dead — the "never name
	// a dead key" convention must hold here too, not just at exactly 1 account.
	empty := NewAccountsOverlay(&config.Config{}, config.DefaultState())
	empty.SetSize(80, 24)
	assert.NotContains(t, empty.Render(), "J/K reorder", "0 accounts: reorder is dead")
}

// TestAccountsOverlay_LegendFitsAndKeepsLimitedClaudeOnly pins where "l
// limited" landed after the reflow (line 2, alongside "t test routing", so
// line 1 doesn't wrap once it also carries "J/K reorder") and that it stays
// Claude-scoped. The fit check measures o.legendHints()'s raw (unstyled)
// strings against o.inner(), not the rendered box: once a line passes through
// t.OverlayHintStyle().Render() inside the Border()+Padding()+Width() box,
// lipgloss pads every line — wrapped or not — out to the same total width, so
// a post-render width comparison can never detect a wrap. Measuring the raw
// text before it's styled is what actually catches a line that no longer fits
// o.inner() (74 cols at an 80-column terminal).
func TestAccountsOverlay_LegendFitsAndKeepsLimitedClaudeOnly(t *testing.T) {
	cfg := twoTabCfg() // Claude: 2 accounts — widest case (J/K reorder + l limited both shown)
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	hint, extras := o.legendHints()
	assert.LessOrEqual(t, lipgloss.Width(hint), o.inner(), "line 1 must fit inside the box without wrapping")
	assert.LessOrEqual(t, lipgloss.Width(extras), o.inner(), "line 2 must fit inside the box without wrapping")

	// Exact, not Contains. The rows stopped spelling "limited" in #478, so this line
	// is now the only place the mark is explained — and `Contains("l limited")`
	// cannot tell `l limited ⊘` from `l limited`, i.e. cannot see the mark being
	// dropped again. (SKILL.md: vocabulary guards must be exact match.)
	assert.Equal(t, "l limited "+theme.Current().Glyphs.AcctLimited+" · t test routing · esc close", extras)

	out := o.Render()
	// "l limited" must share a line with "t test routing" (line 2), not with
	// "d delete"/"J/K reorder" (line 1) — the actual move this task makes. This
	// pins placement; it's the width check above that pins fit.
	assert.Contains(t, rowLine(t, out, "l limited"), "t test routing",
		"l limited moved onto the second hint line alongside t test routing")

	o.selectTab(tabGH)
	assert.NotContains(t, o.Render(), "l limited", "l limited is Claude-only")
}

// TestAccountsOverlay_PooledMembersRenderBracketed pins the render-level wiring of
// poolGutter into renderList: two adjacent Claude accounts sharing a pool are
// bracketed top-to-bottom by the gutter glyphs, not merely named as a pool via the
// existing "pool:x" chip.
func TestAccountsOverlay_PooledMembersRenderBracketed(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	out := o.Render()
	assert.Contains(t, rowLine(t, out, "work-1"), "┌", "run head carries the top bracket")
	assert.Contains(t, rowLine(t, out, "work-2"), "└", "run tail carries the bottom bracket")
}

// TestAccountsOverlay_SplitPoolShowsNoteAndNoBracket covers the case poolGutter
// cannot bracket: when a pool's members are not adjacent, no bracket glyph renders
// at all, and the list instead prints the nudge to use J/K to bring them together.
func TestAccountsOverlay_SplitPoolShowsNoteAndNoBracket(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	// renderList (not Render) so the box border's own top-left corner glyph can't
	// collide with the "no bracket" assertion below — see
	// TestAccountsOverlay_NoPoolsRendersFlat, which documents why.
	out := o.renderList()
	assert.NotContains(t, out, "┌", "non-adjacent members can't be bracketed")
	assert.Contains(t, out, "pool 'work' is split")
}

// TestAccountsOverlay_NoPoolsRendersFlat pins the dormancy contract: with no pools
// configured, no gutter glyph renders (not even blank padding) and no split note
// appears — the table renders exactly as it did before this feature existed.
func TestAccountsOverlay_NoPoolsRendersFlat(t *testing.T) {
	o := NewAccountsOverlay(twoTabCfg(), config.DefaultState())
	o.SetSize(80, 24)

	// renderList (not Render) so the box border's own "│" edge can't collide with
	// the gutter's middle-connector glyph in the assertion below.
	out := o.renderList()
	assert.NotContains(t, out, "┌")
	assert.NotContains(t, out, "│")
	assert.NotContains(t, out, "└")
	assert.NotContains(t, out, "is split")
}

// TestAccountsOverlay_GutterIsClaudeTabOnly guards the o.tab == tabClaude gate:
// only ClaudeAccount carries a Pool field, so the gutter and split note must never
// appear on the GH tab even when its account names happen to look like pool
// members. The Claude side deliberately carries BOTH an adjacent run (would
// bracket) and a genuinely split pool (would print a note) so this test can't
// pass merely because poolGutter/splitPools happen to return nil for this
// fixture regardless of the tab gate — a config with only a split pool leaves
// poolGutter nil on its own, so removing the gate around the gutter
// computation specifically would go undetected.
func TestAccountsOverlay_GutterIsClaudeTabOnly(t *testing.T) {
	cfg := &config.Config{
		ClaudeAccounts: []config.ClaudeAccount{
			{Name: "work-1", Pool: "work"}, // adjacent run: would bracket on the Claude tab
			{Name: "work-2", Pool: "work"},
			{Name: "personal"},
			{Name: "home-1", Pool: "home"}, // split: would print a note on the Claude tab
			{Name: "mid"},
			{Name: "home-2", Pool: "home"},
		},
		GHAccounts: []config.GHAccount{
			{Name: "work-1", ConfigDir: "~/.config/gh-work1"},
			{Name: "work-2", ConfigDir: "~/.config/gh-work2"},
		},
	}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	o.selectTab(tabGH)

	// renderList (not Render): the box border's own top-left corner glyph must not
	// collide with the "no bracket" assertion — see
	// TestAccountsOverlay_NoPoolsRendersFlat, which documents why.
	out := o.renderList()
	assert.NotContains(t, out, "┌")
	assert.NotContains(t, out, "is split")
}

// TestAccountsOverlay_GutterSurvivesWindowScroll mirrors
// TestAccountsOverlay_CatchAllBadgeSurvivesWindowScroll: poolGutter is computed over
// the whole account slice before windowing, so scrolling until the run head lands
// exactly on the window's first visible row still renders its "┌" correctly. A bug
// that recomputed the gutter on the windowed sub-slice, or indexed it by
// window-relative position instead of the absolute config index, would show a blank
// gutter cell (or the wrong glyph) on this row instead.
func TestAccountsOverlay_GutterSurvivesWindowScroll(t *testing.T) {
	cfg := &config.Config{}
	for i := 0; i < 10; i++ {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name: fmt.Sprintf("acct%02d", i), ConfigDir: "~/.claude",
		})
	}
	cfg.ClaudeAccounts = append(cfg.ClaudeAccounts,
		config.ClaudeAccount{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work"},
		config.ClaudeAccount{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	)
	for i := 12; i < 30; i++ {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name: fmt.Sprintf("acct%02d", i), ConfigDir: "~/.claude",
		})
	}

	o := NewAccountsOverlay(cfg, config.DefaultState())
	// budget 12 rows: this pool run is adjacent (not split), so splitPools is empty
	// and the split-pool note never renders — chrome stays 12.
	o.SetSize(80, 24)

	for i := 0; i < 21; i++ {
		o.HandleKeyPress(keyMsg("down"))
	}
	require.Equal(t, 21, o.cursorIndex())

	// Window is [10,22): rows 0-9 (including the run head's would-be predecessors)
	// scrolled off, and the run (10,11) is fully in view with work-1 landing on the
	// window's very first visible row.
	out := o.Render()
	require.NotContains(t, out, "acct09", "rows above the window scrolled off")
	require.Contains(t, out, "work-1", "the run head is in the window")
	assert.Contains(t, rowLine(t, out, "work-1"), "┌", "run head keeps its top bracket even as the window's first visible row")
	assert.Contains(t, rowLine(t, out, "work-2"), "└")
}

// TestAccountsOverlay_SplitPoolTwoRunsRendersBracketsAndNote covers the Task 4
// review's uncovered interaction: a pool with two SEPARATE adjacent runs (not one
// contiguous block) is bracketed at each run by poolGutter *and* still flagged split
// by splitPools, so the brackets and the split note render together in the same
// frame. poolGutter and splitPools are each unit-tested alone; this pins that
// combination specifically.
func TestAccountsOverlay_SplitPoolTwoRunsRendersBracketsAndNote(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "w1", ConfigDir: "~/.claude-w1", Pool: "work"},
		{Name: "w2", ConfigDir: "~/.claude-w2", Pool: "work"},
		{Name: "other", ConfigDir: "~/.claude-other"},
		{Name: "w3", ConfigDir: "~/.claude-w3", Pool: "work"},
		{Name: "w4", ConfigDir: "~/.claude-w4", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	out := o.Render()
	assert.Contains(t, rowLine(t, out, "w1"), "┌", "first run's head is bracketed")
	assert.Contains(t, rowLine(t, out, "w2"), "└", "first run's tail is bracketed")
	assert.Contains(t, rowLine(t, out, "w3"), "┌", "second run's head is bracketed")
	assert.Contains(t, rowLine(t, out, "w4"), "└", "second run's tail is bracketed")
	assert.Contains(t, out, "pool 'work' is split",
		"two runs are still not ONE contiguous block, so splitPools flags the pool "+
			"even though every member sits inside some bracketed run")
}

// TestAccountsOverlay_ReorderGroupsSplitPool is the end-to-end pin for the note's own
// advice: pressing J on the account between two split-pool members actually groups
// them, the bracket appears, and the note clears. Fixture: work-1 (pool work),
// personal, work-2 (pool work) — cursor moves down to personal, then J swaps
// personal past work-2, landing the order work-1, work-2, personal.
func TestAccountsOverlay_ReorderGroupsSplitPool(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	// Before: the pool's members are not adjacent — no bracket anywhere, and the
	// note names exactly this pool as split. renderList (not Render): the box
	// border's own top-left corner glyph must not collide with the "no bracket"
	// assertion — see TestAccountsOverlay_NoPoolsRendersFlat, which documents why.
	before := o.renderList()
	assert.NotContains(t, before, "┌", "non-adjacent pool members render no bracket yet")
	assert.Contains(t, before, "pool 'work' is split")

	o.HandleKeyPress(keyMsg("down"))
	require.Equal(t, 1, o.cursorIndex(), "cursor now on personal")

	closed, dirty := o.HandleKeyPress(textMsg("J"))
	assert.False(t, closed)
	assert.True(t, dirty, "reorder mutates config, so the app must persist it")
	assert.Equal(t, []string{"work-1", "work-2", "personal"}, claudeNames(cfg),
		"personal swapped past work-2, grouping the pool")

	// After: the note's fix worked — the pool is now one contiguous run, bracketed,
	// and the split note is gone.
	after := o.Render()
	assert.Contains(t, rowLine(t, after, "work-1"), "┌", "run head carries the top bracket")
	assert.Contains(t, rowLine(t, after, "work-2"), "└", "run tail carries the bottom bracket")
	assert.NotContains(t, after, "is split", "the pool is no longer split, so the nudge clears")
}

// TestAccountsOverlay_GutterNarrowsDirNotRowWidth pins the fix for the pool-gutter
// width regression a manual smoke test found: the gutter's 2 columns must come OUT
// of the dir field, not get added on top of the row, so a row that fit before the
// gutter existed still fits with it. The measured row is a third account
// ("personal") with a dir long enough to hit truncTail's cap either way, carrying
// the widest badge ("unreachable" — it's the second rule-less account) plus the
// Claude tab's mark, at a 100-column terminal (boxWidth caps at 84 -> inner() == 80).
//
// FIXTURE, after #478. The original pair was "one pool" vs "no pool anywhere", which
// isolated the gutter only because an unpooled row's chips were identical in both.
// That stopped being true when the pool chip became a padded COLUMN: a row with no
// pool of its own now carries a blank cell in that column whenever anything in the
// list is pooled, so "no pool anywhere" changes the row by two things at once and
// the comparison stops being about the gutter.
//
// The pair is now "one pool of two adjacent members" vs "two singleton pools with
// same-length names". Both render the same badges and the same pool column; only the
// first has a contiguous run for poolGutter to bracket. That is a sharper isolation
// than the original, not a weaker one — the gutter is the single variable.
//
// SCOPE. The width EQUALITY below is what guards the gutter. Its two companions are
// kept only because they cost nothing:
//
//   - the "<= inner()" check was sharp when this row measured exactly 80 of 80;
//     shortening the badge and the availability chip left it with real slack, so it
//     would now pass a badly broken gutter. The claim it used to make — that the
//     widest realistic row fits — belongs to
//     TestAccountsOverlay_PooledRuleLessRowFitsTheBox and the sweep beside it, which
//     use a fixture built to sit at the boundary.
//   - the line-count equality was sharp for the same reason and lost it the same
//     way. accounts_layout_test.go's assertNothingWraps is the version with teeth:
//     it compares the box against its OWN body rather than against another config.
//
// Per this file's own note on TestAccountsOverlay_LegendFitsAndKeepsLimitedClaudeOnly:
// comparing rendered widths AFTER lipgloss pads a Border()+Padding()+Width() box
// can't detect a wrap — every line, wrapped or not, comes out at the same padded
// width. So this measures the unstyled row string renderList builds, directly.
func TestAccountsOverlay_GutterNarrowsDirNotRowWidth(t *testing.T) {
	longDir := "~/.claude-configs/some/very/long/nested/directory/path"
	// secondPool names work-2's pool: the same as work-1's makes a contiguous run,
	// a different one of the same length makes two singletons and no run at all.
	mk := func(secondPool string) *config.Config {
		return &config.Config{ClaudeAccounts: []config.ClaudeAccount{
			{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work", RemoteMatches: []string{"acme/"}},
			{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: secondPool}, // rule-less #1 → "default"
			{Name: "personal", ConfigDir: longDir},                           // rule-less #2 → "unreachable"
		}}
	}

	oGutter := NewAccountsOverlay(mk("work"), config.DefaultState()) // one pool, two adjacent members → gutter renders
	oGutter.SetSize(100, 30)
	oFlat := NewAccountsOverlay(mk("wrk2"), config.DefaultState()) // two singleton pools → no run, no gutter column
	oFlat.SetSize(100, 30)
	require.Nil(t, poolGutter(oFlat.cfg.ClaudeAccounts), "the flat fixture must genuinely have no run to bracket")

	require.Equal(t, 80, oGutter.inner(), "pinning the reproduction's 100-col/84-cap/80-inner numbers")

	gutterLine := rowLine(t, oGutter.renderList(), "personal")
	flatLine := rowLine(t, oFlat.renderList(), "personal")

	assert.Equal(t, lipgloss.Width(flatLine), lipgloss.Width(gutterLine),
		"the gutter's 2 columns must come out of the row, not add to it")
	assert.LessOrEqual(t, lipgloss.Width(gutterLine), oGutter.inner(),
		"a sanity floor with 22 columns of slack since #478 shortened the badge and the chip — "+
			"TestAccountsOverlay_PooledRuleLessRowFitsTheBox is where the fit claim is actually pinned")

	// A wrap only shows up once the row is laid out inside the bordered,
	// Width()-constrained box — lipgloss pads every line of that box to the same
	// width regardless of wrapping, so line COUNT (not width) is what proves it.
	// Cross-config, so it can only see a wrap that hits ONE of the two; the
	// same-config form (assertNothingWraps) is the one that survives both fitting.
	gutterLines := strings.Count(oGutter.Render(), "\n")
	flatLines := strings.Count(oFlat.Render(), "\n")
	assert.Equal(t, flatLines, gutterLines, "the gutter must not add a wrapped extra line")
}

// TestAccountsOverlay_ReorderPersists is the persistence half of Task 5: dirty is
// the app's ONLY cue to call config.SaveConfig (handleAccountsState,
// app/app_accounts.go) — the overlay itself never writes to disk. This proves the
// permuted order actually round-trips through a real save/load cycle, not merely
// that SaveConfig didn't error.
func TestAccountsOverlay_ReorderPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: LoadConfig/SaveConfig must never touch the real data dir

	cfg := twoTabCfg() // Claude: work, personal
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	_, dirty := o.HandleKeyPress(textMsg("J"))
	require.True(t, dirty, "reorder must report dirty so the app knows to persist")
	require.Equal(t, []string{"personal", "work"}, claudeNames(cfg), "in-memory order after the swap")

	require.NoError(t, config.SaveConfig(cfg))

	loaded := config.LoadConfig()
	assert.Equal(t, []string{"personal", "work"}, claudeNames(loaded),
		"the permuted order survives a save/load round trip, not just an in-memory swap")
}

// twoMemberPoolCfg returns a two-member "work" pool where both accounts are
// rule-less (so an empty remote/path preview input still routes to the pool
// via the catch-all), work-1 first in config order.
func twoMemberPoolCfg() *config.Config {
	return &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
}

// openPreview presses 't' to enter the routing-preview mode.
func openPreview(o *AccountsOverlay) {
	o.HandleKeyPress(textMsg("t"))
}

// poolRowLine returns a member's row inside the pool block, not the "Claude →
// <name> (...)" headline above it — which, when the headline names that same
// member (it picked them), also contains the member's name and would
// otherwise satisfy rowLine's plain substring search on the wrong line. Every
// pool-block member row carries an availability chip ("available" or
// "limited"); the headline never does, so requiring one disambiguates them.
func poolRowLine(t *testing.T, view, name string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, name) && (strings.Contains(line, "available") || strings.Contains(line, "limited")) {
			return line
		}
	}
	t.Fatalf("no pool-block row for %q; full view:\n%s", name, view)
	return ""
}

// The pool, both members, and the limited marker all render — the "no pool
// information" half of the report.
func TestAccountsOverlay_PreviewShowsPoolAndMembers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-2", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.Contains(t, out, "pool 'work' ⇄", "the pool header names the pool and carries the rotation glyph")
	// The mark comes from the glyph table (theme.Glyphs.Acct*) so the ascii rung can
	// swap it; the WORD is what this block keeps and the account rows gave up (#478).
	g := theme.Current().Glyphs
	assert.Contains(t, poolRowLine(t, out, "work-1"), g.AcctAvailable+" available", "the available member shows the available chip")
	assert.Contains(t, poolRowLine(t, out, "work-2"), g.AcctLimited+" limited", "the limited member shows the limited chip")
}

// The report's other half: a limited member must not be presented as the pick.
func TestAccountsOverlay_PreviewSkipsLimitedAndSaysWhy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	// work-1 limited -> headline names work-2, work-2 row has "← next", and the
	// decision line reads "work-1 limited → rotating to work-2".
	assert.Contains(t, rowLine(t, out, "Claude → "), "work-2", "headline names the non-limited member")
	assert.NotContains(t, rowLine(t, out, "Claude → "), "work-1", "the limited member must not be the headline pick")
	assert.Contains(t, poolRowLine(t, out, "work-2"), "← next", "work-2's own row carries the rotation marker")
	assert.NotContains(t, poolRowLine(t, out, "work-1"), "← next", "the limited member must not carry the marker")
	assert.Contains(t, out, "work-1 limited → rotating to work-2")
}

// Mirrors what creation ACTUALLY does when everything is limited: the confirm,
// pinned to the soonest-to-reset member.
func TestAccountsOverlay_PreviewAllLimitedShowsConfirmDecision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	require.NoError(t, st.SetAccountLimited("work-2", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.Contains(t, rowLine(t, out, "Claude → "), "⚠ all 'work' accounts limited")
	assert.Contains(t, poolRowLine(t, out, "work-1"), "← on confirm",
		"both members are indefinite, so SoonestResetMember falls back to the first")
	assert.NotContains(t, poolRowLine(t, out, "work-2"), "← on confirm")
	assert.Contains(t, out, "creating asks to confirm, then uses work-1 (first member)")
}

// The defect this guards: the all-limited decision sentence used to run to
// 80 columns against a 74-column inner width (9-column previewIndent + a
// 71-column sentence), so it wrapped at the default 80x24 terminal and its
// unindented continuation line cost the block a row previewMemberBudget
// never counted for. Measuring o.Render() can't catch this — Style().Width()
// on the bordered box pads every line to the same width, so a post-render
// width assert can never fail (this project has shipped that tautology
// before) — so this measures o.renderPreview()'s own lines instead, which
// are unpadded. lipgloss.Width is ANSI-aware, so the theme styling
// renderPreview applies to parts of each line doesn't throw off the count.
func TestAccountsOverlay_PreviewAllLimitedDecisionNeverExceedsInnerWidth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	require.NoError(t, st.SetAccountLimited("work-2", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	for _, line := range strings.Split(out, "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(line), o.inner(), "line exceeds the box's inner width: %q", line)
	}
}

// The marker and the decision sentence must name the SAME member even when
// the rotation cursor points elsewhere. renderPoolDecision's marked is
// config.SoonestResetMember's pick, not SelectPoolMember's defensive cursor
// fallback (chosen) — the two disagree here on purpose: the cursor sits on
// work-2, but work-1 carries a parseable, later Until, so SoonestResetMember
// picks work-1 instead. If renderPoolDecision ever passed chosen (the cursor
// fallback) to previewDecisionLine instead of marked, the marker would stay
// on work-1 (it's rendered from marked directly) while the sentence would
// start naming work-2 — the exact drift this test exists to catch.
func TestAccountsOverlay_PreviewAllLimitedMarkerAndSentenceNameSameMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", "2099-01-01T00:00:00Z"))
	require.NoError(t, st.SetAccountLimited("work-2", ""))
	require.NoError(t, st.SetAccountRotation("work", 1))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.Contains(t, poolRowLine(t, out, "work-1"), "← on confirm",
		"SoonestResetMember picks work-1 (its Until parses and is earliest), not the rotation cursor's work-2")
	assert.NotContains(t, poolRowLine(t, out, "work-2"), "← on confirm",
		"the rotation cursor's own member must not carry the marker once the pool is exhausted")
	assert.Contains(t, out, "creating asks to confirm, then uses work-1 (resets soonest)",
		"the decision sentence must name the same member the marker landed on")
}

// Dormancy: a config with no pool must render exactly as it did before this feature.
func TestAccountsOverlay_PreviewNoPoolUnchanged(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "solo", ConfigDir: "~/.claude-solo"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.NotContains(t, out, "pool '")
	assert.NotContains(t, out, "⇄")
	assert.NotContains(t, out, "← next")
	assert.Contains(t, out, "solo (", "still resolves to the account")
	assert.Contains(t, out, ".claude-solo)", "still shows its config dir")
}

// Dormancy: a DECLARED pool with exactly one member is also fewer than two
// members, so gateAllExhausted's asymmetry (spec §3) requires the block stay
// suppressed here too — this is a genuinely pooled account, not "no pool" or
// an ungrouped single account, so it exercises a different corner of the
// len(members) < 2 branch than TestAccountsOverlay_PreviewNoPoolUnchanged.
func TestAccountsOverlay_PreviewSingletonPoolUnchanged(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "solo", ConfigDir: "~/.claude-solo", Pool: "work"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.NotContains(t, out, "pool '")
	assert.NotContains(t, out, "⇄")
	assert.NotContains(t, out, "← next")
	assert.Contains(t, out, "solo (", "still resolves to the account")
	assert.Contains(t, out, ".claude-solo)", "still shows its config dir")
}

// The cap keeps the overlay usable AND keeps the decision visible.
//
// ConfigDir deliberately has no leading "~": ResolvedConfigDir expands that
// against the ambient $HOME, and the headline embeds it, so a "~"-prefixed
// dir would make this test's 24-row assert depend on len($HOME) — passing at
// a short home directory and wrapping (thus failing, for an unrelated reason)
// at a deep one. An absolute path here sidesteps that dependency entirely.
func TestAccountsOverlay_PreviewCapsLongPool(t *testing.T) {
	cfg := &config.Config{}
	for i := 1; i <= 12; i++ {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name:      fmt.Sprintf("work-%02d", i),
			ConfigDir: fmt.Sprintf("/claude-work%02d", i),
			Pool:      "work",
		})
	}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)
	openPreview(o)

	out := o.renderPreview()
	assert.Contains(t, out, "esc back", "the hint bar must survive the capped block")
	assert.Contains(t, out, "more members not shown")
	assert.Contains(t, poolRowLine(t, out, "work-01"), "← next", "the chosen member's row stays in the capped window")
	assert.LessOrEqual(t, strings.Count(o.Render(), "\n")+1, 24, "the whole overlay must still fit the terminal")
}

// The read-only contract. Bubble Tea re-renders per keystroke; a writing preview
// would rotate the pool once per typed character.
func TestAccountsOverlay_PreviewNeverAdvancesRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoMemberPoolCfg()
	st := config.DefaultState()
	require.NoError(t, st.SetAccountRotation("work", 1))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)
	openPreview(o)

	o.renderPreview()
	o.renderPreview()
	o.renderPreview()
	assert.Equal(t, 1, st.GetAccountRotation("work"), "rendering the preview must never advance the pool cursor")
}
