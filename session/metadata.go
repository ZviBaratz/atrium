package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/git"
)

// Derived git metadata: the diff-stats and pull-request snapshots the metadata
// poll computes off the main thread (Compute*) and the main-thread-only cache
// the View reads (Set*/Get*). Both follow the same operability gate.

// UpdateDiffStats updates the git diff statistics for this instance. Like
// SetDiffStats it mutates the unguarded diffStats field, so it must be called from a
// single-threaded context per instance (the main event loop, or the daemon's
// single poll goroutine) — never concurrently with the View/poll readers.
func (i *Instance) UpdateDiffStats() error {
	if !i.isStarted() {
		i.diffStats = nil
		return nil
	}

	if i.Paused() {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	wt := i.worktree()
	if wt == nil {
		// Direct session: no worktree, so no diff to compute.
		i.diffStats = nil
		return nil
	}

	stats := wt.Diff()
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			return nil
		}
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	return nil
}

// ComputeDiff runs the expensive git diff I/O and returns the result without
// mutating instance state. Safe to call from a background goroutine.
func (i *Instance) ComputeDiff() *git.DiffStats {
	if !i.operableGitSession() {
		return nil
	}
	return i.worktree().Diff()
}

// ComputeDiffNumstat runs a lightweight git diff --numstat and returns only the
// added/removed line counts (Content is left empty). Safe to call from a
// background goroutine. Use this for instances whose full diff content is not
// currently needed so we avoid keeping large diffs in memory.
func (i *Instance) ComputeDiffNumstat() *git.DiffStats {
	if !i.operableGitSession() {
		return nil
	}
	return i.worktree().DiffNumstat()
}

// ComputeRepoStats runs only the branch-level half of the diff — Commits, Behind,
// Unpushed, Dirty — leaving the line counts and content zero. Safe to call from a
// background goroutine. Use this for an instance whose tree cannot have changed
// since the last full computation: it keeps every number a confirmation dialog
// reads fresh while skipping the untracked-file walk and `git diff`.
func (i *Instance) ComputeRepoStats() *git.DiffStats {
	if !i.operableGitSession() {
		return nil
	}
	return i.worktree().RepoStats()
}

// DiffContentAt reports when this instance's diff CONTENT (line counts and patch
// text) was last computed, or the zero time if it never has been. Read from the
// metadata poll's background goroutines, hence the lock — unlike diffStats itself,
// which only the main loop touches.
func (i *Instance) DiffContentAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.diffContentAt
}

// NoteDiffContentComputed stamps the content clock. Called on the main loop when a
// result carrying real content is applied, so the next poll can tell how stale the
// line counts are.
func (i *Instance) NoteDiffContentComputed(at time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.diffContentAt = at
}

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
}

// CarryDiffContent copies the line counts and patch text from the cached stats onto
// fresh repo-only stats, so a tick that deliberately skipped the diff leaves the
// row's +/- chip showing its last known value instead of blanking it to zero.
//
// Main-loop only, like SetDiffStats: it reads the cached stats directly. A no-op
// when nothing is cached — a session whose content was never computed has no
// number to preserve, and zeroes are the honest answer there.
func (i *Instance) CarryDiffContent(into *git.DiffStats) {
	if into == nil || i.diffStats == nil {
		return
	}
	into.Added = i.diffStats.Added
	into.Removed = i.diffStats.Removed
	into.FilesChanged = i.diffStats.FilesChanged
	into.Content = i.diffStats.Content
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// clearCachedDirty marks the cached diff stats as having no uncommitted changes.
// Called from pause(), which runs on the main event loop, so it shares the same
// "main loop only" contract as SetDiffStats. It is a no-op when no stats are
// cached yet (a never-polled session reads as clean anyway).
func (i *Instance) clearCachedDirty() {
	if i.diffStats != nil {
		i.diffStats.Dirty = false
	}
}

// noteAutoPauseCommit folds pause's auto-WIP commit into the cached commit count
// so the kill dialog warns about the now-committed work. The metadata poll skips
// paused instances, so without this the auto-commit stays invisible in cached and
// persisted stats until a Resume. Creates the stats if none are cached yet (a
// never-polled session about to be paused): the branch holds at least this commit.
// Main-loop only, like clearCachedDirty/SetDiffStats.
func (i *Instance) noteAutoPauseCommit() {
	if i.diffStats == nil {
		i.diffStats = &git.DiffStats{}
	}
	i.diffStats.Commits++
	// The WIP commit was just made locally and never pushed, so it is also at risk:
	// a paused session is never polled, making this the only chance to record that
	// before the kill dialog reads it.
	i.diffStats.Unpushed++
}

// noteAutoPauseUnwind reverses noteAutoPauseCommit's cached accounting when Resume
// soft-resets the n auto-WIP commits back into pending changes: drop them from the
// cached ahead-count and mark the tree dirty again. Without this the pause-bumped
// count drifts above the real ahead-count after a resume — durably (and persisted)
// for a session re-paused before the next poll, since the poll skips paused
// instances. No-op when no stats are cached. Main-loop only, like noteAutoPauseCommit.
func (i *Instance) noteAutoPauseUnwind(n int) {
	if i.diffStats == nil || n <= 0 {
		return
	}
	i.diffStats.Commits = max(0, i.diffStats.Commits-n)
	// The unwound commits were the unpushed ones noteAutoPauseCommit counted, so drop
	// them from the at-risk count too; their content lives on as pending changes.
	i.diffStats.Unpushed = max(0, i.diffStats.Unpushed-n)
	i.diffStats.Dirty = true
}

// ComputePRStatus fetches the session branch's pull-request status off the main
// thread (it may shell out to gh over the network). Returns nil for sessions
// that cannot have a PR — not started, paused, or direct (no worktree/branch).
// selected requests the eager cache TTL for the focused session.
func (i *Instance) ComputePRStatus(selected bool) *git.PRStatus {
	if !i.operableGitSession() {
		return nil
	}
	s := i.worktree().PRStatus(i.baseContext(), selected)
	return &s
}

// SetPRStatus sets the PR status on the instance. Should be called from the main
// event loop to avoid data races with View.
func (i *Instance) SetPRStatus(s *git.PRStatus) {
	i.prStatus = s
}

// GetPRStatus returns the current pull-request snapshot (nil until first fetched).
func (i *Instance) GetPRStatus() *git.PRStatus {
	return i.prStatus
}
