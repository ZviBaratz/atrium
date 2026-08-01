package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/memo"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

// The tabbed window is memoized on tabbedKey (#565). These tests hold that key to
// its claim: it must cover every input compose reads, and nothing it does not.
//
// Every one of them asserts ComposeRuns rather than only comparing frames, because
// "the second render matched the first" is equally true of a memo that never ran —
// the mistake #561 shipped until a require.Zero on a counting seam was added.

// newMemoWindow returns a laid-out window with a preview pane holding real text, so
// a stale frame would be visible rather than two identical blanks.
func newMemoWindow(t *testing.T) *TabbedWindow {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(60, 20)
	w.preview.previewState = previewState{text: "first frame"}
	return w
}

func TestTabbedWindowMemo_UnchangedInputsComposeOnce(t *testing.T) {
	w := newMemoWindow(t)

	first := w.String()
	for range 9 {
		require.Equal(t, first, w.String(), "an unchanged window must render an identical frame")
	}

	require.Equal(t, 1, w.ComposeRuns(), "10 renders of an unchanged window must compose once")
}

// The negative control. Without it the test above passes against a memo that never
// invalidates — which would freeze the right pane on its first frame forever.
func TestTabbedWindowMemo_ChangedPaneContentRecomposes(t *testing.T) {
	w := newMemoWindow(t)

	first := w.String()
	require.Equal(t, 1, w.ComposeRuns())

	w.preview.previewState = previewState{text: "second frame"}
	second := w.String()

	require.Equal(t, 2, w.ComposeRuns(), "changed pane content must recompose")
	require.NotEqual(t, first, second, "and the frame must actually differ")
	require.Contains(t, second, "second frame")
}

// One case per scalar in tabbedKey. Each changes exactly that input and requires a
// recompose; dropping the field from the key fails precisely the matching case,
// which is what makes the key's coverage falsifiable rather than asserted in a
// comment. Most also require a different frame — a recompose that produced the same
// bytes would mean the input was in the key for no reason.
//
// It cannot carry the whole burden, and two of its cases prove that on their own:
// SetSize and Toggle both change the pane CONTENT as well (a taller pane renders
// more lines; another tab is another renderer), so the memo invalidates through
// content whether or not height and activeTab are in the key at all — and both
// mutations survive here. What compose then does with the stale value is the
// visible bug, so those two are pinned by consequence below, in
// TestTabbedWindowMemo_ToggleMovesTheTabStrip and
// TestTabbedWindowMemo_FrameIsExactlyItsHeight.
func TestTabbedWindowMemo_EveryKeyedScalarInvalidates(t *testing.T) {
	cases := []struct {
		name string
		// sameFrame marks an input the key covers CONSERVATIVELY: it invalidates,
		// but this pane's own output does not depend on it.
		sameFrame bool
		change    func(t *testing.T, w *TabbedWindow)
	}{
		{name: "width", change: func(_ *testing.T, w *TabbedWindow) { w.SetSize(80, 20) }},
		{name: "height", change: func(_ *testing.T, w *TabbedWindow) { w.SetSize(60, 24) }},
		{name: "activeTab", change: func(_ *testing.T, w *TabbedWindow) { w.Toggle() }},
		// Scroll mode on the TERMINAL pane, while the preview tab is showing: it
		// flips paneScrolling (and so the pane's accent chrome) without touching the
		// content the key already covers, which is the only way to isolate focused.
		{name: "focused", change: func(_ *testing.T, w *TabbedWindow) { w.terminal.isScrolling = true }},
		{name: "theme palette", change: func(t *testing.T, _ *TabbedWindow) { t.Cleanup(theme.Set("tokyo-night")) }},
		// The glyph set gets a fresh *Theme from theme.compose, so it invalidates —
		// but the window's chrome is borders and colours with no glyph in it, so the
		// frame is byte-identical. Pinned anyway, and pinned as sameFrame rather than
		// dropped, because it is what holds the key to the whole *Theme pointer: a
		// key narrowed to the palette name would still pass every other case here
		// while leaving the glyph axis uncovered for whatever compose draws next.
		{name: "theme glyph set", sameFrame: true, change: func(t *testing.T, _ *TabbedWindow) {
			t.Cleanup(theme.SetGlyphSet(theme.GlyphSetASCII))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newMemoWindow(t)
			first := w.String()
			require.Equal(t, 1, w.ComposeRuns())

			tc.change(t, w)
			second := w.String()

			require.Equal(t, 2, w.ComposeRuns(), "changing %s must recompose", tc.name)
			if tc.sameFrame {
				require.Equal(t, first, second, "changing %s must not change this pane's frame", tc.name)
				return
			}
			require.NotEqual(t, first, second, "changing %s must change the frame", tc.name)
		})
	}
}

// compose renders with k.theme and never reads the global.
//
// This is the property that makes the key's theme entry a data dependency rather
// than a bare invalidation token, and no other test here can see it: in a
// synchronous test k.theme and theme.Current() are always the same pointer, so
// reverting any one style call to theme.Current() survives every assertion above.
// Pinning k.theme and moving the global underneath it is what separates them —
// two composes of one key must agree whatever the global says.
//
// It matters because compose is reachable with a theme that is not the current one
// the moment anything composes off the update loop, and because the cached frame is
// filed under k.theme: a body reading the global would cache a frame drawn in one
// palette under another palette's key, and serve it until some unrelated input moved.
func TestTabbedWindowMemo_ComposeRendersWithTheKeyedThemeNotTheGlobal(t *testing.T) {
	w := newMemoWindow(t)
	k := tabbedKey{
		content:   w.activePaneContent(),
		width:     w.width,
		height:    w.height,
		activeTab: w.activeTab,
		theme:     theme.Get("catppuccin-mocha"),
	}

	restore := theme.Set("unicode")
	underUnicode := w.compose(k)
	restore()

	restore = theme.Set("catppuccin-latte")
	underLatte := w.compose(k)
	restore()

	require.Equal(t, underUnicode, underLatte,
		"compose must build the frame from k.theme alone; a theme.Current() left in its "+
			"body makes the same key render differently as the global moves")
}

// Mutation note for the test above: reverting windowStyle, activeTabStyle or
// inactiveTabStyle in compose to theme.Current() fails it. The fourth call —
// activeTabStyle(k.theme, false) for tabHeight — does not, and cannot: it reads
// only GetVerticalFrameSize(), which is 2 for every palette in the registry, so
// that mutant is equivalent rather than uncaught. It is threaded for consistency
// with the other three, not because a test could tell.

// tabStrip is the pane's top three rows: the tab border, the labels, and the
// border that closes under the inactive tabs. It is what carries which tab is
// selected, and it is independent of the body below it.
func tabStrip(frame string) string {
	lines := strings.SplitN(frame, "\n", 4)
	return strings.Join(lines[:min(3, len(lines))], "\n")
}

// Switching tabs moves the highlight in the strip.
//
// Asserted separately from the invalidation table because the table cannot see it:
// another tab renders other content, so the memo invalidates through content alone
// and a compose reading a stale activeTab would still produce a different — and
// wrongly labelled — frame. Two tabs showing identical bodies is not hypothetical
// either: an empty diff pane and an empty terminal pane are both placeholders.
func TestTabbedWindowMemo_ToggleMovesTheTabStrip(t *testing.T) {
	w := newMemoWindow(t)

	preview := tabStrip(w.String())
	w.Toggle()
	diff := tabStrip(w.String())

	require.NotEqual(t, preview, diff, "the tab strip must follow the active tab")
}

// The pane fills its column exactly, at whatever height it was last given.
//
// This is the #251 guarantee the height clamp in compose exists for — View joins
// this pane against the list with JoinHorizontal, so one row too many scrolls the
// whole frame. It doubles as the assertion that compose reads a CURRENT height:
// invalidation alone cannot show that, because resizing also reflows the pane
// content the memo is keyed on.
func TestTabbedWindowMemo_FrameIsExactlyItsHeight(t *testing.T) {
	w := newMemoWindow(t)

	for _, h := range []int{20, 30, 12} {
		w.SetSize(60, h)
		require.Equal(t, h, lipgloss.Height(w.String()), "the pane must be exactly %d rows tall", h)
	}
}

// Returning to an earlier state recomposes rather than resurrecting a frame: the
// cache holds one entry. Pinned because the hit rates the change is justified by
// depend on it — a window alternating between two states saves nothing.
func TestTabbedWindowMemo_AlternatingStatesNeverHit(t *testing.T) {
	w := newMemoWindow(t)

	_ = w.String()
	w.Toggle()
	_ = w.String()
	w.ToggleReverse()
	_ = w.String()

	require.Equal(t, 3, w.ComposeRuns(), "the earlier frame is evicted, not remembered")
}

// A window that has never been sized returns "" without composing — the early
// return predates the memo and must stay in front of it, or the first real frame
// is keyed against a zero-size one.
func TestTabbedWindowMemo_UnsizedWindowNeverComposes(t *testing.T) {
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))

	require.Empty(t, w.String())
	require.Zero(t, w.ComposeRuns())
}

// With memoization off the window composes every time. This is the seam the
// frame-equivalence table in package app rests on; asserted here so a broken seam
// fails where it lives rather than as a confusing equality failure over there.
func TestTabbedWindowMemo_DisabledComposesEveryTime(t *testing.T) {
	defer memo.SetEnabled(false)()

	w := newMemoWindow(t)
	first := w.String()
	require.Equal(t, first, w.String())

	require.Equal(t, 2, w.ComposeRuns())
}

// ResetMemo forces the next render to compose. It is what keeps the cold
// benchmarks cold, so it is asserted rather than assumed.
// ResetMemo drops the entry, not just the count. See the note on the List twin:
// asserting only "reset, render, expect 1" passes against an inert ResetMemo,
// because the render is then a hit and the count never moves.
func TestTabbedWindowMemo_ResetForcesARecompose(t *testing.T) {
	w := newMemoWindow(t)

	_ = w.String()
	_ = w.String()
	require.Equal(t, 1, w.ComposeRuns(), "precondition: the second render hits")

	w.ResetMemo()
	require.Zero(t, w.ComposeRuns(), "Reset must zero the count")

	_ = w.String()
	require.Equal(t, 1, w.ComposeRuns(), "and the next render must actually recompose")
}
