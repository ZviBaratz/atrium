package repotrust

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
)

// Assessment is one repo's repo-local config held up against the ledger: what
// the create ref declares, and where the user's trust stands on exactly those
// bytes. It is the shared answer behind the create-time prompt, `atrium
// trust`, and doctor — one derivation, so no two surfaces can disagree about
// whether a repo would prompt.
//
// It deliberately reads a committed ref (git.FileAtRef), not the working tree:
// a worktree materializes only tracked content, so the working-tree file can
// differ from — or exist entirely apart from — the bytes a session will
// actually hold. Granting those would approve a script that never runs while
// saying nothing about the one that does. And the ref is the session's START
// POINT, not literal HEAD: with update_base_on_create (default on) a worktree
// checks out origin/<branch> whenever that is ahead of local, and the form can
// pick a base branch outright — hashing HEAD there would show and grant one
// version while the worktree materializes another. Enforcement
// (session/repoconfig.go's routeRepoLocal) is the other half of the pact: it
// hashes the worktree's own file at the moment of use.
type Assessment struct {
	// Root is the resolved repo toplevel; Key its canonical ledger identity.
	Root string
	Key  string
	// Ref is the ref actually read — the session start point handed in (or
	// derived), "HEAD" when nothing fresher applied. Surfaces name it so "absent"
	// and "changed" verdicts say absent/changed WHERE.
	Ref string
	// Remote is the origin remote, carried into grants as display metadata. Only
	// resolved when the file declares something grantable — it costs a git fork,
	// and nothing else here consumes it.
	Remote string

	// Present is whether Ref carries a .atrium.json at all.
	Present bool
	// Hash is the content hash of that file's checked-out form ("" when absent
	// or unreadable).
	Hash string
	// Local is the parsed file: the usable repo_scripts entry it declares, if any
	// (at most one — repocfg's one-entry rule — carrying its position in the file),
	// the refused one as a Problem, and the two seed lists it layers over the
	// user's own (#815). One field rather than a spread of them so every asking
	// surface can describe the file through repocfg.RepoLocalSurfaces and none can
	// describe half of it. Templates are NOT compiled here — content is still
	// untrusted at assessment time.
	Local repocfg.RepoLocal

	// Granted is the verdict for exactly (Key, Hash) AND for everything this file
	// declares — see ScopeUpgrade. HasGrant reports whether the ledger holds ANY
	// record for Key — what separates "never asked" from "granted for different
	// content" in prompt copy. Record is that record.
	Granted  bool
	HasGrant bool
	Record   Record

	// ScopeUpgrade is the one case where the bytes match a grant and Granted is
	// still false: the record predates GrantVersionSeeds and this file declares
	// seed lists, so the prompt that wrote it could not have described them. The
	// file has not changed, so prompt copy must not say it has.
	ScopeUpgrade bool

	// FileErr is a present-but-unusable file: over the size cap, unreadable,
	// undecodable JSON, more than one entry, or a repo_scripts entry the parse
	// refused (a Problem). Nothing from such a file applies — enforcement refuses
	// it whole, so nothing here may describe it as grantable — and surfaces report
	// it rather than dropping it silently.
	FileErr error
	// LedgerErr is a ledger that could not be read (corrupt, future version).
	// Grants read as zero while it stands — fail closed — and surfaces say so.
	LedgerErr error
}

// WantsPrompt reports whether creating a session from this repo should ask the
// user: the create ref declares something the grant would put into the session,
// and the ledger does not grant these bytes (or grants them at a version that
// predates half of what they now confer — see ScopeUpgrade). A file that declares
// nothing usable never prompts — there is nothing to apply, so there is nothing
// to ask about — and neither does an unusable one: AssessRepo turns a refused
// entry into FileErr precisely so Local is empty here, because enforcement
// refuses such a file whole and a prompt for it would grant nothing, forever.
//
// "Declares something" is repocfg.RepoLocalSurfaces, which is also what
// enforcement calls to decide the same question and what the dialog and `trust
// allow` describe the file by — so the prompt cannot offer to grant a file the
// gate would treat as absent, or stay silent about one it would apply. Since the
// parse refuses configures-nothing entries in compile's own words and refuses an
// unusable seed list whole, everything the list names is something enforcement
// could actually act on. Note that a seed-only file (carry_files / link_paths,
// no repo_scripts) prompts: it executes nothing, but it decides which of the
// user's own gitignored files are copied in front of an agent and which of their
// trees it may write through, which is #815's recorded reason for gating both
// halves behind one grant.
func (a Assessment) WantsPrompt() bool {
	return a.Present && len(repocfg.RepoLocalSurfaces(a.Local)) > 0 && !a.Granted
}

// LiveState compares a recorded grant with its repo as it stands now, in the
// words `atrium trust status` and `atrium doctor` both print — one spelling,
// one comparison, so the two surfaces cannot drift apart. The comparison is
// against the ref a default new session would start from (AssessCreateDefault
// — updateBase is the user's update_base_on_create), so "current" means "a
// session created now runs what you granted". Each git probe underneath
// self-bounds (session/git's local timeout), so a wedged repo costs timeouts,
// not a hang.
//
// declares is what the grant covers, in RepoLocalSurfaces' words — the same list
// the create-time dialog and `trust allow`'s receipt print. It rides this function
// rather than a second call because the assessment behind it is the same one, and
// a separate derivation would double the git forks per trusted repo. It is empty
// for every state but "current": a grant whose repo has changed, lost the file, or
// gone away covers nothing that would apply now, and printing the old file's
// surfaces there would describe a session nobody can create.
func LiveState(ctx context.Context, key string, rec Record, updateBase bool) (state, declares string) {
	if _, err := os.Stat(key); err != nil {
		return "missing (repo gone?)", ""
	}
	a, err := AssessCreateDefault(ctx, key, updateBase)
	if err != nil || a.FileErr != nil {
		return "unreadable", ""
	}
	switch {
	case !a.Present:
		return "absent at " + a.Ref, ""
	case a.ScopeUpgrade:
		// The bytes match, so this is not "changed" — but the grant predates the
		// prompt that describes the file's seed lists, so nothing applies until the
		// user re-allows. Saying "current" here would report a session that would
		// run untrusted as trusted.
		return "needs re-allow (new keys)", ""
	case a.Hash == rec.Hash:
		return "current", strings.Join(repocfg.RepoLocalSurfaces(a.Local), " + ")
	default:
		return "changed (re-allow to use)", ""
	}
}

// AssessCreateDefault is AssessRepo at the ref a session created off this repo
// with NO explicit base would start from (git.StartPointPreview with no base
// override). The asking surfaces that have no create form in hand — `atrium
// trust allow`, `trust status`, doctor — use this, so their verdicts describe
// the session the user would actually get.
func AssessCreateDefault(ctx context.Context, path string, updateBase bool) (Assessment, error) {
	return AssessRepo(ctx, path, git.StartPointPreview(ctx, path, "", updateBase))
}

// AssessRepo derives the Assessment for the repository containing path, read
// at ref ("" means literal HEAD; create paths pass git.StartPointPreview's
// answer for their base). The error is for a path that has no assessable
// repository (not a git repo, git unable to answer, no derivable key); every
// condition ABOUT the repo's config or the ledger comes back inside the
// Assessment instead, so callers on the asking side can render it.
//
// It never writes: not the ledger, not the repo, not the data dir.
func AssessRepo(ctx context.Context, path, ref string) (Assessment, error) {
	root, err := git.RepoRoot(ctx, path)
	if err != nil {
		return Assessment{}, fmt.Errorf("repotrust: %s is not inside a git repository: %w", path, err)
	}
	key, err := CanonicalRoot(root)
	if err != nil {
		return Assessment{}, err
	}
	if ref == "" {
		ref = "HEAD"
	}
	a := Assessment{Root: root, Key: key, Ref: ref}

	data, present, err := git.FileAtRef(ctx, root, ref, repocfg.RepoLocalFileName, repocfg.MaxRepoLocalBytes)
	a.Present = present
	if err != nil {
		a.FileErr = err
		return a, nil
	}
	if !present {
		return a, nil
	}

	a.Hash = HashBytes(data)
	parsed, err := repocfg.ParseRepoLocal(data)
	switch {
	case err != nil:
		a.FileErr = err
	case len(parsed.Problems) > 0:
		// A refused entry makes the file unusable, not partly usable. Enforcement
		// refuses it whole (routeRepoLocal's Problems check runs before its
		// surfaces check), so leaving the seed lists in Local here would let the
		// dialog offer — and `trust allow` write — a grant for a file that then
		// applies nothing, permanently: re-creating re-prompts and re-granting
		// re-succeeds. Reporting it as unusable is what keeps the asking surfaces
		// and the gate on one answer, and it is what main did before the seed
		// lists gave a Problems-only file something to describe.
		a.FileErr = fmt.Errorf("%s: %w", repocfg.RepoLocalFileName, parsed.Problems[0])
	default:
		a.Local = parsed
	}
	if len(repocfg.RepoLocalSurfaces(a.Local)) > 0 {
		a.Remote = git.GetRemoteURL(ctx, root)
	}

	ledger, err := Load()
	a.LedgerErr = err
	a.Record, a.HasGrant = ledger.Lookup(key)
	// The SAME scoped question the enforcement funnel asks (session/repoconfig.go's
	// routeRepoLocal), so the prompt cannot offer to grant something the gate would
	// refuse, or stay silent about something it would apply. Asking a different
	// question here is precisely how the version gate came to be advisory-only.
	need := GrantScope{Seeds: declaresSeeds(a.Local)}
	a.Granted = ledger.GrantedFor(key, a.Hash, need)
	// Distinguish "the file changed" from "this atrium reads more of it than the one
	// that granted it did" — the bytes match in the second case, so prompt copy must
	// not claim an edit nobody made.
	a.ScopeUpgrade = !a.Granted && a.HasGrant && a.Record.Hash == a.Hash && need.Seeds && !a.Record.CoversSeeds()
	return a, nil
}

// declaresSeeds reports whether a parsed file layers anything over the user's own
// seed lists — the half of a grant that GrantVersionSeeds gates.
//
// It delegates rather than deciding, so this and the enforcement funnel cannot drift:
// see repocfg.DeclaresLayers for why the predicate is derived from the layer map.
func declaresSeeds(rl repocfg.RepoLocal) bool {
	return repocfg.DeclaresLayers(rl)
}
