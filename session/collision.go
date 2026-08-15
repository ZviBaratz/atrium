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
// the `_term` twin of runSessionSuffix. Exported because the shell is minted in ui, which
// is a different package; reserved below so no agent session can claim it.
//
// It is a const rather than a literal at each site because the two production spellings
// used to be a bare "_term" here and another in ui, coupled only by a prose
// cross-reference — a value in two homes with nothing to fail when they diverge.
const TermSessionSuffix = "_term"

// reservedTmuxSuffixes are the sibling tmux sessions Atrium mints from a session's own
// name: the terminal tab's shell (Instance.MintTerminalSessionName, hosted by
// ui/terminal.go) and the run command's host (session/runcmd.go). Each is
// `<tmux name><suffix>`, so a session TITLE that sanitizes to another session's sibling
// name would have two owners on one socket.
var reservedTmuxSuffixes = []string{TermSessionSuffix, runSessionSuffix}

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

// OwnedSiblingCollides reports whether cand would land on a sibling session inst already
// OWNS — its terminal shell or its run-command host.
//
// It is the half DerivedTmuxNameCollides structurally cannot see. That guard derives both
// siblings from inst's CURRENT tmux name, but neither sibling's name is derived: each is
// minted once and then owned (Instance.termName, Instance.runName), so a deep rename moves
// the agent's name and leaves the siblings on their old ones. From that moment the old
// title is free, and a new session claiming it mints exactly the names the renamed session
// is still holding.
//
// Both directions, for the same reason DerivedTmuxNameCollides needs both. `cand == owned`
// is a candidate whose own agent session would sit on the sibling (a title of "web.term"
// sanitizes onto session "web"'s shell). `cand+suffix == owned` is the commoner one after a
// rename: the candidate's own shell or server would be minted onto the sibling, so two
// sessions would host one tmux session and either teardown would take the other's with it.
func OwnedSiblingCollides(cand string, inst *Instance) bool {
	if cand == "" || inst == nil {
		return false
	}
	for _, sib := range []struct{ owned, suffix string }{
		{inst.TerminalSessionName(), TermSessionSuffix},
		{inst.RunSessionName(), runSessionSuffix},
	} {
		if sib.owned == "" {
			continue
		}
		if cand == sib.owned || cand+sib.suffix == sib.owned {
			return true
		}
	}
	return false
}
