package app

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"
)

// pendingFork is a fork armed on the checkpoint timeline and not yet submitted.
// It lives on the model between `f` and the create form's submit, because the
// form is the ordinary one — the fork is what the submit does differently, not
// what the form looks like.
type pendingFork struct {
	// sourceTitle is the forking session's immutable Title, the stem the new
	// session's title is derived from. Not its display name, which is cosmetic and
	// may not be a usable branch stem.
	sourceTitle string
	// sourceTranscript, cutEntryID and droppedMessageID are the seed proper; see
	// session.ForkSeed, which they are copied into at spawn.
	sourceTranscript string
	cutEntryID       string
	droppedMessageID string
	// at stamps the chosen checkpoint into the form's heading, so a mis-aimed cursor
	// is visible before a worktree is built rather than after.
	at time.Time
	// sourceBranch is the forking session's own branch, which the new worktree
	// should branch from: the conversation being forked is ABOUT that branch's code,
	// so branching off the repo's default would hand the fork a transcript that
	// discusses files its worktree does not contain (#657). Empty for a direct
	// session, or when the branch no longer exists locally.
	sourceBranch string
}

// forkTitleSuffix is appended to the source's title to derive the fork's stem.
const forkTitleSuffix = "-fork"

// firstFreeTitle returns the first conflict-free title from stem: the bare stem,
// then stem-2, stem-3, and so on. "" when the scan is exhausted.
//
// It is planVariantTitles' suffix search with a batch of one, which that function
// cannot express — its total<=1 branch is the pre-#387 contract and returns the
// bare stem or a conflict, never a suffixed alternative. The conflict predicate is
// the shared one, so a fork title is held to the same bar as a variant's: an
// orphan branch left by a killed session disqualifies a name here too, rather than
// failing a background Start later.
//
// Numbering starts at 2 because the bare stem is the 1: `x-fork`, `x-fork-2`.
func (m *home) firstFreeTitle(stem, path string, direct bool) string {
	if m.variantTitleConflict(stem, path, direct) == "" {
		return stem
	}
	for n := 2; n <= variantTitleScan; n++ {
		cand := fmt.Sprintf("%s-%d", stem, n)
		if m.variantTitleConflict(cand, path, direct) == "" {
			return cand
		}
	}
	return ""
}

// openForkForm opens the ordinary create form, seeded for the armed fork: the
// source's project, a free title derived from its own, and a heading naming the
// checkpoint.
//
// The form is deliberately not a special one. Everything a new session needs to
// decide — program, base branch, account, the first prompt — is decided the same
// way here as anywhere else, and routing through openCreateFormSeeded means the
// cap gates, the account routing and the title checks all apply without a second
// implementation of any of them.
func (m *home) openForkForm(path string, pf *pendingFork) (tea.Model, tea.Cmd) {
	if pf == nil {
		return m, nil
	}
	title := m.firstFreeTitle(pf.sourceTitle+forkTitleSuffix, path, false)
	if title == "" {
		return m, m.handleError(fmt.Errorf("no free session name derived from %q — rename or clean up the existing forks", pf.sourceTitle))
	}

	// Armed AFTER the open, never before: openCreateFormSeeded disarms whatever was
	// armed, so that every other route into this form — a bare n, smart dispatch, a
	// restored crash draft — cannot inherit a fork the user walked away from and
	// spawn one from a form that says nothing about it.
	cmd := m.openCreateFormSeeded(path, false, &PrefillResult{Path: path, Title: title})
	m.pendingFork = pf
	if m.textInputOverlay != nil {
		// The heading is the only thing that says this submit will fork. It carries the
		// checkpoint's own timestamp, in the timeline's format, so it can be read
		// against the row the cursor was on — the form is otherwise indistinguishable
		// from a plain create, and a fork seeded from the wrong checkpoint is not
		// visible again until the session is running.
		m.textInputOverlay.Title = "Fork from checkpoint · " + pf.at.Format(overlay.CheckpointTimeFormat)
		// The prompt field defaults to saying it is optional, which is true of every
		// other route into this form and false here: the print run that materializes
		// the fork will not run without one, so the submit refuses an empty value. A
		// field that says "Optional" while the submit refuses it is the form
		// contradicting itself at the moment the user decides whether to type.
		m.textInputOverlay.SetPromptPlaceholder(overlay.PromptPlaceholderFork)

		// Point the base at the conversation's own branch. The picker stays free —
		// this is a default, not a pin — and the seeded filter both guarantees the
		// branch is in the capped result set and shows why that base is selected.
		//
		// The initial branch search openCreateFormSeeded queued ran under the previous
		// filter version, so it is now stale and will be rejected on arrival; this
		// reissues it under the version PreferBranch returns. Without the reissue the
		// picker would sit empty, since nothing else fires a search until a keystroke.
		if pf.sourceBranch != "" {
			version := m.textInputOverlay.PreferBranch(pf.sourceBranch)
			cmd = tea.Batch(cmd, m.runBranchSearch(pf.sourceBranch, version))
		}
	}
	return m, cmd
}

// stashPendingFork parks an armed fork beside the create-form draft it belongs
// to, and restorePendingFork brings it back with that draft.
//
// The two move together because the draft carries the fork's heading: a restored
// form still says "Fork from checkpoint", so a restore that did not re-arm would
// leave a form claiming a fork and quietly performing a plain create. Parking it
// rather than dropping it is what makes escaping a half-typed fork non-destructive,
// the same promise the draft itself makes.
func (m *home) stashPendingFork() {
	m.stashedFork = m.pendingFork
	m.pendingFork = nil
}

func (m *home) restorePendingFork() {
	m.pendingFork = m.stashedFork
	m.stashedFork = nil
}

// forkSeedForSpawn mints the session id and returns the seed to hand the new
// instance, or nil when no fork is armed.
//
// The id is generated here, at submit, rather than when `f` was pressed: claude
// refuses a --session-id already in use, and an armed fork the user abandoned and
// re-armed would otherwise carry the same one twice.
func (m *home) forkSeedForSpawn() (*session.ForkSeed, error) {
	pf := m.pendingFork
	if pf == nil {
		return nil, nil
	}
	id, err := session.NewSessionID()
	if err != nil {
		return nil, err
	}
	return &session.ForkSeed{
		SourceTranscript: pf.sourceTranscript,
		CutEntryID:       pf.cutEntryID,
		DroppedMessageID: pf.droppedMessageID,
		NewSessionID:     id,
	}, nil
}
