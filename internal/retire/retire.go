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
// There are two decisions here, not one, because a session can be unretirable for
// two unrelated kinds of reason. Admits covers what its lifecycle says — a direct
// session has no worktree, a parked one's is already gone, a starting one's is still
// being built — and Gate covers what its tree says. Both are shared, and the second
// one was not at first: the command screened the lifecycle states before spooling and
// the drain re-checked only the tree, so a session parked in between was torn down by
// the same code whose front door refuses it.
//
// The refusal is not a formality. `atrium kill` deletes a branch, so the posture
// is that safety must be ESTABLISHED rather than merely un-contradicted — a
// distinction that matters because the fields this reads have no way to say "I
// don't know". git.DiffStats.Dirty is a plain bool and Unpushed a plain int, so a
// DiffStats whose computation failed is indistinguishable, field by field, from
// one describing a genuinely clean tree. Two fields tell them apart: Error, for the
// failures that stop computation starting, and BranchStatsMeasured, for the git
// commands that ran and failed — which Error does not carry, because the poll wants
// those swallowed. Both are consulted before anything else, and a nil DiffStats
// refuses rather than clearing.
//
// Every zero value in this package refuses, for the same reason. An undecided Verdict
// and an unnamed Verb are things a caller can produce without meaning to, and the one
// direction in which that must not resolve is "go ahead".
package retire

import (
	"fmt"

	"github.com/ZviBaratz/atrium/session/git"
)

// unrecognizedReason is what Reason falls back to for a Condition it has no wording for.
// A named constant because it is the one answer a refusal must never actually give: it
// tells an agent nothing it can act on. Reason returns it rather than "" — which would
// read as cleared — and the enum walk in the tests is what keeps it unreachable.
const unrecognizedReason = "it failed an unrecognized safety check"

// Condition is why a session may not be retired, or Clear when it may.
type Condition int

const (
	// Unknown is the zero value, and it is a refusal.
	//
	// Clear deliberately does not sit here. Verdict and Condition are exported and two
	// packages in two module trees construct them, so Gate and Admits are not
	// boundaries anything outside can be held to — a struct field left unset, a map
	// miss, append's zero fill, a `return Verdict{}, err` whose error is later folded
	// away. Every one of those produces a zero Verdict, and if Clear were iota's 0
	// every one of them would clear a teardown. Costing one constant to make the
	// undecided verdict refuse removes the whole class.
	Unknown Condition = iota
	// Clear means nothing stands in the way of retiring this session.
	Clear
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
	// Direct means the session is a direct (non-git) one, running in the user's own
	// checkout with no worktree or branch of its own. Neither verb has anything to act
	// on, and a kill has nothing to read either.
	Direct
	// Parked means a kill was asked for a session that is already paused. Its worktree
	// is gone, so what the kill would discard cannot be established.
	Parked
	// AlreadyParked means a pause was asked for a session that is already paused.
	// Separate from Parked because the two verbs are refused for opposite reasons —
	// one because nothing can be measured, one because nothing is left to do — and the
	// caller acts on the difference.
	AlreadyParked
	// Starting means the session's Start is still in flight. Its worktree is being
	// populated and its tmux session created, so a teardown would race the setup it is
	// tearing down.
	Starting
	// conditionEnd is one past the last condition, and it is not a condition. It exists
	// so a table test can walk the whole enum and prove it covered every member, rather
	// than however many somebody remembered to list — which is what let a condition ship
	// with no wording of its own. Anything added above it is covered automatically;
	// anything added below it is not, so add above.
	conditionEnd
)

// Verb is which retirement is being asked for. The two differ in what they destroy,
// so a rule that refuses a state has to know which one is asking.
type Verb int

const (
	// VerbUnknown is the zero value and names no verb, so a rule asked about it
	// refuses. Same polarity, and same reason, as Condition's Unknown.
	VerbUnknown Verb = iota
	// Kill deletes the branch and removes the worktree.
	Kill
	// Pause removes the worktree and keeps the branch.
	Pause
)

// State is what a session's lifecycle says about whether a verb can act on it at all,
// independent of anything in its tree.
//
// Three plain booleans rather than a status enum, because the two callers hold the
// status in different shapes — the command reads a decoded session.InstanceData, the
// drain reads a live Instance — and translating each into these three at the call site
// is the narrowest thing they can agree on.
type State struct {
	// Direct is a session running in the user's own checkout, with no worktree.
	Direct bool
	// Paused is a session already parked: agent stopped, worktree removed, branch kept.
	Paused bool
	// Loading is a session whose Start has not finished.
	Loading bool
}

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

// Transient reports whether a refusal describes a MOMENT rather than the session, so a
// caller holding a durable request should wait rather than answer it.
//
// Two conditions qualify and the rest do not, and the split is about who can change
// the answer. A session that is Starting finishes starting; a tree whose numbers could
// not be taken is re-measured by the next poll. Neither needs anybody to do anything,
// and both are states a session passes through on an ordinary launch — the drain's walk
// can land on either while a row is still coming online. Uncommitted, Unpushed and Busy
// are refusals a PERSON clears (push the branch, wait for the turn), and they are the
// answer the request deserves. Direct, Parked and AlreadyParked describe the session
// itself and never clear on their own.
//
// Only a caller that can afford to wait should read this: for a durable spool record,
// holding costs a tick and answering costs the request. For the command, which has
// nobody to hold for, a transient refusal is still a refusal.
func (v Verdict) Transient() bool {
	switch v.Condition {
	case Starting, Unestablished:
		return true
	default:
		return false
	}
}

// Reason states the refusal as a clause that completes "refusing to retire
// <session>: …", or "" for a cleared verdict.
func (v Verdict) Reason() string {
	switch v.Condition {
	case Clear:
		return ""
	case Unknown:
		return "nothing decided whether it was safe to retire, which is not the same " +
			"as deciding that it was"
	case Direct:
		return "it is a direct (non-git) session, so it has no worktree or branch to " +
			"retire"
	case Parked:
		return "it is paused, so its worktree is gone and what a teardown would " +
			"discard cannot be established — resume it, or retire it from the TUI"
	case AlreadyParked:
		return "it is already paused"
	case Starting:
		return "it is still starting up, so a teardown would race the setup it is " +
			"tearing down"
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
		return unrecognizedReason
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
	// Three ways to have no trustworthy numbers, and only the first two are obvious.
	// BranchStatsMeasured is the third: git.RepoStats sets Error for one cause (an
	// unset base commit) and swallows every subprocess failure into a zero value, so a
	// tree whose `git status` never ran arrives here reporting no error, no changes and
	// nothing unpushed. That field is the only thing that separates it from a tree that
	// was measured and found clean.
	if stats == nil || stats.Error != nil || !stats.BranchStatsMeasured {
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

// Admits decides whether verb can act on a session in st at all, before anything
// about its tree is measured.
//
// This is the rule the tree gate cannot express, and it is shared for the reason the
// tree gate is: it has two callers on two sides of a spool, and the first
// implementation put it on only one of them. `atrium kill` screened these states
// before spooling, but the drain re-ran only Gate — so a session parked or started in
// the window between the two was torn down anyway, by the very code path whose command
// refuses it outright. Both sides now ask here.
//
// Direct is reported ahead of the other two because it is the state no verb can ever
// act on, whatever else is true: the others describe a moment, this one describes the
// session.
func Admits(verb Verb, st State) Verdict {
	if verb != Kill && verb != Pause {
		return Verdict{Condition: Unknown}
	}
	if st.Direct {
		return Verdict{Condition: Direct}
	}
	if st.Paused {
		if verb == Pause {
			return Verdict{Condition: AlreadyParked}
		}
		return Verdict{Condition: Parked}
	}
	if st.Loading {
		return Verdict{Condition: Starting}
	}
	return Verdict{Condition: Clear}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
