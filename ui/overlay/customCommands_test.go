package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runeKeyMsg builds the key message bubbletea delivers for a single printable rune.
func runeKeyMsg(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// namedKeyMsg builds the key message for a named key ("esc", "up", "enter").
func namedKeyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	panic("unknown key " + name)
}

func threeRows() []CustomCommandRow {
	return []CustomCommandRow{
		{Key: "g", Description: "lazygit in this worktree", Terminal: true},
		{Key: "c", Description: "just ci"},
		{Key: "f", Description: "git fetch --all", Repo: true},
	}
}

// sizedCustomCommands builds an overlay at a terminal-derived size, the way
// app_layout does.
func sizedCustomCommands(t *testing.T, rows []CustomCommandRow, w, h int) *CustomCommandsOverlay {
	t.Helper()
	o := NewCustomCommandsOverlay(rows)
	o.SetSize(w, h)
	return o
}

func TestCustomCommands_RowsShowKeyDescriptionAndRepoMarker(t *testing.T) {
	out := xansi.Strip(sizedCustomCommands(t, threeRows(), 60, 20).Render())

	assert.Contains(t, out, "Custom commands", "the box names itself")
	for _, want := range []string{"g", "lazygit in this worktree", "c", "just ci", "f", "git fetch --all"} {
		assert.Contains(t, out, want)
	}
	assert.Contains(t, out, customCmdRepoMarker,
		"a repo-context row must say so — it is the difference between two directories")
	assert.Contains(t, out, customCmdTerminalMarker,
		"a terminal row must say so — it is the difference between losing the screen for "+
			"minutes and not, which is why `output` is a required config key")
	// And the marker must not be claimed by rows that do not take the terminal, or it
	// says nothing at all.
	assert.Equal(t, 1, strings.Count(out, customCmdTerminalMarker),
		"only the terminal row may carry the marker")
}

// TestCustomCommands_MarkersFitTheNarrowestBox measures the widest tail a config can
// produce: BOTH markers plus a refusal, against a wide description that competes for the
// same columns. The terminal marker is 11 cells, so it moved this budget; a row that
// overshoots costs the box its border to the composer.
func TestCustomCommands_MarkersFitTheNarrowestBox(t *testing.T) {
	rows := []CustomCommandRow{
		{Key: "t", Description: strings.Repeat("日本語", 20), Repo: true, Terminal: true},
		{Key: "u", Description: strings.Repeat("ascii desc ", 8), Repo: true, Terminal: true,
			Inert: "worktree freed — resume first"},
		{Key: "v", Description: "short", Repo: true, Terminal: true},
	}
	for _, w := range []int{60, 80, 120} {
		out := xansi.Strip(sizedCustomCommands(t, rows, w, 20).Render())
		for _, l := range strings.Split(out, "\n") {
			assert.Equalf(t, w, xansi.StringWidth(l), "width=%d: line is the wrong width: %q", w, l)
		}
		assert.Containsf(t, out, customCmdTerminalMarker,
			"width=%d: the marker must survive a competing wide description", w)
		// The reason still outranks both markers — clipped to width/3 at the narrow end
		// by the existing fallback, which drops the markers whole rather than shortening
		// the reason. Asserted on its head, not the full string, because that clip is
		// the designed behaviour and predates this marker.
		assert.Containsf(t, out, "worktree freed",
			"width=%d: and the reason still outranks both markers", w)
		// Order: what it does to the screen before where it runs, matching the cheatsheet.
		// Two surfaces render these independently, so each needs its own assertion.
		ti, ri := strings.Index(out, customCmdTerminalMarker), strings.Index(out, customCmdRepoMarker)
		require.NotEqual(t, -1, ti)
		require.NotEqual(t, -1, ri)
		assert.Lessf(t, ti, ri,
			"width=%d: markers must read screen-effect first, matching the ? cheatsheet", w)
	}
}

// TestCustomCommands_InertRowIsDimmedNotHidden is the rule the palette established:
// a row that cannot run still appears, carrying its reason.
func TestCustomCommands_InertRowIsDimmedNotHidden(t *testing.T) {
	rows := threeRows()
	rows[0].Inert = "paused"
	out := xansi.Strip(sizedCustomCommands(t, rows, 60, 20).Render())

	assert.Contains(t, out, "lazygit in this worktree", "an inert row is never hidden")
	assert.Contains(t, out, "paused", "an inert row carries its reason")
}

// TestCustomCommands_EmptyListPointsAtTheDocs is why the empty state is not an
// empty box: nothing else in the app tells the user where custom commands come from.
func TestCustomCommands_EmptyListPointsAtTheDocs(t *testing.T) {
	out := xansi.Strip(sizedCustomCommands(t, nil, 60, 20).Render())

	assert.Contains(t, out, "custom_commands",
		"the empty state must name the config key that fills it")
	assert.NotContains(t, out, customCmdFooterHint,
		"there is nothing to move over or run, so the grammar footer would be a lie")
}

func TestCustomCommands_KeyGrammar(t *testing.T) {
	for _, tc := range []struct {
		name       string
		press      []tea.KeyPressMsg
		wantChosen int
		wantClose  bool
	}{
		{"esc closes without choosing", []tea.KeyPressMsg{namedKeyMsg("esc")}, -1, true},
		{"ctrl+c closes without choosing", []tea.KeyPressMsg{namedKeyMsg("ctrl+c")}, -1, true},
		{"enter runs the cursor row", []tea.KeyPressMsg{namedKeyMsg("enter")}, 0, true},
		{"down then enter runs the second", []tea.KeyPressMsg{namedKeyMsg("down"), namedKeyMsg("enter")}, 1, true},
		{"up from the top stays put", []tea.KeyPressMsg{namedKeyMsg("up"), namedKeyMsg("enter")}, 0, true},
		{"down past the end stays put", []tea.KeyPressMsg{
			namedKeyMsg("down"), namedKeyMsg("down"), namedKeyMsg("down"), namedKeyMsg("enter"),
		}, 2, true},
		{"a row's own key runs it", []tea.KeyPressMsg{runeKeyMsg('f')}, 2, true},
		{"an unbound rune does nothing", []tea.KeyPressMsg{runeKeyMsg('z')}, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := sizedCustomCommands(t, threeRows(), 60, 20)
			var chosen int
			var closed bool
			for _, k := range tc.press {
				chosen, closed = o.HandleKeyPress(k)
			}
			assert.Equal(t, tc.wantChosen, chosen)
			assert.Equal(t, tc.wantClose, closed)
		})
	}
}

// TestCustomCommands_NavigationRunesStayAvailableAsCommandKeys is the positive test
// for a deliberate omission: j/k are NOT bound to movement here, unlike every other
// list overlay in the app, because a row's key must run it and j is a key a user may
// bind. Without this test the carve-out is a comment, not a rule.
func TestCustomCommands_NavigationRunesStayAvailableAsCommandKeys(t *testing.T) {
	for _, r := range []rune{'j', 'k'} {
		t.Run(string(r), func(t *testing.T) {
			rows := []CustomCommandRow{
				{Key: "a", Description: "first"},
				{Key: string(r), Description: "bound to a navigation rune"},
			}
			o := sizedCustomCommands(t, rows, 60, 20)

			chosen, closed := o.HandleKeyPress(runeKeyMsg(r))
			assert.Equal(t, 1, chosen, "%q must run its row, not move the cursor", r)
			assert.True(t, closed)
		})
	}
}

// TestCustomCommands_InertRowAnswersInsideTheBox is load-bearing placement, not
// taste: routing the refusal through the app's notice path would find the hint bar
// hidden and fall through to the error box, which recomputes the layout under a
// live overlay — twice, once when the toast expires.
func TestCustomCommands_InertRowAnswersInsideTheBox(t *testing.T) {
	rows := threeRows()
	rows[2].Inert = "worktree freed — resume first"

	for _, tc := range []struct {
		name  string
		press []tea.KeyPressMsg
	}{
		{"by its own key", []tea.KeyPressMsg{runeKeyMsg('f')}},
		{"by enter on the row", []tea.KeyPressMsg{
			namedKeyMsg("down"), namedKeyMsg("down"), namedKeyMsg("enter"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := sizedCustomCommands(t, rows, 60, 20)
			var chosen int
			var closed bool
			for _, k := range tc.press {
				chosen, closed = o.HandleKeyPress(k)
			}

			assert.Equal(t, -1, chosen, "an inert row must not be reported as chosen")
			assert.False(t, closed, "the box stays up to hold its own answer")
			assert.Contains(t, xansi.Strip(o.Render()), "worktree freed",
				"the refusal must appear in the box, not go to the toast")
		})
	}
}

// TestCustomCommands_NoticeClearsOnTheNextKey keeps a stale refusal from sitting
// under a row it no longer describes.
func TestCustomCommands_NoticeClearsOnTheNextKey(t *testing.T) {
	rows := threeRows()
	rows[0].Inert = "paused"
	o := sizedCustomCommands(t, rows, 60, 20)

	// The notice names the key as well as the reason — the cursor may be nowhere
	// near the row that answered — which is also what tells it apart from the same
	// reason rendered in the row's own tail.
	const notice = "g — paused"

	o.HandleKeyPress(runeKeyMsg('g'))
	require.Contains(t, xansi.Strip(o.Render()), notice)

	o.HandleKeyPress(namedKeyMsg("down"))
	assert.NotContains(t, xansi.Strip(o.Render()), notice,
		"moving off the row it refused must retire the notice")
	assert.Contains(t, xansi.Strip(o.Render()), "paused",
		"but the row itself keeps its reason — that is the dim, not the answer")
}

// TestCustomCommands_FitsItsSize is the invariant the composed frame depends on: the
// box must occupy exactly the width it was sized to and no more rows than its height.
//
// The notice case is the one a state sweep cannot reach. frameStates()' wire only
// *opens* the overlay, so no golden and no bounds sweep ever renders a frame with a
// refusal showing — an uncharged notice row would overflow the height budget and
// PlaceOverlay would take the bottom border off, permanently invisible to the suite.
func TestCustomCommands_FitsItsSize(t *testing.T) {
	long := strings.Repeat("a very long description that no terminal is wide enough for ", 3)
	rows := []CustomCommandRow{
		{Key: "g", Description: long},
		{Key: "c", Description: long, Repo: true},
		{Key: "f", Description: long, Inert: "worktree freed — resume first"},
		{Key: "p", Description: long},
		{Key: "q", Description: long},
		{Key: "r", Description: long},
		{Key: "s", Description: long},
		{Key: "t", Description: long},
	}

	for _, dim := range [][2]int{{80, 24}, {68, 20}, {34, 8}, {20, 4}, {120, 40}} {
		w, h := dim[0], dim[1]
		for _, withNotice := range []bool{false, true} {
			o := sizedCustomCommands(t, rows, w, h)
			if withNotice {
				o.HandleKeyPress(runeKeyMsg('f'))
			}

			lines := strings.Split(o.Render(), "\n")
			assert.LessOrEqualf(t, len(lines), max(h, customCmdMinHeight),
				"size=%dx%d notice=%v: %d rows overflows the height it was sized to",
				w, h, withNotice, len(lines))
			for i, l := range lines {
				assert.Equalf(t, max(w, customCmdMinWidth), xansi.StringWidth(l),
					"size=%dx%d notice=%v: line %d is the wrong width", w, h, withNotice, i)
			}
		}
	}
}

// TestCustomCommands_WideRunesDoNotEatTheTail is the case TestCustomCommands_FitsItsSize
// is structurally blind to.
//
// The row was clipped by display width and then padded to a width counted in RUNES, so a
// CJK description — half as many runes as cells — overshot the box by ~half the column,
// and the final clip took it out of the tail. Every line stayed exactly the right width,
// which is all the size guard measures, while `(repo)` rendered as a lone `…`.
//
// The marker is what the overlay's own comment calls load-bearing — it is the only thing
// saying which directory the row runs in — so a marker reduced to an ellipsis is worse
// than one omitted.
func TestCustomCommands_WideRunesDoNotEatTheTail(t *testing.T) {
	wide := strings.Repeat("日本語", 20)
	rows := []CustomCommandRow{
		{Key: "f", Description: wide, Repo: true},
		{Key: "g", Description: strings.Repeat("ascii desc ", 6), Repo: true},
		{Key: "h", Description: wide, Inert: "worktree freed — resume first"},
		// A 2-cell key: validation accepts any printable rune, and the key column was
		// padded by rune count too, which shifted this row's description by a cell.
		{Key: "日", Description: "a two-cell key", Repo: true},
	}

	for _, w := range []int{60, 80, 120} {
		o := sizedCustomCommands(t, rows, w, 20)
		out := xansi.Strip(o.Render())
		lines := strings.Split(out, "\n")

		assert.Equalf(t, 3, strings.Count(out, customCmdRepoMarker),
			"width=%d: every runnable repo row must keep a whole marker, not an ellipsis:\n%s", w, out)
		assert.Containsf(t, out, "worktree freed — resume first",
			"width=%d: the inert row's reason must survive a wide description whole", w)

		for _, l := range lines {
			assert.Equalf(t, w, xansi.StringWidth(l), "width=%d: line is the wrong width: %q", w, l)
		}

		// The description column lines up across rows, including the two-cell key. A
		// column measured in runes would put that row's description one cell right.
		var starts []int
		for _, l := range lines {
			if i := strings.Index(l, "a two-cell key"); i >= 0 {
				starts = append(starts, xansi.StringWidth(l[:i]))
			}
			if i := strings.Index(l, "ascii desc"); i >= 0 {
				starts = append(starts, xansi.StringWidth(l[:i]))
			}
		}
		require.Lenf(t, starts, 2, "width=%d: both marker rows must be present", w)
		assert.Equalf(t, starts[0], starts[1],
			"width=%d: a two-cell key must not shift its description column", w)
	}
}

// TestCustomCommands_WindowsTheListAroundTheCursor proves the height budget windows
// rather than truncates: a cursor driven past the visible rows must stay on screen.
func TestCustomCommands_WindowsTheListAroundTheCursor(t *testing.T) {
	var rows []CustomCommandRow
	for _, r := range "abcdefghijklmnopqrst" {
		rows = append(rows, CustomCommandRow{Key: string(r), Description: "row " + string(r)})
	}
	o := sizedCustomCommands(t, rows, 60, 12)

	for i := 0; i < len(rows); i++ {
		o.HandleKeyPress(namedKeyMsg("down"))
	}
	out := xansi.Strip(o.Render())

	assert.Contains(t, out, "row t", "the cursor's row must stay visible")
	assert.NotContains(t, out, "row a", "the window must have scrolled off the top")
}
