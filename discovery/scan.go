// Package discovery finds git repositories under configurable roots, feeding
// the new-session project picker candidates the user has never opened in
// Atrium. The walk is deliberately defensive: depth-bounded, per-directory and
// total caps, mount boundaries respected, symlinks never followed, and
// context-cancellable with partial results — it runs against real home
// directories, which contain pathological trees.
package discovery

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Walk bounds: a single directory read is capped (mirroring the picker's
// maxDirEntries, so a pathological dir can't balloon the walk) and the total
// result set is capped as a runaway backstop.
const (
	maxEntriesPerDir = 500
	maxTotalRepos    = 2000
	// maxSubmoduleNesting bounds recursion through nested superprojects (a
	// submodule that declares its own submodules). Real layouts rarely exceed
	// two levels; the cap is a cycle/runaway backstop.
	maxSubmoduleNesting = 3
	// maxGitmodulesSize skips pathologically large .gitmodules files — real
	// ones are a few KB.
	maxGitmodulesSize = 1 << 20
)

// ignoredNames are directory names never entered, wherever they appear. Most
// build trees live inside repos and are already pruned by the
// don't-descend-into-found-repos rule; this list guards the non-repo trees a
// real $HOME contains (package caches, env managers, snap, macOS Library).
// Hidden directories are skipped by prefix, not listed here.
//
// The list is intentionally fixed (not user-configurable): the cost of a missed
// repo named exactly e.g. "env" or nested under one of these is a path-browse
// away, whereas walking a giant build/cache tree on every scan is not. A user
// who needs such a tree can name it directly in project_search_roots, which the
// walk treats as explicit consent and descends regardless.
var ignoredNames = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"venv":         true,
	"env":          true,
	"snap":         true,
	"target":       true,
	"dist":         true,
	"out":          true,
	"miniconda3":   true,
	"anaconda3":    true,
	"mambaforge":   true,
	"Library":      true,
}

// Options configures a repo scan.
type Options struct {
	// Roots are the directories to search. A leading "~" is expanded to the
	// user's home directory; missing roots are skipped and duplicates collapse.
	Roots []string
	// MaxDepth bounds how far below each root the walk descends: a root's
	// immediate children are depth 1. Directories at depth MaxDepth are still
	// checked for being repos, but never listed.
	MaxDepth int
	// SkipPaths are absolute directories the walk never enters (e.g. the
	// Atrium data dir, whose worktrees must not surface as candidates).
	SkipPaths []string
}

// foundRepo pairs a repo path with its .git entry's mtime, a zero-cost proxy
// for "recently active" used to rank results.
type foundRepo struct {
	path string
	mod  time.Time
}

// Scan walks each root to MaxDepth and returns the absolute paths of
// directories containing a .git entry (dir or file — linked worktrees and
// submodules use a file), ordered most-recently-active first (by .git mtime,
// alphabetical on ties). Best-effort throughout: unreadable directories are
// skipped silently and cancelling ctx returns what was found so far.
func Scan(ctx context.Context, opts Options) []string {
	skip := make(map[string]bool, len(opts.SkipPaths))
	for _, p := range opts.SkipPaths {
		if abs, err := filepath.Abs(expandTilde(p)); err == nil {
			skip[abs] = true
		}
	}
	s := &scanner{ctx: ctx, maxDepth: opts.MaxDepth, skip: skip, seen: map[string]bool{}}
	for _, root := range normalizeRoots(opts.Roots) {
		s.rootDev, s.devOK = deviceID(root)
		s.walk(root, 0, true)
	}
	sort.Slice(s.found, func(i, j int) bool {
		a, b := s.found[i], s.found[j]
		if !a.mod.Equal(b.mod) {
			return a.mod.After(b.mod)
		}
		return a.path < b.path
	})
	out := make([]string, len(s.found))
	for i, r := range s.found {
		out[i] = r.path
	}
	return out
}

type scanner struct {
	ctx      context.Context
	maxDepth int
	skip     map[string]bool
	seen     map[string]bool
	found    []foundRepo
	rootDev  uint64
	devOK    bool
}

func (s *scanner) walk(dir string, depth int, isRoot bool) {
	if s.ctx.Err() != nil || len(s.found) >= maxTotalRepos || s.skip[dir] {
		return
	}
	// Mount-boundary guard (find -xdev): a child on a different device than
	// its root is an NFS/FUSE mount that could stall the walk on network
	// round-trips. Roots themselves are exempt — listing one explicitly is
	// consent to walk it.
	if !isRoot && s.devOK {
		if dev, ok := deviceID(dir); ok && dev != s.rootDev {
			return
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		if !s.seen[dir] {
			s.seen[dir] = true
			s.found = append(s.found, foundRepo{path: dir, mod: fi.ModTime()})
		}
		// The repo-prune below hides everything inside a repo, but submodules
		// are projects in their own right (superproject workflows live in
		// them). Enumerate them from .gitmodules — one cheap file read —
		// instead of walking the repo's interior.
		s.collectSubmodules(dir, 0)
		// A repo's interior holds no further projects — except when the repo
		// is itself a search root (e.g. dotfiles tracked in ~): the user
		// listed it to find the projects inside.
		if !isRoot {
			return
		}
	}
	if depth >= s.maxDepth {
		return
	}
	f, err := os.Open(dir)
	if err != nil {
		return // permissions etc.: skip silently, like the picker's listSubdirs
	}
	entries, _ := f.ReadDir(maxEntriesPerDir) // bounded read; partial slice on error is fine
	_ = f.Close()
	for _, e := range entries {
		// ReadDir does not stat symlink targets, so a symlinked dir reports
		// IsDir() == false — symlinks are never followed (no cycle risk).
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || ignoredNames[name] {
			continue
		}
		s.walk(filepath.Join(dir, name), depth+1, false)
	}
}

// collectSubmodules records the initialized submodules a repo declares in its
// .gitmodules, recursing into each for nested superprojects. Declared paths
// without a .git present (uninitialized) are skipped, as are paths that would
// escape the repo and symlinked entries — .gitmodules is repo-controlled data,
// not a trusted walk input.
func (s *scanner) collectSubmodules(repo string, nesting int) {
	if nesting >= maxSubmoduleNesting {
		return
	}
	for _, rel := range gitmodulePaths(filepath.Join(repo, ".gitmodules")) {
		if s.ctx.Err() != nil || len(s.found) >= maxTotalRepos {
			return
		}
		sub := filepath.Join(repo, rel) // Join cleans, so ".." resolves before the escape check
		if !strings.HasPrefix(sub, repo+string(filepath.Separator)) || s.skip[sub] || s.seen[sub] {
			continue
		}
		if fi, err := os.Lstat(sub); err != nil || !fi.IsDir() {
			continue // missing, or a symlink — never followed, like the walk
		}
		fi, err := os.Lstat(filepath.Join(sub, ".git"))
		if err != nil {
			continue // declared but uninitialized: not a project yet
		}
		s.seen[sub] = true
		s.found = append(s.found, foundRepo{path: sub, mod: fi.ModTime()})
		s.collectSubmodules(sub, nesting+1)
	}
}

// gitmodulePaths extracts the "path" values from a .gitmodules file without
// shelling out to git: trimmed `path = value` lines, value optionally quoted.
// Any read failure or an oversized file yields nil (no submodules).
func gitmodulePaths(file string) []string {
	if fi, err := os.Lstat(file); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxGitmodulesSize {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "path") {
			continue
		}
		if val = strings.Trim(strings.TrimSpace(val), `"`); val != "" {
			out = append(out, val)
		}
	}
	return out
}

// normalizeRoots expands, absolutizes, dedupes, and existence-filters roots.
func normalizeRoots(roots []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		abs, err := filepath.Abs(expandTilde(r))
		if err != nil || seen[abs] {
			continue
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// expandTilde resolves a leading "~" to the home directory; anything else is
// returned unchanged.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
