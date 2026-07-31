package git

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// errBaseCommitNotSet is returned (via DiffStats.Error) when a diff is requested
// before the worktree's base commit has been resolved — the brief not-fully-set-up
// window after creation, and legacy sessions persisted without one. Callers
// (Instance.UpdateDiffStats, the metadata-poll handler) recognize it by this exact
// message and treat it as "no diff yet" rather than a hard error, so the wording is
// a cross-package contract; running `git diff ""` instead yields a confusing failure.
var errBaseCommitNotSet = errors.New("base commit SHA not set")

// revListCacheTTL is how long the rev-list commit counts (ahead/behind) returned
// by computeRepoStats are reused without re-running git rev-list.
// Commits-ahead/behind change only on an explicit commit, so a 3-second cache
// is imperceptible in normal use and cuts rev-list subprocess pressure by ~83%
// at a 500ms tick interval (1 run per 6 ticks rather than 1 per tick).
const revListCacheTTL = 3 * time.Second

// dirtyCacheTTL is how long the git-status dirty flag is cached. A 1-second
// window halves subprocess pressure at the 500ms tick (1 run per 2 ticks) at
// the cost of up to 1s lag before the pencil glyph updates in session rows.
const dirtyCacheTTL = 1 * time.Second

// repoStatsEntry holds the cached results from a single computeRepoStats run.
// commits-ahead/behind and unpushed are cached for revListCacheTTL; dirty is cached
// for dirtyCacheTTL (shorter, because it reflects uncommitted file edits). unpushed
// shares the commits clock deliberately: it is computed by the same revListCounts
// call and moves on the same events (a commit, or a push), so it needs no TTL field
// of its own. Atrium's own CommitChanges/PushChanges invalidate the entry outright; a
// push the agent runs itself inside the session does not, and is simply picked up
// when the TTL lapses.
type repoStatsEntry struct {
	commits         int
	behind          int
	unpushed        int
	dirty           bool
	dirtyComputedAt time.Time
	computedAt      time.Time
}

// cacheFresh reports whether a cache entry computed at the given time is still
// within its TTL. A zero time means "never computed" and is always stale.
func cacheFresh(computedAt time.Time, ttl time.Duration) bool {
	return !computedAt.IsZero() && time.Since(computedAt) < ttl
}

// DiffStats holds statistics about the changes in a diff
type DiffStats struct {
	// Content is the full diff content
	Content string
	// Added is the number of added lines
	Added int
	// Removed is the number of removed lines
	Removed int
	// FilesChanged is the number of files touched relative to the base commit
	FilesChanged int
	// Commits is the number of commits the session branch is ahead of the base
	// (i.e. committed progress made within the session)
	Commits int
	// Unpushed is the number of commits reachable from the session branch but not
	// from any origin remote-tracking ref — the commits that a kill's `git branch -D`
	// would actually destroy. Unlike Commits (ahead of base), it is 0 for a fully
	// pushed branch, whose work survives on the remote regardless of the session.
	// It is bounded by the base, so a repo with no origin degrades to Commits rather
	// than counting the whole history.
	Unpushed int
	// Behind is the number of commits the base ref has advanced since the session
	// forked from it. It is 0 when unknown (e.g. the base ref was not persisted).
	Behind int
	// Dirty reports whether the worktree has uncommitted changes
	Dirty bool
	// Error holds any error that occurred during diff computation
	// This allows propagating setup errors (like missing base commit) without breaking the flow
	Error error
}

// IsEmpty reports whether the diff has no changes at all (no added or removed
// lines and no content).
func (d *DiffStats) IsEmpty() bool {
	return d.Added == 0 && d.Removed == 0 && d.Content == ""
}

// diffWith runs the scaffolding shared by Diff and DiffNumstat and delegates only
// the differing middle to fill. It checks for a base commit (surfacing the
// recognizable errBaseCommitNotSet rather than running `git diff ""`), snapshots
// the worktree path under the lock so a concurrent deep Rename — which moves the
// worktree and swaps the field — can't tear the read (subsequent git calls run
// against the local snapshot without holding the lock), surfaces untracked files
// best-effort (see intentAddUntracked; its error is intentionally not
// propagated), then runs fill to populate the diff stats (the line/file counts,
// plus the full content for Diff). On a base-commit or fill error it returns early
// with stats.Error set and no repo stats; on success it fills the
// commit/behind/dirty counters before returning.
func (g *Worktree) diffWith(fill func(wt string, stats *DiffStats) error) *DiffStats {
	return g.diffFrom(true, fill)
}

// diffFrom is diffWith with the intent-to-add step made optional.
//
// Staging untracked files is only there so `git diff <base>` can see them, so a
// caller that computes no diff content has no reason to pay for it — and it is not
// free: it walks the whole worktree with `git ls-files --others`, which on a repo
// carrying node_modules or a build tree is the most expensive thing here.
//
// Skipping it does not change Dirty: computeRepoStats derives that from
// `git status --porcelain`, which reports untracked files as `??` whether or not
// they have been intent-added.
func (g *Worktree) diffFrom(intentAdd bool, fill func(wt string, stats *DiffStats) error) *DiffStats {
	stats := &DiffStats{}

	if g.GetBaseCommitSHA() == "" {
		stats.Error = errBaseCommitNotSet
		return stats
	}

	wt := g.snapshotWorktreePath()
	if intentAdd {
		g.intentAddUntracked(wt)
	}

	if err := fill(wt, stats); err != nil {
		stats.Error = err
		return stats
	}

	g.computeRepoStats(stats, wt)
	return stats
}

// RepoStats returns only the branch-level counters — Commits, Behind, Unpushed and
// Dirty — leaving Added/Removed/FilesChanged/Content zero.
//
// It is the cheap half of Diff: those four fields come from `git rev-list` and
// `git status --porcelain`, both already TTL-cached here, so most calls fork
// nothing at all. What it omits is the pair that is not cached and not cheap — the
// untracked-file walk and `git diff` itself.
//
// The split is drawn here because every number that reaches a destructive
// confirmation is on this side of it: the kill dialog's "uncommitted changes /
// N unpushed commits" is built from Dirty and Unpushed alone (killDataWarning).
// Commits and Behind are display-only, and are kept simply because the same two
// commands already yield them. Nothing the diff computes — the +/- chip and the
// diff pane — is ever read before deleting a branch. A caller that wants to poll
// an idle session less often can therefore keep every number a user acts on
// fresh, and let only the cosmetic ones age.
func (g *Worktree) RepoStats() *DiffStats {
	return g.diffFrom(false, func(string, *DiffStats) error { return nil })
}

// Diff returns the git diff between the worktree and the base branch along with statistics
func (g *Worktree) Diff() *DiffStats {
	return g.diffWith(func(wt string, stats *DiffStats) error {
		content, err := g.runGitCommand(wt, "--no-pager", "diff", g.GetBaseCommitSHA())
		if err != nil {
			return err
		}
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				stats.Added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				stats.Removed++
			}
		}
		stats.Content = content
		stats.FilesChanged = countDiffFiles(content)
		return nil
	})
}

// DiffNumstat returns the added/removed line counts between the worktree and the
// base branch without loading the full diff content into memory. Use this when
// only the summary counts are needed (e.g. for unselected instances in the list).
func (g *Worktree) DiffNumstat() *DiffStats {
	return g.diffWith(func(wt string, stats *DiffStats) error {
		out, err := g.runGitCommand(wt, "--no-pager", "diff", "--numstat", g.GetBaseCommitSHA())
		if err != nil {
			return err
		}
		stats.Added, stats.Removed, stats.FilesChanged = parseNumstat(out)
		return nil
	})
}

// snapshotWorktreePath reads worktreePath under the read lock so background diff
// computation can't race the in-place field swap a deep Rename performs.
func (g *Worktree) snapshotWorktreePath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.worktreePath
}

// intentAddUntracked makes the agent's untracked files visible in `git diff <base>`
// by staging just those paths as intent-to-add (`git add -N`). The old code ran an
// unconditional `add -N .` on every 500ms poll tick, which rewrites the index even when
// the worktree is clean — needless churn that also races the agent's own git operations
// on the shared index. This first lists untracked paths with a read-only
// `git ls-files --others` and only runs `add -N` when there is something to add, so the
// common steady state costs one read-only tree walk and no index write.
//
// ls-files (rather than parsing `git status`) is deliberate: it is not affected by
// `status.showUntrackedFiles`, so new files still surface even when the user has
// configured `git status` to hide untracked files — matching the original `add .`, which
// ignored that setting (a status-based scan would silently drop them). `--exclude-standard`
// applies .gitignore/exclude exactly like `add -N .` did, and `--directory` collapses a
// wholly-untracked directory to a single `dir/` entry that `add -N -- dir/` recurses into,
// bounding the argument list. `--no-empty-directory` then drops the entries that hold
// nothing addable: git cannot track an empty directory, so listing one only ever produces
// a no-op `add -N`. Without it a directory containing solely ignored content — the parent
// dirs seedLocalPaths creates for a nested link_paths entry, for instance — is reported as
// untracked forever, which would run the add on every tick for the life of the session and
// defeat the "only when there is something to add" guarantee above.
//
// It is best-effort and never blocks the diff: on failure the untracked files are simply
// absent from this tick (tracked changes still render) and the next poll retries. In
// particular `add -N` is allowed to fail silently on the common race where the agent
// creates and then deletes an untracked temp/swap file between the listing and the add —
// failing the whole command there would otherwise blank the diff on every editor write.
func (g *Worktree) intentAddUntracked(wt string) {
	paths := g.untrackedPathsToIntentAdd(wt)
	if len(paths) == 0 {
		return
	}
	// Tolerate a path that vanished between the listing and the add (see above); the
	// next poll recovers, so a transient race must not surface as a diff error.
	_, _ = g.runGitCommand(wt, append([]string{"add", "-N", "--"}, paths...)...)
}

// untrackedPathsToIntentAdd lists the paths intentAddUntracked will stage, and is
// empty in the steady state — that emptiness is what skips the index write, so it
// is asserted on directly by tests rather than inferred from index residue (an
// `add -N` of a directory holding only ignored content leaves none either way).
func (g *Worktree) untrackedPathsToIntentAdd(wt string) []string {
	// -z emits raw, NUL-terminated paths so names with spaces/tabs/unicode round-trip
	// through `add -N --` without the C-quoting git applies by default.
	out, err := g.runGitCommand(wt, "ls-files", "--others", "--exclude-standard", "--directory", "--no-empty-directory", "-z")
	if err != nil {
		log.WarningLog.Printf("intent-add: list untracked files failed: %v", err)
		return nil
	}
	out = strings.TrimRight(out, "\x00")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\x00")
}

// computeRepoStats fills in the commit/behind/dirty fields on stats. It is
// best-effort: any failure leaves the corresponding field at its zero value and
// never sets stats.Error, so a hiccup in a cosmetic counter can't blank the diff.
// wt is the worktree path snapshotted by the caller under the read lock; baseRef and
// baseCommitSHA are not mutated by Rename, so they need no g.mu — revListCounts reads
// them through baseMu (their getters) instead, which guards the setup-time writes.
//
// The rev-list result (commits ahead/behind) is cached for revListCacheTTL because
// it only changes on an explicit commit and the subprocess is relatively expensive on
// large histories. The dirty flag (git status --porcelain) is cached for the shorter
// dirtyCacheTTL — a 1-second window halves subprocess count at the 500ms tick.
func (g *Worktree) computeRepoStats(stats *DiffStats, wt string) {
	// Serve rev-list from cache when fresh; otherwise re-run and update.
	g.statsCacheMu.Lock()
	if cacheFresh(g.statsCache.computedAt, revListCacheTTL) {
		stats.Commits = g.statsCache.commits
		stats.Behind = g.statsCache.behind
		stats.Unpushed = g.statsCache.unpushed
		g.statsCacheMu.Unlock()
	} else {
		g.statsCacheMu.Unlock()

		// Only cache a successful result. A transient rev-list failure must not be
		// stored, or the zero it leaves would suppress retries for the whole TTL;
		// leaving the cache empty makes the next tick recompute immediately.
		if commits, behind, unpushed, ok := g.revListCounts(wt); ok {
			stats.Commits = commits
			stats.Behind = behind
			stats.Unpushed = unpushed

			// Update only the rev-list fields: a whole-struct replace would wipe
			// the dirty cache, forcing a redundant git status on the next tick.
			g.statsCacheMu.Lock()
			g.statsCache.commits = commits
			g.statsCache.behind = behind
			g.statsCache.unpushed = unpushed
			g.statsCache.computedAt = time.Now()
			g.statsCacheMu.Unlock()
		}
	}

	// Cache the dirty flag for dirtyCacheTTL: a 1-second window halves git-status
	// subprocess calls at a 500ms tick, at the cost of up to 1s lag before the
	// pencil glyph reflects a new edit in session rows.
	// Inline the check against the snapshotted path rather than calling IsDirty
	// (which reads g.worktreePath) so this background path never touches the mutable field.
	g.statsCacheMu.Lock()
	if cacheFresh(g.statsCache.dirtyComputedAt, dirtyCacheTTL) {
		stats.Dirty = g.statsCache.dirty
		g.statsCacheMu.Unlock()
	} else {
		g.statsCacheMu.Unlock()
		if out, err := g.runGitCommand(wt, "status", "--porcelain"); err == nil {
			dirty := len(out) > 0
			stats.Dirty = dirty
			g.statsCacheMu.Lock()
			g.statsCache.dirty = dirty
			g.statsCache.dirtyComputedAt = time.Now()
			g.statsCacheMu.Unlock()
		}
	}
}

// revListCounts returns the session's commits-ahead, commits-behind (when the base
// ref is known), and unpushed count by shelling out to git rev-list. ok is false only
// when a subprocess that was attempted failed (or its output couldn't be parsed); the
// no-base case returns (0, 0, 0, true) because zero is the correct, cacheable answer
// rather than an error. The split lets computeRepoStats cache good results while
// skipping bad ones.
func (g *Worktree) revListCounts(wt string) (commits, behind, unpushed int, ok bool) {
	// A single rev-list gives both "ahead" (session commits) and "behind" (base
	// advanced) when the base ref is known; fall back to ahead-only otherwise.
	// Read baseRef/baseCommitSHA through baseMu (via their getters): setup writes
	// both on the Start goroutine while this can run from the poll loop.
	if baseRef := g.GetBaseRef(); baseRef != "" {
		out, err := g.runGitCommand(wt, "rev-list", "--left-right", "--count", baseRef+"...HEAD")
		if err != nil {
			return 0, 0, 0, false
		}
		behind, ahead, parsed := parseLeftRightCount(out)
		if !parsed {
			return 0, 0, 0, false
		}
		return ahead, behind, g.unpushedCount(wt, baseRef, ahead), true
	}
	if baseCommitSHA := g.GetBaseCommitSHA(); baseCommitSHA != "" {
		out, err := g.runGitCommand(wt, "rev-list", "--count", baseCommitSHA+"..HEAD")
		if err != nil {
			return 0, 0, 0, false
		}
		ahead, aerr := strconv.Atoi(strings.TrimSpace(out))
		if aerr != nil {
			return 0, 0, 0, false
		}
		return ahead, 0, g.unpushedCount(wt, baseCommitSHA, ahead), true
	}
	// No base to compare against: zero is a legitimate, cacheable result.
	return 0, 0, 0, true
}

// unpushedCount returns how many of the session's ahead commits are reachable from
// no origin remote-tracking ref — the ones a kill's `git branch -D` would actually
// destroy. base is whatever revListCounts measured ahead against, and ahead is that
// count; both are passed in so this never re-derives them.
//
// The range is bounded by base rather than walking from HEAD, which matters in two
// ways. A repo with no origin has nothing to exclude, so an unbounded walk would
// return the entire history; bounded, it collapses to ahead — correct, since none of
// that work is pushed. And excluding --remotes=origin (rather than diffing against
// origin/<branch>) means a never-pushed branch is counted rather than erroring: git
// fatals on `origin/<branch>..HEAD` when that ref does not exist, and this function's
// failure path would then read as "nothing at risk" on precisely the branch that has
// everything at risk.
//
// On any failure it returns ahead, not zero: we know that many commits exist and
// could not prove any of them are pushed, so assume none are. Over-warning is the
// safe direction — this is the input to a data-loss warning. A remote not named
// origin (a fork/upstream setup) lands here too: nothing matches --remotes=origin, so
// the whole ahead-range counts as at-risk. Over-warning again, and consistent with
// the rest of the codebase, which hardcodes origin.
//
// It reads only local remote-tracking refs, never the network, so the answer is as
// fresh as the last fetch. A ref that is stale-behind over-warns (safe); the one
// unsafe direction is a ref left pointing at a branch since deleted on the remote,
// which no local-only check can detect.
func (g *Worktree) unpushedCount(wt, base string, ahead int) int {
	// Not ahead of base means every commit on this branch is also reachable from
	// base, which survives `git branch -D` — so nothing is at risk regardless of
	// whether base itself is pushed, and the common idle case pays no subprocess.
	if ahead <= 0 {
		return 0
	}
	out, err := g.runGitCommand(wt, "rev-list", "--count", base+"..HEAD", "--not", "--remotes=origin")
	if err != nil {
		return ahead
	}
	n, perr := strconv.Atoi(strings.TrimSpace(out))
	if perr != nil {
		return ahead
	}
	return n
}

// invalidateStatsCache clears the cached rev-list counts and dirty flag so the
// next computeRepoStats call re-runs both git rev-list and git status. Call this
// after any operation that alters the commit graph or worktree contents (commit,
// push) so the ahead/behind counts and the dirty glyph update on the very next
// tick rather than waiting for the TTLs to expire.
func (g *Worktree) invalidateStatsCache() {
	g.statsCacheMu.Lock()
	g.statsCache = repoStatsEntry{}
	g.statsCacheMu.Unlock()
}

// parseNumstat sums the added/removed columns and counts the files from
// `git diff --numstat` output. Each line is formatted as <added>\t<removed>\t<path>.
// Binary files report "-\t-\t<path>": they count toward files but not line totals.
func parseNumstat(out string) (added int, removed int, files int) {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		files++
		a, aerr := strconv.Atoi(fields[0])
		r, rerr := strconv.Atoi(fields[1])
		if aerr != nil || rerr != nil {
			// Binary files ("-\t-\t<path>") have no line counts but still count as a file.
			continue
		}
		added += a
		removed += r
	}
	return added, removed, files
}

// parseLeftRightCount parses `git rev-list --left-right --count <baseRef>...HEAD`
// output, formatted as "<behind>\t<ahead>". The left side is commits reachable from
// baseRef but not HEAD (the base advanced), the right side is commits reachable from
// HEAD but not baseRef (session progress). ok is false when the output is missing or
// malformed, so callers can fall back without surfacing an error.
func parseLeftRightCount(out string) (behind int, ahead int, ok bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return 0, 0, false
	}
	behind, berr := strconv.Atoi(fields[0])
	ahead, aerr := strconv.Atoi(fields[1])
	if berr != nil || aerr != nil {
		return 0, 0, false
	}
	return behind, ahead, true
}

// countDiffFiles counts the files in a full `git diff` by counting "diff --git "
// section headers. Such headers always start at column 0, so added/removed content
// lines (which begin with '+'/'-') are never miscounted.
func countDiffFiles(content string) int {
	files := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			files++
		}
	}
	return files
}
