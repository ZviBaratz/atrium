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

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
)

// createDisclosuresShown caps how many spent requests one report enumerates, for
// customCommandProblemsShown's reason: the count has no ceiling this side of the spool, and
// no field in an entry is length-bounded — a title refused FOR being over-long is one of the
// things that gets disclosed, and a repo path and a worktree path have no limit at all.
const createDisclosuresShown = 5

// loadCreateDisclosures reads the disclosures an earlier process left, for newHome to
// buffer.
//
// It runs after reconcileCreateClaims so a refusal that reconcile just reached is in this
// launch's report rather than the next one's. Undecodable files are dropped here rather
// than carried: nothing downstream can act on a disclosure, so one nobody can read has no
// reader to preserve it for — unlike a spool record, where the same file means a caller is
// owed a receipt.
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
			if err := outbox.ClearDisclosure(e.Path); err != nil {
				log.ErrorLog.Printf("failed to clear an unreadable create disclosure: %v", err)
			}
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// discloseCreateLeftovers records what a spent create request left behind and queues it for
// this process's own report.
//
// Buffered even when the write fails. The file's job is to survive a crash; the buffer's is
// to reach the person at the terminal, and a full disk is no reason to withhold the one
// mention of an orphaned branch from the only party who can delete it.
func (m *home) discloseCreateLeftovers(record string, d outbox.Disclosure) {
	if err := outbox.Disclose(record, d); err != nil {
		log.ErrorLog.Printf("failed to record what the create request for %q left behind: %v",
			d.Title, err)
	}
	log.WarningLog.Printf("create request for %q left artifacts belonging to no session "+
		"(branch %q, worktree %q, tmux %q): %s", d.Title, d.Branch, d.Worktree, d.TmuxName, d.Reason)
	m.pendingCreateDisclosures = append(m.pendingCreateDisclosures,
		outbox.DisclosureEntry{Path: record, Disclosure: d})
}

// flushCreateDisclosures opens the report for the spent create requests that left something
// behind, once there is a frame to show it on and no overlay owns the screen — the shape
// flushRepoScriptProblems uses, including the deferral: showInfo switches to stateInfo as it
// builds, so a second report in the same tick waits for the next one instead of clobbering
// this one.
//
// The buffer is cleared as it fires so the 100ms preview tick cannot reopen it forever, and
// the files are unlinked HERE rather than at the read: a quit inside the window before the
// first tick leaves the explanation on disk for the next launch instead of erasing it, which
// is flushDeferredRecovery's rule and the whole failure this path exists for.
//
// Entries with nothing to name are dropped without a report. Every refusal writes a
// disclosure, because the mark is what keeps a claim from being re-judged
// (applyCreateClaim), and most refusals happen before anything durable was built — a modal
// saying "a request failed, and left nothing" would be noise the receipt already covered.
func (m *home) flushCreateDisclosures() tea.Cmd {
	if len(m.pendingCreateDisclosures) == 0 || m.state != stateDefault {
		return nil
	}
	entries := m.pendingCreateDisclosures
	m.pendingCreateDisclosures = nil
	var withLeftovers []outbox.Disclosure
	for _, e := range entries {
		if err := outbox.ClearDisclosure(e.Path); err != nil {
			// Logged, not surfaced: the notice the user needs is about to be on screen. A
			// persistent failure repeats this report on every later launch until the TTL
			// horizon sweeps the file, and the thing it repeats is still true of artifacts
			// still stranded — so no poisoning set, for flushDeferredRecovery's reason.
			log.ErrorLog.Printf("failed to clear a delivered create disclosure: %v", err)
		}
		if e.Disclosure.Leftovers() {
			withLeftovers = append(withLeftovers, e.Disclosure)
		}
	}
	if len(withLeftovers) == 0 {
		return nil
	}
	return m.showInfo(createDisclosureReport(withLeftovers))
}

// createDisclosureReport is that modal's text, bounded on both axes for
// repoScriptProblemsReport's reason.
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
// instead, on a line short enough to survive, and only when some entry actually has a tmux
// session to name.
//
// The trailer's first sentence is the fact that makes the list actionable rather than
// alarming: a branch Atrium still owns is not the user's to delete.
func createDisclosureReport(ds []outbox.Disclosure) string {
	if len(ds) == 0 {
		return ""
	}
	head := "an interrupted `atrium new` left artifacts behind:"
	if len(ds) > 1 {
		head = fmt.Sprintf("%d interrupted `atrium new` requests left artifacts behind:", len(ds))
	}
	lines := []string{head}
	shown := ds
	if len(shown) > createDisclosuresShown {
		shown = shown[:createDisclosuresShown]
	}
	var anyTmux bool
	for _, d := range shown {
		row := func(label, value string) {
			lines = append(lines, clipReportLine(fmt.Sprintf("    %-8s %s", label, value)))
		}
		lines = append(lines, "", clipReportLine(fmt.Sprintf("%q in %s", d.Title, d.Repo)))
		// The reason gets its own budget, wider than a name or a path row's, because it is
		// the one value that comes from somewhere else — a git or filesystem error, whose
		// useful half is usually at the END ("…: no space left on device"). At a path row's
		// budget the cause is what gets cut, which leaves the report saying a create failed
		// without saying why.
		lines = append(lines, clipTo(fmt.Sprintf("    %-8s %s", "why", d.Reason), 200))
		if d.Branch != "" {
			row("branch", d.Branch)
		}
		if d.Worktree != "" {
			row("worktree", d.Worktree)
		}
		if d.TmuxName != "" {
			row("tmux", d.TmuxName)
			anyTmux = true
		}
	}
	if len(ds) > len(shown) {
		lines = append(lines, fmt.Sprintf("    … and %d more", len(ds)-len(shown)))
	}
	lines = append(lines, "",
		"Nothing in atrium's records points at these, so nothing here will clean them up.",
		"Remove them by hand, or create a session on the branch yourself.")
	if anyTmux {
		// The socket from config.RuntimeName rather than hardcoded: a legacy install is on
		// "claudesquad", and `tmux -L atrium` there finds nothing (CLAUDE.md).
		lines = append(lines, fmt.Sprintf("Sessions above are on socket %q: tmux -L %s kill-session -t <name>",
			config.RuntimeName(), config.RuntimeName()))
	}
	return strings.Join(lines, "\n")
}
