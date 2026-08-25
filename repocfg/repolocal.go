package repocfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"

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

// MaxRepoLocalSeedEntries caps how many entries a repo-local carry_files or
// link_paths may declare. It bounds work, not bytes, and that is why the byte
// cap above does not subsume it: every seeded entry costs a `git check-ignore`
// fork inside the session's worktree (session/git's carryLocalFile and
// linkLocalPath each run one), on every worktree materialization — create and
// every resume. A few thousand entries fit comfortably under a megabyte and
// would fork git a few thousand times per session start. Sixty-four is two
// orders of magnitude past the handful a real project declares, and past it the
// file is refused WHOLE like every other structural refusal here: a truncated
// list would seed a set the trust prompt never described.
const MaxRepoLocalSeedEntries = 64

// RepoLocal is a parsed .atrium.json: the repo_scripts entry that survived the
// structural rules (at most one — ParseRepoLocal's one-entry rule), the two
// path lists it layers over the user's own (#815), or the refusal that kept the
// entry out. The entry lists are mutually exclusive here, unlike Validate's over
// the global config: with one entry there is no sibling for a Problem to sit
// beside.
type RepoLocal struct {
	Entries  []RepoLocalEntry
	Problems []Problem

	// CarryFiles and LinkPaths are the repo's own seed lists, canonicalized and
	// proven repo-relative by ParseRepoLocal. They are UNIONED with the user's
	// global lists rather than replacing them (#815): the values are sets of
	// independent facts, so a repo declaring its dependency tree must not also
	// drop the user's personal carry (.claude/settings.local.json) in that repo.
	// Union is also what makes #477's fix a MOVE — project entries leave the
	// global list for the project's file, and global keeps the universal ones.
	CarryFiles []string
	LinkPaths  []string
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

// repoLocalWire is the file's schema. Unknown top-level keys are deliberately
// tolerated so a key a newer atrium reads can land in repos before every user
// has upgraded — an older atrium ignores it rather than refusing the whole file.
// That is the tolerance carry_files and link_paths themselves shipped under
// before #815 read them; anything added next inherits it.
type repoLocalWire struct {
	RepoScripts []config.RepoScript `json:"repo_scripts"`
	CarryFiles  []string            `json:"carry_files"`
	LinkPaths   []string            `json:"link_paths"`
}

// RepoLocalLayerKeys names the config.json keys a repo-local file may layer
// over — the seed lists, not repo_scripts, which a repo-local entry REPLACES
// rather than adds to. It is exported for the settings panel, whose
// scopeRepoLayered rows must be exactly these keys and no others;
// TestRepoLocalLayerKeysMatchTheWireSchema holds it to repoLocalWire by
// reflection, so a third layered key cannot reach the file without reaching the
// panel too.
func RepoLocalLayerKeys() []string {
	return []string{"carry_files", "link_paths"}
}

// CanonicalSeedPath is the one lexical rule for a carry_files / link_paths
// entry: the slash-separated spelling git pathspecs and filesystem joins must
// both be derived from, or an error saying why the entry can never be seeded.
//
// Pure by requirement, not by preference. It is called from ParseRepoLocal —
// which runs on untrusted repo bytes, before any grant, and may touch neither
// the filesystem nor the template engine — and from session/git's
// resolveSeedPaths, which needs the same verdict against the user's own list.
// One definition because the two callers refuse for the same reasons; a private
// copy on either side would let a repo-local entry be accepted by the parser and
// then refused at seed time (a grant for something that never runs) or the
// reverse.
//
// path.Clean, not filepath.Clean: git pathspecs are always slash-separated,
// including on Windows, and ToSlash first folds an entry a Windows user spelled
// with backslashes into that same form. The repo-root refusal is explicit
// because filepath.IsLocal accepts ".": only the join in resolveSeedPaths
// collapses it to a root that is not strictly inside itself, and a rule that
// relied on that would not exist here at all.
func CanonicalSeedPath(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty entry")
	}
	canon := path.Clean(filepath.ToSlash(rel))
	if !filepath.IsLocal(canon) {
		return "", errors.New("must be a repo-relative path inside the repo")
	}
	if canon == "." {
		return "", errors.New("names the repo root, not a path inside it")
	}
	return canon, nil
}

// RepoLocalSurfaces names everything a parsed repo-local file would put into a
// session, in a fixed order: the entry's execution-adjacent surfaces
// (DeclaredSurfaces, unchanged) followed by the seed lists with their counts.
// It is the file-level analogue of DeclaredSurfaces and exists for the same
// reason: the create-time trust prompt, `atrium trust allow`, `trust status` and
// doctor all describe a file by this list, and enforcement treats an empty list
// as "declares nothing" — so a private copy in any of those places could
// describe a file the others refuse, or grant one whose lists nobody mentioned.
//
// Empty means the file applies nothing, which is what makes a bare `{}` (or a
// file carrying only keys a future atrium reads) silent rather than a standing
// untrusted nag whose remedies would both refuse.
func RepoLocalSurfaces(rl RepoLocal) []string {
	var out []string
	if len(rl.Entries) > 0 {
		out = append(out, DeclaredSurfaces(rl.Entries[0].RepoScript)...)
	}
	if n := len(rl.CarryFiles); n > 0 {
		out = append(out, fmt.Sprintf("%d carried file%s", n, plural(n)))
	}
	if n := len(rl.LinkPaths); n > 0 {
		out = append(out, fmt.Sprintf("%d linked path%s", n, plural(n)))
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
// A MALFORMED SEED ENTRY refuses the whole file too, for the sibling reason:
// the two lists are seeded as a set, the trust prompt describes them by count,
// and dropping one silently would seed a set the user was never shown. It is
// also the only refusal a repo can act on — the message names the entry, the fix
// changes the file's content, and the fixed content re-prompts. That is why
// these are an error rather than a Problem: a Problem beside a surviving entry
// would let the script run while the lists it shipped with went missing.
//
// The error is for a file that is unusable as a whole (undecodable JSON, more
// than one entry, an unusable or over-cap seed list); the single entry's
// per-entry refusals come back as Problems. Problem.Error() prints
// `<section>[N]`, which is the entry's position in THIS file — display sites
// prefix the filename so a user with both a global and a repo-local list can
// tell which one is being named.
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

	carry, err := parseSeedList("carry_files", wire.CarryFiles)
	if err != nil {
		return RepoLocal{}, err
	}
	link, err := parseSeedList("link_paths", wire.LinkPaths)
	if err != nil {
		return RepoLocal{}, err
	}
	out.CarryFiles, out.LinkPaths = carry, link
	return out, nil
}

// parseSeedList canonicalizes one repo-local seed list, or refuses the file.
// The returned entries are canonical, so the hash a grant covers and the
// pathspec git is asked about are derived from the same spelling the user was
// shown — the same property resolveSeedPaths keeps for the global list.
func parseSeedList(section string, raw []string) ([]string, error) {
	if len(raw) > MaxRepoLocalSeedEntries {
		return nil, fmt.Errorf(
			"%s declares %d %s entries — a repo-local list carries at most %d (each one costs a git probe in every worktree of this repo, on every start and resume)",
			RepoLocalFileName, len(raw), section, MaxRepoLocalSeedEntries)
	}
	var out []string
	for i, rel := range raw {
		canon, err := CanonicalSeedPath(rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", RepoLocalFileName, Problem{Section: section, Index: i, Name: rel}.where(), err)
		}
		out = append(out, canon)
	}
	return out, nil
}
