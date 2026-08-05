package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestStartupParkNotice pins the copy for sessions a startup recovery left paused for
// want of host budget (#474). A silent Running→Paused reads as a user pause (#270), so
// the park is reported — and because the notice is the whole of the user's explanation,
// it has to carry both the reason and the key that undoes it.
func TestStartupParkNotice(t *testing.T) {
	t.Run("nothing deferred says nothing", func(t *testing.T) {
		require.Empty(t, startupParkNotice(session.DeferredRecovery{}))
	})

	t.Run("one session", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{Titles: []string{"alpha"}, Limit: 2})
		require.Equal(t, "1 session stayed paused — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	t.Run("several batch into one count", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{Titles: []string{"a", "b", "c"}, Limit: 2})
		require.Equal(t, "3 sessions stayed paused — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	// r is deliberately NOT advertised, at either count: it acts on the selected row,
	// and a parked session is never the startup selection, so the advice would earn a
	// refusal notice instead of a resume.
	t.Run("never advertises the selection-dependent key", func(t *testing.T) {
		for _, n := range []int{1, 2, 7} {
			got := startupParkNotice(session.DeferredRecovery{Titles: make([]string, n), Limit: 2})
			require.NotContains(t, got, "press r")
			require.Contains(t, got, "ctrl+r")
		}
	})

	// The titles are deliberately absent, unlike surfaceLostRecoveries' park toast: a
	// session title is unbounded user input, and this row truncates its tail, so naming
	// one would drop the key the line exists to teach.
	t.Run("no session title is interpolated", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{
			Titles: []string{"a-very-long-session-title-the-user-typed"}, Limit: 2,
		})
		require.NotContains(t, got, "a-very-long-session-title")
	})
}

// TestStartupParkNoticeFitsNarrowBar is the assertion behind the width claim in
// startupParkNotice's doc comment, rather than a measurement taken once and trusted.
// ui.Menu truncates a notice at width-2 and truncates the TAIL, so a spelling that
// outgrows an 80-column terminal loses "press r to resume" — the actionable half — on
// the terminal most users have.
func TestStartupParkNoticeFitsNarrowBar(t *testing.T) {
	for _, d := range []session.DeferredRecovery{
		{Titles: []string{"a"}, Limit: 2},
		// Three digits in both interpolations, the worst case each can actually reach:
		// the count is bounded only by the fleet, and the capacity is DefaultSessionCap()
		// — half the host's threads, so 128 on a 256-thread machine. This is the case
		// that caught the first spelling at 82 cells.
		{Titles: make([]string, 128), Limit: 128},
	} {
		got := startupParkNotice(d)
		require.LessOrEqual(t, ansi.StringWidth(got), startupParkNoticeMaxWidth,
			"the notice must fit an 80-column hint bar, which truncates the key off the tail: %q", got)
	}
}

// TestFlushDeferredRecovery pins the delivery half: the report is buffered at
// construction (there is no frame to toast on yet) and flushed by the preview tick,
// exactly once, and never over an overlay that owns the screen.
func TestFlushDeferredRecovery(t *testing.T) {
	t.Run("flushes onto the hint bar and clears the buffer", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.pendingDeferredRecovery = session.DeferredRecovery{Titles: []string{"a", "b"}, Limit: 2}

		cmd := h.flushDeferredRecovery()

		require.NotNil(t, cmd, "the toast schedules its own auto-hide")
		require.Equal(t, stateDefault, h.state, "a startup park must not pop a modal")
		require.Contains(t, h.menu.NoticeText(), "2 sessions stayed paused")
		require.Contains(t, h.menu.NoticeText(), "ctrl+r")
		require.Empty(t, h.pendingDeferredRecovery.Titles, "flushing clears the buffer")
		require.Nil(t, h.flushDeferredRecovery(), "so the 100ms preview tick cannot re-toast it forever")
	})

	t.Run("nothing deferred is a clean no-op", func(t *testing.T) {
		h := newCreateFormHome(t)
		require.Nil(t, h.flushDeferredRecovery())
		require.Empty(t, h.menu.NoticeText())
	})

	t.Run("waits rather than clobbering an overlay", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.pendingDeferredRecovery = session.DeferredRecovery{Titles: []string{"a"}, Limit: 2}
		h.state = statePrompt // an overlay owns the screen

		require.Nil(t, h.flushDeferredRecovery(), "no toast while an overlay is up")
		require.Equal(t, statePrompt, h.state, "and the overlay is untouched")
		require.NotEmpty(t, h.pendingDeferredRecovery.Titles, "the report is still buffered")

		h.state = stateDefault
		require.NotNil(t, h.flushDeferredRecovery(), "and lands once the screen is free")
		require.Contains(t, h.menu.NoticeText(), "stayed paused")
	})
}
