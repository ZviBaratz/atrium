package session

import (
	"fmt"
	"strings"

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

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
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
