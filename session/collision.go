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
