package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/memo"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// The frame build consults three memos (#561, #565): frameCached over the stack and
// the zone scan, the tabbed window over the right pane, the list over its panel
// chrome. Each is keyed on the bytes it composes plus a few scalars. These tests
// hold the app-level one to that, and hold all three to the property the whole
// change rests on — that a memoized frame is byte-identical to an unmemoized one.

// An absent component and an empty one are different frames. JoinVertical renders
// "" as a blank line, so the hint bar reserving a quiet row (#438) must not compare
// equal to no hint bar at all — which is why frameKey carries a presence flag beside
// each optional component instead of letting "" mean absent.
//
// Asserted on joinFrame, the pure function, rather than through a model that can
// only reach one of the two states at a time.
func TestJoinFrame_AbsentIsNotEmpty(t *testing.T) {
	body := frameKey{body: "body"}

	require.NotEqual(t,
		joinFrame(body),
		joinFrame(frameKey{body: "body", hasMenu: true}),
		"a reserved-but-blank menu row must not join like no menu row")
	require.NotEqual(t,
		joinFrame(body),
		joinFrame(frameKey{body: "body", hasErr: true}),
		"an empty error box must not join like no error box")
	require.NotEqual(t,
		joinFrame(body),
		joinFrame(frameKey{body: "body", hasBanner: true}),
		"a blank banner must not join like no banner")
}

// The same distinction has to survive the memo, which compares frameKeys with ==.
// A key that dropped the presence flags would serve the frame built for the other
// state, and this is the assertion that would catch it.
func TestFrameCached_AbsentAndEmptyComponentsAreDistinctKeys(t *testing.T) {
	h := newBenchHome(t, 1)

	absent := h.frameCached(frameKey{body: "body"})
	present := h.frameCached(frameKey{body: "body", hasMenu: true})

	require.NotEqual(t, absent, present)
	require.Equal(t, 2, h.frameMemo.Runs(), "the two states must not share a cache entry")
}

// resetRenderMemos must reach every memo, or the cold benchmarks silently measure
// the caches instead of the frame. That already happened once: BenchmarkViewContent
// cleared the zone-scan memo by hand, and the first run after the tabbed window's
// memo landed reported a 62% drop in allocs/op that was entirely the new cache.
//
// A memo added without joining the helper fails here.
//
// The counts have to be read BETWEEN the reset and the next render, which is not
// the obvious way to write it and is the only way that works: Reset zeroes the run
// count, so a layer the helper skipped reads exactly 1 after "reset, then render"
// — the same as a layer that was reset and recomposed. Written that way all three
// skip-a-layer mutations survived. Asserting zero first separates them, and the
// render after it still has to bring each back to one, which is what proves Reset
// dropped the cached entry rather than only the counter.
func TestResetRenderMemos_ForcesEveryLayerToRecompose(t *testing.T) {
	h := newBenchHome(t, 3)

	h.viewContent()
	// Without a reset the second render is a hit at every layer — the precondition
	// that makes everything below mean something.
	h.viewContent()
	require.Equal(t, 1, h.frameMemo.Runs(), "precondition: an unchanged model hits")
	require.Equal(t, 1, h.tabbedWindow.ComposeRuns(), "precondition: an unchanged model hits")
	require.Equal(t, 1, h.list.PanelComposeRuns(), "precondition: an unchanged model hits")

	h.resetRenderMemos()

	require.Zero(t, h.frameMemo.Runs(), "resetRenderMemos must reach the frame memo")
	require.Zero(t, h.tabbedWindow.ComposeRuns(), "resetRenderMemos must reach the tabbed window")
	require.Zero(t, h.list.PanelComposeRuns(), "resetRenderMemos must reach the list panel")

	h.viewContent()

	require.Equal(t, 1, h.frameMemo.Runs(), "the frame must be stacked again")
	require.Equal(t, 1, h.tabbedWindow.ComposeRuns(), "the tabbed window must be composed again")
	require.Equal(t, 1, h.list.PanelComposeRuns(), "the list panel must be drawn again")
}

// The tabbed window survives a list that is changing under it — the case the whole
// change is justified by. On a busy fleet the spinner rewrites the list ten times a
// second, so the frame memo and the panel memo both miss; the right pane, which is
// most of the build, must not.
func TestRenderMemos_ASpinningListDoesNotRecomposeTheRightPane(t *testing.T) {
	h := newBenchHome(t, 3)
	h.list.GetInstances()[0].SetStatus(session.Running)

	h.viewContent()
	require.Equal(t, 1, h.tabbedWindow.ComposeRuns())

	// Through Update, so the spinner advances the way the 10Hz loop advances it.
	h.spinnerTicking = true
	for i := range 3 {
		h.Update(spinner.TickMsg{ID: h.spinner.ID()})
		h.viewContent()

		require.Equal(t, i+2, h.frameMemo.Runs(), "a moving spinner must restack the frame")
		require.Equal(t, 1, h.tabbedWindow.ComposeRuns(),
			"but it must not recompose the right pane, which is 40% of the build")
	}
}

// One frame composes the right pane once.
//
// It used to compose it twice: viewContent assigned m.tabbedWindow.String() and
// then, in every layout but focus mode, called it again inside the JoinHorizontal
// that overwrote the first result. The memo hides that — the second call is a hit —
// so the count is taken with memoization OFF, which is the only way the duplicate
// is visible at all.
func TestViewContent_ComposesTheRightPaneOncePerFrame(t *testing.T) {
	defer memo.SetEnabled(false)()

	// Both layout branches: the duplicate lived in the one that joins a list beside
	// the pane, and the other has to keep composing it exactly once too.
	for _, tc := range []struct {
		name   string
		hidden bool
	}{
		{name: "list visible"},
		{name: "list hidden (focus preset)", hidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newBenchHome(t, 3)
			if tc.hidden {
				for i, p := range layoutPresets {
					if p.listHidden {
						h.layoutIndex = i
						break // the FIRST hidden-list preset; without this a second one would silently take over
					}
				}
			}
			require.Equal(t, tc.hidden, h.listHidden(), "precondition: the layout under test")

			h.resetRenderMemos()
			h.viewContent()

			require.Equal(t, 1, h.tabbedWindow.ComposeRuns(),
				"the most expensive component in the frame must be built once")
		})
	}
}

// The net: a frame built with the memos live must be byte-identical to one built
// without them, across every mutation the model can make between two renders.
//
// The per-key-field tests in ui/ prove each enumerated input invalidates. This
// covers the other direction — that a key does not have to be remembered to be
// exercised — by driving real model changes and requiring byte equality.
//
// It proves the enumeration covers THESE mutations, and nothing stronger. An input
// no row below touches is invisible to it: the terminal tab is never selected, the
// splash variant and splash_enabled are never flipped, and the list's sort and
// group modes never move. Growing the table is how that gap closes; do not read a
// green run as "the keys are complete".
func TestRenderMemos_MemoizedFrameMatchesUnmemoized(t *testing.T) {
	cases := []struct {
		name string
		// inert marks the one row that is not supposed to move the frame — the
		// control for the control. Every other row must, or it is comparing two
		// renders of a model nothing happened to and would pass against any memo
		// at all.
		inert bool
		// arm puts the model into the state the row is ABOUT, before the baseline
		// frame is captured. Setup done inside mutate instead moves the frame on its
		// own, which satisfies the inert check for free and lets the actual mutation
		// be deleted with the row still green — the defect that hid in the splash
		// row until it was checked by deleting SetSplashFrame.
		arm    func(t *testing.T, h *home)
		mutate func(t *testing.T, h *home)
	}{
		{name: "nothing", inert: true, mutate: func(_ *testing.T, _ *home) {}},
		{name: "selection moves", mutate: func(_ *testing.T, h *home) { h.list.SetSelectedInstance(2) }},
		{name: "tab toggles", mutate: func(_ *testing.T, h *home) { h.tabbedWindow.Toggle() }},
		{name: "preview enters scroll mode", mutate: func(_ *testing.T, h *home) {
			h.tabbedWindow.SetPreviewScrollContent(h.list.GetInstances()[0], "scrolled text")
		}},
		// The exit is its own row rather than the tail of a round trip. As a round
		// trip the frame returned to where it started, so the inert check passed
		// whether or not scroll mode was ever entered; armed instead, the baseline is
		// the scrolled frame and leaving has to visibly change it.
		{
			name: "preview leaves scroll mode",
			arm: func(_ *testing.T, h *home) {
				h.tabbedWindow.SetPreviewScrollContent(h.list.GetInstances()[0], "scrolled text")
			},
			mutate: func(t *testing.T, h *home) {
				require.NoError(t, h.tabbedWindow.ResetPreviewToNormalMode(h.list.GetInstances()[0]))
			},
		},
		{name: "hint overlay opens", mutate: func(_ *testing.T, h *home) {
			h.tabbedWindow.SetPreviewHintOverlay(h.list.GetInstances()[0], "hinted frame")
		}},
		// The splash clock only reaches the frame while the idle splash is the thing
		// on screen, so pointing the pane at no instance is ARMING, not mutating. Done
		// inside mutate it moved the frame by itself and SetSplashFrame could be
		// deleted with the row still green — the one axis it exists to cover.
		{
			name: "splash frame advances",
			arm:  func(t *testing.T, h *home) { require.NoError(t, h.tabbedWindow.UpdatePreview(nil)) },
			mutate: func(_ *testing.T, h *home) {
				h.tabbedWindow.SetSplashFrame(42)
			},
		},
		{name: "a status flips", mutate: func(_ *testing.T, h *home) { h.list.GetInstances()[0].SetStatus(session.Running) }},
		{name: "a badge appears", mutate: func(_ *testing.T, h *home) { h.list.SetUpdateBadge("v9.9.9") }},
		{name: "a drift badge appears", mutate: func(_ *testing.T, h *home) { h.list.SetDriftBadge("stale") }},
		{name: "the filter opens", mutate: func(_ *testing.T, h *home) { h.list.SetFilterActive(true); h.list.SetFilter("bench") }},
		{name: "a group collapses", mutate: func(_ *testing.T, h *home) { h.list.ClickHeader(h.list.GetInstances()[0].GroupKey()) }},
		{name: "the palette switches", mutate: func(t *testing.T, _ *home) { t.Cleanup(theme.Set("catppuccin-latte")) }},
		{name: "the glyph set switches", mutate: func(t *testing.T, _ *home) { t.Cleanup(theme.SetGlyphSet(theme.GlyphSetASCII)) }},
		{name: "the banner arms", mutate: func(_ *testing.T, h *home) { h.autoYes = true }},
		{name: "a notice shows", mutate: func(_ *testing.T, h *home) { h.errBox.SetNotice("saved", ui.NoticeInfo) }},
		{name: "the window resizes", mutate: func(_ *testing.T, h *home) {
			h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		}},
		{name: "an overlay opens", mutate: func(_ *testing.T, h *home) { h.showHelpScreen(helpTypeGeneral{}, nil) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One home, rendered twice: two homes cannot be compared, because
			// newBenchHome gives each instance its own TempDir and the repo-group
			// header is the directory's name.
			h := newBenchHome(t, 3)
			if tc.arm != nil {
				tc.arm(t, h)
			}
			before := h.viewContent() // fill every memo, so the frame under test can be a hit

			tc.mutate(t, h)
			got := h.viewContent()

			// The same model state, rebuilt from nothing with the memos off.
			// Deferred, not called inline: a panic in viewContent would otherwise
			// leave memoization off process-wide and fail every later test in the
			// package with wrong run counts, burying the real failure.
			want := func() string {
				defer memo.SetEnabled(false)()
				h.resetRenderMemos()
				return h.viewContent()
			}()

			require.Equal(t, want, got,
				"a memoized frame must be byte-identical to an unmemoized one after: %s", tc.name)
			// And the mutation has to have done something, or the row compares two
			// renders of an untouched model and would pass against any memo at all.
			if tc.inert {
				require.Equal(t, before, got, "%s must leave the frame alone", tc.name)
				return
			}
			require.NotEqual(t, before, got, "%s must actually move the frame", tc.name)
		})
	}
}
