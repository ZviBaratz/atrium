package overlay

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pooledCatchAllCfg is #478's reported shape: a rotation pool whose members carry no
// route rules of their own, which the README calls the NORMAL arrangement ("only one
// member needs its own route rules; the rest just share its pool name"). The second
// rule-less member is therefore the widest row the panel can produce — the long
// `unreachable` badge, the pool chip, and the availability mark all at once.
func pooledCatchAllCfg(pool string) *config.Config {
	return &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work1", Pool: pool, RemoteMatches: []string{"acme/"}},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: pool}, // rule-less #1 → "default"
		{Name: "work-3", ConfigDir: "~/.claude-work3", Pool: pool}, // rule-less #2 → "unreachable"
	}}
}

// bodyLines is how many lines the overlay's body occupies, and boxLines how many the
// bordered box does. With nothing wrapping the second is exactly the first plus six
// (border 2 + vertical padding 2 + the title and its blank line 2), so comparing
// them is the only reliable wrap detector available here: lipgloss pads every line
// of a Border()+Padding()+Width() box out to the same width, so a wrapped row is
// invisible to any width comparison taken after Render (accounts_test.go:1277-1282),
// but it does cost the box a line the body never had.
const boxChromeLines = 6

func assertNothingWraps(t *testing.T, o *AccountsOverlay, ctx string) {
	t.Helper()
	require.Equal(t, lipgloss.Height(o.renderList())+boxChromeLines, lipgloss.Height(o.Render()),
		"%s: the box is taller than its body, i.e. some line wrapped", ctx)
}

// badgeColumn is the printed width of everything a row shows before its badge — the
// column the badge starts in, and therefore the width of the dir column that
// precedes it. ANSI-safe: the badge text is emitted by one Render call, so slicing
// at its byte offset and measuring with lipgloss.Width ignores the escapes in front
// of it.
func badgeColumn(t *testing.T, line, badge string) int {
	t.Helper()
	i := strings.Index(line, badge)
	require.GreaterOrEqualf(t, i, 0, "row %q carries no %q badge", line, badge)
	return lipgloss.Width(line[:i])
}

// TestAccountsOverlay_PooledRuleLessRowFitsTheBox is #478 itself. The reported
// measurement used a 4-character pool name; this one uses `quantivly-work`, the
// repo's own realistic example (accounts_pool.go's splitPoolNote comment), because a
// short pool name lets the copy change alone carry the fixture and the test would
// then prove nothing about the layout. At 80x24 the copy change alone leaves this
// row at 79 cells against an inner width of 74.
func TestAccountsOverlay_PooledRuleLessRowFitsTheBox(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 30}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewAccountsOverlay(pooledCatchAllCfg("quantivly-work"), config.DefaultState())
			o.SetSize(size.w, size.h)

			for _, name := range []string{"work-1", "work-2", "work-3"} {
				line := rowLine(t, o.renderList(), name)
				assert.LessOrEqualf(t, lipgloss.Width(line), o.inner(),
					"%s renders %d cells against an inner width of %d", name, lipgloss.Width(line), o.inner())
			}
			assertNothingWraps(t, o, "the reported fixture")
		})
	}

	// The exact height, not merely "fits the terminal": three rows plus the tabs
	// line, its blank, the trailing blank and two hint lines is 8 body lines, so a
	// single wrapped row shows up here as 15.
	o := NewAccountsOverlay(pooledCatchAllCfg("quantivly-work"), config.DefaultState())
	o.SetSize(100, 30)
	require.Equal(t, 14, lipgloss.Height(o.Render()), "3 rows + 5 chrome lines inside a 6-line box")
}

// TestAccountsOverlay_ShortCopyKeepsTheDirColumnWhole is the copy change's own
// guard, and it exists because the obvious one does not work: once the layout flexes,
// a longer badge or a spelled-out availability chip no longer WRAPS anything — the
// dir column quietly absorbs it — so every width and height assertion above passes
// with the old copy restored. What the shorter copy actually buys is the dir column
// itself. At the box's 84-column cap this fixture keeps all 26 of its cells; with
// `catch-all (unreachable)` back it would keep 15, and with `● available` /
// `⛔ limited` back, 17.
func TestAccountsOverlay_ShortCopyKeepsTheDirColumnWhole(t *testing.T) {
	o := NewAccountsOverlay(pooledCatchAllCfg("quantivly-work"), config.DefaultState())
	o.SetSize(100, 30)
	require.Equal(t, 80, o.inner(), "pinning the 84-cap/80-inner numbers this fixture is sized against")

	assert.Equal(t, markerWidth+gutterCellWidth+nameWidth+1+(dirPadWidthBase-gutterCellWidth)+1,
		badgeColumn(t, rowLine(t, o.renderList(), "work-3"), badgeUnreachable),
		"the widest realistic row must still leave the dir column its full width")
}

// TestAccountsOverlay_RowsNeverWrapTheBox is the guarantee, as opposed to the fix:
// the pool name is free text and the terminal width is the user's, so no arithmetic
// over today's copy can promise anything. Every combination below must leave the box
// exactly as tall as its body.
func TestAccountsOverlay_RowsNeverWrapTheBox(t *testing.T) {
	long := strings.Repeat("q", 40)
	cases := map[string]*config.Config{
		"pool-free":    {ClaudeAccounts: []config.ClaudeAccount{{Name: "personal", ConfigDir: "~/.claude"}}},
		"pooled":       pooledCatchAllCfg("work"),
		"real pool":    pooledCatchAllCfg("quantivly"),
		"40-char pool": pooledCatchAllCfg(long),
		"40-char name": {ClaudeAccounts: []config.ClaudeAccount{
			{Name: long, ConfigDir: "~/.claude-work", Pool: "quantivly", RemoteMatches: []string{"acme/"}},
			{Name: "other", ConfigDir: "~/.claude-other", Pool: "quantivly"},
		}},
		// truncTail bounds display cells, not runes: a 26-rune cap on this path is 52
		// cells, which would blow the dir column's budget by itself.
		"wide-character dir": {ClaudeAccounts: []config.ClaudeAccount{
			{Name: "work", ConfigDir: "~/项目/克劳德配置/工作用账号/深层目录/更深的目录", Pool: "quantivly", RemoteMatches: []string{"acme/"}},
			{Name: "other", ConfigDir: "~/项目/克劳德配置/私人账号", Pool: "quantivly"},
		}},
		"unnamed account": {ClaudeAccounts: []config.ClaudeAccount{
			{Name: "", ConfigDir: "", Pool: "quantivly"},
			{Name: "other", ConfigDir: "~/.claude-other", Pool: "quantivly"},
		}},
	}
	for name, cfg := range cases {
		for _, w := range []int{20, 47, 64, 70, 80, 86, 100, 200} {
			o := NewAccountsOverlay(cfg, config.DefaultState())
			o.SetSize(w, 30)
			assertNothingWraps(t, o, fmt.Sprintf("%s at %d columns", name, w))
		}
	}
}

// A path of wide characters must not outgrow the column it was truncated into. The
// sweep above cannot see this on its own — Render's clip catches an oversize row and
// the box stays the right height — so the row's own width is what has to be measured.
func TestAccountsOverlay_WideCharacterDirFitsItsColumn(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/项目/克劳德配置/工作用账号/深层目录/更深的目录", Pool: "quantivly", RemoteMatches: []string{"acme/"}},
		{Name: "other", ConfigDir: "~/项目/克劳德配置/私人账号", Pool: "quantivly"},
	}}, config.DefaultState())
	o.SetSize(100, 30)

	for _, name := range []string{"work", "other"} {
		line := rowLine(t, o.renderList(), name)
		assert.LessOrEqualf(t, lipgloss.Width(line), o.inner(),
			"%s prints %d cells against an inner width of %d — a rune-counting truncation "+
				"passes a 24-rune path that prints 40 cells", name, lipgloss.Width(line), o.inner())
	}
}

// The dir column is sized once per frame from the widest tail in the list, so every
// row's badge starts in the same column. A per-row flex would satisfy every width
// assertion in this file while making the badge column zig-zag down the panel.
func TestAccountsOverlay_DirColumnIsUniformAcrossRows(t *testing.T) {
	o := NewAccountsOverlay(pooledCatchAllCfg("quantivly-work"), config.DefaultState())
	o.SetSize(100, 30)

	out := o.renderList()
	want := badgeColumn(t, rowLine(t, out, "work-1"), badgeRouted)
	assert.Equal(t, want, badgeColumn(t, rowLine(t, out, "work-2"), badgeDefault),
		"the `default` row's badge starts in the same column as the `routed` row's")
	assert.Equal(t, want, badgeColumn(t, rowLine(t, out, "work-3"), badgeUnreachable),
		"so does the widest row's — it is what the column was sized for")
}

// columnOf is the printed width of everything before the first occurrence of want —
// i.e. the column want starts in. Same ANSI reasoning as badgeColumn.
func columnOf(t *testing.T, line, want string) int {
	t.Helper()
	i := strings.Index(line, want)
	require.GreaterOrEqualf(t, i, 0, "row %q does not contain %q", line, want)
	return lipgloss.Width(line[:i])
}

// The badge is a column, so what follows it lines up. `routed` is 6 cells and
// `unreachable` is 11, and without a pad the pool chip and the availability mark
// start five columns apart on adjacent rows.
func TestAccountsOverlay_BadgeIsPaddedSoTheChipsLineUp(t *testing.T) {
	o := NewAccountsOverlay(pooledCatchAllCfg("quantivly-work"), config.DefaultState())
	o.SetSize(100, 30)
	out := o.renderList()

	g := theme.Current().Glyphs
	want := columnOf(t, rowLine(t, out, "work-1"), "pool:") // the `routed` row, shortest badge
	for _, name := range []string{"work-2", "work-3"} {     // `default` and `unreachable`
		line := rowLine(t, out, name)
		assert.Equalf(t, want, columnOf(t, line, "pool:"), "%s: the pool chip starts in its own column", name)
		assert.Equalf(t, columnOf(t, rowLine(t, out, "work-1"), g.AcctAvailable),
			columnOf(t, line, g.AcctAvailable), "%s: so does the availability mark", name)
	}
}

// The pad belongs to the tab that has something after the badge. On the GitHub and
// Antigravity tabs the badge ends the row, so padding it would be trailing
// whitespace — charged against the dir column, invisible on screen.
func TestAccountsOverlay_BadgeIsNotPaddedWithoutChips(t *testing.T) {
	cfg := twoTabCfg()
	cfg.GHAccounts = append(cfg.GHAccounts, config.GHAccount{Name: "third", ConfigDir: "~/.config/gh3"})
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(100, 30)
	o.selectTab(tabGH)

	line := rowLine(t, o.renderList(), cfg.GHAccounts[0].Name)
	assert.False(t, strings.HasSuffix(line, " "), "a GitHub row ends at its badge, with no pad behind it: %q", line)
}

// And it is sized from the whole list, not the visible window, so scrolling never
// moves it. (The same reason poolGutter and the catch-all ordering are whole-slice.)
func TestAccountsOverlay_DirColumnIsStableWhileScrolling(t *testing.T) {
	// A pool name long enough that the column actually has to shrink. With one short
	// enough to leave slack, whole-list and window-scoped sizing agree and the test
	// could not tell them apart.
	const pool = "quantivly-rotation-pool-a"
	cfg := &config.Config{}
	for i := 0; i < 30; i++ {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name: fmt.Sprintf("acct%02d", i), ConfigDir: "~/.claude", Pool: pool,
			RemoteMatches: []string{fmt.Sprintf("github.com/org%02d", i)},
		})
	}
	// The two rule-less accounts sit at the bottom, so the widest tail — the long
	// `unreachable` badge — lives off-window until the very end: a window-scoped flex
	// would re-size the column as it scrolls in.
	cfg.ClaudeAccounts = append(cfg.ClaudeAccounts,
		config.ClaudeAccount{Name: "first", ConfigDir: "~/.claude", Pool: pool},
		config.ClaudeAccount{Name: "dead", ConfigDir: "~/.claude", Pool: pool})

	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(100, 24)
	top := badgeColumn(t, rowLine(t, o.renderList(), "acct00"), badgeRouted)

	for i := 0; i < 31; i++ {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, 31, o.cursorIndex(), "scrolled to the bottom of the list")
	assert.Equal(t, top, badgeColumn(t, rowLine(t, o.renderList(), "dead"), badgeUnreachable),
		"the dir column must not resize as rows scroll in and out of the window")
}

// A negative control, not a guard: it can only fail against a fix that shrinks the
// dir column when it did not have to. An ordinary config has room to spare, so the
// column keeps the exact width it has always had and every existing row renders
// byte-identically.
func TestAccountsOverlay_OrdinaryConfigKeepsTheFullDirColumn(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-work", RemoteMatches: []string{"acme/"}},
		{Name: "personal", ConfigDir: "~/.claude"},
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(100, 30)

	// No pool anywhere → no gutter column, so the dir column is at its base width.
	assert.Equal(t, markerWidth+nameWidth+1+dirPadWidthBase+1,
		badgeColumn(t, rowLine(t, o.renderList(), "work"), badgeRouted),
		"a config with room to spare keeps the full %d-column dir field", dirPadWidthBase)
}

// A name longer than its column used to widen the whole row — padRight pads, it never
// clamps. Differential rather than absolute, so it stays sharp however much slack the
// row happens to have.
func TestAccountsOverlay_LongNameDoesNotWidenTheRow(t *testing.T) {
	mk := func(name string) *AccountsOverlay {
		o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
			{Name: name, ConfigDir: "~/.claude-work", Pool: "quantivly", RemoteMatches: []string{"acme/"}},
			{Name: "second", ConfigDir: "~/.claude-two", Pool: "quantivly"},
		}}, config.DefaultState())
		o.SetSize(100, 30)
		return o
	}
	short := rowLine(t, mk("brief").renderList(), "brief")
	longName := strings.Repeat("n", 30)
	long := rowLine(t, mk(longName).renderList(), longName[:nameWidth-1])

	assert.Equal(t, lipgloss.Width(short), lipgloss.Width(long),
		"an over-long name is clamped to its column, not allowed to push the row wider")
	// Kept from the head: a name is read left to right, unlike a path.
	assert.Contains(t, long, longName[:nameWidth-1], "the clamp keeps the start of the name")
}

// TestDirWidths pins each rung of the geometry to the exact cell it engages at. A
// sweep of `≤ inner()` assertions cannot see an off-by-one here — it only knows the
// row fit, not that it kept every column it was entitled to.
func TestDirWidths(t *testing.T) {
	// fixed = marker 2 + gutter + name 12 + two separators.
	const gutter = 2
	fixed := markerWidth + gutter + nameWidth + 2
	tail := 20

	t.Run("room to spare keeps the base width", func(t *testing.T) {
		trunc, pad := dirWidths(fixed+tail+dirPadWidthBase-gutter, gutter, tail)
		assert.Equal(t, dirPadWidthBase-gutter, pad, "exactly enough room → the full column")
		assert.Equal(t, dirTruncWidthBase-gutter, trunc)
	})
	t.Run("one cell short gives up one cell", func(t *testing.T) {
		_, pad := dirWidths(fixed+tail+dirPadWidthBase-gutter-1, gutter, tail)
		assert.Equal(t, dirPadWidthBase-gutter-1, pad, "the column absorbs the shortfall, one for one")
	})
	t.Run("the floor holds", func(t *testing.T) {
		trunc, pad := dirWidths(fixed+tail+dirMinWidth-1, gutter, tail)
		assert.Equal(t, dirMinWidth, pad, "below the floor the column stops shrinking")
		assert.Equal(t, dirMinWidth-dirGapWidth, trunc)
	})
	t.Run("the gutter comes out of the column", func(t *testing.T) {
		_, withGutter := dirWidths(200, gutterCellWidth, tail)
		_, without := dirWidths(200, 0, tail)
		assert.Equal(t, gutterCellWidth, without-withGutter,
			"the gutter's columns come OUT of the dir field, never on top of the row (#475)")
	})
}

// The availability marks are glyph-table tokens, not literals in this file, so a
// terminal on the ascii rung gets 7-bit marks like every other glyph. That is the
// whole reason they were moved into theme.Glyphs when the words came off the rows:
// as literals they bypassed the nerd/plain/ascii ladder AND TestGlyphWidths, and a
// mark nobody can render is not a signal. The legend has to move with them, or the
// panel explains a glyph it is not painting.
func TestAccountsOverlay_AvailabilityMarksFollowTheGlyphSet(t *testing.T) {
	defer theme.SetGlyphSet(theme.GlyphSetASCII)()

	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-2", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(100, 30)

	g := theme.Current().Glyphs
	require.Equal(t, "*", g.AcctAvailable, "the ascii rung's available mark")
	require.Equal(t, "x", g.AcctLimited, "the ascii rung's limited mark")

	out := o.renderList()
	assert.Contains(t, rowLine(t, out, "work-1"), g.AcctAvailable)
	assert.Contains(t, rowLine(t, out, "work-2"), g.AcctLimited)
	assert.NotContains(t, out, "●", "no plain-rung mark survives on the ascii rung")
	assert.NotContains(t, out, "⊘")

	_, extras := o.legendHints()
	assert.Equal(t, "l limited "+g.AcctLimited+" · t test routing · esc close", extras,
		"the legend names the mark the rows are actually painting")
}

// truncTail's contract is display cells, because the dir column is what the row's
// width guarantee spends.
func TestTruncTailBoundsCellsNotRunes(t *testing.T) {
	wide := "~/项目/克劳德配置" // 10 runes, 17 cells
	require.Equal(t, 10, len([]rune(wide)))
	require.Equal(t, 17, lipgloss.Width(wide), "the fixture's whole point is that cells outnumber runes")

	assert.LessOrEqual(t, lipgloss.Width(truncTail(wide, 10)), 10,
		"a rune budget would pass this at 10 runes while printing 17 cells")
	assert.Equal(t, "…", truncTail(wide, 1), "the degenerate case is bounded, not the input handed back")
	assert.Equal(t, "", truncTail(wide, 0))
	assert.Equal(t, wide, truncTail(wide, 17), "a string that already fits is untouched")
}
