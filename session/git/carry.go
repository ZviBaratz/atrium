package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
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
//
// The repository's own trusted .atrium.json layers over carry_files (#815), through
// the repoLocalSeeds resolver the Instance installed. The resolver's link half is
// part of the seam but production supplies nothing for it — link_paths is not a
// repo-layerable key yet, see repocfg.RepoLocalLayerKeys — so today only the carry
// union has a repo side. Repo entries come FIRST and the union is deduplicated on
// the canonical spelling, which matters only where the two sides name the same path:
// the never-clobber guards below make the first entry to materialize the one that
// wins, so ordering is how "the repo knows its own layout" is expressed. Union
// rather than replacement is the recorded #815 decision — these values are sets of
// independent paths, so a repo naming the local-config files its developers already
// name must not silently drop the user's personal carry (the default
// .claude/settings.local.json) in that one repo. What overrides a repo's additions
// is revoking its trust grant.
func (g *Worktree) seedLocalPaths() {
	cfg := config.LoadConfig()
	repoCarry, repoLink := g.resolveRepoLocalSeeds()

	carries := unionSeedEntries(repoCarry, cfg.GetCarryFiles(), "carry_files")
	links := unionSeedEntries(repoLink, cfg.GetLinkPaths(), "link_paths")
	if !g.isolateDeps {
		carries = dropCarriesUnderLinks(carries, links)
	}
	for _, e := range carries {
		g.carryLocalFile(e)
	}

	if g.isolateDeps {
		if len(links) > 0 {
			log.InfoLog.Printf("link_paths: session is dependency-isolated, so %v are not linked — this worktree starts without them and whatever installs into it stays private to it", seedEntryPaths(links))
			for _, e := range links {
				g.warnIsolatedPathNotIgnored(e.kind, e.rel)
			}
		}
		return
	}
	for _, e := range links {
		g.linkLocalPath(e)
	}
}

// seedEntry is one entry to seed plus the config key to blame in a warning about
// it. kind carries the entry's PROVENANCE, not just its key name, which is #477's
// third complaint answered: a refusal caused by a project's own committed entry
// used to read as a problem with the global list the user maintains by hand.
//
// repo says the same thing in a form code can branch on, and one guard does: a
// repo-authored entry is confined to the repo through resolved symlinks
// (seedSourceEscapes), where the user's own entry is deliberately allowed to point
// at a shared store outside it. The string is for humans, this is for the gate —
// deriving the gate from the string would make a copy-edit a security change.
type seedEntry struct {
	kind string
	rel  string
	repo bool
}

// unionSeedEntries lays the repo's entries ahead of the user's and drops the
// duplicates, comparing canonical spellings so "node_modules/" and
// "./node_modules" are recognized as one path rather than seeded twice (the second
// attempt would warn about clobbering the first).
//
// An entry the canonical rule refuses is kept, keyed by its raw text: it must reach
// its own leaf function to be warned about there, in the one place that names why.
// Only the repo side can be pre-canonicalized — repocfg did it at parse time — so
// this is the global list's first canonicalization. It feeds the dedupe key AND the
// user-set below, which decides provenance and so decides whether seedSourceEscapes
// judges an entry at all — it is not inert, and a reader who trusts an older
// "dedupe key alone" here would take a security-bearing value for a scratch one.
// The raw spelling is what travels, so every warning quotes what the user actually
// wrote.
func unionSeedEntries(repo, global []string, key string) []seedEntry {
	out := make([]seedEntry, 0, len(repo)+len(global))
	seen := make(map[string]bool, len(repo)+len(global))
	canon := func(rel string) string {
		if c, err := repocfg.CanonicalSeedPath(rel); err == nil {
			return c
		}
		return rel
	}
	// The user's own set, by canonical spelling. A path the user ALSO declares is
	// not repo-authored for the purpose of the containment guard, however the dedupe
	// orders it: the user asked for that path independently, so the repo is not the
	// reason it is being seeded. Without this, a repo naming a path the user already
	// names — the likely overlap, since a repo names the same local-config paths its
	// developers do — silently transferred repo provenance onto the user's entry, and
	// seedSourceEscapes then refused the user's own documented setup and blamed the
	// repo for it.
	userSet := make(map[string]bool, len(global))
	for _, rel := range global {
		userSet[canon(rel)] = true
	}
	add := func(rel, kind string, fromRepo bool) {
		dedupe := canon(rel)
		if seen[dedupe] {
			return
		}
		seen[dedupe] = true
		out = append(out, seedEntry{kind: kind, rel: rel, repo: fromRepo && !userSet[dedupe]})
	}
	for _, rel := range repo {
		add(rel, key+" ("+repocfg.RepoLocalFileName+")", true)
	}
	for _, rel := range global {
		add(rel, key, false)
	}
	return out
}

// dropCarriesUnderLinks removes any carry entry that lands INSIDE a path the link
// list will symlink, and says so. Ordering alone cannot resolve this collision, in
// either direction: carrying first has os.MkdirAll create a real directory at the
// link's path, so linkLocalPath's never-clobber guard finds something there and
// skips the link with only a log line — a repo's `carry_files: ["node_modules/.x"]`
// silently costs the user their whole linked node_modules, in every session in that
// repo, which is exactly the suppression union semantics exist to prevent ("your
// entries are never replaced", in the README and on the carry_files settings row —
// the link_paths row lost that sentence when the key stopped being repo-layerable).
// Linking first
// is worse: the carry would then write THROUGH the symlink into the user's own
// checkout.
//
// So the link wins and the carry is refused. That is the conservative side — the
// file is reachable through the link anyway, where a lost dependency tree has to be
// reinstalled — and it is the side that keeps the never-replaced promise, since a
// link entry is the larger artifact and the likelier one to be the user's.
//
// Not applied to a dependency-isolated session: no link is created there, so
// nothing collides and the carry is the only way that file arrives.
func dropCarriesUnderLinks(carries, links []seedEntry) []seedEntry {
	if len(links) == 0 {
		return carries
	}
	linkCanon := make([]string, 0, len(links))
	for _, l := range links {
		if canon, err := repocfg.CanonicalSeedPath(l.rel); err == nil {
			linkCanon = append(linkCanon, canon)
		}
	}
	out := make([]seedEntry, 0, len(carries))
	for _, c := range carries {
		canon, err := repocfg.CanonicalSeedPath(c.rel)
		if err != nil {
			out = append(out, c) // unusable: let its own leaf function say why
			continue
		}
		under := ""
		for _, l := range linkCanon {
			if canon == l || strings.HasPrefix(canon, l+"/") {
				under = l
				break
			}
		}
		if under == "" {
			out = append(out, c)
			continue
		}
		log.WarningLog.Printf("%s: skipping %q: it is inside %q, which link_paths symlinks — carrying it would create a real directory there and silently cost you the link. It is already reachable through the link", c.kind, c.rel, under)
	}
	return out
}

// seedEntryPaths is the entries' raw spellings, for a log line about the set.
func seedEntryPaths(entries []seedEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.rel)
	}
	return out
}

// resolveRepoLocalSeeds asks the installed resolver what this repository's own
// trusted .atrium.json adds. It is the one call, made after checkout and before
// any seeding, so the bytes judged are the bytes this worktree holds.
//
// Best-effort like everything else here: no resolver (a test literal, a session
// created before the field existed) means no repo-local layer, which is the
// pre-#815 behavior exactly.
func (g *Worktree) resolveRepoLocalSeeds() (carry, link []string) {
	if g.repoLocalSeeds == nil {
		return nil, nil
	}
	return g.repoLocalSeeds(g.worktreePath)
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
func (g *Worktree) warnIsolatedPathNotIgnored(kind, rel string) {
	canon, _, _, ok := g.resolveSeedPaths(kind, rel)
	if !ok {
		return // resolveSeedPaths already warned
	}
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", canon+"/"); err != nil {
		log.WarningLog.Printf("%s: %q is not ignored in this dependency-isolated session's worktree, and it is about to be filled by a real install: whatever lands there would be committed on pause. The rule must reach the worktree — committed on this session's base, or in .git/info/exclude", kind, rel)
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
	// The lexical rule itself lives in repocfg, which applies it to a repo-local
	// list at parse time — before any grant, so it may touch neither the filesystem
	// nor the template engine. One definition because a divergence would let a
	// repo-local entry be granted here and refused there, or the reverse.
	canon, err := repocfg.CanonicalSeedPath(rel)
	if err != nil {
		log.WarningLog.Printf("%s: skipping %q: %v — entries must be repo-relative paths inside the repo", kind, rel, err)
		return "", "", "", false
	}

	src = filepath.Join(g.repoPath, canon)
	dst = filepath.Join(g.worktreePath, canon)
	// CanonicalSeedPath already rejects escapes and the repo root; verify the joined
	// results stayed inside their roots anyway (also marks the paths clean for taint
	// analysis).
	//
	// A seed list is no longer necessarily the user's own content: since #815 a
	// repository's own trusted .atrium.json contributes entries, so the lexical rule
	// above is what confines a repo-authored entry to the repo, and this is its
	// second opinion. Both checks here are purely LEXICAL, and that is not
	// sufficient on its own for a repo-authored entry: a symlinked path component
	// inside the origin checkout can point anywhere, and neither check follows one.
	// seedSourceEscapes is the half that does, and the callers apply it — this
	// function cannot, because it is also the isolated session's probe path, where
	// no source is read.
	if !strings.HasPrefix(src, g.repoPath+string(filepath.Separator)) ||
		!strings.HasPrefix(dst, g.worktreePath+string(filepath.Separator)) {
		log.WarningLog.Printf("%s: skipping %q: resolves outside the repo or worktree", kind, rel)
		return "", "", "", false
	}
	return canon, src, dst, true
}

// seedSourceEscapes reports whether src leaves the repository once symlinks are
// followed, and is the guard that makes a repo-authored entry's confinement real
// rather than lexical.
//
// The lexical checks above cannot see this, and neither can the worktree-side
// check-ignore probe that a comment here used to name as the guard for it. That
// reasoning was circular twice over: the paths a repo may seed are exactly the
// gitignored ones, which are exactly the untracked ones that CAN be a symlink to
// anywhere — and the repo authors the ignore rule too. Worse, the probe runs in the
// WORKTREE, where the symlink does not exist: with `deps/` committed in .gitignore,
// `git check-ignore -q -- deps/id_rsa` exits 0 there even though `deps` is absent,
// while os.Stat in the ORIGIN follows a user's convenience symlink (deps → ~/.ssh,
// .venv → /opt/..., a pnpm store) and reports a regular file. A trusted repo could
// then name any file the user can read and have it copied in front of the agent, or
// handed over as a writable symlink — powers no trust dialog describes.
//
// Applied to repo-authored entries only, and that asymmetry is deliberate: the
// user's own link_paths entry pointing at a shared package store outside the repo
// is a documented, working configuration (linkLocalPath Lstats rather than Stats
// for exactly that reason), and narrowing it would break setups that predate #815.
// The repo side has no such history and no such claim.
//
// A source that does not exist is not an escape — EvalSymlinks fails on it and the
// callers' own absence checks handle it, which keeps "the file simply is not there"
// the silent common case it has always been.
func (g *Worktree) seedSourceEscapes(kind, rel, src string) bool {
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(g.repoPath)
	if err != nil {
		root = g.repoPath
	}
	if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return false
	}
	log.WarningLog.Printf("%s: refusing %q: it resolves through a symlink to %q, outside this repository. A repository's own seed list may only name paths inside it — the trust grant covers the repo's files, not everything you can read", kind, rel, resolved)
	return true
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
func (g *Worktree) carryLocalFile(e seedEntry) {
	kind, rel := e.kind, e.rel
	canon, src, dst, ok := g.resolveSeedPaths(kind, rel)
	if !ok {
		return
	}
	if e.repo && g.seedSourceEscapes(kind, rel, src) {
		return
	}

	info, err := os.Stat(src)
	if err != nil {
		return // not present in the origin checkout: nothing to carry
	}
	if !info.Mode().IsRegular() {
		log.WarningLog.Printf("%s: skipping %q: not a regular file (a directory belongs in link_paths, which symlinks it)", kind, rel)
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
		log.WarningLog.Printf("%s: skipping %q: git would not ignore it in the session worktree (it would be committed on pause) — add it to .gitignore and commit that on this session's base, or add it to .git/info/exclude. A .gitignore edit left uncommitted in %s does not reach the worktree", kind, rel, g.repoPath)
		return
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		log.WarningLog.Printf("%s: create parent dirs for %q: %v", kind, rel, err)
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		log.WarningLog.Printf("%s: read %q: %v", kind, src, err)
		return
	}
	// Preserve the source mode: local config may hold secrets kept at 0600.
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		log.WarningLog.Printf("%s: write %q: %v", kind, dst, err)
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
func (g *Worktree) linkLocalPath(e seedEntry) {
	kind, rel := e.kind, e.rel
	canon, src, dst, ok := g.resolveSeedPaths(kind, rel)
	if !ok {
		return
	}
	if e.repo && g.seedSourceEscapes(kind, rel, src) {
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
		log.WarningLog.Printf("%s: skipping %q: something already exists at that path in the worktree (tracked content, or a directory an earlier carry_files/link_paths entry created) — never clobbered", kind, rel)
		return
	}
	if _, err := g.runGitCommand(g.worktreePath, "check-ignore", "-q", "--", canon); err != nil {
		// Cause-agnostic by design: the worktree is checked out from a commit (the
		// resolved start point at creation, the session branch on resume), so this
		// also fires when the ignore rule exists but is not committed yet, or when
		// the session branched off a base predating it. Naming only the dir-only
		// pattern would misdiagnose those into a dead end, so lead with the state
		// and offer the most common cause as a hint.
		log.WarningLog.Printf("%s: skipping %q: git would not ignore a symlink at that path in the session worktree (it would be committed on pause). The rule must reach the worktree — committed on this session's base, or in .git/info/exclude — and must not end in a slash: %q ignores the symlink, %q only the directory", kind, rel, canon, canon+"/")
		return
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		log.WarningLog.Printf("%s: create parent dirs for %q: %v", kind, rel, err)
		return
	}
	// An absolute target keeps the link valid regardless of how deep the worktree
	// sits under the data dir. Symlink creation needs a privilege on Windows, so
	// a failure here is logged and skipped like any other best-effort miss.
	if err := os.Symlink(src, dst); err != nil {
		log.WarningLog.Printf("%s: symlink %q -> %q: %v", kind, dst, src, err)
		return
	}
}
