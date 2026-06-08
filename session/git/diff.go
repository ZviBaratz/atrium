package git

import (
	"strconv"
	"strings"
	"time"
)

// revListCacheTTL is how long the rev-list commit counts (ahead/behind) returned
// by computeRepoStats are reused without re-running git rev-list. git status
// (the dirty flag) is always run fresh because it reflects uncommitted file
// changes that must be visible immediately after the user edits or commits.
// Commits-ahead/behind change only on an explicit commit, so a 3-second cache
// is imperceptible in normal use and cuts rev-list subprocess pressure by ~83%
// at a 500ms tick interval (1 run per 6 ticks rather than 1 per tick).
const revListCacheTTL = 3 * time.Second

// repoStatsEntry holds the cached rev-list result from a single computeRepoStats
// run. Only commits-ahead and commits-behind are cached; the dirty flag is not
// (git status --porcelain runs on every tick for accurate real-time display).
type repoStatsEntry struct {
	commits    int
	behind     int
	computedAt time.Time
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

// Diff returns the git diff between the worktree and the base branch along with statistics
func (g *Worktree) Diff() *DiffStats {
	stats := &DiffStats{}

	// Snapshot the worktree path under the lock so a concurrent deep Rename (which moves
	// the worktree and swaps the field) can't tear the read. Subsequent git calls run
	// against the local without holding the lock.
	wt := g.snapshotWorktreePath()

	// -N stages untracked files (intent to add), including them in the diff
	_, err := g.runGitCommand(wt, "add", "-N", ".")
	if err != nil {
		stats.Error = err
		return stats
	}

	content, err := g.runGitCommand(wt, "--no-pager", "diff", g.GetBaseCommitSHA())
	if err != nil {
		stats.Error = err
		return stats
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

	g.computeRepoStats(stats, wt)
	return stats
}

// DiffNumstat returns the added/removed line counts between the worktree and the
// base branch without loading the full diff content into memory. Use this when
// only the summary counts are needed (e.g. for unselected instances in the list).
func (g *Worktree) DiffNumstat() *DiffStats {
	stats := &DiffStats{}

	// See Diff: snapshot the worktree path so a concurrent rename can't tear the read.
	wt := g.snapshotWorktreePath()

	// -N stages untracked files (intent to add), including them in the diff
	_, err := g.runGitCommand(wt, "add", "-N", ".")
	if err != nil {
		stats.Error = err
		return stats
	}

	out, err := g.runGitCommand(wt, "--no-pager", "diff", "--numstat", g.GetBaseCommitSHA())
	if err != nil {
		stats.Error = err
		return stats
	}

	stats.Added, stats.Removed, stats.FilesChanged = parseNumstat(out)

	g.computeRepoStats(stats, wt)
	return stats
}

// snapshotWorktreePath reads worktreePath under the read lock so background diff
// computation can't race the in-place field swap a deep Rename performs.
func (g *Worktree) snapshotWorktreePath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.worktreePath
}

// computeRepoStats fills in the commit/behind/dirty fields on stats. It is
// best-effort: any failure leaves the corresponding field at its zero value and
// never sets stats.Error, so a hiccup in a cosmetic counter can't blank the diff.
// wt is the worktree path snapshotted by the caller under the read lock; baseRef and
// baseCommitSHA are not mutated by Rename, so they're read directly.
//
// The rev-list result (commits ahead/behind) is cached for revListCacheTTL because
// it only changes on an explicit commit and the subprocess is relatively expensive on
// large histories. The dirty check (git status --porcelain) always runs fresh so the
// UI reflects uncommitted edits without delay.
func (g *Worktree) computeRepoStats(stats *DiffStats, wt string) {
	// Serve rev-list from cache when fresh; otherwise re-run and update.
	g.statsCacheMu.Lock()
	if !g.statsCache.computedAt.IsZero() && time.Since(g.statsCache.computedAt) < revListCacheTTL {
		stats.Commits = g.statsCache.commits
		stats.Behind = g.statsCache.behind
		g.statsCacheMu.Unlock()
	} else {
		g.statsCacheMu.Unlock()

		// Only cache a successful result. A transient rev-list failure must not be
		// stored, or the zero it leaves would suppress retries for the whole TTL;
		// leaving the cache empty makes the next tick recompute immediately.
		if commits, behind, ok := g.revListCounts(wt); ok {
			stats.Commits = commits
			stats.Behind = behind

			g.statsCacheMu.Lock()
			g.statsCache = repoStatsEntry{
				commits:    commits,
				behind:     behind,
				computedAt: time.Now(),
			}
			g.statsCacheMu.Unlock()
		}
	}

	// Always run git status fresh: the dirty flag must reflect uncommitted edits
	// immediately (unlike commit counts, it changes without an explicit commit).
	// Inline the check against the snapshotted path rather than calling IsDirty
	// (which reads g.worktreePath) so this background path never touches the mutable field.
	if out, err := g.runGitCommand(wt, "status", "--porcelain"); err == nil {
		stats.Dirty = len(out) > 0
	}
}

// revListCounts returns the session's commits-ahead and, when the base ref is
// known, commits-behind by shelling out to git rev-list. ok is false only when a
// subprocess that was attempted failed (or its output couldn't be parsed); the
// no-base case returns (0, 0, true) because zero is the correct, cacheable answer
// rather than an error. The split lets computeRepoStats cache good results while
// skipping bad ones.
func (g *Worktree) revListCounts(wt string) (commits, behind int, ok bool) {
	// A single rev-list gives both "ahead" (session commits) and "behind" (base
	// advanced) when the base ref is known; fall back to ahead-only otherwise.
	if g.baseRef != "" {
		out, err := g.runGitCommand(wt, "rev-list", "--left-right", "--count", g.baseRef+"...HEAD")
		if err != nil {
			return 0, 0, false
		}
		behind, ahead, parsed := parseLeftRightCount(out)
		if !parsed {
			return 0, 0, false
		}
		return ahead, behind, true
	}
	if g.baseCommitSHA != "" {
		out, err := g.runGitCommand(wt, "rev-list", "--count", g.baseCommitSHA+"..HEAD")
		if err != nil {
			return 0, 0, false
		}
		ahead, aerr := strconv.Atoi(strings.TrimSpace(out))
		if aerr != nil {
			return 0, 0, false
		}
		return ahead, 0, true
	}
	// No base to compare against: zero is a legitimate, cacheable result.
	return 0, 0, true
}

// invalidateRevListCache clears the cached rev-list result so the next
// computeRepoStats call re-runs git rev-list unconditionally. Call this after
// any operation that alters the commit graph (commit, push) so the ahead/behind
// counts update on the very next tick rather than waiting for the TTL to expire.
func (g *Worktree) invalidateRevListCache() {
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
