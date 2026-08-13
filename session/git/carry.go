package git

import (
	"os"
	"path"
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
// The two lists differ in write direction, which is the sharp edge of link_paths.
// A carried file is a per-session copy, so a session's writes are private to it.
// A linked path is the origin's own tree under another name: writes through it
// land in the user's checkout and are visible to every other session at once, so
// an agent running `npm install` or `rm -rf node_modules` inside one session
// mutates the real dependency tree and every sibling's. That is the point for the
// read-only session that is the overwhelmingly common case — it is why a copy would
// be the wrong default.
//
// It is the wrong ANSWER, though, for the one task type that must mutate
// dependencies: a session whose whole job is upgrading them. So the sharing is now
// per-session rather than unconditional (#481). A worktree marked isolateDeps gets
// none of the links — not a symlink and not a directory — so the worktree looks
// exactly like a fresh checkout in which nobody ever ran `npm install`, which is the
// state a repo's setup_script (#389, session/setupscript.go) is already written
// against and which it runs immediately afterwards, into a tree that is now private.
// Deliberately not a copy: a copy is the cost link_paths exists to avoid, cannot be
// made atomic under the never-error contract above (a half-copy looks installed), and
// would re-copy on every resume, replacing the session's upgraded tree with the
// origin's stale one just after pause committed the new manifests.
//
// The config is loaded once for both lists: config.LoadConfig also sweeps and
// quarantines files in the data dir, so it is not a free read. It is deliberately
// read here rather than captured on the Worktree at construction, even though
// that would save the read: instances are rebuilt once at startup (see
// session.LoadInstances), and Resume reuses that same Worktree, so a captured
// list would freeze whatever the config said when the app launched and a
// settings-panel edit would never reach a resumed session. Nor can this bail out
// early when both lists are empty — knowing they are empty is what the read is
// for, and carry_files has a non-empty default, so the common case must read it
// anyway.
//
// isolateDeps is the deliberate exception to that rule, not a violation of it: it is
// a property of the SESSION, chosen once when the session was created, so freezing it
// is the correct behaviour and a later settings edit must not reach it. That is why it
// rides the Worktree (pushed in by the Instance) while the lists are read here.
func (g *Worktree) seedLocalPaths() {
	cfg := config.LoadConfig()
	for _, rel := range cfg.GetCarryFiles() {
		g.carryLocalFile(rel)
	}
	if g.isolateDeps {
		if paths := cfg.GetLinkPaths(); len(paths) > 0 {
			log.InfoLog.Printf("link_paths: session is dependency-isolated, so %v are not linked — this worktree starts without them and whatever installs into it stays private to it", paths)
			for _, rel := range paths {
				g.warnIsolatedPathNotIgnored(rel)
			}
		}
		return
	}
	for _, rel := range cfg.GetLinkPaths() {
		g.linkLocalPath(rel)
	}
}

// warnIsolatedPathNotIgnored is the isolated session's half of the worktree-side ignore
// check linkLocalPath runs, and it is needed for the same reason with the direction of
// the risk reversed. The shared path warns because a symlink git would not ignore leaks
// into pause's `git add .`; an isolated session creates no symlink, but it is the one
// session GUARANTEED to fill the path — the setup script runs moments later, or the
// agent's first install does — so an unignored path there is committed as a whole
// dependency tree, onto the branch and into any PR cut from it. Skipping the link must
// not also skip the diagnosis.
//
// The probe differs from linkLocalPath's in exactly one character, and that character is
// the point. Asking about a slash-terminated pathspec asks git about a DIRECTORY, which
// is what will exist here, so a dir-only rule (`node_modules/`) correctly passes — where
// the slash-less form linkLocalPath must use, because git stores a symlink as a file,
// correctly fails. Isolation is legitimately satisfied by the stricter-looking rule that
// linking cannot accept, and warning about it would be a false alarm on the commonest
// .gitignore in the ecosystem.
//
// Warn-only, like everything else here: the session is materially fine — its tree really
// is private, which is what was asked for — and the never-error contract above forbids
// turning this into a Setup failure regardless.
func (g *Worktree) warnIsolatedPathNotIgnored(rel string) {
	canon, _, _, ok := g.resolveSeedPaths("link_paths", rel)
	if !ok {
		return // resolveSeedPaths already warned
	}
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", canon+"/"); err != nil {
		log.WarningLog.Printf("link_paths: %q is not ignored in this dependency-isolated session's worktree, and it is about to be filled by a real install: whatever lands there would be committed on pause. The rule must reach the worktree — committed on this session's base, or in .git/info/exclude", rel)
	}
}

// resolveSeedPaths maps one repo-relative entry to its canonical spelling and
// its (src, dst) pair, or reports ok=false after warning when the entry is not a
// safe repo-relative path. kind names the config key in that warning.
//
// canon is the slash-separated form the filesystem paths were derived from, and
// is what callers must hand to git as a pathspec. Deriving the two from the same
// spelling is load-bearing rather than tidiness: filepath.Join silently drops a
// trailing separator, so passing the raw entry to `git check-ignore` would ask
// about a different path than the one being created. git resolves a
// slash-terminated pathspec as a *directory*, which a dir-only pattern
// (`node_modules/`) matches — while the symlink actually created is slash-less
// and, being a file to git, is not matched by it. The ignore guard would report
// "ignored" and the link would still leak into pause's `git add .`.
func (g *Worktree) resolveSeedPaths(kind, rel string) (canon, src, dst string, ok bool) {
	// path.Clean, not filepath.Clean: git pathspecs are always slash-separated,
	// including on Windows, and ToSlash first folds an entry a Windows user spelled
	// with backslashes into that same form.
	canon = path.Clean(filepath.ToSlash(rel))
	if rel == "" || !filepath.IsLocal(canon) {
		log.WarningLog.Printf("%s: skipping %q: entries must be repo-relative paths inside the repo", kind, rel)
		return "", "", "", false
	}

	src = filepath.Join(g.repoPath, canon)
	dst = filepath.Join(g.worktreePath, canon)
	// IsLocal above already rejects escapes; verify the joined results stayed
	// inside their roots anyway (also marks the paths clean for taint analysis).
	// This is also what refuses an entry naming the repo root: IsLocal accepts ".",
	// but Join collapses it to the root itself, which is not strictly inside.
	// Both checks are lexical: a symlinked path component could still point
	// elsewhere, but the repo, the worktree, and the seed lists are all the
	// user's own content — no trust boundary is crossed.
	if !strings.HasPrefix(src, g.repoPath+string(filepath.Separator)) ||
		!strings.HasPrefix(dst, g.worktreePath+string(filepath.Separator)) {
		log.WarningLog.Printf("%s: skipping %q: resolves outside the repo or worktree", kind, rel)
		return "", "", "", false
	}
	return canon, src, dst, true
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
//
// Like linkLocalPath, that last check is answered from the *worktree*, because
// only the worktree's view of the ignore rules decides what pause will stage. The
// origin checkout's answer can differ, and in the direction that leaks: a
// .gitignore edit that is uncommitted there (or a rule committed after the commit
// this worktree is checked out from) makes the origin report "ignored" while the
// worktree does not, so the file is carried and then committed. Refusing instead is
// the conservative side of that trade: the session loses local config it can be
// told about, rather than silently publishing it, which matters because the default
// entry (.claude/settings.local.json) is the kind of file that can hold secrets.
//
// "git ignores it in the worktree" is the whole condition, and committing the rule
// is only one way to meet it: .git/info/exclude lives in the *common* git dir and
// is therefore shared by every linked worktree, and core.excludesFile is global —
// a rule in either reaches the worktree without being committed anywhere. The
// warning below must not promise otherwise.
func (g *Worktree) carryLocalFile(rel string) {
	canon, src, dst, ok := g.resolveSeedPaths("carry_files", rel)
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
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", canon); err != nil {
		// Cause-agnostic, like linkLocalPath's equivalent: the commonest cause is no
		// ignore rule at all, so lead with the state and give that fix first. The
		// uncommitted-rule case is the same warning (the check runs in the worktree),
		// and .git/info/exclude satisfies it without touching the branch.
		log.WarningLog.Printf("carry_files: skipping %q: git would not ignore it in the session worktree (it would be committed on pause) — add it to .gitignore and commit that on this session's base, or add it to .git/info/exclude. A .gitignore edit left uncommitted in %s does not reach the worktree", rel, g.repoPath)
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
// Writes through the link reach the origin checkout and every other session — see
// seedLocalPaths on the two lists' write directions. A session that will rewrite the
// tree should be created dependency-isolated instead, which skips this entirely; this
// function is never reached for one.
//
// It shares carryLocalFile's guards, including the worktree-side ignore check and
// every reason given there for it. The deliberate divergences: the source is
// Lstat'd rather than Stat'd and needs no IsRegular check (a directory is the whole
// point), the clobber guard warns instead of returning silently, and the ignore
// check has a second reason to run in the *worktree* that carry has no equivalent
// of. A dir-only pattern (`node_modules/`) matches the origin directory but not the
// symlink, which git stores as a file — checking in the origin checkout would pass
// and then leak the link into `git add .` (pause) and into the untracked paths the
// diff stages every poll tick. Checking the not-yet-created path in the worktree is
// conservative in exactly the right direction: git cannot match a dir-only pattern
// there either, so only a slash-less pattern is accepted.
func (g *Worktree) linkLocalPath(rel string) {
	canon, src, dst, ok := g.resolveSeedPaths("link_paths", rel)
	if !ok {
		return
	}

	// Lstat, not Stat: an origin path that is itself a symlink (a shared package
	// store) is still worth linking to. Absence is the silent common case — the
	// dependencies simply are not installed yet.
	if _, err := os.Lstat(src); err != nil {
		return
	}
	// Never clobber. Unlike carry's equivalent this warns: git-tracked content is
	// only one way to get here, since carry_files entries and earlier link_paths
	// entries also create directories under the worktree, and a link silently not
	// made leaves the session without the dependencies it was configured to get.
	if _, err := os.Lstat(dst); err == nil {
		log.WarningLog.Printf("link_paths: skipping %q: something already exists at that path in the worktree (tracked content, or a directory an earlier carry_files/link_paths entry created) — never clobbered", rel)
		return
	}
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", canon); err != nil {
		// Cause-agnostic by design: the worktree is checked out from a commit (the
		// resolved start point at creation, the session branch on resume), so this
		// also fires when the ignore rule exists but is not committed yet, or when
		// the session branched off a base predating it. Naming only the dir-only
		// pattern would misdiagnose those into a dead end, so lead with the state
		// and offer the most common cause as a hint.
		log.WarningLog.Printf("link_paths: skipping %q: git would not ignore a symlink at that path in the session worktree (it would be committed on pause). The rule must reach the worktree — committed on this session's base, or in .git/info/exclude — and must not end in a slash: %q ignores the symlink, %q only the directory", rel, canon, canon+"/")
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
