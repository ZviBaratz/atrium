package app

// create_disclose.go — telling the user what a spent `atrium new` request left behind
// (#731, #732).
//
// A create that gives up after Worktree.Setup has run leaves a branch, usually a worktree
// and sometimes a live agent, and no row in state.json pointing at any of them. The caller
// hears about it through its rejection receipt; the person at the TUI heard nothing at all,
// because the only durable trace was destroyed by the same call that answered the caller.
// outbox.Disclose keeps that trace (see internal/outbox/create_disclosure.go); this is the
// side that reads it.
//
// Two producers, one reader. A disclosure written by a process that then died is found at
// startup; one written by this process is buffered as it is written, so a live TUI does not
// make the user relaunch to hear about an orphan it made a moment ago. Both land in
// home.pendingCreateDisclosures and both are shown by flushCreateDisclosures.
//
// A modal rather than a toast, which is the case showInfo's own doc names: the report lists
// a branch, a directory and a tmux session, and the remedy is for the user to go and remove
// them. A truncating notice row would name the branch and lose the rest, sending the one
// person who can act on it to the log for the half that got cut.

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// createDisclosuresShown caps how many spent requests one report enumerates, for
// customCommandProblemsShown's reason: the count has no ceiling this side of the spool, and no
// field in an entry is length-bounded. The repo path and the worktree path have no limit at
// all; and the title has one only where `atrium new` wrote the record — cli_new refuses past
// session.MaxTitleLen before anything is spooled, while executeCreateRequest re-checks it
// because a spool file from a build with a different limit does reach here, and refusing it now
// leaves a mark like any other refusal.
const createDisclosuresShown = 5

// loadCreateDisclosures reads the disclosures an earlier process left, for newHome to
// buffer.
//
// It runs after reconcileCreateClaims so a refusal that reconcile just reached is in this
// launch's report rather than the next one's. Undecodable files are dropped from the REPORT
// here — nothing downstream can act on a disclosure, so one nobody can read has no reader
// to preserve it for, unlike a spool record where the same file means a caller is owed a
// receipt — and offered to ClearDisclosure, which keeps it anyway if a record or claim is
// still sitting beside it. That distinction is the point: an unreadable disclosure is still
// a terminal mark, and the version it could not decode may be a newer atrium's.
func loadCreateDisclosures() []outbox.DisclosureEntry {
	entries, err := outbox.ListDisclosures()
	if err != nil {
		log.ErrorLog.Printf("failed to read the create spool for stranded artifacts: %v", err)
		return nil
	}
	kept := make([]outbox.DisclosureEntry, 0, len(entries))
	for _, e := range entries {
		if e.Err != nil {
			log.ErrorLog.Printf("discarding an unreadable create disclosure: %v", e.Err)
			if _, err := outbox.ClearDisclosure(e.Path); err != nil {
				log.ErrorLog.Printf("failed to clear an unreadable create disclosure: %v", err)
			}
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// writeCreateDisclosure records on disk, and in the log, what a spent create request left
// behind. It returns the disclosure as written — outbox.Disclose stamps Version and
// CreatedAt — so a caller buffering the same value for this process's report shows the
// record that exists rather than one missing both.
//
// The log line is written whether or not the file could be, and it is the permanent account
// either way. It is the only one that survives a full disk; and the file itself is not
// permanent by design — ClearDisclosure drops it once nothing is left for it to guard, so a
// disclosure whose leftovers were reported and whose record is gone leaves only this line.
func writeCreateDisclosure(record string, d outbox.Disclosure) outbox.Disclosure {
	if err := outbox.Disclose(record, &d); err != nil {
		log.ErrorLog.Printf("failed to record what the create request for %q left behind: %v",
			d.Title, err)
	}
	log.WarningLog.Printf("create request for %q left artifacts belonging to no session "+
		"(branch %q, worktree %q, tmux %q): %s", d.Title, d.Branch, d.Worktree, d.TmuxName, d.Reason)
	return d
}

// discloseCreateLeftovers records what a spent create request left behind and queues it for
// this process's own report.
//
// For leftovers with no session: a refused request, whose branch and worktree belong to
// nothing in the list, so a modal is the only way the person who can remove them hears
// about them. discloseLiveButUnrecorded is the other case and deliberately does not queue.
//
// Buffered even when the write fails. The file's job is to survive a crash; the buffer's is
// to reach the person at the terminal, and a full disk is no reason to withhold the one
// mention of an orphaned branch from the only party who can delete it.
func (m *home) discloseCreateLeftovers(record string, d outbox.Disclosure) {
	m.pendingCreateDisclosures = append(m.pendingCreateDisclosures,
		outbox.DisclosureEntry{Path: record, Disclosure: writeCreateDisclosure(record, d)})
}

// discloseLiveButUnrecorded records the leftovers of a create whose session is live and
// whose row is not, and remembers the instance so a later successful persist can withdraw it.
//
// No report, for discloseUnrecordedSession's reason: the artifacts belong to a session in
// the list, and a modal telling the user to remove them by hand would be telling them to
// destroy a running agent. The file covers the only way they become orphans, which is this
// process dying before the row lands.
//
// Withdrawn rather than left, because the same file is a false report the moment the row
// IS durable — the next launch would name a branch, a worktree and a tmux session that
// state.json has a row for, under a header saying nothing points at them. persistInstances
// is where that changes, so that is where the withdrawal is.
//
// The INSTANCE is remembered alongside the record, and not only the record, because "a
// persist landed" is not the same event as "this row landed". See withdrawUnrecordedCreates.
func (m *home) discloseLiveButUnrecorded(inst *session.Instance, record string, d outbox.Disclosure) {
	writeCreateDisclosure(record, d)
	m.unrecordedCreates = append(m.unrecordedCreates, unrecordedCreate{inst: inst, record: record})
}

// unrecordedCreate pairs the disclosure written for a live-but-unrecorded session with the
// session itself, which is what the withdrawal has to check rather than infer.
type unrecordedCreate struct {
	inst   *session.Instance
	record string
}

// withdrawUnrecordedCreates clears the disclosures written for sessions whose rows have now
// been persisted. Called from persistInstances on success, with the list that was saved.
//
// The list, rather than the mere fact of success, and that is the whole of the check. Two
// things make "a save landed" the wrong event on its own:
//
//   - Storage.SaveInstances skips any instance for which Started() is false, so a success
//     does not mean every row it was handed is on disk;
//   - a session removed from the list before its row ever landed is gone from the save, and
//     the kill that removed it may have failed. applyKillDone drops the row and THEN reports
//     "killed but teardown was incomplete", so a branch, a worktree and an agent can all
//     still be standing. Withdrawing there deletes the one file that named them — #732 by
//     way of its own fix.
//
// So a disclosure is withdrawn only when the session it describes is in what was just
// written. Anything else is kept, which over-reports: a kill whose teardown SUCCEEDED leaves
// a disclosure naming artifacts that are gone, and the next launch reports them. That is the
// harmless direction for this type (see outbox.Disclosure) and the report tells its reader to
// read the reason before acting.
//
// A kept entry stays in the slice, because ClearDisclosure also declines to remove a file
// still guarding a record or claim that could not be unlinked — and there the next persist is
// the retry that lands it. Dropped only once the file is actually gone, which is what the
// removed flag is for; a failure is logged and the entry kept for the same reason.
func (m *home) withdrawUnrecordedCreates(persisted []*session.Instance) {
	if len(m.unrecordedCreates) == 0 {
		return
	}
	durable := make(map[*session.Instance]bool, len(persisted))
	for _, inst := range persisted {
		if inst.Started() {
			durable[inst] = true
		}
	}
	kept := m.unrecordedCreates[:0]
	for _, u := range m.unrecordedCreates {
		if !durable[u.inst] {
			kept = append(kept, u)
			continue
		}
		removed, err := outbox.ClearDisclosure(u.record)
		if err != nil {
			log.ErrorLog.Printf("failed to withdraw the disclosure for a create that has since "+
				"been recorded: %v", err)
		}
		if !removed {
			kept = append(kept, u)
		}
	}
	m.unrecordedCreates = kept
}

// flushCreateDisclosures opens the report for the spent create requests that left something
// behind, once there is a frame to show it on and no overlay owns the screen — the shape
// flushRepoScriptProblems uses, including the deferral: showInfo switches to stateInfo as it
// builds, so a second report in the same tick waits for the next one instead of clobbering
// this one.
//
// The buffer is cleared as it fires so the 100ms preview tick cannot reopen it forever, and
// the files are offered up HERE rather than at the read: a quit inside the window before the
// first tick leaves the explanation on disk for the next launch instead of erasing it, which
// is flushDeferredRecovery's rule and the whole failure this path exists for.
//
// "Offered", because ClearDisclosure is the one that decides. Showing the report finishes
// the disclosure's job as a report and not its job as the mark that keeps a surviving
// record or claim from being executed, so a disclosure with one of those still beside it
// outlives the modal and is reported again on the next launch. That repetition is the
// intended shape: what it repeats is still true of artifacts still stranded, and every
// launch retries the unlink that left them.
//
// Only what this report NAMES is offered up. The report is capped at createDisclosuresShown
// entries, and an earlier draft unlinked all of them and enumerated the first five — so the
// sixth onward were destroyed having never been on screen, with a count as the only remedy
// for the newest orphans. The tail is left on disk instead and reported by the next launch,
// which is the same rule the kept-mark case above already follows: a file whose job is not
// finished is not offered up.
//
// Entries with nothing to name ARE offered up, and there are plenty: every giving-up writes a
// disclosure whether or not it has an inventory, because the mark is what keeps a claim from
// being re-judged and a record from being re-drained (outbox.Disclose). A modal saying "a
// request failed, and left nothing" would be noise the receipt already covered — so their
// report job is finished by there being nothing to report.
func (m *home) flushCreateDisclosures() tea.Cmd {
	if len(m.pendingCreateDisclosures) == 0 || m.state != stateDefault {
		return nil
	}
	entries := m.pendingCreateDisclosures
	m.pendingCreateDisclosures = nil
	drop := func(record string) {
		if _, err := outbox.ClearDisclosure(record); err != nil {
			// Logged, not surfaced: the notice the user needs is about to be on screen. A
			// persistent failure repeats this report on every later launch until the TTL
			// horizon sweeps the file, and the thing it repeats is still true of artifacts
			// still stranded — so no poisoning set, for flushDeferredRecovery's reason.
			log.ErrorLog.Printf("failed to clear a delivered create disclosure: %v", err)
		}
	}
	var shown, held []outbox.Disclosure
	for _, e := range entries {
		switch {
		case !e.Disclosure.Leftovers():
			drop(e.Path)
		case len(shown) < createDisclosuresShown:
			shown = append(shown, e.Disclosure)
			drop(e.Path)
		default:
			held = append(held, e.Disclosure)
		}
	}
	if len(shown) == 0 {
		return nil
	}
	return m.showInfo(createDisclosureReport(shown, held, time.Now()))
}

// createDisclosureReport is that modal's text, bounded on both axes for
// repoScriptProblemsReport's reason. held is what the cap left on disk for the next launch,
// which the report counts rather than names.
//
// One labelled row per artifact rather than a sentence, because the user is about to retype
// these into `git branch -d`, `git worktree remove` and `tmux kill-session`, and because it
// is the shape that survives the overlay: TextOverlay wraps, and at any width a session list
// leaves room for, a worktree path is longer than the line. A wrapped value is still legible
// as a value; a wrapped sentence with three paths in it is not.
//
// Which is also why no row carries a command. A copy-pasteable `tmux -L … kill-session -t …`
// beside the session name reads well in source and arrives split across two lines with the
// modal's border between them — pasteable in neither half. The socket goes in the trailer
// instead, on a line of its own inside reportNarrowWidth, and only when some entry actually
// has a tmux session to name — including one only the count mentions, because the trailer is
// what makes that entry actionable when its turn comes.
//
// Each row clips its VALUE rather than the whole line, so the label survives a long path —
// and the reason clips from the other end (clipReportLineEnd). Every reason opens with
// boilerplate ("a previous atrium was interrupted…", "the outbox was cleared…") and closes
// with what to do about it, so a truncation from the right keeps the preamble and drops the
// remedy, along with the cause of a persist failure ("…: no space left on device").
//
// The entry's own line clips its two values for that same reason and appends the age
// unclipped. Composed and then clipped whole — which it was — a monorepo path of ordinary
// depth pushes `(given up on 2d ago)` off the end, and Disclosure.CreatedAt has no other
// reader: two orphans from different days then render identically.
//
// The trailer states what is true and stops short of an instruction, because the report
// cannot tell the three cases apart: a branch Atrium made and abandoned is the user's to
// delete, a worktree they created themselves is not, and the whole inventory was measured
// when the create gave up rather than now.
func createDisclosureReport(shown, held []outbox.Disclosure, now time.Time) string {
	if len(shown) == 0 {
		return ""
	}
	total := len(shown) + len(held)
	head := "an interrupted `atrium new` left artifacts behind:"
	if total > 1 {
		head = fmt.Sprintf("%d interrupted `atrium new` requests left artifacts behind:", total)
	}
	lines := []string{head}
	for _, d := range shown {
		row := func(label, value string) {
			lines = append(lines, fmt.Sprintf("    %-8s %s", label, clipReportLine(value)))
		}
		lines = append(lines, "", fmt.Sprintf("%q in %s%s",
			clipReportLine(d.Title), clipReportLine(d.Repo), gaveUp(d.CreatedAt, now)))
		row("why", clipReportLineEnd(d.Reason))
		if d.Branch != "" {
			row("branch", d.Branch)
		}
		if d.Worktree != "" {
			row("worktree", d.Worktree)
		}
		if d.TmuxName != "" {
			row("tmux", d.TmuxName)
		}
	}
	if len(held) > 0 {
		lines = append(lines, fmt.Sprintf("    … and %d more, kept for the next launch", len(held)))
	}
	lines = append(lines, "",
		"Nothing in atrium's records points at these. Read each `why`",
		"before acting: a branch may be one to resume a session on, and",
		"a worktree may be one you made.")
	if anyTmuxName(shown, held) {
		// The socket from config.RuntimeName rather than hardcoded: a legacy install is on
		// "claudesquad", and `tmux -L atrium` there finds nothing (CLAUDE.md).
		//
		// Two lines, because one was 76 runes for "atrium" and 86 for "claudesquad" against
		// an overlay inner width of 64 at a 72-column terminal — so the command arrived with
		// the modal's border through the middle of it, which is the outcome the paragraph
		// above rejects a per-row command for.
		socket := config.RuntimeName()
		lines = append(lines,
			fmt.Sprintf("Those tmux sessions are on socket %q. To kill one:", socket),
			fmt.Sprintf("    tmux -L %s kill-session -t <name>", socket))
	}
	return strings.Join(lines, "\n")
}

// anyTmuxName reports whether any entry names a tmux session, counting the ones the cap held
// back as well as the ones on screen.
//
// Both, because the trailer supplies the socket and nothing else does: on a legacy install it
// is "claudesquad" and unguessable. Computed over the shown entries alone — which it was — a
// report whose five oldest entries are branch-only refusals drops the socket line while the
// two it held back are the live agents whose only remedy is that command.
func anyTmuxName(sets ...[]outbox.Disclosure) bool {
	for _, set := range sets {
		for _, d := range set {
			if d.TmuxName != "" {
				return true
			}
		}
	}
	return false
}

// gaveUp dates one entry, which is the only thing that tells two orphans from different
// days apart — and the reason Disclosure.CreatedAt is on the wire at all.
//
// Empty for a zero timestamp rather than "0s ago". A disclosure written by an atrium that
// predates the field, or one whose Disclose failed before it could stamp, has no answer,
// and inventing today's date for it would make the oldest entry look like the newest.
//
// A coarse age rather than a Duration's own String, which renders three hours as "3h0m0s"
// and two days as "49h0m0s" — precision nobody reads, in the units nobody wanted. Same shape
// as overlay.relTime, with a day tier added and its seconds tier collapsed into words: a
// disclosure can outlive a weekend where a command-log row cannot, and the sub-minute case
// here is a clock that moved rather than something worth counting.
//
// doctor.HumanAge is the exported ladder next door and is deliberately not reused: it renders
// the same three hours as "3h0m" and the same two days as "2d0h", which is the precision this
// one exists to drop. It is answering a different question — how long a leaked worktree has
// been leaking, where the minutes matter to whoever is deciding whether it is still in use.
func gaveUp(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	var age string
	switch d := now.Sub(at); {
	case d < time.Minute:
		age = "under a minute" // and any negative, from a clock that moved
	case d < time.Hour:
		age = fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		age = fmt.Sprintf("%dh", int(d.Hours()))
	default:
		age = fmt.Sprintf("%dd", int(d.Hours())/24)
	}
	return fmt.Sprintf(" (given up on %s ago)", age)
}
