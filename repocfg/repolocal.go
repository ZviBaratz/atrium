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

// RepoLocal is a parsed .atrium.json: the repo_scripts entry that survived the
// structural rules (at most one — ParseRepoLocal's one-entry rule), or the
// refusal that kept it out. The two lists are mutually exclusive here, unlike
// Validate's over the global config: with one entry there is no sibling for a
// Problem to sit beside.
type RepoLocal struct {
	Entries  []RepoLocalEntry
	Problems []Problem
}

// RepoLocalEntry is one surviving entry plus its position in the FILE's list.
// With the one-entry rule the index is always 0 today; it is carried anyway so
// `repo_scripts[N]` in a Problem is the file's N by construction rather than by
// the current rule staying true — ValidateOne takes the index for exactly that
// message.
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
// A repo-local file carries AT MOST ONE repo_scripts entry, and the rule is a
// consent-integrity guard, not tidiness: matchers are refused here, so entry
// selection has no routing dimension and only ever picks the first — a second
// entry could never legitimately run, but a file whose first entry fails late
// validation while a second passes would show the user one script in the trust
// prompt and execute the other. Refusing the whole file (an error, like
// undecodable JSON — nothing prompts, nothing runs, the notice says why) makes
// "the entry the prompt described" and "the entry that runs" the same entry by
// construction. An entry that configures nothing is likewise refused here, with
// compile's own words, so a file that declares nothing runnable can never reach
// a trust prompt only to be refused after the grant.
//
// The error is for a file that is unusable as a whole (undecodable JSON, more
// than one entry); the single entry's per-entry refusals come back as Problems.
// Problem.Error() prints `repo_scripts[N]`, which is the entry's position in
// THIS file — display sites prefix the filename so a user with both a global
// and a repo-local list can tell which one is being named.
func ParseRepoLocal(raw []byte) (RepoLocal, error) {
	var wire repoLocalWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return RepoLocal{}, fmt.Errorf("%s does not decode: %w", RepoLocalFileName, err)
	}
	if len(wire.RepoScripts) > 1 {
		return RepoLocal{}, fmt.Errorf(
			"%s declares %d repo_scripts entries — a repo-local file carries exactly one (matchers have no meaning here, so a second entry could only mask the one that runs)",
			RepoLocalFileName, len(wire.RepoScripts))
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
		if len(DeclaredSurfaces(e)) == 0 {
			out.Problems = append(out.Problems, Problem{Index: i, Name: e.Name, Msg: configuresNothingMsg})
			continue
		}
		out.Entries = append(out.Entries, RepoLocalEntry{Index: i, RepoScript: e})
	}
	return out, nil
}
