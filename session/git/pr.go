package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PR status is fetched over the network (gh pr view), so unlike the local
// rev-list counts it is throttled with a much longer TTL: CI runs and human
// reviews change state on the order of minutes, not seconds. The background TTL
// caps gh invocations at ~1 per session per prCacheTTL even though the metadata
// tick fires every 500ms; the selected session refreshes more eagerly so the
// badge feels live when the user is looking at it.
const (
	prCacheTTL         = 25 * time.Second
	prCacheTTLSelected = 8 * time.Second
	// prNetworkTimeout bounds a single `gh pr view`. It is deliberately tighter
	// than gitNetworkTimeout (which sizes data-transferring push/fetch/sync): a
	// PR read is a small request, and this call runs inside the synchronous
	// metadata barrier (app_poll.go's wg.Wait), so a hung gh would otherwise
	// stall every session's status/diff refresh for the full budget. Capping it
	// here bounds that worst case while staying generous for a slow network.
	prNetworkTimeout = 10 * time.Second
)

// CIStatus is the rolled-up state of a PR's status checks. The zero value
// (CINone) means "no checks configured" and renders nothing.
type CIStatus int

// CI check rollup states, ordered from "nothing to show" to "needs action".
const (
	CINone    CIStatus = iota // no checks
	CIPending                 // at least one check still running, none failed
	CIPassing                 // all checks completed successfully
	CIFailing                 // at least one check failed
)

// ReviewStatus mirrors GitHub's reviewDecision. The zero value (ReviewNone)
// means no review has been requested/given and renders nothing.
type ReviewStatus int

// Review decision states, mirroring GitHub's reviewDecision field.
const (
	ReviewNone             ReviewStatus = iota
	ReviewRequired                      // "REVIEW_REQUIRED"
	ReviewChangesRequested              // "CHANGES_REQUESTED"
	ReviewApproved                      // "APPROVED"
)

// PRStatus is a per-session snapshot of the pull request for the session
// branch. The zero value (HasPR=false) is the normal "no PR yet" state and
// renders nothing — the same silent-degradation contract the diff stats use.
type PRStatus struct {
	HasPR bool
	// Pushed reports whether the branch has a remote-tracking ref (origin/<branch>).
	// It disambiguates the two ways HasPR can be false: a never-pushed branch
	// (Pushed=false) versus a pushed branch with no PR yet (Pushed=true) — the one
	// state where "create PR" is the right action. Set by PRStatus (a ref fact),
	// never by parsePRStatus (the pure JSON parser).
	Pushed    bool
	Number    int
	URL       string
	State     string // "OPEN" / "MERGED" / "CLOSED"
	IsDraft   bool
	CI        CIStatus
	Review    ReviewStatus
	Mergeable string // "MERGEABLE" / "CONFLICTING" / "UNKNOWN"
	// Check counts feed the fuller diff-tab line.
	ChecksPass    int
	ChecksFail    int
	ChecksPending int
	// fetchedAt timestamps the cache entry (zero = never fetched).
	fetchedAt time.Time
}

// PRStatus returns the pull-request snapshot for the session branch, throttled
// by a TTL cache. It is best-effort: any failure (no gh, no PR, no remote,
// unauthenticated) yields an empty PRStatus that renders nothing, exactly like
// an empty diff. selected picks the eager TTL so the badge stays live while the
// user is looking at the session.
//
// Three gates keep network pressure low, cheapest first:
//  1. TTL cache — most ticks return here with no I/O at all.
//  2. Local origin/<branch> ref check (no network) — an unpushed branch cannot
//     have a PR, so it never spends a gh call. This is the primary rate-limit
//     defense.
//  3. gh pr view — only reached for pushed branches whose cache has expired.
func (g *Worktree) PRStatus(ctx context.Context, selected bool) PRStatus {
	ttl := prCacheTTL
	if selected {
		ttl = prCacheTTLSelected
	}

	// Gate 1: serve from cache when fresh.
	g.prCacheMu.Lock()
	if cacheFresh(g.prCache.fetchedAt, ttl) {
		cached := g.prCache
		g.prCacheMu.Unlock()
		return cached
	}
	g.prCacheMu.Unlock()

	wt := g.snapshotWorktreePath()
	branch := g.currentBranch(wt)

	// Gate 2: no pushed branch => no PR possible. Cache the empty result so we
	// don't even run the local ref check again until the TTL lapses.
	if _, err := g.runGitCommand(wt, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err != nil {
		return g.storePRStatus(PRStatus{})
	}

	// Gate 2 passed: the branch is pushed. Every store below carries Pushed=true
	// so create gating can distinguish "pushed, no PR" from "never pushed".

	// Gate 3: the network call. Tag ctx with this worktree's gh account so the
	// poll's `gh pr view` reads the right account (matching what create/merge use).
	out, err := runGHPRView(g.ghContext(ctx), wt, branch)
	if err != nil {
		if isBenignGHError(err) {
			// No PR / no remote / gh missing: a legitimate, cacheable "nothing".
			return g.storePRStatus(PRStatus{Pushed: true})
		}
		// A genuine, possibly-transient failure (timeout, API 5xx): do NOT cache,
		// so the next eligible tick retries. Return the last good value if any.
		g.prCacheMu.Lock()
		cached := g.prCache
		g.prCacheMu.Unlock()
		return cached
	}

	status, perr := parsePRStatus(out)
	if perr != nil {
		g.prCacheMu.Lock()
		cached := g.prCache
		g.prCacheMu.Unlock()
		return cached
	}
	status.Pushed = true
	return g.storePRStatus(status)
}

// currentBranch resolves the branch to poll for a PR. It prefers git's actually
// checked-out HEAD over the stored branchName, so a worktree that was manually
// repointed to a different branch (e.g. the long-lived "Review" worktree checked
// out onto a feature branch) is polled for the branch the PR really lives on. It
// falls back to the stored name when the worktree path is empty, when git can't
// be reached (a paused session whose worktree is gone, CurrentBranchName -> ""),
// or on a detached HEAD (CurrentBranchName -> "HEAD").
func (g *Worktree) currentBranch(wt string) string {
	stored := g.GetBranchName()
	if wt == "" {
		return stored
	}
	if b := CurrentBranchName(g.baseContext(), wt); b != "" && b != "HEAD" {
		return b
	}
	return stored
}

// storePRStatus stamps and caches a freshly computed PR status, returning the
// stored value.
func (g *Worktree) storePRStatus(s PRStatus) PRStatus {
	s.fetchedAt = time.Now()
	g.prCacheMu.Lock()
	g.prCache = s
	g.prCacheMu.Unlock()
	return s
}

// invalidatePRCache clears the cached PR status so the next PRStatus call
// re-fetches. Called after a push, which may have just opened or updated the PR.
func (g *Worktree) invalidatePRCache() {
	g.prCacheMu.Lock()
	g.prCache = PRStatus{}
	g.prCacheMu.Unlock()
}

// MergeBlockedReason returns a short human reason the PR cannot be merged, or ""
// when a merge may be attempted. It blocks only on hard-negative signals; it
// deliberately does NOT require a review approval (a self-authored PR on a solo
// repo has Review == ReviewNone and must still be mergeable) and does not block on
// pending CI or an as-yet-unknown mergeability — gh, honoring branch protection,
// is the final authority for those and surfaces its own error on a refused merge.
func (s PRStatus) MergeBlockedReason() string {
	switch {
	case !s.HasPR:
		return "no open PR for this branch — push and open one first"
	case s.State != "OPEN":
		return "PR is already " + strings.ToLower(s.State)
	case s.IsDraft:
		return "PR is a draft — mark it ready for review first"
	case s.Mergeable == "CONFLICTING":
		return "PR has conflicts — resolve them first"
	case s.Review == ReviewChangesRequested:
		return "PR has requested changes — address them first"
	case s.CI == CIFailing:
		return "CI is failing — fix it before merging"
	default:
		return ""
	}
}

// CreateBlockedReason returns a short human reason a PR cannot be created for the
// branch, or "" when creation may be attempted. It mirrors MergeBlockedReason: a
// pure, snapshot-only decision the UI thread can make with no I/O. Creation is
// right in exactly one state — the branch is pushed and has no PR yet. An
// existing PR hands off to merge (m); an unpushed branch must be pushed (P) first
// rather than auto-pushed, keeping create a single-responsibility action.
func (s PRStatus) CreateBlockedReason() string {
	switch {
	case s.HasPR:
		return "this branch already has a PR — merge it with m"
	case !s.Pushed:
		return "branch isn't pushed yet — push it with P first"
	default:
		return ""
	}
}

// mergeArgv builds the `gh pr merge` argument vector for the branch. Squash is the
// repo convention. Kept pure so the argv is unit-testable without invoking gh.
func mergeArgv(branch string) []string {
	return []string{"pr", "merge", branch, "--squash"}
}

// createArgv builds the `gh pr create` argument vector for the branch. --fill
// derives the title and body from the branch's commits (no prompt); --head names
// the branch explicitly so creation is robust regardless of which worktree HEAD
// is checked out. --draft is appended when the PR should open as a draft. Kept
// pure so the argv is unit-testable without invoking gh.
func createArgv(branch string, draft bool) []string {
	argv := []string{"pr", "create", "--fill", "--head", branch}
	if draft {
		argv = append(argv, "--draft")
	}
	return argv
}

// runGH runs `gh argv…` in dir, capturing stdout and stderr. On failure it folds
// gh's trimmed stderr (its human-readable diagnostic) into the error, tagged with
// opName; on success it returns stdout. It is the gh analog of localGit: one place
// that builds the command, captures both streams, and formats the error. Callers
// that don't need stdout discard it. The caller owns the context's timeout — runGH
// stays timeout-agnostic because runGHPRView bounds itself with a different cap.
func runGH(ctx context.Context, dir, opName string, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", argv...)
	cmd.Dir = dir
	cmd.Env = ghEnv(ctx) // select the gh account from ctx (nil = inherit), see ghContext
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	recordCmd(cmd, "", start, stderr.Bytes(), err)
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %s: %w", opName, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// runGHMerge shells out to `gh pr merge` for the branch. Like runGHPRView it is a
// package var so tests can swap it out. gh infers owner/repo from the origin
// remote of dir, so no --repo is needed.
var runGHMerge = func(ctx context.Context, dir, branch string) error {
	_, err := runGH(ctx, dir, "gh pr merge", mergeArgv(branch)...)
	return err
}

// MergePR squash-merges the session branch's pull request via gh, then invalidates
// the PR cache so the next poll reflects the MERGED state. It runs gh from the
// origin repo (always present, even when the session is paused and its worktree
// removed), which infers the same owner/repo as the worktree would.
func (g *Worktree) MergePR() error {
	base := g.ghContext(g.baseContext())
	if err := checkGHCLI(base); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(base, gitNetworkTimeout)
	defer cancel()
	if err := runGHMerge(ctx, g.repoPath, g.GetBranchName()); err != nil {
		return err
	}
	g.invalidatePRCache()
	return nil
}

// runGHCreate shells out to `gh pr create` for the branch and returns its stdout
// (the new PR's URL on success). Like runGHMerge it is a package var so tests can
// swap it out. gh infers owner/repo from the origin remote of dir, so no --repo
// is needed.
var runGHCreate = func(ctx context.Context, dir string, argv []string) ([]byte, error) {
	return runGH(ctx, dir, "gh pr create", argv...)
}

// CreatePR opens a pull request for the session branch via gh, then invalidates
// the PR cache so the next poll reflects the new PR (flipping the badge/hint
// toward merge). draft selects whether the PR opens as a draft. It returns the
// new PR's number (0 if gh's output had no parseable number). It runs gh from the
// worktree, where --fill reliably reads the branch's commits; creation requires
// an active, pushed session, so the worktree is present.
func (g *Worktree) CreatePR(draft bool) (int, error) {
	base := g.ghContext(g.baseContext())
	if err := checkGHCLI(base); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(base, gitNetworkTimeout)
	defer cancel()
	out, err := runGHCreate(ctx, g.worktreePath, createArgv(g.GetBranchName(), draft))
	if err != nil {
		return 0, err
	}
	g.invalidatePRCache()
	return prNumberFromURL(string(out)), nil
}

// prNumberFromURL extracts the trailing PR number from a gh-created PR URL
// (e.g. "https://github.com/o/r/pull/42" => 42), returning 0 when the URL has no
// numeric tail. Pure, so the success-notice parsing is unit-testable.
func prNumberFromURL(url string) int {
	trimmed := strings.TrimRight(strings.TrimSpace(url), "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return 0
	}
	n, err := strconv.Atoi(trimmed[idx+1:])
	if err != nil {
		return 0
	}
	return n
}

// runGHPRWeb shells out to `gh pr view --web` for the branch, opening the PR in
// the default browser. Like runGHMerge it is a package var so tests can swap it
// out. gh infers owner/repo from the origin remote of dir, so no --repo is needed.
var runGHPRWeb = func(ctx context.Context, dir, branch string) error {
	_, err := runGH(ctx, dir, "gh pr view --web", "pr", "view", branch, "--web")
	return err
}

// OpenPRURL opens the session branch's pull request in the default browser. Like
// MergePR it runs gh from the origin repo (g.repoPath), which always exists even
// when the session is paused and its worktree removed, and resolves the PR by
// branch exactly as the PR poll does — so if the poll saw a PR, this opens that
// same one.
func (g *Worktree) OpenPRURL() error {
	base := g.ghContext(g.baseContext())
	if err := checkGHCLI(base); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(base, gitNetworkTimeout)
	defer cancel()
	if err := runGHPRWeb(ctx, g.repoPath, g.GetBranchName()); err != nil {
		return fmt.Errorf("failed to open PR for branch %s: %w", g.GetBranchName(), err)
	}
	return nil
}

// runGHPRView shells out to `gh pr view` for the branch and returns its JSON on
// stdout. It is a package var so tests can swap in canned output without a real
// gh on PATH. gh infers owner/repo from the worktree's origin remote (like the
// existing gh browse call), so no --repo is needed.
var runGHPRView = func(ctx context.Context, dir, branch string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, prNetworkTimeout) // keep: runGHPRView's own cap
	defer cancel()
	return runGH(ctx, dir, "gh pr view", "pr", "view", branch,
		"--json", "number,url,state,statusCheckRollup,reviewDecision,mergeable,isDraft")
}

// isBenignGHError reports whether a gh failure means "there is simply nothing to
// show" (no PR for the branch, no remote, gh not installed, or gh not
// authenticated) rather than a transient/real error. Benign failures are cached
// as an empty status; real ones are not, so they retry on the next tick.
//
// Auth failures count as benign because they are deterministic and non-transient:
// without caching them, every pushed session would re-spawn gh each TTL forever.
// The cost is that recovery after `gh auth login` lags by up to one TTL, which is
// an acceptable trade for not churning subprocesses on a steady-state condition.
func isBenignGHError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"no pull requests found",
		"no open pull requests",
		"no pull request",
		"no default remote",
		"no git remote",
		"executable file not found",
		"gh auth login", // not authenticated (setup prompt)
		"not logged in", // not authenticated (explicit)
		"authentication required",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// ghPRView mirrors the subset of `gh pr view --json` output we consume. The
// statusCheckRollup array is heterogeneous: CheckRun entries carry status +
// conclusion; legacy StatusContext entries carry only state.
type ghPRView struct {
	Number            int       `json:"number"`
	URL               string    `json:"url"`
	State             string    `json:"state"`
	IsDraft           bool      `json:"isDraft"`
	ReviewDecision    string    `json:"reviewDecision"`
	Mergeable         string    `json:"mergeable"`
	StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
}

type ghCheck struct {
	Status     string `json:"status"`     // CheckRun: QUEUED/IN_PROGRESS/COMPLETED
	Conclusion string `json:"conclusion"` // CheckRun: SUCCESS/FAILURE/...
	State      string `json:"state"`      // StatusContext: SUCCESS/PENDING/FAILURE/ERROR
}

// failConclusions are the CheckRun conclusions that count as a failing check.
var failConclusions = map[string]bool{
	"FAILURE":         true,
	"TIMED_OUT":       true,
	"CANCELLED":       true,
	"ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true,
	"STALE":           true,
}

// parsePRStatus parses `gh pr view --json` output into a PRStatus. It is a pure
// function (no I/O) so the rollup logic is exhaustively table-testable.
func parsePRStatus(jsonBytes []byte) (PRStatus, error) {
	var v ghPRView
	if err := json.Unmarshal(jsonBytes, &v); err != nil {
		return PRStatus{}, fmt.Errorf("parse gh pr view: %w", err)
	}

	s := PRStatus{
		HasPR:     v.Number > 0,
		Number:    v.Number,
		URL:       v.URL,
		State:     v.State,
		IsDraft:   v.IsDraft,
		Mergeable: v.Mergeable,
		Review:    parseReviewDecision(v.ReviewDecision),
	}

	for _, c := range v.StatusCheckRollup {
		switch {
		case c.Status != "": // CheckRun
			switch {
			case failConclusions[strings.ToUpper(c.Conclusion)]:
				s.ChecksFail++
			case strings.ToUpper(c.Status) != "COMPLETED":
				s.ChecksPending++
			default:
				s.ChecksPass++
			}
		case c.State != "": // legacy StatusContext
			switch strings.ToUpper(c.State) {
			case "SUCCESS":
				s.ChecksPass++
			case "FAILURE", "ERROR":
				s.ChecksFail++
			default: // PENDING, EXPECTED
				s.ChecksPending++
			}
		}
	}

	switch {
	case s.ChecksPass+s.ChecksFail+s.ChecksPending == 0:
		s.CI = CINone
	case s.ChecksFail > 0:
		s.CI = CIFailing
	case s.ChecksPending > 0:
		s.CI = CIPending
	default:
		s.CI = CIPassing
	}

	return s, nil
}

func parseReviewDecision(d string) ReviewStatus {
	switch strings.ToUpper(d) {
	case "APPROVED":
		return ReviewApproved
	case "CHANGES_REQUESTED":
		return ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return ReviewRequired
	default:
		return ReviewNone
	}
}
