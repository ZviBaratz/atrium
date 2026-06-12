package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --- Claude ghost-suggestion fixtures. Raw bytes (ANSI intact) pinned against
// a live `tmux capture-pane -p -e -J` of claude 2.1.17x on 2026-06-12: the
// suggestion renders as SGR-dim text after the "❯" inside the input box, with
// no hint string anywhere — the dim styling is the only signal, which is why
// these fixtures, unlike every other fixture in this package, must keep their
// escape sequences.

// suggestionPane builds a raw pane around the given raw input-box line:
// a transcript line above, the box's two horizontal rules, and the footer
// hints below — the live layout the detector anchors on.
func suggestionPane(boxLine string) string {
	return "Some transcript prose discussing earlier work.\n" +
		"\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
		boxLine + "\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
		"\x1b[39m  ? for shortcuts\x1b[39m"
}

// ghostBoxLine is the live-captured suggestion line: prompt char, then the
// ghost text wrapped in SGR dim (ESC[2m … ESC[0m).
const ghostBoxLine = "\x1b[39m❯ \x1b[2mGo ahead and resolve the threads, then merge if unblocked\x1b[0m"

func TestClaudeSuggestion_GhostTextVisible(t *testing.T) {
	// A dim transcript line above the box must not be what fires the match:
	// the detector reads only the input-box line.
	pane := "\x1b[2mdim transcript note\x1b[0m\n" + suggestionPane(ghostBoxLine)
	require.True(t, claudeSuggestionVisible(pane))
}

func TestClaudeSuggestion_EmptyBoxNotVisible(t *testing.T) {
	require.False(t, claudeSuggestionVisible(suggestionPane("\x1b[39m❯ ")))
}

// TestClaudeSuggestion_TypedDraftNotVisible is the safety-critical case: text
// the user typed renders without dim, and Enter would submit it — the dim
// gate must reject it so `a` can never send a half-written draft.
func TestClaudeSuggestion_TypedDraftNotVisible(t *testing.T) {
	require.False(t, claudeSuggestionVisible(suggestionPane("\x1b[39m❯ fix the failing test")))
	// Mixed styling (typed text with one dim word) must also be rejected:
	// only an all-dim interior is a ghost suggestion.
	require.False(t, claudeSuggestionVisible(suggestionPane("\x1b[39m❯ fix \x1b[2mthe\x1b[0m failing test")))
}

func TestClaudeSuggestion_DimTranscriptAboveBoxNotVisible(t *testing.T) {
	pane := "\x1b[2mAll of this scrolled-back prose is dim.\x1b[0m\n" +
		suggestionPane("\x1b[39m❯ ")
	require.False(t, claudeSuggestionVisible(pane))
}

func TestClaudeSuggestion_NoBoxNotVisible(t *testing.T) {
	// A dialog/degenerate capture with no horizontal rules has no locatable
	// input box; the detector fails closed.
	require.False(t, claudeSuggestionVisible("❯ \x1b[2mdim text but no box rules\x1b[0m"))
	require.False(t, claudeSuggestionVisible(""))
}

// TestClaudeSuggestion_StatusLineDividerBelowBox mirrors the
// footerVisibleInSegments scenario: a custom statusLine below the box drawing
// its own ─── divider becomes the last rule on screen, so "the last two rules"
// would miss the box. The bottom-up adjacent-pair scan must still find it.
func TestClaudeSuggestion_StatusLineDividerBelowBox(t *testing.T) {
	pane := suggestionPane(ghostBoxLine) + "\n" +
		"\x1b[38;5;244m────────────────────────────────────────\x1b[0m\n" +
		"\x1b[39mstatusline: main · 3 files changed\x1b[0m"
	require.True(t, claudeSuggestionVisible(pane))
}

// TestClaudeSuggestion_BorderedBoxStyle covers the older box style with "│"
// side borders (still present in this package's poll-test fixtures): the
// trailing border must not count as a non-dim visible char.
func TestClaudeSuggestion_BorderedBoxStyle(t *testing.T) {
	require.True(t, claudeSuggestionVisible(suggestionPane(
		"\x1b[39m│ ❯ \x1b[2mrun the tests\x1b[0m\x1b[39m │")))
	require.False(t, claudeSuggestionVisible(suggestionPane(
		"\x1b[39m│ ❯ typed draft\x1b[39m │")))
}

// TestClaudeSuggestion_CombinedAndClearedSGR pins the state-machine corners:
// dim set via a combined parameter sequence (ESC[2;39m), and dim explicitly
// cleared mid-line via ESC[22m — text after the clear is not dim.
func TestClaudeSuggestion_CombinedAndClearedSGR(t *testing.T) {
	require.True(t, claudeSuggestionVisible(suggestionPane(
		"\x1b[39m❯ \x1b[2;39msuggested prompt text\x1b[0m")))
	require.False(t, claudeSuggestionVisible(suggestionPane(
		"\x1b[39m❯ \x1b[2mdim lead-in\x1b[22m then typed text")))
}

// TestSuggestionDetector_ClaudeOnly pins the adapter gate: only claude has a
// suggestion UI, so every other adapter must leave SuggestionVisible nil —
// that nil is what spares non-claude panes the capture in AcceptSuggestion.
func TestSuggestionDetector_ClaudeOnly(t *testing.T) {
	require.NotNil(t, claude.SuggestionVisible)
	for _, a := range []*Adapter{codex, gemini, aider, Generic} {
		require.Nil(t, a.SuggestionVisible, "adapter %s", a.Key)
	}
}
