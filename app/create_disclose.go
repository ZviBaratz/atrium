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

// createDisclosureBacklog is the order the reader works through at startup: the disclosures
// the reconcile could not WRITE first, then the ones found on disk.
//
// That order is the whole of this function, and it is decided here rather than at the call
// site because the reason is not local to either input. The reconcile's undisclosed entries
// are the same refusals with the richest inventory — a branch, a registered worktree, a
// running agent — reached on a launch where every Disclose failed, which in practice means a
// full disk. They exist nowhere but in this slice.
//
// flushCreateDisclosures caps what one report names and decides by position, so appended —
// which they were — five older disk-backed entries took every slot and these went to the held
// tail. The tail is re-buffered rather than lost, so within a run they are still shown; what
// the order buys is the crash. A process that dies before the second pass takes the
// memory-only entries with it, while a disk-backed one is read again by the next launch, so
// the entries that cannot be re-read are the ones that go first.
func createDisclosureBacklog(onDisk, undisclosed []outbox.DisclosureEntry) []outbox.DisclosureEntry {
	return append(undisclosed, onDisk...)
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
//
// Two lines, on the two levels, keyed on whether there is an inventory. Most giving-ups have
// none: every gate refusal writes a mark and an ordinary title collision names no branch, no
// worktree and no tmux session. Logged through the one wording, that reads
// `left artifacts belonging to no session (branch "", worktree "", tmux "")` at WARNING — the
// opposite of what happened, on the level an operator greps, for the overwhelming majority of
// the lines. It also buries the case the wording exists for, which is the one that is true.
func writeCreateDisclosure(record string, d outbox.Disclosure) outbox.Disclosure {
	if err := outbox.Disclose(record, &d); err != nil {
		log.ErrorLog.Printf("failed to record what the create request for %q left behind: %v",
			d.Title, err)
	}
	if d.Leftovers() {
		log.WarningLog.Printf("create request for %q left artifacts belonging to no session "+
			"(branch %q, worktree %q, tmux %q): %s",
			d.Title, d.Branch, d.Worktree, d.TmuxName, d.Reason)
	} else {
		log.InfoLog.Printf("create request for %q was given up on, leaving nothing behind: %s",
			d.Title, d.Reason)
	}
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
//
// Buffered only when there IS one, though, and that is what bounds the slice. A mark with
// nothing to name is never shown — flushCreateDisclosures drops it unread — so buffering one
// grew a slice nothing could ever empty while an overlay owned the screen, at up to a gate
// refusal per tick for as long as the modal stood. The two things a buffered entry buys are
// both about being seen: the report, and clearMarkOverADroppedRecord's promise not to delete
// a file nobody has read yet. Neither applies to a mark with no reader, and its guard job is
// held by ClearDisclosure's own recordStillSpooled check rather than by this slice.
func (m *home) discloseCreateLeftovers(record string, d outbox.Disclosure) {
	written := writeCreateDisclosure(record, d)
	if !written.Leftovers() {
		return
	}
	m.pendingCreateDisclosures = append(m.pendingCreateDisclosures,
		outbox.DisclosureEntry{Path: record, Disclosure: written})
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
// A kept entry stays in the slice, and the two reasons it is kept have different futures.
// ClearDisclosure declines to remove a file still guarding a record or claim whose unlink
// failed, and there a later persist genuinely does retry it, because the row is durable and
// the withdrawal is re-attempted every time. A row that never landed is the other case, and
// nothing in this process retries THAT: applyKillDone drops the row before it reports an
// incomplete teardown, so the instance is absent from every later save and the entry is held
// for the life of the run. That is deliberate — the artifacts may still be standing — and it
// is why the report screens against the live list rather than trusting the file (see
// flushCreateDisclosures).
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
// for the newest orphans. The tail is left on disk instead, which is the same rule the
// kept-mark case above already follows: a file whose job is not finished is not offered up.
//
// The tail stays in the BUFFER too, and it has to. The buffer is the only thing
// clearMarkOverADroppedRecord consults before deleting a mark, so a tail dropped from it was
// unguarded from the instant this report fired: one drain tick later the same records took
// the terminal-mark arm, were answered and unlinked, and every held mark went with them —
// while the modal on screen said they were kept. They were also the NEWEST orphans, the ones
// likeliest to still have an agent running, because ListDisclosures is oldest-first. Held
// entries carry their record path for the same reason: a Disclosure alone cannot be re-offered
// or re-buffered.
//
// So the tail is reported on the next TICK rather than the next launch, which is the same
// sentence with a better horizon: each pass shows five and the buffer strictly shrinks, so
// the 100ms tick pages through a backlog instead of re-opening one report forever.
//
// Entries with nothing to name are offered up unread. Every giving-up writes a mark whether
// or not it has an inventory, because the mark is what keeps a claim from being re-judged and
// a record from being re-drained (outbox.Disclose), and a modal saying "a request failed, and
// left nothing" would be noise the receipt already covered — so their report job is finished
// by there being nothing to report. This process no longer buffers those at all; the ones
// that arrive here came off disk.
//
// A disclosure whose artifacts belong to a session in the LIST is dropped unread as well, and
// that is the one screen the file cannot do for itself. discloseLiveButUnrecorded writes a
// mark for a session that is running while its row is not, and withdraws it when the row
// lands — but a session killed before it ever persisted is absent from every later save, so
// the withdrawal never comes and the file outlives the run. Neither name has anything in it
// that varies between two sessions of the same title in the same repository —
// git.BranchNameForSession takes a configured prefix and the title, tmux.QualifiedSessionName
// a repo group and the title — so the next session created under that title is described,
// byte for byte, by a file saying nothing in atrium's records points at it, under a report
// that hands the reader a kill command. Matching the live list is what tells those apart.
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
	var shown []outbox.Disclosure
	var held []outbox.DisclosureEntry
	for _, e := range entries {
		switch {
		case !e.Disclosure.Leftovers() || m.liveSessionOwns(e.Disclosure):
			drop(e.Path)
		case len(shown) < createDisclosuresShown:
			shown = append(shown, e.Disclosure)
			drop(e.Path)
		default:
			held = append(held, e)
		}
	}
	m.pendingCreateDisclosures = held
	if len(shown) == 0 {
		return nil
	}
	return m.showInfo(createDisclosureReport(shown, held, time.Now()))
}

// liveSessionOwns reports whether the artifacts d names belong to a session in the list, in
// which case they are not stranded and the report must not name them.
//
// The branch and the tmux name, either of them, and only when non-empty: a direct session has
// no branch and matching on "" would swallow every disclosure that names only a worktree. A
// branch a live row holds is that row's, whatever a file written before it says; the same goes
// for a tmux session, whose name tmux.QualifiedSessionName derives from (repo group, title)
// and nothing else, so it is exactly as reusable as the branch.
//
// Started(), because an unstarted row owns nothing yet — it has no branch and no tmux session
// — so counting one would suppress a genuine orphan on the strength of a row that has not
// been built.
func (m *home) liveSessionOwns(d outbox.Disclosure) bool {
	for _, inst := range m.list.GetInstances() {
		if inst == nil || !inst.Started() {
			continue
		}
		if d.Branch != "" && inst.Branch == d.Branch && inst.Path == d.Repo {
			return true
		}
		if d.TmuxName != "" && inst.TmuxSessionName() == d.TmuxName {
			return true
		}
	}
	return false
}

// createDisclosureReport is that modal's text, bounded on both axes for
// repoScriptProblemsReport's reason. held is what the cap left for the next pass, which the
// report counts rather than names.
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
// instead, where the sentence and the command get a line each and both fit reportNarrowWidth,
// and only when some entry actually has a tmux session to name — including one only the count
// mentions, because the trailer is what makes that entry actionable when its turn comes.
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
func createDisclosureReport(shown []outbox.Disclosure, held []outbox.DisclosureEntry, now time.Time) string {
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
		lines = append(lines, fmt.Sprintf("    … and %d more, shown after this one", len(held)))
	}
	lines = append(lines, "")
	lines = append(lines, createDisclosureTrailer(config.RuntimeName(), anyTmuxName(shown, held))...)
	return strings.Join(lines, "\n")
}

// createDisclosureTrailer is the report's fixed prose: what the reader is being told about the
// entries above, and — when any of them names a tmux session — the socket to reach it on.
//
// A function of the socket rather than lines inlined at the call site, because these are the
// lines in the report that must not WRAP, and nothing in the Go suite can see a wrap. The
// entry rows above may: a wrapped path is still a path, and clipping them at reportNarrowWidth
// instead would cut paths a user has to retype. These cannot — a command arriving with the
// modal's border through the middle of it is pasteable in neither half — so they are built
// somewhere a test can measure every one of them, at every socket name an install can have.
//
// The socket comes from config.RuntimeName rather than hardcoded: a legacy install is on
// "claudesquad", and `tmux -L atrium` there finds nothing (CLAUDE.md). It is also why the
// sentence and the command get a line each: as one line they are over reportNarrowWidth for
// either socket name, and the test measures both.
//
// tmuxNamed rather than reading the entries again, and it counts the ones the cap held back as
// well as the ones on screen: the socket is unguessable and nothing else supplies it, so a
// report whose five shown entries are branch-only refusals must still carry the line for the
// live agents behind them (see anyTmuxName).
func createDisclosureTrailer(socket string, tmuxNamed bool) []string {
	lines := []string{
		"Nothing in atrium's records points at these. Read each `why`",
		"before acting: a branch may be one to resume a session on, and",
		"a worktree may be one you made.",
	}
	if !tmuxNamed {
		return lines
	}
	return append(lines,
		fmt.Sprintf("Those tmux sessions are on socket %q. To kill one:", socket),
		fmt.Sprintf("    tmux -L %s kill-session -t <name>", socket))
}

// anyTmuxName reports whether any entry names a tmux session, counting the ones the cap held
// back as well as the ones on screen.
//
// Both, because the trailer supplies the socket and nothing else does: on a legacy install it
// is "claudesquad" and unguessable. Computed over the shown entries alone — which it was — a
// report whose five oldest entries are branch-only refusals drops the socket line while the
// two it held back are the live agents whose only remedy is that command.
//
// Two named parameters rather than a variadic of slices, which is the shape this had: a
// variadic makes anyTmuxName(shown) a legal call, and forgetting the second argument is the
// exact bug the function exists to prevent. Two required parameters make the compiler hold it.
func anyTmuxName(shown []outbox.Disclosure, held []outbox.DisclosureEntry) bool {
	for _, d := range shown {
		if d.TmuxName != "" {
			return true
		}
	}
	for _, e := range held {
		if e.Disclosure.TmuxName != "" {
			return true
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
// one exists to drop. It is answering a different question — how long a tmux server has been
// up, where the minutes matter to whoever is deciding whether it is still in use.
//
// The two also disagree about where days begin, on purpose and visibly: HumanAge switches at
// 24h, this at 48h, so a 36-hour orphan reads "36h ago" here and "1d12h" in `atrium doctor`.
// A single tier is what the disagreement costs, and it buys the case that matters more — one
// number for "yesterday evening", where "1d12h" makes the reader do the arithmetic to find
// out whether an agent could still be running.
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
