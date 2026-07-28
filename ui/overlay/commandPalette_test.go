package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paletteFixture is a small stand-in for the generated keymap: rows whose verb
// matches, rows whose prose does, and rows that match neither.
//
// "resume" is deliberately listed *before* "pause", and its prose says "paused".
// A query of "pause" therefore scores the two the same — both are a contiguous,
// word-boundary hit — so with score alone the stable sort would hand the win to
// whichever came first. Ordering resume first is what makes the tier the only
// thing that can put pause on top; with the fixture the other way round the
// ranking test passes whether the tiering exists or not.
// "prev tab" is listed first and starts a word with p, so it scores a boundary
// hit on the query "p" identical to pause's own — the real tie that put it above
// pause in the live keymap. Without it the exact-key rule looks like it works
// while doing nothing: every other row's p is mid-word and loses on score alone.
func paletteFixture() []PaletteAction {
	return []PaletteAction{
		{Key: "shift-tab", Label: "prev tab", Detail: "next / prev pane", Group: "Navigate"},
		{Key: "↑/k", Label: "up", Detail: "move selection", Group: "Navigate"},
		{Key: "r", Label: "resume", Detail: "resume a paused session", Group: "Handoff"},
		{Key: "p", Label: "pause", Detail: "commit changes + free the worktree", Group: "Handoff"},
		{Key: "m", Label: "merge PR", Detail: "merge the session's PR (squash)", Group: "Handoff"},
	}
}

func typePalette(p *CommandPaletteOverlay, q string) {
	for _, r := range q {
		p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// paletteIndexOf finds a fixture row by verb, so these tests assert against the fixture's
// meaning rather than its order — the order is itself load-bearing for the ranking
// test, and hardcoded positions turn a deliberate reorder into unrelated failures.
func paletteIndexOf(t *testing.T, actions []PaletteAction, label string) int {
	t.Helper()
	for i, a := range actions {
		if a.Label == label {
			return i
		}
	}
	t.Fatalf("fixture has no action labelled %q", label)
	return -1
}

// The palette reports an index into the list it was *given*, not into the ranked
// list it is showing. Filtering reorders rows, so a report that forgets to map
// back runs whatever happens to sit at that position in the original order —
// silently, and only for a non-empty query, which is exactly the case a
// happy-path test skips.
func TestPaletteChosenMapsThroughTheRanking(t *testing.T) {
	actions := paletteFixture()
	want := paletteIndexOf(t, actions, "merge PR") // last unfiltered, but it ranks first here
	p := NewCommandPaletteOverlay(actions)
	typePalette(p, "merge")

	p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	action, idx, ok := p.Chosen()
	require.True(t, ok)
	require.NotZero(t, want, "the row must not sit at position 0, or a lost mapping would look correct")
	assert.Equal(t, "merge PR", action.Label)
	assert.Equal(t, want, idx, "the index must address the caller's list, not the filtered view")
}

// A verb hit outranks a prose hit even when the prose scores better. "resume"
// mentions "paused" in its prose, so a score-only ordering can float it above the
// action actually called pause.
func TestPaletteRanksVerbHitsAboveProseHits(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	typePalette(p, "pause")

	p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	action, _, ok := p.Chosen()
	require.True(t, ok)
	assert.Equal(t, "pause", action.Label, "the verb hit must lead, not the action whose prose says 'paused'")
}

// Typing an action's key finds that action. A one-character query otherwise ties
// on score across every row containing that letter and falls back to list order,
// so "p" surfaced whichever row came first rather than pause — in a palette whose
// premise is that every row shows its key.
func TestPaletteExactKeyMatchWins(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	typePalette(p, "p")

	p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	action, _, ok := p.Chosen()
	require.True(t, ok)
	assert.Equal(t, "pause", action.Label, "typing a key must lead with the action bound to it")
}

// And the exact-key match is case-sensitive, because the keymap is: m merges and
// M mutes, r resumes and R renames. Folding case here surfaces the wrong half of
// every such pair — which it did, until this test.
func TestPaletteExactKeyMatchIsCaseSensitive(t *testing.T) {
	actions := []PaletteAction{
		{Key: "M", Label: "mute notifications", Detail: "mute / unmute", Group: "Manage"},
		{Key: "m", Label: "merge PR", Detail: "merge the session's PR", Group: "Handoff"},
	}
	for _, tc := range []struct{ query, want string }{
		{"m", "merge PR"},
		{"M", "mute notifications"},
	} {
		p := NewCommandPaletteOverlay(actions)
		typePalette(p, tc.query)
		p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

		action, _, ok := p.Chosen()
		require.True(t, ok)
		assert.Equalf(t, tc.want, action.Label, "query %q must lead with the action actually bound to it", tc.query)
	}
}

// Prose stays searchable: the pause action's verb never says "worktree", and
// finding it by what it does is half the point of a palette.
func TestPaletteFindsAnActionByItsProseAlone(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	typePalette(p, "worktree")

	p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	action, _, ok := p.Chosen()
	require.True(t, ok)
	assert.Equal(t, "pause", action.Label)
}

// Enter on an empty result set must choose nothing rather than the stale cursor's
// row — the shape that would run an action the user can no longer see.
func TestPaletteEnterOnNoMatchChoosesNothing(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	typePalette(p, "zzzz")

	shouldClose := p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, shouldClose)
	_, _, ok := p.Chosen()
	assert.False(t, ok, "an empty result set has nothing to run")
	assert.Contains(t, xansi.Strip(p.Render()), "no action matches")
}

// Esc closes without choosing.
func TestPaletteEscapeChoosesNothing(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())

	assert.True(t, p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}))
	_, _, ok := p.Chosen()
	assert.False(t, ok)
}

// Group headers are a promise that the rows beneath belong to the section. Once a
// query ranks rows across groups that promise is false, so the headers go.
//
// The query has to match rows in more than one group, or the headers are absent
// because nothing from those groups survived the filter — and the test passes
// without the rule it names ever running. "u" hits up (Navigate) and pause,
// resume and merge (Handoff).
func TestPaletteGroupHeadersOnlyWhileUnfiltered(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	p.SetSize(80, 24)

	unfiltered := xansi.Strip(p.Render())
	assert.Contains(t, unfiltered, "Navigate", "an unfiltered palette is the cheatsheet, sections and all")
	assert.Contains(t, unfiltered, "Handoff")

	typePalette(p, "u")
	filtered := xansi.Strip(p.Render())
	require.Contains(t, filtered, "up", "the query must keep rows from both groups, or this proves nothing")
	require.Contains(t, filtered, "pause")
	assert.NotContains(t, filtered, "Navigate",
		"a ranked list interleaves groups, so a header would lie about what follows it")
	assert.NotContains(t, filtered, "Handoff")
}

// Every row shows the key it substitutes for — the property that makes the
// palette self-obsoleting rather than a permanent crutch.
func TestPaletteRowsShowTheirKey(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	p.SetSize(80, 24)

	out := xansi.Strip(p.Render())
	for _, a := range paletteFixture() {
		assert.Containsf(t, out, a.Key, "row %q must teach its key", a.Label)
	}
}

// An inert row is dimmed and carries its reason instead of its prose — a row that
// cannot run owes the user why, not what it would have done. (The reasons
// themselves arrive with the gating; this pins the rendering contract they land on.)
func TestPaletteInertRowShowsItsReasonInsteadOfProse(t *testing.T) {
	actions := paletteFixture()
	inert := paletteIndexOf(t, actions, "pause")
	require.NotZero(t, inert, "the inert row must not be the one under the cursor, whose styling would decide the colours")
	actions[inert].Inert = "already paused"
	p := NewCommandPaletteOverlay(actions)
	p.SetSize(100, 24)

	out := xansi.Strip(p.Render())
	assert.Contains(t, out, "already paused")
	assert.NotContains(t, out, "free the worktree", "the reason replaces the prose, it does not join it")
}

// The narrowest supported terminal. The palette's text is generated from the
// cheatsheet, none of it authored to a width, so the row builder is the only
// thing standing between 68 columns and a wrap that costs the box a row.
func TestPaletteFitsANarrowBox(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	p.SetSize(68, 24)

	for _, line := range strings.Split(p.Render(), "\n") {
		assert.LessOrEqualf(t, xansi.StringWidth(line), 68, "line wider than the box: %q", xansi.Strip(line))
	}
}

// Below the width where prose is worth showing, the prose is what goes: the key
// and the verb are what make a row actionable.
//
// Asserting only on the prose's *tail* would pass without the rule — a column
// truncated to seven characters has already lost the last word, so "worktree"
// is absent either way. The first word is what tells "dropped" from "truncated
// to a stub".
func TestPaletteDropsProseBeforeTheVerb(t *testing.T) {
	p := NewCommandPaletteOverlay(paletteFixture())
	p.SetSize(34, 24)

	out := xansi.Strip(p.Render())
	assert.Contains(t, out, "pause", "the verb survives the narrowest box")
	assert.NotContains(t, out, "commit", "the prose column is dropped, not truncated to a stub")
	assert.NotContains(t, out, "worktree")
}

// The group is matched on its own, never glued to the prose. Concatenating them
// lets a section name donate the letters a query is missing, returning rows whose
// own text never matched at all — "Manage" supplying the g to "merge" is the case
// that motivated the split, and it cost a third of the real keymap.
func TestPaletteGroupNameDoesNotDonateLettersToTheProse(t *testing.T) {
	p := NewCommandPaletteOverlay([]PaletteAction{
		{Key: "v", Label: "multi-select", Detail: "space marks, p/r/x act on the marked set", Group: "Manage"},
	})
	typePalette(p, "merge")

	p.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	_, _, ok := p.Chosen()
	assert.False(t, ok, "no field of this row contains 'merge'; only prose+group glued together does")
}
