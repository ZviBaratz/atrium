package session

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// DerivedNamesCollide reports whether two session titles in the same repo group
// would collide at any derived-name layer: the (qualified) tmux session name —
// whose per-title segment strips whitespace and maps dots to underscores — or
// the git branch slug, which lowercases and dashes spaces. Comparing raw titles
// would miss these ("Fix Bug" vs "fixbug" are distinct titles, one tmux
// segment). The tmux comparison is case-insensitive on purpose: tmux itself is
// case-sensitive, but two sessions distinguishable only by case in one group
// would be confusing, so they are conservatively treated as duplicates.
func DerivedNamesCollide(branchPrefix, a, b string) bool {
	return strings.EqualFold(tmux.SanitizeNameSegment(a), tmux.SanitizeNameSegment(b)) ||
		git.BranchNameForSession(branchPrefix, a) == git.BranchNameForSession(branchPrefix, b)
}

// TermSessionSuffix is what turns an agent session's tmux name into its terminal shell's,
// the `_term` twin of RunSessionSuffix. Reserved below so no agent session can claim it.
//
// It is a const rather than a literal at each site because the two production spellings
// used to be a bare "_term" here and another in ui, coupled only by a prose
// cross-reference — a value in two homes with nothing to fail when they diverge. Both are
// exported for the same reason, and it is no longer the mint: since #708 both siblings are
// minted in this package. It is the guards' FIXTURES, which live in app and have to spell a
// held sibling's name to build one.
const TermSessionSuffix = "_term"

// reservedTmuxSuffixes are the sibling tmux sessions Atrium mints from a session's own
// name: the terminal tab's shell (Instance.MintTerminalSessionName, hosted by
// ui/terminal.go) and the run command's host (session/runcmd.go). Each is
// `<tmux name><suffix>`, so a session TITLE that sanitizes to another session's sibling
// name would have two owners on one socket.
var reservedTmuxSuffixes = []string{TermSessionSuffix, RunSessionSuffix}

// DerivedTmuxNameCollides reports whether a candidate qualified tmux name can coexist
// with an existing session's: they must differ, and neither may be the other's derived
// sibling.
//
// Both directions matter, and the second is the one that is easy to miss.
// QualifiedSessionName maps a dot to an underscore, so a session titled "web.run" mints
// the exact name session "web" hosts its dev server under — the candidate is the
// SIBLING here, not the parent. A guard that only checked cand+suffix == name would let
// that through, and the loser is a session whose dev server is killed by an unrelated
// teardown.
func DerivedTmuxNameCollides(cand, name string) bool {
	if name == "" {
		return false
	}
	if cand == name {
		return true
	}
	for _, suffix := range reservedTmuxSuffixes {
		if cand == name+suffix || cand+suffix == name {
			return true
		}
	}
	return false
}

// OwnsSiblingNamed reports whether inst already hosts a sibling — its terminal shell or its
// run-command host — on exactly cand.
//
// This is the half that is a conflict for EVERY session including the candidate's own: an
// agent session renamed onto one of these names would share a tmux session with the shell
// or server underneath it. A title of "web.term" sanitizes onto session "web"'s own shell.
func OwnsSiblingNamed(cand string, inst *Instance) bool {
	if cand == "" || inst == nil {
		return false
	}
	for _, owned := range []string{inst.TerminalSessionName(), inst.RunSessionName()} {
		if owned != "" && cand == owned {
			return true
		}
	}
	return false
}

// OwnedSiblingCollides reports whether a session named cand can coexist with the siblings
// inst already OWNS. It is for an instance OTHER than the one being named: see
// OwnsSiblingNamed for the self case, and the second paragraph below for why the difference
// is not a nicety.
//
// It is the half DerivedTmuxNameCollides structurally cannot see. That guard derives both
// siblings from inst's CURRENT tmux name, but neither sibling's name is derived: each is
// minted once and then owned (Instance.termName, Instance.runName), so a deep rename moves
// the agent's name and leaves the siblings on their old ones. From that moment the old
// title is free, and a new session claiming it mints exactly the names the renamed session
// is still holding.
//
// The `cand+suffix == owned` arm is what that costs, and it is the arm that must not be
// asked about the session being renamed. Against another session it is the commoner hazard:
// the candidate's own shell or server would be minted onto a sibling someone else hosts, so
// two sessions would share one tmux session and either teardown would take the other's with
// it. Against ITSELF the same equation is the consistent case, not a collision — a session
// whose shell is `<g>_web_term` matches it for the candidate `<g>_web`, which is the name it
// already has. Asking it here refused every no-op rename by a session with an open terminal
// tab, and every round trip back to a title the session used to hold.
func OwnedSiblingCollides(cand string, inst *Instance) bool {
	if cand == "" || inst == nil {
		return false
	}
	if OwnsSiblingNamed(cand, inst) {
		return true
	}
	for _, sib := range []struct{ owned, suffix string }{
		{inst.TerminalSessionName(), TermSessionSuffix},
		{inst.RunSessionName(), RunSessionSuffix},
	} {
		if sib.owned != "" && cand+sib.suffix == sib.owned {
			return true
		}
	}
	return false
}
