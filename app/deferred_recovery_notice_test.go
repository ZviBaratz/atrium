package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// parkedSessions builds the report payload for the given titles. The notices are counts, so the
// paths are irrelevant here — the pair is asserted where identity actually matters
// (session/recovery_test.go for what the budget records, park_report_test.go for how a
// spooled report is reconciled).
func parkedSessions(titles ...string) []session.ParkedSession {
	out := make([]session.ParkedSession, 0, len(titles))
	for _, title := range titles {
		out = append(out, session.ParkedSession{Title: title, Path: "/repo"})
	}
	return out
}

// TestStartupParkNotice pins the copy for sessions a startup recovery left paused for
// want of host budget (#474). A silent Running→Paused reads as a user pause (#270), so
// the park is reported — and because the notice is the whole of the user's explanation,
// it has to carry both the reason and the key that undoes it.
func TestStartupParkNotice(t *testing.T) {
	t.Run("nothing deferred says nothing", func(t *testing.T) {
		require.Empty(t, startupParkNotice(session.DeferredRecovery{}))
	})

	t.Run("one session", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{Sessions: parkedSessions("alpha"), Limit: 2})
		require.Equal(t, "1 session stayed paused — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	t.Run("several batch into one count", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{Sessions: parkedSessions("a", "b", "c"), Limit: 2})
		require.Equal(t, "3 sessions stayed paused — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	// r is deliberately NOT advertised, at either count: it acts on the selected row,
	// and a parked session is never the startup selection, so the advice would earn a
	// refusal notice instead of a resume.
	t.Run("never advertises the selection-dependent key", func(t *testing.T) {
		for _, n := range []int{1, 2, 7} {
			got := startupParkNotice(session.DeferredRecovery{Sessions: make([]session.ParkedSession, n), Limit: 2})
			require.NotContains(t, got, "press r")
			require.Contains(t, got, "ctrl+r")
		}
	})

	// The titles are deliberately absent, unlike surfaceLostRecoveries' park toast: a
	// session title is unbounded user input, and this row truncates its tail, so naming
	// one would drop the key the line exists to teach.
	t.Run("no session title is interpolated", func(t *testing.T) {
		got := startupParkNotice(session.DeferredRecovery{
			Sessions: parkedSessions("a-very-long-session-title-the-user-typed"), Limit: 2,
		})
		require.NotContains(t, got, "a-very-long-session-title")
	})
}

// TestEarlierParkNotice pins the copy for a park a DIFFERENT process made while the user
// was away (#622). It carries the same reason and the same key as the in-process
// spelling, and differs in exactly one thing: it does not claim the park happened on the
// load the user just triggered.
func TestEarlierParkNotice(t *testing.T) {
	t.Run("nothing deferred says nothing", func(t *testing.T) {
		require.Empty(t, earlierParkNotice(session.DeferredRecovery{}))
	})

	t.Run("one session", func(t *testing.T) {
		got := earlierParkNotice(session.DeferredRecovery{Sessions: parkedSessions("alpha"), Limit: 2})
		require.Equal(t, "1 session parked earlier — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	t.Run("several batch into one count", func(t *testing.T) {
		got := earlierParkNotice(session.DeferredRecovery{Sessions: parkedSessions("a", "b", "c"), Limit: 2})
		require.Equal(t, "3 sessions parked earlier — host capacity is 2 (ctrl+r resumes paused)", got)
	})

	// The one substantive difference from startupParkNotice, and the reason there are two
	// spellings at all: "stayed" is present-tense about a load the user just performed,
	// and this report describes a decision another process took hours ago.
	t.Run("does not date the park to this launch", func(t *testing.T) {
		got := earlierParkNotice(session.DeferredRecovery{Sessions: parkedSessions("a"), Limit: 2})
		require.NotContains(t, got, "stayed")
		require.Contains(t, got, "earlier")
	})

	// Same reasons as the in-process spelling: a title is unbounded input on a row that
	// truncates, and r acts on a selection a parked row is never part of.
	t.Run("names no session and advertises only ctrl+r", func(t *testing.T) {
		got := earlierParkNotice(session.DeferredRecovery{
			Sessions: parkedSessions("a-very-long-session-title-the-user-typed"), Limit: 2,
		})
		require.NotContains(t, got, "a-very-long-session-title")
		require.NotContains(t, got, "press r")
		require.Contains(t, got, "ctrl+r")
	})
}

// TestStartupParkNoticeFitsNarrowBar is the assertion behind the width claim in
// startupParkNotice's doc comment, rather than a measurement taken once and trusted.
// ui.Menu truncates a notice at width-2 and truncates the TAIL, so a spelling that
// outgrows an 80-column terminal loses "press r to resume" — the actionable half — on
// the terminal most users have.
//
// Both spellings are held to the bound, because both ride the same row: #620's reworded
// copy measured 82 cells at the worst case and only this assertion caught it.
func TestStartupParkNoticeFitsNarrowBar(t *testing.T) {
	for name, notice := range map[string]func(session.DeferredRecovery) string{
		"this load's own park": startupParkNotice,
		"a park made earlier":  earlierParkNotice,
	} {
		for _, d := range []session.DeferredRecovery{
			{Sessions: parkedSessions("a"), Limit: 2},
			// Three digits in both interpolations, the worst case each can actually reach:
			// the count is bounded only by the fleet, and the capacity is DefaultSessionCap()
			// — half the host's threads, so 128 on a 256-thread machine. This is the case
			// that caught the first spelling at 82 cells.
			{Sessions: make([]session.ParkedSession, 128), Limit: 128},
		} {
			got := notice(d)
			require.LessOrEqual(t, ansi.StringWidth(got), startupParkNoticeMaxWidth,
				"%s must fit an 80-column hint bar, which truncates the key off the tail: %q", name, got)
		}
	}
}

// TestFlushDeferredRecovery pins the delivery half: the report is buffered at
// construction (there is no frame to toast on yet) and flushed by the preview tick,
// exactly once, and never over an overlay that owns the screen.
func TestFlushDeferredRecovery(t *testing.T) {
	t.Run("flushes onto the hint bar and clears the buffer", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.pendingDeferredRecovery = session.DeferredRecovery{Sessions: parkedSessions("a", "b"), Limit: 2}

		cmd := h.flushDeferredRecovery()

		require.NotNil(t, cmd, "the toast schedules its own auto-hide")
		require.Equal(t, stateDefault, h.state, "a startup park must not pop a modal")
		require.Contains(t, h.menu.NoticeText(), "2 sessions stayed paused")
		require.Contains(t, h.menu.NoticeText(), "ctrl+r")
		require.Empty(t, h.pendingDeferredRecovery.Sessions, "flushing clears the buffer")
		require.Nil(t, h.flushDeferredRecovery(), "so the 100ms preview tick cannot re-toast it forever")
	})

	t.Run("nothing deferred is a clean no-op", func(t *testing.T) {
		h := newCreateFormHome(t)
		require.Nil(t, h.flushDeferredRecovery())
		require.Empty(t, h.menu.NoticeText())
	})

	t.Run("waits rather than clobbering an overlay", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.pendingDeferredRecovery = session.DeferredRecovery{Sessions: parkedSessions("a"), Limit: 2}
		h.state = statePrompt // an overlay owns the screen

		require.Nil(t, h.flushDeferredRecovery(), "no toast while an overlay is up")
		require.Equal(t, statePrompt, h.state, "and the overlay is untouched")
		require.NotEmpty(t, h.pendingDeferredRecovery.Sessions, "the report is still buffered")

		h.state = stateDefault
		require.NotNil(t, h.flushDeferredRecovery(), "and lands once the screen is free")
		require.Contains(t, h.menu.NoticeText(), "stayed paused")
	})
}
