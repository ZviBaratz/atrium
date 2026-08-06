package app

// park_report.go — delivering a deferral report another process spooled.
//
// Startup recovery is rationed by the host session budget, and what it parks is
// toasted (#474). The autoyes daemon reaches the identical code with no UI, so its
// parks used to arrive as unexplained Paused rows: they persist, and the next load
// reattaches them without probing anything, so its own report is empty (#622). The
// daemon now spools the report instead (internal/parkreport) and this is the consumer
// side.

import (
	"time"

	"github.com/ZviBaratz/atrium/internal/parkreport"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// pendingParkReports splits what a launch owes the user about parked recovery into the
// two buffers home holds: own is what THIS load deferred, earlier is what some previous
// process did (reconciled; see earlierParkReport).
//
// The spool is read only when this load deferred nothing, which is what makes the two
// mutually exclusive — one hint-bar row shows one notice, and the two date the park
// differently, so a launch that had both would have to misdate one of them. Leaving the
// file unread costs nothing: the next launch delivers it, bounded by the spool's TTL.
// Both non-empty needs the cap to have changed between two loads — the earlier process
// parked exactly what it could not afford, so what it did recover fits this load's budget
// unless the number itself moved.
func pendingParkReports(deferred session.DeferredRecovery, instances []*session.Instance, now time.Time) (own, earlier session.DeferredRecovery) {
	if len(deferred.Sessions) > 0 {
		return deferred, session.DeferredRecovery{}
	}
	return session.DeferredRecovery{}, earlierParkReport(instances, now)
}

// earlierParkReport returns the spooled report reconciled against the fleet this load
// just brought online, or the zero value when there is nothing honest to say.
//
// Reconciliation is not tidying: a report describes what SOME EARLIER PROCESS decided,
// and the rows it names may have moved since. A session is kept only if it is still
// loaded, still paused, and has not changed status since the report was written.
//
// Still paused covers the park that did not stick — a daemon killed before its save (see
// daemon.saveAndReport) persists no park, so the row loads as Running and gets recovered,
// and reporting it would tell the user capacity parked a session that is running in front
// of them. It equally drops one the user has since killed or resumed.
//
// Unchanged-since is what makes the attribution honest rather than merely plausible, and
// it is needed because a report can outlive the launch that read it: delivery unlinks the
// file, so a quit inside the window before the first preview tick — or an unlink that
// failed — leaves it for a later launch, with a whole session in between. In that session
// the user can resume a named row and pause it again themselves. Paused alone would then
// toast "parked earlier — host capacity is N" about a pause they made, which is the same
// false claim the failed-save case is guarded against. StatusChangedAt is persisted for
// exactly this kind of question (session/storage.go), and the paused branch of reattach
// leaves it alone, so it still dates the park itself.
//
// Both zero-time cases stay permissive, matching Report.Expired: a report with no
// timestamp cannot date anything, so it does not get to reject; a session whose stamp is
// absent (a state file predating the field) is not evidence of a change.
//
// Matching is the (Title, Path) pair, never the title alone, for the reason the outbox
// drain matches that way too: titles are unique only within a repo group, so a
// same-titled session in another repo must not answer for this one.
//
// A report that keeps at least one session is left on disk; the file is removed when the
// notice is actually shown (flushDeferredRecovery), because an explanation dropped
// before anyone read it is the defect this path exists to fix. A report that keeps
// nothing is removed here instead: it describes no current park, so no later launch
// could deliver it either.
func earlierParkReport(instances []*session.Instance, now time.Time) session.DeferredRecovery {
	report, ok := parkreport.Read(now)
	if !ok {
		return session.DeferredRecovery{}
	}

	var kept []session.ParkedSession
	for _, spooled := range report.Sessions {
		for _, inst := range instances {
			if inst.Title != spooled.Title || inst.Path != spooled.Path {
				continue
			}
			if inst.Paused() && !changedSince(inst, report.CreatedAt) {
				kept = append(kept, session.ParkedSession{Title: spooled.Title, Path: spooled.Path})
			}
			break // the pair is unique, so this was the row the report meant
		}
	}
	if len(kept) == 0 {
		// Deliberately not "none is still paused": a row can also be dropped because it is
		// paused for a newer reason than this report, which is a different fact about it.
		log.InfoLog.Printf("discarding a deferred-recovery report for %d session(s): none of them is still parked by it",
			len(report.Sessions))
		if err := parkreport.Remove(); err != nil {
			log.WarningLog.Printf("could not remove a reconciled-away deferred-recovery report: %v", err)
		}
		return session.DeferredRecovery{}
	}
	return session.DeferredRecovery{Sessions: kept, Limit: report.Limit}
}

// changedSince reports whether inst's status has moved since a report written at
// reportedAt — i.e. whether the pause on this row is a later one than the park the report
// describes.
//
// Either timestamp being zero answers false: neither absence is evidence of a change, and
// treating it as one would empty a report from a version that omits the field, or one
// naming a session whose state file predates StatusChangedAt.
func changedSince(inst *session.Instance, reportedAt time.Time) bool {
	changedAt := inst.StatusChangedAt()
	if reportedAt.IsZero() || changedAt.IsZero() {
		return false
	}
	return changedAt.After(reportedAt)
}
