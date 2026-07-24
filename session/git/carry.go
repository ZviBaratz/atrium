package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
)

// seedLocalPaths materializes the configured gitignored paths from the origin
// checkout into a freshly created worktree: carry_files are copied, link_paths
// are symlinked. Worktrees carry only tracked files, so local project config
// (hooks, output style, MCP allowlists) and installed dependency trees would
// otherwise never reach a session.
//
// Strictly best-effort by contract: every failure logs a warning and is
// skipped. Nothing here may ever surface as a Setup error — Setup's callers
// (Instance.Start's deferred Kill, Resume) tear the whole worktree down on
// error, which would turn a cosmetic copy failure into a destroyed session.
//
// Seeding runs on every Setup, including the paused→resume recreation: being
// gitignored these paths are never committed by pause, so edits made to a
// carried file inside a session do not survive a pause/resume cycle — the origin
// copy wins.
//
// The config is loaded once for both lists: config.LoadConfig also sweeps and
// quarantines files in the data dir, so it is not a free read.
func (g *Worktree) seedLocalPaths() {
	cfg := config.LoadConfig()
	for _, rel := range cfg.GetCarryFiles() {
		g.carryLocalFile(rel)
	}
	for _, rel := range cfg.GetLinkPaths() {
		g.linkLocalPath(rel)
	}
}

// resolveSeedPaths maps one repo-relative entry to its (src, dst) pair, or
// reports ok=false after warning when the entry is not a safe repo-relative
// path. kind names the config key in that warning.
func (g *Worktree) resolveSeedPaths(kind, rel string) (src, dst string, ok bool) {
	if rel == "" || !filepath.IsLocal(rel) {
		log.WarningLog.Printf("%s: skipping %q: entries must be repo-relative paths inside the repo", kind, rel)
		return "", "", false
	}

	src = filepath.Join(g.repoPath, rel)
	dst = filepath.Join(g.worktreePath, rel)
	// IsLocal above already rejects escapes; verify the joined results stayed
	// inside their roots anyway (also marks the paths clean for taint analysis).
	// Both checks are lexical: a symlinked path component could still point
	// elsewhere, but the repo, the worktree, and the seed lists are all the
	// user's own content — no trust boundary is crossed.
	if !strings.HasPrefix(src, g.repoPath+string(filepath.Separator)) ||
		!strings.HasPrefix(dst, g.worktreePath+string(filepath.Separator)) {
		log.WarningLog.Printf("%s: skipping %q: resolves outside the repo or worktree", kind, rel)
		return "", "", false
	}
	return src, dst, true
}

// carryLocalFile copies one repo-relative file from repoPath into the
// worktree, when it is safe to do so:
//
//   - the path must stay inside the repo (relative, no ".." escape);
//   - the source must exist as a regular file (absence is the silent common
//     case — most repos never created the file). A directory belongs in
//     link_paths, which symlinks it instead of duplicating it;
//   - the destination must not already exist (a tracked file matching an
//     ignore pattern still materializes; never clobber it);
//   - git must ignore the path: pause commits the worktree with `git add .`,
//     so carrying a non-ignored file would silently leak it into the session
//     branch and any PR cut from it.
func (g *Worktree) carryLocalFile(rel string) {
	src, dst, ok := g.resolveSeedPaths("carry_files", rel)
	if !ok {
		return
	}

	info, err := os.Stat(src)
	if err != nil {
		return // not present in the origin checkout: nothing to carry
	}
	if !info.Mode().IsRegular() {
		log.WarningLog.Printf("carry_files: skipping %q: not a regular file (a directory belongs in link_paths, which symlinks it)", rel)
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

// linkLocalPath symlinks one repo-relative path (config link_paths) from the
// origin checkout into the worktree, with an absolute target. It exists for
// dependency trees a copy would be wrong for — node_modules is huge and slow to
// duplicate, and the tooling resolves through a symlink fine.
//
// It shares carryLocalFile's guards, with one deliberate difference: the
// gitignore check runs in the *worktree*, not the origin repo. A dir-only
// pattern (`node_modules/`) matches the origin directory but not the symlink,
// which git stores as a file — checking in the origin checkout would pass and
// then leak the link into `git add .` (pause) and into the untracked paths the
// diff stages every poll tick. Checking the not-yet-created path in the worktree
// is conservative in exactly the right direction: git cannot match a dir-only
// pattern there either, so only a slash-less pattern is accepted.
func (g *Worktree) linkLocalPath(rel string) {
	src, dst, ok := g.resolveSeedPaths("link_paths", rel)
	if !ok {
		return
	}

	// Lstat, not Stat: an origin path that is itself a symlink (a shared package
	// store) is still worth linking to. Absence is the silent common case — the
	// dependencies simply are not installed yet.
	if _, err := os.Lstat(src); err != nil {
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		return // already materialized (e.g. force-tracked content): never clobber
	}
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", rel); err != nil {
		log.WarningLog.Printf("link_paths: skipping %q: git would not ignore a symlink at that path (it would be committed on pause). Ignore it with a pattern that has no trailing slash — %q, not %q", rel, rel, rel+"/")
		return
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		log.WarningLog.Printf("link_paths: create parent dirs for %q: %v", rel, err)
		return
	}
	// An absolute target keeps the link valid regardless of how deep the worktree
	// sits under the data dir. Symlink creation needs a privilege on Windows, so
	// a failure here is logged and skipped like any other best-effort miss.
	if err := os.Symlink(src, dst); err != nil {
		log.WarningLog.Printf("link_paths: symlink %q -> %q: %v", dst, src, err)
		return
	}
}
