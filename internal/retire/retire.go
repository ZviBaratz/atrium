// Package retire holds the one decision that stands between an agent-issued
// retirement and a teardown: given what a session's tree and pane actually say,
// may this session be retired?
//
// It is a pure function over inputs somebody else fetched, and that split is the
// whole design. Two callers need this decision and they reach the same numbers by
// different routes — the headless `kill`/`pause` commands recompute from git and
// tmux, while the TUI's drain already holds fresh figures from its poll — so a
// shared *fetcher* would have to be right for both and a shared *decision* only
// has to be right once. Nothing here forks a subprocess, reads a file or resolves
// a data dir, which is why both sides can call it and why every branch below is
// reachable from a table test.
//
// It lives in its own package because neither caller can import the other: the
// commands are in package main, the drain is in package app, and main imports app.
//
// The refusal is not a formality. `atrium kill` deletes a branch, so the posture
// is that safety must be ESTABLISHED rather than merely un-contradicted — a
// distinction that matters because the fields this reads have no way to say "I
// don't know". git.DiffStats.Dirty is a plain bool and Unpushed a plain int, so a
// DiffStats whose computation failed is indistinguishable, field by field, from
// one describing a genuinely clean tree. Error is the only thing that tells them
// apart, which is why it is consulted first and why a nil DiffStats refuses
// instead of clearing.
package retire

import (
	"fmt"

	"github.com/ZviBaratz/atrium/session/git"
)

// Condition is why a session may not be retired, or Clear when it may.
type Condition int

const (
	// Clear means nothing stands in the way of retiring this session.
	Clear Condition = iota
	// Unestablished means the tree's numbers could not be computed, so nothing
	// about what a teardown would destroy is known. Distinct from a clean tree, and
	// deliberately so — see the package doc.
	Unestablished
	// Uncommitted means the worktree holds changes no commit carries. A kill runs
	// `git worktree remove -f`, so these are the changes that go.
	Uncommitted
	// Unpushed means the branch holds commits no remote carries. A kill runs
	// `git branch -D`, so these are the commits only the undo ref would hold.
	Unpushed
	// Busy means the agent is mid-turn, or has background work still running.
	// Nothing is at risk that the two conditions above do not already cover — this
	// one is about not throwing away a turn in flight.
	Busy
)

// Verdict is one gate decision, and the reason for it.
type Verdict struct {
	Condition Condition
	// Commits is how many unpushed commits were counted, carried so a refusal can
	// name the number. The caller sees only the Verdict, never the DiffStats it
	// came from, so this is the one place the count survives.
	Commits int
}

// Allowed reports whether the session may be retired.
func (v Verdict) Allowed() bool { return v.Condition == Clear }

// Reason states the refusal as a clause that completes "refusing to retire
// <session>: …", or "" for a cleared verdict.
func (v Verdict) Reason() string {
	switch v.Condition {
	case Clear:
		return ""
	case Unestablished:
		return "its tree state could not be established, so what a teardown would " +
			"destroy is unknown"
	case Uncommitted:
		return "it has uncommitted changes"
	case Unpushed:
		if v.Commits <= 0 {
			return "it has unpushed commits"
		}
		return fmt.Sprintf("it has %d unpushed commit%s", v.Commits, plural(v.Commits))
	case Busy:
		return "its agent is still working"
	default:
		// An unnamed condition is a refusal that cannot explain itself, which is
		// worse than any wording. Say that rather than return "" and read as cleared.
		return "it failed an unrecognized safety check"
	}
}

// Gate decides whether a session carrying stats may be retired, with busy
// reporting whether its agent is mid-turn.
//
// The order of the checks is load-bearing twice over. Error comes first because a
// failed computation leaves every other field at a zero value that reads as safe,
// so any other ordering would clear a tree it never managed to look at. Busy comes
// last because a caller acts on the first reason it is given, and the two git
// conditions say work would be DESTROYED while busy only says the timing is
// wasteful — reporting the cheap one first would send an agent to wait out a turn
// when the real answer was that its worker had unpushed commits.
func Gate(stats *git.DiffStats, busy bool) Verdict {
	if stats == nil || stats.Error != nil {
		return Verdict{Condition: Unestablished}
	}
	if stats.Dirty {
		return Verdict{Condition: Uncommitted}
	}
	if stats.Unpushed > 0 {
		return Verdict{Condition: Unpushed, Commits: stats.Unpushed}
	}
	if busy {
		return Verdict{Condition: Busy}
	}
	return Verdict{Condition: Clear}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
