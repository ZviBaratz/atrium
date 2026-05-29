package git

import (
	"strconv"
	"strings"
)

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

func (d *DiffStats) IsEmpty() bool {
	return d.Added == 0 && d.Removed == 0 && d.Content == ""
}

// Diff returns the git diff between the worktree and the base branch along with statistics
func (g *GitWorktree) Diff() *DiffStats {
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
func (g *GitWorktree) DiffNumstat() *DiffStats {
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
func (g *GitWorktree) snapshotWorktreePath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.worktreePath
}

// computeRepoStats fills in the commit/behind/dirty fields on stats. It is
// best-effort: any failure leaves the corresponding field at its zero value and
// never sets stats.Error, so a hiccup in a cosmetic counter can't blank the diff.
// wt is the worktree path snapshotted by the caller under the read lock; baseRef and
// baseCommitSHA are not mutated by Rename, so they're read directly.
func (g *GitWorktree) computeRepoStats(stats *DiffStats, wt string) {
	// A single rev-list gives both "ahead" (session commits) and "behind" (base
	// advanced) when the base ref is known; fall back to ahead-only otherwise.
	if g.baseRef != "" {
		if out, err := g.runGitCommand(wt, "rev-list", "--left-right", "--count", g.baseRef+"...HEAD"); err == nil {
			if behind, ahead, ok := parseLeftRightCount(out); ok {
				stats.Behind = behind
				stats.Commits = ahead
			}
		}
	} else if g.baseCommitSHA != "" {
		if out, err := g.runGitCommand(wt, "rev-list", "--count", g.baseCommitSHA+"..HEAD"); err == nil {
			if ahead, aerr := strconv.Atoi(strings.TrimSpace(out)); aerr == nil {
				stats.Commits = ahead
			}
		}
	}

	// Inline the dirtiness check against the snapshotted path rather than calling IsDirty
	// (which reads g.worktreePath) so this background path never touches the mutable field.
	if out, err := g.runGitCommand(wt, "status", "--porcelain"); err == nil {
		stats.Dirty = len(out) > 0
	}
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
