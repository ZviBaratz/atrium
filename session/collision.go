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

// reservedTmuxSuffixes are the sibling tmux sessions Atrium derives from a session's own
// name: the terminal tab's shell (ui/terminal.go) and the run command's host
// (session/runcmd.go). Each is `<tmux name><suffix>`, so a session TITLE that sanitizes
// to another session's derived name would have two owners on one socket.
var reservedTmuxSuffixes = []string{"_term", runSessionSuffix}

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
