package git

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/internal/teardown"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"path/filepath"
	"strings"
)

// Setup creates a new worktree for the session
func (g *Worktree) Setup() error {
	// Ensure worktrees directory exists early (can be done in parallel with branch check)
	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return err
	}

	// The session always gets its own branch. baseRef only selects the start point at first
	// creation; once the branch exists it holds the session's committed work (including the
	// WIP commit pause makes), so resume must reuse it rather than `branch -D` it away and
	// rebuild from baseRef — which silently discarded that work for base-branch sessions
	// (#146). Branch existence is the discriminator, so a pre-existing branch here is
	// read as a resume of a base-branch or HEAD-based session.
	//
	// That is a contract on the callers, not something this function can check: a
	// creation whose title derives an existing branch slug does not fail here, it takes
	// the resume branch and silently adopts someone else's work. Two of the three
	// creation paths hold up their end — the new-session form (createSessionFromForm)
	// and the `atrium new` drain (executeCreateRequest), both via the variantTitleConflict
	// pair in app/app_session.go, the only *creation* predicate that consults
	// git.LocalBranchExists. The drain has one deliberate exception: a request the
	// startup reconcile marked Adopt skips the branch half of that gate, because the
	// branch it is taking has already been proved to be its own interrupted build's
	// (app/create_drain.go's createConflictIn, app/create_recover.go). That is the only
	// sanctioned way into the resume path from a creation, and it is why the base commit
	// seeded below exists at all.
	//
	// The other two callers do not gate anything: app_branchsearch.go
	// computes the create form's async branch verdict, and app_checkpoints.go's is
	// forkBaseBranch — on the fork *create* flow, choosing which base branch the fork
	// starts from, never deciding whether it may.
	// Smart auto-dispatch does not: it calls titleConflict, whose
	// branch arm reads m.titleBranchExists, an async verdict only the create form ever
	// schedules. An auto-dispatched title matching an orphan branch therefore reaches
	// this line and resumes it (atrium#711).
	var setupErr error
	if _, refErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName)); refErr == nil {
		setupErr = g.setupFromExistingBranch()
	} else {
		setupErr = g.setupNewWorktree()
	}
	if setupErr != nil {
		return setupErr
	}

	// The worktree is materialized; seed the configured gitignored paths from the
	// origin checkout into it — carry_files copied, link_paths symlinked unless this
	// session is dependency-isolated, which skips them (best-effort, never an error —
	// see carry.go).
	g.seedLocalPaths()
	return nil
}

// setupFromExistingBranch creates a worktree from an existing branch
func (g *Worktree) setupFromExistingBranch() error {
	// Directory already created in Setup(), skip duplicate creation

	// Clean up any existing worktree first.
	g.clearStaleWorktree()

	// Check if the local branch exists
	_, localErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName))
	if localErr != nil {
		// Local branch doesn't exist — check if remote tracking branch exists
		_, remoteErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", g.branchName))
		if remoteErr != nil {
			return fmt.Errorf("branch %s not found locally or on remote", g.branchName)
		}
		// Create a local tracking branch via worktree add -b
		if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, fmt.Sprintf("origin/%s", g.branchName)); err != nil {
			if busyErr := g.busyBranchError(err); busyErr != nil {
				return busyErr
			}
			return fmt.Errorf("failed to create worktree from remote branch %s: %w", g.branchName, err)
		}
		g.seedBaseCommitFromBranch()
		return nil
	}

	// Create a new worktree from the existing local branch
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", g.worktreePath, g.branchName); err != nil {
		// Defense in depth: the Resume pre-check frees the branch first, but the
		// branch can become busy again between that check and here (another
		// session/manual checkout). Translate git's raw "already used by
		// worktree" output into the same friendly, path-named message.
		if busyErr := g.busyBranchError(err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to create worktree from branch %s: %w", g.branchName, err)
	}

	g.seedBaseCommitFromBranch()
	return nil
}

// seedBaseCommitFromBranch gives a worktree that has no base commit one, reading the
// branch it was just checked out on. A no-op when a base is already set.
//
// Nothing else ever fills that field on this path: setBaseCommitSHA has exactly one
// other call site, in setupNewWorktree. Left empty it is not a cosmetic gap — diffFrom
// returns errBaseCommitNotSet before it runs any git (diff.go), so for the whole life of
// the session the diff tab renders "base commit SHA not set", the row's +/- chip is
// suppressed, and RepoStats comes back through the same early return with Dirty false
// and Unpushed 0. That last one is what killDataWarning reads, so the kill confirmation
// would also drop its "uncommitted changes" warning.
//
// The branch tip is the honest base for the case that made this reachable: a create the
// startup reconcile re-queued to adopt its own interrupted build's branch (#716). That
// branch was cut from the start point and nothing was committed to it, so the tip IS the
// start point and the seeded base is the same value setupNewWorktree would have written.
// Smart auto-dispatch landing on an orphan branch (atrium#711) gets the same treatment.
//
// Guarded on empty because resume must keep its own: FromInstanceData restores the
// persisted SHA before Setup runs, so a resumed session's diff still spans its whole
// history. The one case this changes is a session persisted before base_commit_sha
// existed, which stops erroring and starts diffing from here — under-reporting its past
// work rather than refusing to report at all.
func (g *Worktree) seedBaseCommitFromBranch() {
	if g.GetBaseCommitSHA() != "" {
		return
	}
	out, err := g.runGitCommand(g.repoPath, "rev-parse", g.branchName)
	if err != nil {
		log.WarningLog.Printf("could not resolve a base commit for branch %s; this session's diff will be unavailable: %v", g.branchName, err)
		return
	}
	g.setBaseCommitSHA(strings.TrimSpace(out))
}

// busyBranchError returns a *BranchCheckedOutError when err is git's "branch
// already used by another worktree" failure, or nil otherwise. It shares the
// typed error the Resume pre-check returns so the app layer detects both origins
// with a single errors.As — including the path-less fallback, which the app
// recovers via IsBranchHeldByBaseRepo regardless.
func (g *Worktree) busyBranchError(err error) error {
	path, busy := busyBranchHolder(err)
	if !busy {
		return nil
	}
	return &BranchCheckedOutError{Branch: g.branchName, Path: path}
}

// busyBranchHolder scans a git error for the "already used by worktree" /
// "already checked out" signatures (wording varies across git versions) and
// returns the worktree path git named, plus whether the error was a busy-branch
// conflict at all. A marker match with an unparseable path yields ("", true).
func busyBranchHolder(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	for _, marker := range []string{"is already used by worktree at '", "is already checked out at '"} {
		idx := strings.Index(msg, marker)
		if idx < 0 {
			continue
		}
		rest := msg[idx+len(marker):]
		if end := strings.IndexByte(rest, '\''); end >= 0 {
			return rest[:end], true
		}
		// Marker matched but the closing quote is missing — git's output format may
		// have drifted. Still report the busy-branch conflict (path-less), and warn
		// so the parser gap is visible rather than silently degrading to "".
		log.WarningLog.Printf("busy-branch error matched %q but no closing quote for the worktree path: %q", marker, msg)
		return "", true
	}
	return "", false
}

// setupNewWorktree creates a new worktree on a fresh session branch, started from g.baseRef
// (an existing branch to base on) or HEAD when baseRef is empty.
func (g *Worktree) setupNewWorktree() error {
	// Clean up any existing worktree first.
	g.clearStaleWorktree()

	// Clean up any existing branch using git CLI (much faster than go-git PlainOpen)
	_, _ = g.runGitCommand(g.repoPath, "branch", "-D", g.branchName) // Ignore error if branch doesn't exist

	// Optionally refresh the base branch from origin so the session starts off the
	// freshest remote tip rather than a stale local branch (and, when opted in,
	// fast-forward the local base). Strictly best-effort: it never fails creation,
	// logging and falling back to the local base on any problem — see worktree_base.go.
	if g.updateBaseOnCreate {
		g.updateBaseRef()
	}

	// Resolve the start point. Branching off a ref (rather than checking it out) succeeds
	// even when that ref is checked out in another worktree, which is the whole point.
	startPoint, err := g.resolveStartPoint()
	if err != nil {
		return err
	}

	output, err := g.runGitCommand(g.repoPath, "rev-parse", startPoint)
	if err != nil {
		return fmt.Errorf("failed to resolve start point %s: %w", startPoint, err)
	}
	g.setBaseCommitSHA(strings.TrimSpace(output))

	// Create a new worktree on its own branch from the start point. Starting from a commit
	// (rather than the current worktree) gives the session a clean slate without inheriting
	// uncommitted changes.
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, startPoint); err != nil {
		return fmt.Errorf("failed to create worktree on branch %s from %s: %w", g.branchName, startPoint, err)
	}

	return nil
}

// resolveStartPoint returns the ref to branch the session off. When baseRef is empty this is
// HEAD; otherwise it is the local branch baseRef, falling back to its remote-tracking
// counterpart origin/<baseRef> when no local branch exists.
//
// When updateBaseOnCreate is set, it instead prefers origin/<ref> whenever the remote tip is
// ahead of (or equal to) local — freshenRef decides — so the session starts from the latest
// remote state. In that case it rewrites g.baseRef to the chosen origin/<ref> so the
// ahead/behind diff stays honest (see freshenRef). A start point is only ever chosen from a
// ref that exists; local-ahead/diverged and remoteless cases fall through to the historical
// local-preferred resolution unchanged.
func (g *Worktree) resolveStartPoint() (string, error) {
	if g.baseRef == "" {
		if _, err := g.runGitCommand(g.repoPath, "rev-parse", "--verify", "HEAD"); err != nil {
			if strings.Contains(err.Error(), "fatal: ambiguous argument 'HEAD'") ||
				strings.Contains(err.Error(), "fatal: not a valid object name") ||
				strings.Contains(err.Error(), "fatal: HEAD: not a valid object name") {
				return "", fmt.Errorf("this appears to be a brand new repository: please create an initial commit before creating an instance")
			}
			return "", fmt.Errorf("failed to get HEAD commit hash: %w", err)
		}
		if g.updateBaseOnCreate {
			if branch := CurrentBranchName(g.baseContext(), g.repoPath); branch != "" && branch != "HEAD" {
				if remote := g.freshenRef(branch); remote != "" {
					g.setBaseRef(remote)
					return remote, nil
				}
			}
		}
		return "HEAD", nil
	}

	// An explicit base ref may carry a re-entry "origin/" prefix (set by a prior
	// freshen and persisted); strip it back to the bare branch name for lookups.
	name := strings.TrimPrefix(g.baseRef, "origin/")

	if g.updateBaseOnCreate {
		if remote := g.freshenRef(name); remote != "" {
			g.setBaseRef(remote)
			return remote, nil
		}
	}

	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", name)); err == nil {
		return name, nil
	}
	if _, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", name)); err == nil {
		return fmt.Sprintf("origin/%s", name), nil
	}
	return "", fmt.Errorf("base branch %q not found locally or on remote", g.baseRef)
}

// Cleanup removes the worktree and associated branch
func (g *Worktree) Cleanup() error {
	var tc teardown.Errors

	// Check if worktree path exists before attempting removal
	if _, err := os.Stat(g.worktreePath); err == nil {
		// Remove the worktree using git command
		if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
			// The git removal can fail when the repo itself is unreachable — e.g.
			// the user renamed or deleted the project directory the session was
			// created from. Fall back to deleting the directory outright so an
			// orphaned worktree is never left behind, guarding the path to the
			// managed worktrees/ tree so a bug can't RemoveAll something arbitrary.
			if rmErr := removeOrphanedWorktreeDir(g.worktreePath); rmErr != nil {
				tc.Add(err)
				tc.Add(rmErr)
			} else {
				log.WarningLog.Printf("git worktree remove failed for %s, removed directory directly: %v", g.worktreePath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		// Only append error if it's not a "not exists" error
		tc.Record("check worktree path", err)
	}

	// Prune stale worktree registrations before deleting the branch. If a prior
	// partial teardown removed the worktree *directory* but left its admin files
	// behind — e.g. Setup's fallback ran under a cancelled shutdown context (#282),
	// where `git worktree remove` and this prune both failed — git still reports
	// the branch as "used by worktree at <gone path>" and refuses `branch -D`.
	// Pruning first clears that registration so the deletion below can succeed; in
	// the normal path `git worktree remove` already dropped the registration, so
	// this is a no-op.
	tc.Add(g.Prune())

	// Delete the branch using git CLI, but skip if this is a pre-existing branch
	if !g.isExistingBranch {
		if _, err := g.runGitCommand(g.repoPath, "branch", "-D", g.branchName); err != nil {
			// Only record if it's not a "branch not found" error
			if !strings.Contains(err.Error(), "not found") {
				tc.Record(fmt.Sprintf("remove branch %s", g.branchName), err)
			}
		}
	}

	return tc.Err()
}

// clearStaleWorktree force-removes any worktree registration at g.worktreePath
// and deletes a leftover directory, so a subsequent `git worktree add` starts
// from a clean slate. Best-effort: the registration may not exist, and the
// directory delete is guarded so it refuses anything outside the managed
// worktrees/ tree (see removeOrphanedWorktreeDir).
func (g *Worktree) clearStaleWorktree() {
	_, _ = g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath) // Ignore error if worktree doesn't exist
	if err := removeOrphanedWorktreeDir(g.worktreePath); err != nil {
		log.WarningLog.Printf("failed to clear stale worktree dir %s: %v", g.worktreePath, err)
	}
}

// removeOrphanedWorktreeDir deletes worktreePath, but only when it lives under the
// managed worktrees/ tree. The containment check is a safety belt: Cleanup calls
// this as a fallback when git can no longer manage the worktree, and we never want
// an unexpected path to turn into a recursive delete of something important.
func removeOrphanedWorktreeDir(worktreePath string) error {
	absPath, managed, err := underManagedWorktrees(worktreePath)
	if err != nil {
		return err
	}
	if !managed {
		return fmt.Errorf("refusing to remove worktree path outside managed tree: %s", absPath)
	}
	if err := os.RemoveAll(absPath); err != nil {
		return fmt.Errorf("failed to remove orphaned worktree directory %s: %w", absPath, err)
	}
	return nil
}

// underManagedWorktrees reports whether worktreePath lives inside the data dir's
// worktrees/ tree, alongside its absolutized form. One definition, because three callers
// turn on it and they must agree: removeOrphanedWorktreeDir uses it to refuse a
// recursive delete outside the managed tree, ReleaseManagedWorktree re-checks it before
// authorising that delete, and StrandedWorktreeFor uses it to tell a worktree Atrium
// minted from one a person checked out by hand.
//
// CleanupWorktrees asks the same question and deliberately does not come here; see the
// comment at its own check for why its input needs the resolved comparison alone.
func underManagedWorktrees(worktreePath string) (abs string, managed bool, err error) {
	root, err := getWorktreeDirectory()
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve worktrees directory: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve worktrees directory: %w", err)
	}
	abs, err = filepath.Abs(worktreePath)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve worktree path: %w", err)
	}
	// Two comparisons, each with both sides on the SAME basis, and never one of each.
	//
	// The literal one first, because it is the one that always holds still. The resolved
	// one is a fallback for the case StrandedWorktreeFor introduced: git reports the path
	// it registered, which on macOS routinely arrives as /private/var where
	// getWorktreeDirectory returns /var, and unresolved that comparison misses.
	//
	// Mixing them is what breaks, and it breaks in the direction that matters.
	// resolvePath falls back to Clean for a path that does not EXIST — and the busiest
	// caller here is removeOrphanedWorktreeDir, reached from clearStaleWorktree and from
	// ReleaseManagedWorktree straight after a `git worktree remove` has already deleted
	// the directory. So the worktree side stays /var while the still-present root side
	// resolves to /private/var, Rel answers "../..", and a worktree squarely inside the
	// managed tree is refused as outside it.
	contained := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if contained(absRoot, abs) || contained(resolvePath(absRoot), resolvePath(abs)) {
		return abs, true, nil
	}
	return abs, false, nil
}

// StrandedWorktreeFor returns the worktree currently holding branch in the repository
// at repoPath, and whether it is one Atrium minted (inside the data dir's worktrees/
// tree). An empty path means no worktree holds the branch.
//
// It exists for the create-recovery path (#716). A build interrupted between
// `git worktree add` and persistInstances leaves both a branch and a worktree, and the
// worktree is the half that blocks a second attempt: resolveWorktreePaths stamps every
// worktree directory with time.Now().UnixNano(), so the retry mints a DIFFERENT path,
// its clearStaleWorktree clears a path that never existed, and `git worktree add` then
// fails with "already used by worktree" against the first attempt's directory. Adopting
// the branch alone therefore does not finish the session; the stale registration has to
// go first.
//
// The managed flag is the caller's licence to remove it. A path under worktrees/ is a
// name only Atrium mints; anything else is a checkout somebody made deliberately, and
// no recovery should delete one of those.
//
// The error is separate from the answer on purpose. "git could not be asked" and "no
// worktree holds this branch" are the same two return values if a failure is folded into
// the empty path, and the caller acts on that difference: reading a failed
// `git worktree list` as "nothing holds it" is what would let the recovery adopt a branch
// whose stale registration is still in place, which then fails with git's own "already
// used by worktree" — the exact dead end this function exists to prevent.
func StrandedWorktreeFor(ctx context.Context, repoPath, branch string) (path string, managed bool, err error) {
	out, err := localGit(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, fmt.Errorf("failed to list worktrees in %s: %w", repoPath, err)
	}
	for wt, held := range parseWorktreeList(out) {
		if held != branch {
			continue
		}
		abs, ok, err := underManagedWorktrees(wt)
		if err != nil {
			// The path is known and it could not be classified, so it is reported as
			// unmanaged rather than as absent: a caller that may only touch what Atrium
			// minted must read "we cannot tell" as "not yours".
			return wt, false, nil
		}
		return abs, ok, nil
	}
	return "", false, nil
}

// ReleaseManagedWorktree detaches a stranded worktree from its branch: git's
// registration first, then the directory, then a prune so git forgets a registration
// whose directory was already gone.
//
// It refuses any path outside the managed worktrees/ tree, through the same check
// StrandedWorktreeFor reports — the caller is expected to have consulted that, and this
// re-checks rather than trusting it, because the argument is a path and the failure mode
// is a recursive delete.
//
// The branch itself is untouched. That is the whole point: the branch holds whatever the
// interrupted build committed, and the caller is removing the worktree precisely so a
// second attempt can check that branch out again.
func ReleaseManagedWorktree(ctx context.Context, repoPath, worktreePath string) error {
	abs, managed, err := underManagedWorktrees(worktreePath)
	if err != nil {
		return err
	}
	if !managed {
		return fmt.Errorf("refusing to release worktree outside the managed tree: %s", abs)
	}
	// Best-effort: a registration whose directory a person already deleted makes this
	// fail, and the prune below is what finishes the job in that case.
	_, _ = localGit(ctx, repoPath, "worktree", "remove", "-f", abs)
	if err := removeOrphanedWorktreeDir(abs); err != nil {
		return err
	}
	if _, err := localGit(ctx, repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees in %s: %w", repoPath, err)
	}
	return nil
}

// Remove removes the worktree but keeps the branch
func (g *Worktree) Remove() error {
	// Remove the worktree using git command
	if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// Prune removes all working tree administrative files and directories
func (g *Worktree) Prune() error {
	if _, err := g.runGitCommand(g.repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}

// parseWorktreeList parses `git worktree list --porcelain` output into a map of
// worktree-path → branch-name. Detached-HEAD worktrees map to an empty branch.
func parseWorktreeList(output string) map[string]string {
	result := make(map[string]string)
	current := ""
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch ") && current != "":
			branchPath := strings.TrimPrefix(line, "branch ")
			result[current] = strings.TrimPrefix(branchPath, "refs/heads/")
		}
	}
	return result
}

// resolvePath returns the symlink-resolved absolute path, falling back to the
// cleaned input when resolution fails (e.g. the path no longer exists). It lets
// the worktree-prefix check below match even when git reports a path through a
// different symlink than getWorktreeDirectory() returns — e.g. macOS resolves
// the temp dir /var/... to /private/var/....
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// uniqueNonEmptyStrings returns the input with empty strings and duplicates
// removed, preserving first-seen order.
func uniqueNonEmptyStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// CleanupWorktrees removes every worktree managed by atrium and its associated
// session branch. repoPaths must be the git repository roots that had active
// sessions: each git command runs with `git -C <repoPath>` so cleanup succeeds
// even when the caller's working directory is not a git repository (e.g.
// `atrium reset` from a home directory) or when sessions span multiple repos.
//
// The order is dictated by git: `git worktree list` only reports a worktree's
// branch while it is still registered, so branches are collected first; and
// `git branch -D` refuses to delete a branch checked out in a live worktree, so
// the directories are removed and pruned (detaching the branches) before the
// branches are finally deleted.
//
// Failures that mean cleanup did not happen — a worktree directory that could not
// be removed, or a branch that could not be deleted — are accumulated and returned
// so the caller (atrium reset) reports an incomplete cleanup instead of a false
// success. Per-repo enumeration failures (`worktree list`/`prune`) are only logged:
// they are commonly a since-deleted project repo, whose physical worktree
// directories are still removed by the entries sweep below regardless.
func CleanupWorktrees(ctx context.Context, repoPaths []string) error {
	var tc teardown.Errors

	worktreesDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read worktree directory: %w", err)
	}

	worktreePrefix := resolvePath(worktreesDir) + string(filepath.Separator)
	repos := uniqueNonEmptyStrings(repoPaths)

	// Collect the session branch of every worktree that lives under our managed
	// worktrees directory, remembering which repo owns it. Worktree directories
	// are nested under a branch-prefix subdir, so match by path prefix rather
	// than by top-level directory name.
	//
	// Deliberately NOT underManagedWorktrees, though it answers the same question for
	// the other three callers. Its first comparison is a literal one, which this input
	// can never need and must not have: every path here comes from
	// `git worktree list --porcelain`, and git normalises what it reports — a worktree
	// added through a symlinked root is reported at the resolved path, before AND after
	// its directory is deleted (measured, not assumed). So both sides here are already on
	// the resolved basis and the single comparison below is sound, while adding the
	// literal one would newly accept a path that is literally inside the tree but
	// resolves outside it — and what this loop authorises is `git branch -D`.
	//
	// The literal comparison earns its place in underManagedWorktrees because its other
	// callers pass an Atrium-minted g.worktreePath, which is UNRESOLVED (/var/... on
	// macOS) and is compared against an equally unresolved root. Different input, so a
	// different test: one containment helper does not fit every caller here.
	type repoBranch struct{ repo, branch string }
	var branchesToDelete []repoBranch
	for _, repoPath := range repos {
		output, err := localGit(ctx, repoPath, "worktree", "list", "--porcelain")
		if err != nil {
			log.ErrorLog.Printf("failed to list worktrees for repo %s: %v", repoPath, err)
			continue
		}
		for wtPath, branch := range parseWorktreeList(output) {
			if branch == "" || !strings.HasPrefix(resolvePath(wtPath), worktreePrefix) {
				continue
			}
			branchesToDelete = append(branchesToDelete, repoBranch{repo: repoPath, branch: branch})
		}
	}

	// Remove the physical worktree directories before pruning and deleting
	// branches, so git no longer treats the branches as checked out.
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := removeOrphanedWorktreeDir(filepath.Join(worktreesDir, entry.Name())); err != nil {
			tc.Add(fmt.Errorf("failed to remove worktree dir %s: %w", entry.Name(), err))
		}
	}

	// Prune git's internal worktree tracking now that the directories are gone.
	for _, repoPath := range repos {
		if _, err := localGit(ctx, repoPath, "worktree", "prune"); err != nil {
			log.ErrorLog.Printf("failed to prune worktrees for repo %s: %v", repoPath, err)
		}
	}

	// Finally delete the session branches; they are no longer checked out.
	for _, rb := range branchesToDelete {
		if _, err := localGit(ctx, rb.repo, "branch", "-D", rb.branch); err != nil {
			tc.Add(fmt.Errorf("failed to delete branch %s in %s: %w", rb.branch, rb.repo, err))
		}
	}

	return tc.Err()
}
