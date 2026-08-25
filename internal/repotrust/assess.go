package repotrust

import (
	"context"
	"fmt"
	"os"

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
	// Entries is the usable repo_scripts entry it declares, if any (at most one —
	// repocfg's one-entry rule — carrying its position in the file); Problems the
	// refused one. Templates are NOT compiled here — content is still untrusted
	// at assessment time.
	Entries  []repocfg.RepoLocalEntry
	Problems []repocfg.Problem

	// Granted is the verdict for exactly (Key, Hash). HasGrant reports whether
	// the ledger holds ANY record for Key — what separates "never asked" from
	// "granted for different content" in prompt copy. Record is that record.
	Granted  bool
	HasGrant bool
	Record   Record

	// FileErr is a present-but-unusable file: over the size cap, unreadable,
	// undecodable JSON, or more than one entry. Nothing from such a file executes
	// (there are no Entries), and surfaces report it rather than dropping it
	// silently.
	FileErr error
	// LedgerErr is a ledger that could not be read (corrupt, future version).
	// Grants read as zero while it stands — fail closed — and surfaces say so.
	LedgerErr error
}

// WantsPrompt reports whether creating a session from this repo should ask the
// user: the create ref declares a usable entry and the ledger does not grant
// these bytes. A file that declares nothing usable never prompts — there is
// nothing to run, so there is nothing to ask about (its Problems still
// surface at enforcement). "Usable" is parse-usable, which since the parse
// refuses configures-nothing entries in compile's own words means the entry
// the prompt describes is one enforcement could actually run.
func (a Assessment) WantsPrompt() bool {
	return a.Present && len(a.Entries) > 0 && !a.Granted
}

// LiveState compares a recorded grant with its repo as it stands now, in the
// words `atrium trust status` and `atrium doctor` both print — one spelling,
// one comparison, so the two surfaces cannot drift apart. The comparison is
// against the ref a default new session would start from (AssessCreateDefault
// — updateBase is the user's update_base_on_create), so "current" means "a
// session created now runs what you granted". Each git probe underneath
// self-bounds (session/git's local timeout), so a wedged repo costs timeouts,
// not a hang.
func LiveState(ctx context.Context, key string, rec Record, updateBase bool) string {
	if _, err := os.Stat(key); err != nil {
		return "missing (repo gone?)"
	}
	a, err := AssessCreateDefault(ctx, key, updateBase)
	if err != nil || a.FileErr != nil {
		return "unreadable"
	}
	switch {
	case !a.Present:
		return "absent at " + a.Ref
	case a.Hash == rec.Hash:
		return "current"
	default:
		return "changed (re-allow to use)"
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
	if err != nil {
		a.FileErr = err
	} else {
		a.Entries = parsed.Entries
		a.Problems = parsed.Problems
	}
	if len(a.Entries) > 0 {
		a.Remote = git.GetRemoteURL(ctx, root)
	}

	ledger, err := Load()
	a.LedgerErr = err
	a.Granted = ledger.Granted(key, a.Hash)
	a.Record, a.HasGrant = ledger.Lookup(key)
	return a, nil
}
