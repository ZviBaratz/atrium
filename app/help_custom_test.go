package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The custom section of the ? cheatsheet (#375).
//
// These tests exist because the guards already in the tree cannot see this section at
// all. helpTypeGeneral{} is a bare composite literal at ten sites, so adding a field
// left every one of them compiling with a nil slice — including
// TestHelpOverlayFitsShortTerminal, which therefore renders ZERO custom rows and would
// stay green with the section arbitrarily broken. Only a test that POPULATES the field
// says anything.

// TestHelpCustomSectionTruncatesLongDescriptions pins the width arithmetic
// helpCustomRowWidth and customDescWidth claim.
//
// Asserted on toContent() rather than on the composed frame, and that is the point:
// TextOverlay hard-wraps its content to the box's inner width, so a per-line width
// assertion over the rendered frame passes with no truncation whatsoever. The frame
// check below covers the row budget; this one covers the bound.
func TestHelpCustomSectionTruncatesLongDescriptions(t *testing.T) {
	long := strings.Repeat("a pathologically long description ", 6)
	// The third is CJK: two display cells per rune, so a bound counted in runes would
	// pass this fixture at twice the width it was measured for.
	wide := strings.Repeat("日本語", 60)
	cmds := validCommands(t,
		config.CustomCommand{Key: "g", Description: long, Command: "true", Output: "background"},
		config.CustomCommand{Key: "f", Description: long, Context: "repo", Command: "true", Output: "background"},
		config.CustomCommand{Key: "w", Description: wide, Command: "true", Output: "background"},
		// The widest row a config can produce: a long description AND both markers. It
		// belongs here because helpCustomDescWidth is defined as this row's budget — the
		// fixtures were all `background`, so the terminal marker's 11 cells were unmeasured
		// and a description bound left at 55 pushed this row two cells over.
		config.CustomCommand{Key: "t", Description: long, Context: "repo", Command: "true", Output: "terminal"},
		config.CustomCommand{Key: "T", Description: wide, Context: "repo", Command: "true", Output: "terminal"},
	)
	content := xansi.Strip(helpTypeGeneral{commands: cmds}.toContent())

	// Sliced at the section heading rather than filtered on a "! " prefix: the
	// leader's own cheatsheet row in the Other group starts the same way, and
	// counting it would make the assertion below pass on the wrong lines.
	_, section, ok := strings.Cut(content, helpCustomHeading)
	require.True(t, ok, "the custom section must be present")
	var custom []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "! ") {
			custom = append(custom, line)
		}
	}
	require.Len(t, custom, 5, "every command must be listed")

	for _, line := range custom {
		// Trailing spaces trimmed: lipgloss.JoinVertical pads every line out to the
		// widest one in the block, so measuring untrimmed would report the whole
		// cheatsheet's width and never the row's.
		assert.LessOrEqualf(t, ansi.PrintableRuneWidth(strings.TrimRight(line, " ")), 74,
			"a custom row must fit an 80-column terminal's help box: %q", line)
		assert.Containsf(t, line, "…", "a long description must be truncated: %q", line)
	}

	// The budget is charged PER ROW: an unmarked row's DESCRIPTION must be wider than the
	// both-markers row's, not truncated to the same worst case. Without this the markers'
	// 18 cells came off every row in the list, including the ones that carry none.
	// Derived from the constants rather than restating 18, so a new marker cannot leave
	// this measuring a width nothing charges any more.
	markerCells := helpCustomRowWidth - helpCustomDescWidth
	unmarked := ansi.PrintableRuneWidth(strings.TrimRight(custom[0], " "))
	bothMarkers := ansi.PrintableRuneWidth(strings.TrimRight(custom[3], " "))
	assert.Greaterf(t, unmarked, bothMarkers-markerCells,
		"an unmarked row must not pay for markers it does not carry: %q vs %q",
		custom[0], custom[3])
	assert.Contains(t, custom[1], "(repo)",
		"the repo marker must survive truncation — it is what says which directory")
	// Both markers, on the widest row, in the order the menu shows them: what it will do
	// to the screen before where it will run.
	for _, i := range []int{3, 4} {
		assert.Containsf(t, custom[i], "(terminal)",
			"the terminal marker must survive truncation — it is what says the row takes "+
				"the screen, which is why `output` is a required key: %q", custom[i])
		assert.Containsf(t, custom[i], "(repo)", "and the repo marker with it: %q", custom[i])
		assert.Lessf(t, strings.Index(custom[i], "(terminal)"), strings.Index(custom[i], "(repo)"),
			"markers must read screen-effect first, matching the menu: %q", custom[i])
	}
	// The background rows must NOT claim the terminal, or the marker means nothing.
	for _, i := range []int{0, 1, 2} {
		assert.NotContainsf(t, custom[i], "(terminal)",
			"a background row must not be marked as taking the terminal: %q", custom[i])
	}
}

// TestHelpCustomSectionListsTheKeysAndOmitsItselfWhenEmpty is the auto-listing AC:
// every configured command is documented where every other key is.
func TestHelpCustomSectionListsTheKeysAndOmitsItselfWhenEmpty(t *testing.T) {
	assert.NotContains(t, xansi.Strip(helpTypeGeneral{}.toContent()), helpCustomHeading,
		"no commands configured, no section — an empty heading teaches nothing")

	cmds := validCommands(t,
		config.CustomCommand{Key: "g", Description: "lazygit in this worktree", Command: "true", Output: "background"},
		config.CustomCommand{Key: "c", Description: "just ci", Command: "true", Output: "background"},
	)
	content := xansi.Strip(helpTypeGeneral{commands: cmds}.toContent())

	assert.Contains(t, content, helpCustomHeading,
		"the section must say how to reach the keys it lists")
	assert.Contains(t, content, "! g")
	assert.Contains(t, content, "lazygit in this worktree")
	assert.Contains(t, content, "! c")
	assert.Contains(t, content, "just ci")
}

// TestHelpWithCustomCommandsFitsShortTerminal is the composed-frame half: the
// cheatsheet is one scrollable overlay, and a section that spent more rows than it
// should would push the legend off a short terminal.
func TestHelpWithCustomCommandsFitsShortTerminal(t *testing.T) {
	long := strings.Repeat("a pathologically long description ", 6)
	var entries []config.CustomCommand
	for _, k := range strings.Split("abcdefghij", "") {
		entries = append(entries, config.CustomCommand{
			Key: k, Description: long, Command: "true", Output: "background",
		})
	}
	cmds := validCommands(t, entries...)

	for _, dim := range [][2]int{{80, 24}, {120, 40}, {100, 20}} {
		w, h := dim[0], dim[1]
		home := newCreateFormHome(t)
		home.customCommands = cmds
		home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})
		_, _ = home.handleKeyPress(runeKey("?"))
		require.Equal(t, stateHelp, home.state)

		lines := strings.Split(home.View().Content, "\n")
		assert.LessOrEqualf(t, len(lines), h, "size=%dx%d: %d lines exceeds the height", w, h, len(lines))
		for i, l := range lines {
			assert.Equalf(t, w, ansi.PrintableRuneWidth(l), "size=%dx%d: line %d is the wrong width", w, h, i)
		}
	}
}

// TestHelpOpenSitePassesTheCommands closes the loop: the field is useless if the one
// production construction leaves it empty.
func TestHelpOpenSitePassesTheCommands(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "g", Description: "a distinctive description", Command: "true", Output: "background"},
	)
	home := newCreateFormHome(t)
	home.customCommands = cmds
	// Tall enough that the cheatsheet does not scroll: the overlay renders only its
	// visible window, so at 80x24 the section is below the fold and an assertion on
	// the render would be about the window, not about the wiring.
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 140, Height: 120})

	_, _ = home.handleKeyPress(runeKey("?"))
	require.Equal(t, stateHelp, home.state)
	assert.Contains(t, xansi.Strip(home.textOverlay.Render()), "a distinctive description",
		"the ? screen must list the configured commands — the open site is the only "+
			"place that can pass them")
}
