package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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
	HasPR     bool
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
	if !g.prCache.fetchedAt.IsZero() && time.Since(g.prCache.fetchedAt) < ttl {
		cached := g.prCache
		g.prCacheMu.Unlock()
		return cached
	}
	g.prCacheMu.Unlock()

	wt := g.snapshotWorktreePath()
	branch := g.GetBranchName()

	// Gate 2: no pushed branch => no PR possible. Cache the empty result so we
	// don't even run the local ref check again until the TTL lapses.
	if _, err := g.runGitCommand(wt, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err != nil {
		return g.storePRStatus(PRStatus{})
	}

	// Gate 3: the network call.
	out, err := runGHPRView(ctx, wt, branch)
	if err != nil {
		if isBenignGHError(err) {
			// No PR / no remote / gh missing: a legitimate, cacheable "nothing".
			return g.storePRStatus(PRStatus{})
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
	return g.storePRStatus(status)
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

// runGHPRView shells out to `gh pr view` for the branch and returns its JSON on
// stdout. It is a package var so tests can swap in canned output without a real
// gh on PATH. gh infers owner/repo from the worktree's origin remote (like the
// existing gh browse call), so no --repo is needed.
var runGHPRView = func(ctx context.Context, dir, branch string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, prNetworkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch,
		"--json", "number,url,state,statusCheckRollup,reviewDecision,mergeable,isDraft")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("gh pr view: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
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
