package repocfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

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

// MaxRepoLocalSeedEntries caps how many entries a repo-local carry_files may
// declare. It bounds work, not bytes, and that is why the byte
// cap above does not subsume it: every entry that is actually PRESENT in the
// origin checkout costs a `git check-ignore` fork inside the session's worktree
// (session/git's carryLocalFile and linkLocalPath each run one, after their own
// absence check, so an entry naming nothing costs none), each time a worktree is
// materialized — a create, and a resume that has to recreate one. A few thousand
// entries fit comfortably under a megabyte and would fork git a few thousand
// times for such a session. Sixty-four is
// far past the handful a real project declares, and past it the file is refused
// WHOLE like every other structural refusal here: a truncated list would seed a
// set the trust prompt never described.
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

	// CarryFiles is the repo's own carry list, canonicalized and proven
	// repo-relative by ParseRepoLocal. It is UNIONED with the user's global list
	// rather than replacing it (#815): the values are sets of independent facts, so
	// a repo declaring its own local config must not also drop the user's personal
	// carry (.claude/settings.local.json) in that repo. Union is also what makes
	// #477's fix a MOVE — project entries leave the global list for the project's
	// file, and global keeps the universal ones.
	//
	// link_paths is deliberately NOT read here yet. A linked path is the user's own
	// tree under another name, writable by the agent and shared with every sibling
	// session at once, and every serious defect review found in the seeding half was
	// specific to that write direction — a repo link suppressing the user's carry, a
	// dangling symlink defeating the containment guard, provenance transfer through
	// the dedupe. A copy has none of those: it is private to the session, has no
	// target to resolve, and os.Stat refuses a dangling link. So the copy half ships
	// first and `link_paths` stays a tolerated unknown key until it can be designed
	// against a foundation that is already correct.
	CarryFiles []string
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
}

// RepoLocalLayerKeys names the config.json keys a repo-local file may layer
// over — the seed lists, not repo_scripts, which a repo-local entry REPLACES
// rather than adds to. It is exported for the settings panel, whose
// scopeRepoLayered rows must be exactly these keys and no others;
// TestRepoLocalLayerKeysMatchTheWireSchema holds it to repoLocalWire by reflection.
//
// That guard is necessary and NOT sufficient on its own, and the difference is worth
// stating because the stronger claim shipped here once: satisfying it (plus the row's
// scope, plus the renderer's routing) still left a third key invisible, because the
// map handed to the panel was built from hardcoded keys by its producer.
// RepoLocalLayers is the other half — see its doc.
func RepoLocalLayerKeys() []string {
	return []string{KeyCarryFiles}
}

// KeyCarryFiles is the layerable key by name. It is a constant because the name is a
// VOCABULARY shared across four packages — the wire tag, RepoLocalLayerKeys,
// RepoLocalLayers' map, and the settings row key the panel matches against — and a
// literal in any one of them is a copy that can drift silently.
const KeyCarryFiles = "carry_files"

// RepoLocalLayers is a parsed file's seed lists KEYED BY the settings-row key each
// one layers over. It is the single mapping from key name to list, and it exists
// because having that mapping in more than one place is how a layered key reaches
// the schema, passes every bridge guard, and then renders nothing.
//
// That is not hypothetical: the settings panel's producer used to build this map
// itself from two hardcoded keys, so a third key would have been declared
// layerable, been given a scopeRepoLayered row, satisfied both bridge guards AND
// the renderer's scope routing — and still shown no badge, because the map handed
// to the panel never carried it. The renderer reading r.scope closed only the
// consumer half; this closes the producer half.
//
// Every layer key is present in the result, with a nil list where the file declares
// nothing for it. Present-but-nil is what lets the panel distinguish "this repo adds
// nothing to this row" from "this key is not one a repo can layer" without consulting
// a second list. TestRepoLocalLayersCoversEveryLayerKey holds the key set to
// RepoLocalLayerKeys.
func RepoLocalLayers(rl RepoLocal) map[string][]string {
	return map[string][]string{
		KeyCarryFiles: rl.CarryFiles,
	}
}

// repoLocalNonLayeringKeys names the wire keys that are deliberately NOT panel
// layers, each with the reason, so the bridge guard can be a completeness sweep
// instead of a bare equality.
//
// The distinction it records is real and the guard needs it: a key REPLACES (there
// is one script to run, so a repo-local entry supersedes the user's for that repo)
// or it LAYERS (the value is a set, so the two combine). Only the second can have a
// scopeRepoLayered row, because only the second leaves the user's own value partly
// in force for the panel to annotate. Without this map the guard was an equality
// over every non-repo_scripts tag, so the next replacing key could not be added
// without either breaking the test or being declared a panel layer it can never
// be — and the failure pointed at the second.
var repoLocalNonLayeringKeys = map[string]string{
	"repo_scripts": "a repo-local entry REPLACES the user's matching entry rather than adding to it, and it has no single panel row to annotate",
}

// Note: link_paths is not in either map because it is not in repoLocalWire at all
// yet — an unread key needs no verdict. See RepoLocal.CarryFiles for why the write
// direction ships separately.

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
// It is the PATH rule only. A display rule lived here briefly — refusing every
// rune unicode.IsPrint rejects, because a repo-authored entry is interpolated into
// a frame — and it was in the wrong place: this same function judges the USER's own
// global carry_files/link_paths, where IsPrint refuses legal filenames (every Zs
// but U+0020, so U+00A0 from a pasted path and U+3000 in a CJK one; all of Cf, so a
// soft hyphen; all of Co, so a Nerd-Font private-use rune). A user whose linked
// directory contains one had it working for months and would have lost it with a
// warning naming the wrong reason. Display safety belongs at the display boundary
// (app/repotrust.go's sanitizeRepoText, and the panel's) plus a repo-only refusal
// in parseSeedList — see repoLocalSeedEntryRefusal.
//
// path.Clean, not filepath.Clean: git pathspecs are always slash-separated,
// including on Windows. ToSlash is a deliberate no-op on POSIX (Separator is
// already '/'), so it does NOT fold a backslash-spelled entry there — a backslash
// is a legal filename character on Linux and macOS, and this function cannot know
// whether one was meant. parseSeedList refuses it for a repo-local list, which is
// the file that gets committed on one platform and read on another. The repo-root
// refusal is explicit because filepath.IsLocal accepts ".": only the join in
// resolveSeedPaths collapses it to a root that is not strictly inside itself, and a
// rule that relied on that would not exist here at all.
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
	out.CarryFiles = carry
	return out, nil
}

// parseSeedList canonicalizes one repo-local seed list, or refuses the file.
// The returned entries are canonical, so the hash a grant covers and the
// pathspec git is asked about are derived from the same spelling the user was
// shown — the same property resolveSeedPaths keeps for the global list.
func parseSeedList(section string, raw []string) ([]string, error) {
	if len(raw) > MaxRepoLocalSeedEntries {
		return nil, fmt.Errorf(
			"%s declares %d %s entries — a repo-local list carries at most %d (each one costs a git probe every time a worktree of this repo is materialized)",
			RepoLocalFileName, len(raw), section, MaxRepoLocalSeedEntries)
	}
	var out []string
	seen := make(map[string]bool, len(raw))
	for i, rel := range raw {
		if err := repoLocalSeedEntryRefusal(rel); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", RepoLocalFileName, Problem{Section: section, Index: i, Name: rel}.where(), err)
		}
		canon, err := CanonicalSeedPath(rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", RepoLocalFileName, Problem{Section: section, Index: i, Name: rel}.where(), err)
		}
		// Dedupe on the canonical spelling. Without this, "node_modules",
		// "./node_modules/" and "node_modules/." are three entries that seed one path:
		// the count in every consent surface (the dialog, `trust allow`'s receipt,
		// trust status, doctor, the settings badge) said three where one applies, and
		// one path could eat 64 slots of a cap whose whole justification is bounding
		// the git probes the union then dedupes away.
		if seen[canon] {
			continue
		}
		seen[canon] = true
		out = append(out, canon)
	}
	return out, nil
}

// repoLocalSeedEntryRefusal is the extra bar a REPO-authored seed entry must clear,
// beyond being a valid repo-relative path. Both halves exist because this entry is
// committed by one person and read by another, on another machine, and is then
// interpolated into a frame:
//
//   - Unprintable runes. A seed entry reaches the trust dialog and the settings
//     panel's provenance line, and the classes unicode.IsPrint rejects are the ones
//     that defeat a width bound: C0/C1 and ESC write straight through a renderer,
//     and Cf (U+200B zero-width space, U+200D joiner, U+202E right-to-left
//     override) measures ZERO cells, so a truncation budget bounds nothing. The
//     display surfaces sanitize as well — this is not their only guard — but a repo
//     has no legitimate reason to commit one, so refusing the file is the honest
//     answer and it keeps the count the dialog shows equal to what applies.
//   - A backslash. filepath.ToSlash is the identity on POSIX, so a Windows-authored
//     "node_modules\\.bin" parses as one legal segment on Linux and macOS: every
//     surface then advertises it, and at seed time os.Lstat simply misses and
//     linkLocalPath returns on its silent absence path. Nothing ever says the entry
//     did not apply. Refusing it names the real problem to the person who can fix
//     it, in the file they can fix.
//
// Combining marks stay ALLOWED, here and in CanonicalSeedPath: macOS stores
// filenames decomposed, so a legitimate "café" is "cafe" plus U+0301. They measure
// as they render, so they defeat no width bound of their own — but a long run of
// them is still repo-authored text with a surprising cell count, which is why the
// display surfaces map Mn/Me to a 1-cell glyph rather than trusting this rule to
// have removed them.
func repoLocalSeedEntryRefusal(rel string) error {
	for _, r := range rel {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("contains the unprintable character U+%04X", r)
		}
	}
	if strings.ContainsRune(rel, '\\') {
		return errors.New(`contains a backslash — repo-local entries are slash-separated on every platform, and a backslash would silently never match here`)
	}
	return nil
}
