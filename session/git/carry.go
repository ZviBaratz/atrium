package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
)

// carryLocalFiles copies the configured gitignored files (config carry_files)
// from the origin checkout into a freshly materialized worktree. Worktrees
// carry only tracked files, so local project config (hooks, output style, MCP
// allowlists) would otherwise never reach a session.
//
// Strictly best-effort by contract: every failure logs a warning and is
// skipped. Nothing here may ever surface as a Setup error — Setup's callers
// (Instance.Start's deferred Kill, Resume) tear the whole worktree down on
// error, which would turn a cosmetic copy failure into a destroyed session.
//
// Carried files are seeded from the origin checkout on every Setup, including
// the paused→resume recreation: being gitignored they are never committed by
// pause, so edits made to them inside a session do not survive a pause/resume
// cycle — the origin copy wins.
func (g *Worktree) carryLocalFiles() {
	for _, rel := range config.LoadConfig().GetCarryFiles() {
		g.carryLocalFile(rel)
	}
}

// carryLocalFile copies one repo-relative file from repoPath into the
// worktree, when it is safe to do so:
//
//   - the path must stay inside the repo (relative, no ".." escape);
//   - the source must exist as a regular file (absence is the silent common
//     case — most repos never created the file);
//   - the destination must not already exist (a tracked file matching an
//     ignore pattern still materializes; never clobber it);
//   - git must ignore the path: pause commits the worktree with `git add .`,
//     so carrying a non-ignored file would silently leak it into the session
//     branch and any PR cut from it.
func (g *Worktree) carryLocalFile(rel string) {
	if rel == "" || !filepath.IsLocal(rel) {
		log.WarningLog.Printf("carry_files: skipping %q: entries must be repo-relative paths inside the repo", rel)
		return
	}

	src := filepath.Join(g.repoPath, rel)
	dst := filepath.Join(g.worktreePath, rel)
	// IsLocal above already rejects escapes; verify the joined results stayed
	// inside their roots anyway (also marks the paths clean for taint analysis).
	// Both checks are lexical: a symlinked path component could still point
	// elsewhere, but the repo, the worktree, and the carry list are all the
	// user's own content — no trust boundary is crossed.
	if !strings.HasPrefix(src, g.repoPath+string(filepath.Separator)) ||
		!strings.HasPrefix(dst, g.worktreePath+string(filepath.Separator)) {
		log.WarningLog.Printf("carry_files: skipping %q: resolves outside the repo or worktree", rel)
		return
	}

	info, err := os.Stat(src)
	if err != nil {
		return // not present in the origin checkout: nothing to carry
	}
	if !info.Mode().IsRegular() {
		log.WarningLog.Printf("carry_files: skipping %q: not a regular file", rel)
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		return // already materialized (e.g. force-tracked): never clobber
	}
	if _, err := g.runGitCommand(g.repoPath, "check-ignore", "-q", "--", rel); err != nil {
		log.WarningLog.Printf("carry_files: skipping %q: not gitignored in %s (it would be committed on pause — add it to .gitignore)", rel, g.repoPath)
		return
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		log.WarningLog.Printf("carry_files: create parent dirs for %q: %v", rel, err)
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		log.WarningLog.Printf("carry_files: read %q: %v", src, err)
		return
	}
	// Preserve the source mode: local config may hold secrets kept at 0600.
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		log.WarningLog.Printf("carry_files: write %q: %v", dst, err)
		return
	}
}
