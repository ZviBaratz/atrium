package repocfg

import (
	"encoding/json"
	"fmt"

	"github.com/ZviBaratz/atrium/config"
)

// RepoLocalFileName is the well-known basename of a repository's own Atrium
// config: <repo root>/.atrium.json. Reading it is gated by the per-repo trust
// ledger (internal/repotrust, #814) — the file is repo-authored content, and
// its repo_scripts execute.
const RepoLocalFileName = ".atrium.json"

// MaxRepoLocalBytes caps how much of a .atrium.json any reader takes. The cap
// is a denial-of-service guard, not a schema rule: enforcement hashes and
// parses this file on session-creation and poll paths, and a repo can commit a
// file of any size. A real config is a few hundred bytes; a megabyte is three
// orders of magnitude of headroom, and anything past it is refused whole (inert
// with a notice), never truncated — a truncated parse could silently drop
// entries, and a truncated hash would grant different bytes than were shown.
const MaxRepoLocalBytes = 1 << 20

// RepoLocal is a parsed .atrium.json: the repo_scripts entries that survived
// the structural rules, and the ones that did not. Both lists can be non-empty
// at once — a broken sibling entry must not hide a good one, Validate's rule.
type RepoLocal struct {
	Entries  []RepoLocalEntry
	Problems []Problem
}

// RepoLocalEntry is one surviving entry plus its position in the FILE's list.
// The index is carried because the structural pass filters entries out, so a
// survivor's slice position can differ from where the user will find it in the
// file — and `repo_scripts[N]` in a message is only useful if N is the file's N.
type RepoLocalEntry struct {
	Index int
	config.RepoScript
}

// repoLocalWire is the file's schema. Only repo_scripts is read today; unknown
// top-level keys are deliberately tolerated so the #815 additions
// (carry_files, link_paths) can land in repos before every user has upgraded —
// an older atrium ignores them rather than refusing the whole file.
type repoLocalWire struct {
	RepoScripts []config.RepoScript `json:"repo_scripts"`
}

// ParseRepoLocal decodes a .atrium.json's bytes. Pure: no filesystem, no exec,
// and — load-bearing — NO template compilation. Callers parse before the trust
// verdict is known (the create-time prompt needs to say what the file
// declares), so nothing here may feed repo-authored strings to the template
// engine; that happens in ValidateOne, which the resolution site calls only
// after the ledger has granted this exact content.
//
// The error is for bytes that are not a config at all (undecodable JSON);
// per-entry refusals come back as Problems beside the entries that survived.
// Problem.Error() prints `repo_scripts[N]`, which is the entry's position in
// THIS file — display sites prefix the filename so a user with both a global
// and a repo-local list can tell which one is being named.
func ParseRepoLocal(raw []byte) (RepoLocal, error) {
	var wire repoLocalWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return RepoLocal{}, fmt.Errorf("%s does not decode: %w", RepoLocalFileName, err)
	}

	var out RepoLocal
	for i, e := range wire.RepoScripts {
		// Routing matchers are refused in a repo-local entry: the file IS the repo,
		// so there is nothing to route. Tolerating them would invite the confusion
		// they exist to resolve — an entry whose matchers name a DIFFERENT repo
		// would still apply to this one — and #815's layering work needs the two
		// lists distinguishable by shape.
		if len(e.RemoteMatches) > 0 || len(e.PathMatches) > 0 {
			out.Problems = append(out.Problems, Problem{
				Index: i, Name: e.Name,
				Msg: "remote_matches/path_matches have no meaning in a repo-local file — the file already belongs to exactly one repo",
			})
			continue
		}
		out.Entries = append(out.Entries, RepoLocalEntry{Index: i, RepoScript: e})
	}
	return out, nil
}
