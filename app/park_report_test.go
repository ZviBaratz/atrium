package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/parkreport"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolReport plants a report the way the daemon would, in a data dir private to this
// test, and returns nothing: what every test here asserts is what the READER makes of it.
func spoolReport(t *testing.T, limit int, sessions ...parkreport.Session) {
	t.Helper()
	require.NoError(t, parkreport.Write(parkreport.Report{Sessions: sessions, Limit: limit}))
}

// writeRawReport plants a report body Write itself would not produce — here, one with no
// created_at — so the reader's permissiveness about it can be driven.
func writeRawReport(t *testing.T, body string) {
	t.Helper()
	path, err := parkreport.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func quote(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(q)
}

// spooledExists reports whether the spool file is still on disk. It stats the path rather
// than calling parkreport.Read, which would consume an unusable file and make "is it still
// there" depend on the reader under test.
func spooledExists(t *testing.T) bool {
	t.Helper()
	path, err := parkreport.Path()
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	return statErr == nil
}

// TestEarlierParkReportKeepsAStillParkedSession is the path the whole change exists for:
// the daemon parked a session, persisted it, and the next TUI can now say why that row is
// paused instead of leaving the log as the only record.
func TestEarlierParkReportKeepsAStillParkedSession(t *testing.T) {
	h := drainHome(t)
	inst := addPersistedInstance(t, h, "alpha", t.TempDir())
	require.True(t, inst.Paused())
	spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Equal(t, []session.ParkedSession{{Title: "alpha", Path: inst.Path}}, got.Sessions)
	assert.Equal(t, 3, got.Limit, "reported in the cap the earlier load applied, not one re-derived here")
	assert.True(t, spooledExists(t), "the file survives until the notice is actually shown")
}

// TestEarlierParkReportDropsAParkThatDidNotStick is the guard that makes reconciliation
// more than tidying.
//
// A daemon killed before its save (SIGKILL after gracefulStopTimeout) persists no park, so
// the session is still recorded Running and the next load recovers it. Its title is
// nevertheless in the report. Reporting it would tell the user capacity parked a session
// that is running on the row in front of them.
func TestEarlierParkReportDropsAParkThatDidNotStick(t *testing.T) {
	h := drainHome(t)
	inst := addInstance(t, h, "alpha", t.TempDir())
	require.False(t, inst.Paused(), "this row came back, whatever the report says")
	spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Empty(t, got.Sessions, "a park that did not stick is not reported")
	assert.False(t, spooledExists(t), "and the report is cleared rather than retried forever")
}

// TestEarlierParkReportDropsAPauseTheUserMadeSince is the attribution guard, and the case
// "still paused" cannot see on its own.
//
// A report outlives the launch that read it whenever delivery did not happen — a quit
// before the first preview tick, or an unlink that failed — so a whole session can pass
// before it is delivered. In it the user can resume a named row and pause it again
// themselves. That row is paused, and its title and path still match, but the pause is
// theirs: reporting it would claim host capacity did something the user did, which is the
// same false claim the failed-save case is guarded against.
func TestEarlierParkReportDropsAPauseTheUserMadeSince(t *testing.T) {
	h := drainHome(t)
	inst := addPersistedInstance(t, h, "alpha", t.TempDir())
	spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

	// The user's own resume-and-re-pause, which is what moves StatusChangedAt past the
	// report's timestamp.
	inst.SetStatus(session.Running)
	inst.SetStatus(session.Paused)
	require.True(t, inst.Paused(), "the row is paused again — by the user, not by capacity")

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Empty(t, got.Sessions, "a pause the user made is not reported as a capacity park")
	assert.False(t, spooledExists(t), "and the report is cleared rather than re-offered forever")
}

// The faithful positive: the row was parked, its stamp predates the report, and nothing has
// touched it since — so the park is exactly what the report says it is.
func TestEarlierParkReportKeepsASessionUntouchedSinceTheReport(t *testing.T) {
	h := drainHome(t)
	inst := addPersistedInstance(t, h, "alpha", t.TempDir())
	inst.SetStatus(session.Running)
	inst.SetStatus(session.Paused) // the park, stamped before the report is written
	spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Equal(t, []session.ParkedSession{{Title: "alpha", Path: inst.Path}}, got.Sessions)
}

// Both zero-time cases stay permissive, matching Report.Expired: a report that cannot date
// itself does not get to reject anything, and a session whose stamp is absent — a state
// file predating StatusChangedAt — is not evidence of a change.
func TestEarlierParkReportIsPermissiveAboutMissingTimestamps(t *testing.T) {
	t.Run("the report carries no timestamp", func(t *testing.T) {
		h := drainHome(t)
		inst := addPersistedInstance(t, h, "alpha", t.TempDir())
		inst.SetStatus(session.Running)
		inst.SetStatus(session.Paused)
		writeRawReport(t, `{"version":1,"sessions":[{"title":"alpha","path":`+quote(inst.Path)+`}],"limit":3}`)

		got := earlierParkReport(h.list.GetInstances(), time.Now())

		assert.Len(t, got.Sessions, 1, "an undatable report is still delivered")
	})

	t.Run("the session carries no timestamp", func(t *testing.T) {
		h := drainHome(t)
		inst := addPersistedInstance(t, h, "alpha", t.TempDir())
		require.True(t, inst.StatusChangedAt().IsZero(), "loaded from a state file without the field")
		spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

		got := earlierParkReport(h.list.GetInstances(), time.Now())

		assert.Len(t, got.Sessions, 1)
	})
}

// A session the user killed between the park and this launch is gone from the fleet, so
// there is nothing to explain and nothing to keep.
func TestEarlierParkReportDropsAnAbsentSession(t *testing.T) {
	h := drainHome(t)
	spoolReport(t, 3, parkreport.Session{Title: "killed-since", Path: "/repo/web"})

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Empty(t, got.Sessions)
	assert.False(t, spooledExists(t))
}

// TestEarlierParkReportDoesNotMatchOnTitleAlone is why the report carries the pair. The
// same title can legitimately exist in two repos, and here the paused one is NOT the
// session the report is about — matching on the title would report a park that never
// happened to a row the user paused themselves.
func TestEarlierParkReportDoesNotMatchOnTitleAlone(t *testing.T) {
	h := drainHome(t)
	pausedElsewhere := addPersistedInstance(t, h, "alpha", t.TempDir())
	require.True(t, pausedElsewhere.Paused())
	spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: "/repo/some-other-checkout"})

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Empty(t, got.Sessions, "a same-titled session in another repo must not answer for this one")
}

// A partially-stale report still reports what survived: the count on the notice is the
// number of rows the user can actually still act on.
func TestEarlierParkReportKeepsOnlyWhatSurvived(t *testing.T) {
	h := drainHome(t)
	stillParked := addPersistedInstance(t, h, "alpha", t.TempDir())
	spoolReport(t, 3,
		parkreport.Session{Title: "alpha", Path: stillParked.Path},
		parkreport.Session{Title: "resumed-since", Path: "/repo/web"},
	)

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Equal(t, []session.ParkedSession{{Title: "alpha", Path: stillParked.Path}}, got.Sessions)
	assert.True(t, spooledExists(t), "something survived, so the report is still undelivered")
}

// The TTL horizon is parkreport's, not re-derived here; this pins that the reader honors it
// rather than reconciling a report from three weeks ago against today's fleet.
func TestEarlierParkReportHonorsTheHorizon(t *testing.T) {
	h := drainHome(t)
	inst := addPersistedInstance(t, h, "alpha", t.TempDir())
	require.NoError(t, parkreport.Write(parkreport.Report{
		Sessions:  []parkreport.Session{{Title: "alpha", Path: inst.Path}},
		Limit:     3,
		CreatedAt: time.Now().Add(-parkreport.TTL - time.Minute),
	}))

	got := earlierParkReport(h.list.GetInstances(), time.Now())

	assert.Empty(t, got.Sessions, "past the horizon, even though the row is still parked")
}

// No spool is the steady state for anyone whose fleet always fit: silent, and nothing
// created.
func TestEarlierParkReportWithNoSpool(t *testing.T) {
	h := drainHome(t)
	addPersistedInstance(t, h, "alpha", t.TempDir())

	assert.Equal(t, session.DeferredRecovery{}, earlierParkReport(h.list.GetInstances(), time.Now()))
}

// TestPendingParkReports pins the gate newHome uses, which is what keeps the two buffers
// mutually exclusive — the property flushDeferredRecovery's precedence rests on.
func TestPendingParkReports(t *testing.T) {
	t.Run("no park of our own: the spool is read", func(t *testing.T) {
		h := drainHome(t)
		inst := addPersistedInstance(t, h, "alpha", t.TempDir())
		spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})

		own, earlier := pendingParkReports(session.DeferredRecovery{}, h.list.GetInstances(), time.Now())

		assert.Empty(t, own.Sessions)
		assert.Equal(t, []session.ParkedSession{{Title: "alpha", Path: inst.Path}}, earlier.Sessions)
	})

	// The one that matters: a launch with its own park must not also load the spooled one,
	// because a single hint-bar row cannot carry two notices that date the park differently.
	// The file is left where it is, so the next launch delivers it rather than losing it.
	t.Run("our own park suppresses the read and leaves the file", func(t *testing.T) {
		h := drainHome(t)
		inst := addPersistedInstance(t, h, "alpha", t.TempDir())
		spoolReport(t, 3, parkreport.Session{Title: "alpha", Path: inst.Path})
		mine := session.DeferredRecovery{Sessions: parkedSessions("bravo"), Limit: 3}

		own, earlier := pendingParkReports(mine, h.list.GetInstances(), time.Now())

		assert.Equal(t, mine, own)
		assert.Empty(t, earlier.Sessions, "the spool is not read at all")
		assert.True(t, spooledExists(t), "so the next launch can still deliver it")
	})

	t.Run("nothing anywhere is a clean no-op", func(t *testing.T) {
		h := drainHome(t)
		own, earlier := pendingParkReports(session.DeferredRecovery{}, h.list.GetInstances(), time.Now())
		assert.Empty(t, own.Sessions)
		assert.Empty(t, earlier.Sessions)
	})
}

// TestFlushEarlierRecovery is the delivery half for a spooled report: the copy that does
// not misdate the park, once, and the file unlinked only after it has actually been shown.
func TestFlushEarlierRecovery(t *testing.T) {
	t.Run("toasts the earlier copy and clears the spool", func(t *testing.T) {
		h := drainHome(t)
		spoolReport(t, 4, parkreport.Session{Title: "alpha", Path: "/repo/web"})
		h.pendingEarlierRecovery = session.DeferredRecovery{
			Sessions: []session.ParkedSession{{Title: "alpha", Path: "/repo/web"}}, Limit: 4,
		}

		cmd := h.flushDeferredRecovery()

		require.NotNil(t, cmd, "the toast schedules its own auto-hide")
		assert.Equal(t, stateDefault, h.state, "a park must not pop a modal")
		assert.Contains(t, h.menu.NoticeText(), "1 session parked earlier")
		assert.Contains(t, h.menu.NoticeText(), "ctrl+r")
		assert.NotContains(t, h.menu.NoticeText(), "stayed paused", "it was not this load that parked it")
		assert.Empty(t, h.pendingEarlierRecovery.Sessions, "flushing clears the buffer")
		assert.Nil(t, h.flushDeferredRecovery(), "so the 100ms preview tick cannot re-toast it forever")
		assert.False(t, spooledExists(t), "and the delivered report is unlinked")
	})

	// The file is unlinked at DELIVERY, not at the read, so a quit inside the window before
	// the first preview tick leaves the explanation for the next launch instead of erasing
	// it — which is the failure this path was added for.
	t.Run("an undelivered report survives on disk", func(t *testing.T) {
		h := drainHome(t)
		spoolReport(t, 4, parkreport.Session{Title: "alpha", Path: "/repo/web"})
		h.pendingEarlierRecovery = session.DeferredRecovery{
			Sessions: []session.ParkedSession{{Title: "alpha", Path: "/repo/web"}}, Limit: 4,
		}
		h.state = statePrompt // an overlay owns the screen; the tick keeps trying

		assert.Nil(t, h.flushDeferredRecovery())
		assert.NotEmpty(t, h.pendingEarlierRecovery.Sessions, "still buffered")
		assert.True(t, spooledExists(t), "and still on disk, so a quit here does not lose it")
	})

	// The two buffers are mutually exclusive by construction (newHome reads the spool only
	// when its own load deferred nothing). If one ever arrived anyway, this load's own park
	// is the more urgent, and the other must not be silently dropped with it.
	t.Run("this load's own park wins and the spool is left alone", func(t *testing.T) {
		h := drainHome(t)
		spoolReport(t, 4, parkreport.Session{Title: "alpha", Path: "/repo/web"})
		h.pendingDeferredRecovery = session.DeferredRecovery{Sessions: parkedSessions("bravo"), Limit: 4}
		h.pendingEarlierRecovery = session.DeferredRecovery{
			Sessions: []session.ParkedSession{{Title: "alpha", Path: "/repo/web"}}, Limit: 4,
		}

		require.NotNil(t, h.flushDeferredRecovery())

		assert.Contains(t, h.menu.NoticeText(), "stayed paused")
		assert.NotEmpty(t, h.pendingEarlierRecovery.Sessions, "the earlier report is still owed")
		assert.True(t, spooledExists(t), "so its file must not have been consumed")
	})
}
