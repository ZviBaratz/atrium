package repotrust

import (
	"context"
	"fmt"
	"os"

	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
)

// Assessment is one repo's repo-local config held up against the ledger: what
// HEAD declares, and where the user's trust stands on exactly those bytes. It
// is the shared answer behind the create-time prompt, `atrium new`'s advisory,
// `atrium trust`, and doctor — one derivation, so no two surfaces can disagree
// about whether a repo would prompt.
//
// It deliberately reads HEAD (git.HeadFile), not the working tree: a worktree
// materializes only tracked content, so the working-tree file can differ from
// — or exist entirely apart from — the bytes a session will actually hold.
// Granting those would approve a script that never runs while saying nothing
// about the one that does. Enforcement (session/setupscript.go) is the other
// half of the pact: it hashes the worktree's own file at the moment of use.
type Assessment struct {
	// Root is the resolved repo toplevel; Key its canonical ledger identity.
	Root string
	Key  string
	// Remote is the origin remote, carried into grants as display metadata.
	Remote string

	// Present is whether HEAD carries a .atrium.json at all.
	Present bool
	// Hash is the content hash of that file's checked-out form ("" when absent
	// or unreadable).
	Hash string
	// Entries are the structurally-valid repo_scripts it declares (each carrying
	// its position in the file); Problems the refused ones. Templates are NOT
	// compiled here — content is still untrusted at assessment time.
	Entries  []repocfg.RepoLocalEntry
	Problems []repocfg.Problem

	// Granted is the verdict for exactly (Key, Hash). HasGrant reports whether
	// the ledger holds ANY record for Key — what separates "never asked" from
	// "granted for different content" in prompt copy. Record is that record.
	Granted  bool
	HasGrant bool
	Record   Record

	// FileErr is a present-but-unusable file: over the size cap, unreadable,
	// or undecodable JSON. Nothing from such a file executes (there are no
	// Entries), and surfaces report it rather than dropping it silently.
	FileErr error
	// LedgerErr is a ledger that could not be read (corrupt, future version).
	// Grants read as zero while it stands — fail closed — and surfaces say so.
	LedgerErr error
}

// WantsPrompt reports whether creating a session from this repo should ask the
// user: HEAD declares at least one usable entry and the ledger does not grant
// these bytes. A file that declares nothing usable never prompts — there is
// nothing to run, so there is nothing to ask about (its Problems still
// surface at enforcement).
func (a Assessment) WantsPrompt() bool {
	return a.Present && len(a.Entries) > 0 && !a.Granted
}

// LiveState compares a recorded grant with its repo as it stands now, in the
// words `atrium trust status` and `atrium doctor` both print — one spelling,
// one comparison, so the two surfaces cannot drift apart. Each git probe under
// AssessRepo self-bounds (session/git's local timeout), so a wedged repo costs
// one timeout, not a hang.
func LiveState(ctx context.Context, key string, rec Record) string {
	if _, err := os.Stat(key); err != nil {
		return "missing (repo gone?)"
	}
	a, err := AssessRepo(ctx, key)
	if err != nil || a.FileErr != nil {
		return "unreadable"
	}
	switch {
	case !a.Present:
		return "absent at HEAD"
	case a.Hash == rec.Hash:
		return "current"
	default:
		return "changed (re-allow to use)"
	}
}

// AssessRepo derives the Assessment for the repository containing path. The
// error is for a path that has no assessable repository (not a git repo, git
// unable to answer, no derivable key); every condition ABOUT the repo's config
// or the ledger comes back inside the Assessment instead, so callers on the
// asking side can render it.
//
// It never writes: not the ledger, not the repo, not the data dir.
func AssessRepo(ctx context.Context, path string) (Assessment, error) {
	root, err := git.RepoRoot(ctx, path)
	if err != nil {
		return Assessment{}, fmt.Errorf("repotrust: %s is not inside a git repository: %w", path, err)
	}
	key, err := CanonicalRoot(root)
	if err != nil {
		return Assessment{}, err
	}
	a := Assessment{Root: root, Key: key, Remote: git.GetRemoteURL(ctx, root)}

	data, present, err := git.HeadFile(ctx, root, repocfg.RepoLocalFileName, repocfg.MaxRepoLocalBytes)
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

	ledger, err := Load()
	a.LedgerErr = err
	a.Granted = ledger.Granted(key, a.Hash)
	a.Record, a.HasGrant = ledger.Lookup(key)
	return a, nil
}
