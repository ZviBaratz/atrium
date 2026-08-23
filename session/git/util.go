package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ghConfigDirKey is the context key carrying a per-worktree GH_CONFIG_DIR to the
// gh subprocess helpers (runGH, checkGHCLI). It rides the context — rather than a
// parameter on every swap-for-tests seam — so adding account routing changes no
// seam signature. Obtain a tagged context via Worktree.ghContext.
type ghConfigDirKey struct{}

// withGHConfigDir returns ctx tagged with dir, or ctx unchanged when dir is ""
// (the "inherit the ambient gh account" case — inject nothing).
func withGHConfigDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, ghConfigDirKey{}, dir)
}

// ghConfigDirFromContext returns the GH_CONFIG_DIR carried by ctx, or "" if none.
func ghConfigDirFromContext(ctx context.Context) string {
	d, _ := ctx.Value(ghConfigDirKey{}).(string)
	return d
}

// ghEnv returns the environment for a gh subprocess: the parent env plus a
// GH_CONFIG_DIR override when ctx carries one, or nil to inherit os.Environ
// unchanged. A nil result is intentional — exec.Cmd treats a nil Env as "inherit
// the current process env", preserving the pre-routing behavior exactly.
func ghEnv(ctx context.Context) []string {
	if d := ghConfigDirFromContext(ctx); d != "" {
		return append(os.Environ(), "GH_CONFIG_DIR="+d)
	}
	return nil
}

// sanitizeBranchName transforms an arbitrary string into a Git branch name friendly string.
// Note: Git branch names have several rules, so this function uses a simple approach
// by allowing only a safe subset of characters.
func sanitizeBranchName(s string) string {
	// Convert to lower-case
	s = strings.ToLower(s)

	// Replace spaces with a dash
	s = strings.ReplaceAll(s, " ", "-")

	// Remove any characters not allowed in our safe subset.
	// Here we allow: letters, digits, dash, underscore, slash, and dot.
	re := regexp.MustCompile(`[^a-z0-9\-_/.]+`)
	s = re.ReplaceAllString(s, "")

	// Replace multiple dashes with a single dash (optional cleanup)
	reDash := regexp.MustCompile(`-+`)
	s = reDash.ReplaceAllString(s, "-")

	// Trim leading and trailing dashes or slashes to avoid issues
	s = strings.Trim(s, "-/")

	return s
}

// checkGHCLI checks if GitHub CLI is installed and configured. It is a package
// var so tests can stub the gh-availability gate without a real, authenticated
// gh on PATH.
var checkGHCLI = func(ctx context.Context) error {
	// Check if gh is installed
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("GitHub CLI (gh) is not installed. Please install it first")
	}

	// Check if gh is authenticated (may hit the network to validate the token)
	ctx, cancel := context.WithTimeout(ctx, gitNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	cmd.Env = ghEnv(ctx) // validate the account the PR call will actually use
	start := time.Now()
	err := cmd.Run()
	recordCmd(cmd, "", start, nil, err)
	if err != nil {
		return fmt.Errorf("GitHub CLI is not configured. Please run 'gh auth login' first")
	}

	return nil
}

// localGit runs `git -C dir args...` capped at gitLocalTimeout and returns its
// trimmed stdout. It is the package-level analog of Worktree.runGitCommand for the
// helpers here that hold a context and a path but no *Worktree; deriving the
// timeout and building the command once keeps a local-git invocation defined in a
// single place. Unlike runGitCommand's CombinedOutput, stderr is left out of the
// result so a git diagnostic can't corrupt a parsed value; callers that only care
// whether the command succeeded ignore the string and check err.
func localGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	start := time.Now()
	out, err := cmd.Output()
	recordCmd(cmd, "", start, nil, err)
	return strings.TrimSpace(string(out)), err
}

// IsGitRepo checks if the given path is within a git repository
func IsGitRepo(ctx context.Context, path string) bool {
	isRepo, _ := ProbeGitRepo(ctx, path)
	return isRepo
}

// ProbeGitRepo answers IsGitRepo's question and, separately, whether git was in a
// position to answer it: known is false when the probe could not run to a verdict.
//
// IsGitRepo folds the two together — every failure reads as "not a repo" — which is
// what a live form label wants, because a human is looking at it and can cancel. A
// headless caller cannot: for `atrium new` this verdict alone decides whether the
// agent gets an isolated worktree or runs loose in the caller's own checkout, so it
// needs to tell "git says no" from "git did not say".
//
// The first discriminator is how the process ended. git exiting with a status ran
// and answered, so it is a candidate for known. Anything else did not: an
// *exec.Error for git off PATH mid-upgrade, a fork failure under memory pressure,
// or — the case a bare errors.As would misread — a kill by exec.CommandContext when
// gitLocalTimeout expires or ctx is cancelled, which arrives as an *exec.ExitError
// whose ExitCode() is -1 because a signalled process carries no status.
//
// The exit status is not sufficient on its own, and this is the second
// discriminator. "fatal: not a git repository (or any of the parent directories):
// .git" with status 128 is what git prints for a directory that has no repository
// AND for a real repository whose .git it cannot read — the same sentence, byte for
// byte, and the same status. So the message and the code together cannot establish
// the negative, which is the direction with teeth: this verdict is what licenses
// running the agent directly in path instead of in a worktree.
//
// A .git entry that is present while git says there is none is that contradiction,
// and it is the one thing available that git is not the source of. os.Lstat answers
// it without opening anything — it reads the entry out of the parent directory, so a
// .git at mode 000 or a dangling .git symlink still answers present. Present plus
// "no repository here" is reported as not established; absent corroborates the
// negative, which is what keeps a repo somebody deleted answerable in one tick
// (app.recheckAdoption) rather than held for the whole spool horizon.
//
// The residue is a directory holding something named .git that is not a gitdir at
// all, which now reads as unknown rather than as a plain directory. That refuses a
// headless create where it used to make a direct session, and the trade is deliberate:
// a refusal is a receipt the caller can retry against, while a wrong direct session
// has an agent editing the user's own checkout before anyone can look.
func ProbeGitRepo(ctx context.Context, path string) (isRepo, known bool) {
	_, err := localGit(ctx, path, "rev-parse", "--show-toplevel")
	if err == nil {
		return true, true
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() < 0 {
		return false, false
	}
	if _, statErr := os.Lstat(filepath.Join(path, ".git")); statErr == nil {
		return false, false
	}
	return false, true
}

// CurrentBranchName returns the branch HEAD points at in the repo containing path,
// "HEAD" for a detached HEAD (keeping --abbrev-ref's convention), or "" when the path
// is not a git repo (best-effort, like IsGitRepo). `branch --show-current` (rather than
// `rev-parse --abbrev-ref HEAD`) so an unborn HEAD — a fresh init with no commits yet —
// still resolves to its branch name.
func CurrentBranchName(ctx context.Context, path string) string {
	branch, err := localGit(ctx, path, "branch", "--show-current")
	if err != nil {
		return ""
	}
	if branch == "" {
		return "HEAD" // --show-current prints nothing when detached
	}
	return branch
}

// titleHash returns a short, stable hex digest of a raw title, used to mint a
// unique branch slug when the title itself sanitizes to nothing (issue #187).
func titleHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// BranchNameForSession derives the git branch a session titled title owns:
// sanitizeBranchName(prefix + title). It is the single source of the slug —
// the worktree layer mints it and the new-session form predicts it for the
// duplicate check, so the two can never drift.
//
// When the title contributes nothing to the slug (a CJK title, emoji, or
// punctuation-only input all sanitize to ""), prefix+title would collapse to
// just the prefix — so distinct titles would mint the same branch and the form's
// duplicate check would reject the second with a misleading "branch already used"
// error. In that case a short deterministic hash of the raw title is substituted
// so each session still gets a unique, non-degenerate branch.
func BranchNameForSession(prefix, title string) string {
	name := sanitizeBranchName(prefix + title)
	if sanitizeBranchName(title) == "" {
		name = sanitizeBranchName(prefix + "session-" + titleHash(title))
	}
	return name
}

// LocalBranchExists reports whether branch exists as a local head in the repo at
// repoPath. It is an exact ref lookup, deliberately not SearchBranches, whose results
// are capped and merged with origin/ names.
//
// Over LookupLocalBranchTip, like LookupLocalBranch and for the same reason: the exact
// refname comparison was the whole body of all three. What it drops on the way past is
// the error, which is this function's entire remaining difference from LookupLocalBranch
// — see there for which callers may do that and which may not.
func LocalBranchExists(ctx context.Context, repoPath, branch string) bool {
	tip, _ := LookupLocalBranchTip(ctx, repoPath, branch)
	return tip != ""
}

// LookupLocalBranch answers the same question as LocalBranchExists but keeps "git could
// not be asked" out of the answer.
//
// LocalBranchExists is `err == nil`, and show-ref cannot do better: it exits non-zero
// both for a ref that is absent and for a repo it cannot read, so every failure — git
// off PATH mid-upgrade, a fork failure under memory pressure, a cold-repo timeout, a
// cancelled context — is indistinguishable from "no such branch". That is harmless
// where the answer only gates a creation (a false "absent" there just lets the gate
// through to git, which fails honestly a moment later) and harmful where a caller acts
// destructively on the negative, which is why the create-recovery path (#716) uses this
// instead: reading a git failure as "nothing was built" would spend the one recovery a
// stranded request gets and leave the orphan behind permanently.
//
// for-each-ref rather than show-ref, because it exits 0 with empty output for a ref that
// is simply not there and non-zero only when the repository itself could not be read.
// The pattern matches at path boundaries, so refs/heads/<branch>/sub would match too;
// the answer is an exact line comparison rather than "any output".
func LookupLocalBranch(ctx context.Context, repoPath, branch string) (bool, error) {
	// Over LookupLocalBranchTip rather than beside it: one subprocess either way, and the
	// exact-refname comparison the pattern makes necessary (see there) was the whole body of
	// this function duplicated. A branch that exists always has a tip, so "no sha" and "no
	// branch" are the same answer.
	tip, err := LookupLocalBranchTip(ctx, repoPath, branch)
	return tip != "", err
}

// LookupLocalBranchTip returns the commit refs/heads/<branch> points at in the repo at
// repoPath. An empty sha with a nil error means the branch is not there; an error means
// the repository could not be read.
//
// It answers LookupLocalBranch's question and one more, in the same subprocess: a caller
// that has to know whether a branch is still the one it vetted needs existence and
// identity together, and asking twice would let the two answers describe different
// instants. That is why app.recheckAdoption, which is exactly that caller, reaches for this
// one alone. The create recovery reaches for both — LookupLocalBranch to judge the claim
// and this to pin the branch it hands on — because those two readings are deliberately of
// different instants, separated by the worktree release in between.
//
// The identity matters because a branch NAME is not evidence. The create-adoption path
// (#731) skips a load-bearing branch gate on the strength of a reconcile's finding, and
// the request can sit queued for a long while afterwards; a branch deleted and recreated
// in that window has the same name and somebody else's commits, and adopting it is
// silent. for-each-ref for LookupLocalBranch's reason — it exits 0 with empty output for
// a ref that is simply absent, and non-zero only when the repository itself could not be
// read, so "no such branch" never arrives as a failure.
func LookupLocalBranchTip(ctx context.Context, repoPath, branch string) (string, error) {
	ref := "refs/heads/" + branch
	out, err := localGit(ctx, repoPath, "for-each-ref", "--format=%(objectname) %(refname)", ref)
	if err != nil {
		return "", fmt.Errorf("failed to look up branch %q in %s: %w", branch, repoPath, err)
	}
	// The pattern matches at path boundaries, so refs/heads/<branch>/sub answers too, and
	// sorts ahead of the branch itself. Taking the first line would report a sub-ref's sha
	// as this branch's, so the refname is compared exactly. Both siblings above read their
	// answer out of this loop, which is why it is the only place the comparison is made.
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == ref {
			return sha, nil
		}
	}
	return "", nil
}

// LocalBranchSet returns the local branches of the repo at repoPath as a set, keyed the
// way BranchNameForSession spells one. An error means the repository could not be read,
// on LookupLocalBranch's terms — never that it has none.
//
// One subprocess for the whole question, where the siblings above answer about one name.
// A caller walking a numbered series for the first free name has up to VariantTitleScan
// candidates and asks the same unchanging question of each, so per-name lookups cost it a
// fork and a gitLocalTimeout apiece; main.planVariantTitles is that caller. A caller with
// one name in hand should still use LookupLocalBranch, which reads one ref instead of
// every head.
//
// %(refname) rather than %(refname:short), which abbreviates against the rest of the ref
// namespace and can hand back a name that is only unambiguous today. The caller is
// comparing against a name it built from a prefix, so the full ref is cut to size here
// and nothing is left for git to decide.
func LocalBranchSet(ctx context.Context, repoPath string) (map[string]bool, error) {
	out, err := localGit(ctx, repoPath, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("failed to list the branches of %s: %w", repoPath, err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), "refs/heads/")
		if ok && name != "" {
			names[name] = true
		}
	}
	return names, nil
}

// RepoGroupKey predicts the repo-group key the session list will file a session
// under when created from path: the repo root's basename when path is inside a
// git repo (even a subdirectory), else the directory's own basename (how direct
// sessions group). Best-effort: any git failure falls back to the basename.
func RepoGroupKey(ctx context.Context, path string) string {
	if root, err := findGitRepoRoot(ctx, path); err == nil {
		return filepath.Base(root)
	}
	return filepath.Base(path)
}

func findGitRepoRoot(ctx context.Context, path string) (string, error) {
	out, err := localGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("failed to find Git repository root from path: %s", path)
	}
	return out, nil
}

// GetRemoteURL returns the origin remote URL for the repository containing path,
// or "" when there is no origin remote or path is not a git repo (best-effort,
// like CurrentBranchName). Used to route a worktree to a Claude Code account.
func GetRemoteURL(ctx context.Context, path string) string {
	out, err := localGit(ctx, path, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return out
}
