package config

import (
	"os"
	"path/filepath"
	"strings"
)

// AmbientClaudeConfigDir resolves the config dir a claude with no account routing
// reads, by claude's own rule: $CLAUDE_CONFIG_DIR if set, else the home dir. The
// file itself is .claude.json at that dir's root.
//
// This is CLAUDE's config dir, not Atrium's data dir, so RuntimeName() is
// deliberately not involved. It lives here beside ResolvedConfigDir because the
// unrouted session's dir and a routed account's dir are the same kind of value,
// and resolving them in two places is what #359 was: the worktrees-root trust
// wrote $HOME/.claude.json while `atrium doctor` read $CLAUDE_CONFIG_DIR, so the
// opt-in silently did nothing for anyone exporting that variable.
//
// "" means unresolvable (no home dir). Callers must keep filepath.Clean away from
// that sentinel — filepath.Clean("") is ".", which would turn "no dir" into a
// cwd-relative one.
//
// Note the resolution reads the CALLER's env, which is a session's ambient env
// only when the two agree: sessions inherit the tmux server's env, captured at
// server start.
func AmbientClaudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// ResolveClaudeAccount picks the Claude Code account for a target whose origin
// remote is remoteURL and whose directory is targetPath. It returns the account
// name (for the TUI badge), the resolved CLAUDE_CONFIG_DIR to inject (empty =
// inherit the current env), and whether the choice is the default/fallback
// account (drives dim styling). Matching is case-insensitive substring, evaluated
// per account in config order: the remote is tested against remote_matches, then
// the path against path_matches (the only signal for non-git/direct sessions,
// which have no remote); the first account that hits either wins. When nothing
// matches, the first account with no route rules is the inferred default; absent
// that, ("default", "", true) means inherit env.
func (c *Config) ResolveClaudeAccount(remoteURL, targetPath string) (name, configDir string, isDefault bool) {
	if len(c.ClaudeAccounts) == 0 {
		return "", "", false
	}
	idx, isDefault := matchRouteIndex(len(c.ClaudeAccounts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.ClaudeAccounts[i].RemoteMatches },
		func(i int) []string { return c.ClaudeAccounts[i].PathMatches })
	if idx < 0 {
		return "default", "", true
	}
	a := c.ClaudeAccounts[idx]
	return a.Name, a.ResolvedConfigDir(), isDefault
}

// ResolveClaudePool finds the account routing selects (first-match, identical to
// ResolveClaudeAccount) and returns the whole pool that account belongs to: the
// pool name and its members in config order. An account with no Pool is a
// singleton pool whose name is the account name. isDefault mirrors
// ResolveClaudeAccount (the matched account was the rule-less catch-all). Empty
// claude_accounts returns ("", nil, false) — the feature is dormant.
func (c *Config) ResolveClaudePool(remoteURL, targetPath string) (pool string, members []ClaudeAccount, isDefault bool) {
	if len(c.ClaudeAccounts) == 0 {
		return "", nil, false
	}
	idx, isDefault := matchRouteIndex(len(c.ClaudeAccounts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.ClaudeAccounts[i].RemoteMatches },
		func(i int) []string { return c.ClaudeAccounts[i].PathMatches })
	if idx < 0 {
		// No match and no catch-all: mirror ResolveClaudeAccount's synthetic default.
		return "default", []ClaudeAccount{{Name: "default"}}, true
	}
	matched := c.ClaudeAccounts[idx]
	if matched.Pool == "" {
		return matched.Name, []ClaudeAccount{matched}, isDefault
	}
	return matched.Pool, c.PoolMembers(matched.Pool), isDefault
}

// PoolMembers returns the accounts in the named pool, in config order. A name
// matching a single ungrouped account (Pool == "") returns that account as a
// singleton pool, so any pool name — grouped or singleton — resolves uniformly.
func (c *Config) PoolMembers(pool string) []ClaudeAccount {
	var members []ClaudeAccount
	for _, a := range c.ClaudeAccounts {
		if a.Pool == pool || (a.Pool == "" && a.Name == pool) {
			members = append(members, a)
		}
	}
	return members
}

// FindClaudeAccount resolves which configured account an EXISTING session belongs
// to, anchored on the CLAUDE_CONFIG_DIR that session was born with (its stamped
// name is only a tie-breaker). The dir is the durable identity: renaming an
// account, or moving it into a pool, never changes which directory a live session
// injected, so this is what lets a rename adopt its sessions instead of stranding
// them under a name config no longer has (#470).
//
// Deliberately NOT routing: ResolveClaudeAccount/ResolveClaudePool answer "where
// should a NEW session go", which for an existing session would discard a picker
// pin or the rotated member it actually launched on.
//
// An empty configDir never matches, even against an account that also declares
// none: unset means "inherit the ambient env", and the synthetic default this
// package returns when nothing routes (see ResolveClaudeAccount) persists exactly
// that. There is no name-only fallback either — a name that matches while the dir
// does not means the account's config_dir was edited, i.e. the session is still
// running a different login, and adopting that account's pool would cluster it
// with a login it is not on.
//
// Returns (ClaudeAccount{}, false) when the anchor hits nothing, or when it hits
// more than one account and none of them carries the stamped name (two accounts
// may legally share a config_dir — `atrium doctor` warns about it — and a
// mis-guess would move a session into the wrong pool with no undo). Callers keep
// their last-known stamp on false: a rename must heal, a deletion must not blank a
// badge.
func (c *Config) FindClaudeAccount(name, configDir string) (ClaudeAccount, bool) {
	idx := findAccountIndex(len(c.ClaudeAccounts), name, configDir,
		func(i int) string { return c.ClaudeAccounts[i].Name },
		func(i int) string { return c.ClaudeAccounts[i].ResolvedConfigDir() })
	if idx < 0 {
		return ClaudeAccount{}, false
	}
	return c.ClaudeAccounts[idx], true
}

// FindAgyAccount is FindClaudeAccount over the independent AgyAccounts section,
// with the same anchor and the same rules.
func (c *Config) FindAgyAccount(name, configDir string) (AgyAccount, bool) {
	idx := findAccountIndex(len(c.AgyAccounts), name, configDir,
		func(i int) string { return c.AgyAccounts[i].Name },
		func(i int) string { return c.AgyAccounts[i].ResolvedConfigDir() })
	if idx < 0 {
		return AgyAccount{}, false
	}
	return c.AgyAccounts[idx], true
}

// findAccountIndex runs the shared config-dir anchor lookup over an account
// section of length n, returning the matched index or -1. An exact (dir AND name)
// hit wins outright; a single dir hit wins; several dir hits with no name among
// them are ambiguous and match nothing. The accessor closures let the Claude and
// Antigravity sections share this loop without a common interface, mirroring
// matchRouteIndex.
func findAccountIndex(n int, name, configDir string, names, dirs func(i int) string) int {
	if configDir == "" {
		return -1
	}
	want := filepath.Clean(configDir)
	dirHit, hits := -1, 0
	for i := 0; i < n; i++ {
		if filepath.Clean(dirs(i)) != want {
			continue
		}
		if names(i) == name {
			return i // the account did not move at all
		}
		hits++
		if dirHit < 0 {
			dirHit = i
		}
	}
	if hits == 1 {
		return dirHit // renamed (and/or re-pooled) under a stable dir
	}
	return -1
}

// ResolveAgyAccount returns the name and config directory of the AgyAccount
// matching the given git remote URL or target path.
func (c *Config) ResolveAgyAccount(remoteURL, targetPath string) (name, configDir string, isDefault bool) {
	if len(c.AgyAccounts) == 0 {
		return "", "", false
	}
	idx, isDefault := matchRouteIndex(len(c.AgyAccounts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.AgyAccounts[i].RemoteMatches },
		func(i int) []string { return c.AgyAccounts[i].PathMatches })
	if idx < 0 {
		return "", "", false
	}
	a := c.AgyAccounts[idx]
	return a.Name, a.ResolvedConfigDir(), isDefault
}

// ResolveGHConfigDir picks the GH_CONFIG_DIR for a target by the same routing as
// ResolveClaudeAccount (case-insensitive substring, per account in config order,
// remote_matches then path_matches; first rule-less account is the fallback), but
// over the independent GHAccounts section. It returns "" when gh routing is
// unconfigured or nothing matches and there is no catch-all — "" always means
// "inject nothing / inherit the ambient gh account", mirroring an empty
// CLAUDE_CONFIG_DIR. Because it reads its own section, an account/context may set
// a Claude dir but no gh dir (or vice versa) and each resolves independently.
func (c *Config) ResolveGHConfigDir(remoteURL, targetPath string) string {
	dir, _ := c.ResolveGHAccount(remoteURL, targetPath)
	return dir
}

// ResolveGHAccount is ResolveGHConfigDir plus the resolved account's TokenEnv —
// the env var names its gh token should be injected under at session launch (nil
// when nothing matches, there is no catch-all, or the matched account sets no
// TokenEnv). Same routing as ResolveGHConfigDir; callers that need the token-env
// names use this, and ResolveGHConfigDir delegates here so the two never drift.
func (c *Config) ResolveGHAccount(remoteURL, targetPath string) (configDir string, tokenEnv []string) {
	if len(c.GHAccounts) == 0 {
		return "", nil
	}
	idx, _ := matchRouteIndex(len(c.GHAccounts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.GHAccounts[i].RemoteMatches },
		func(i int) []string { return c.GHAccounts[i].PathMatches })
	if idx < 0 {
		return "", nil
	}
	a := c.GHAccounts[idx]
	return a.ResolvedConfigDir(), a.TokenEnv
}

// ResolveRepoScript picks the repo_scripts entry for a repository whose origin
// remote is remoteURL and whose directory is targetPath (#389). ok is false when the
// section is empty, or when nothing matches and no rule-less catch-all is declared —
// which means "this repo has no setup script", the pre-feature behavior.
//
// index is the entry's position in the configured list. It is returned alongside the
// entry because the caller validates that one entry on its own, and the position is the
// only handle a message about it has: an entry need not be named, and a problem
// reported as `repo_scripts[0]` when the broken entry is the fourth points the user at
// an innocent one.
//
// Routing is the accounts' matcher, unchanged: remote substrings first, then the
// target path (the only signal a direct, non-git session has), evaluated per entry in
// config order. Unlike ResolveClaudeAccount there is no synthetic default to fall back
// to — a script that was never configured must never be invented.
func (c *Config) ResolveRepoScript(remoteURL, targetPath string) (entry RepoScript, index int, ok bool) {
	if len(c.RepoScripts) == 0 {
		return RepoScript{}, -1, false
	}
	idx, _ := matchRouteIndex(len(c.RepoScripts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.RepoScripts[i].RemoteMatches },
		func(i int) []string { return c.RepoScripts[i].PathMatches })
	if idx < 0 {
		return RepoScript{}, -1, false
	}
	return c.RepoScripts[idx], idx, true
}

// matchRouteIndex runs the shared per-account routing for an account section of
// length n: in config order it returns the first account whose remote_matches hit
// lowerRemote (tried first) or whose path_matches hit lowerPath, with
// isDefault=false. When none match it returns the first catch-all (rule-less)
// account with isDefault=true, or (-1, false) when there is neither a match nor a
// catch-all. lowerRemote/lowerPath must already be lowercased. The accessor
// closures let both ResolveClaudeAccount and ResolveGHConfigDir share this loop
// without a common interface; catch-all is derived from them (no rules of either
// kind), matching XAccount.IsCatchAll, so callers pass only the two.
func matchRouteIndex(n int, lowerRemote, lowerPath string, remotes, paths func(i int) []string) (idx int, isDefault bool) {
	defaultIdx := -1
	for i := 0; i < n; i++ {
		if len(remotes(i)) == 0 && len(paths(i)) == 0 && defaultIdx == -1 {
			defaultIdx = i // first account with no route rules is the fallback
		}
		// Per account, in config order: try the origin remote first, then the
		// target directory path (the only signal for non-git/direct sessions).
		if lowerRemote != "" && containsAny(lowerRemote, remotes(i)) {
			return i, false
		}
		if lowerPath != "" && containsAny(lowerPath, paths(i)) {
			return i, false
		}
	}
	if defaultIdx >= 0 {
		return defaultIdx, true
	}
	return -1, false
}

// containsAny reports whether lower (already lowercased) contains any non-empty
// substring in subs (each lowercased on the fly), the shared rule used for both
// remote and path route matching.
func containsAny(lower string, subs []string) bool {
	for _, s := range subs {
		if s != "" && strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// ClaudeAccountNamed resolves a claude_accounts entry by the name it is configured
// under. It is the lookup behind `atrium new --account`, where the caller names the
// login instead of letting routing pick one — so unlike ResolveClaudeAccount it
// consults no rules, no pool and no rotation cursor, and unlike FindClaudeAccount it
// is asked before a session exists and so has no config dir to anchor on.
//
// Returning the whole account rather than its directory is the point. The pin's
// stamped name and its injected CLAUDE_CONFIG_DIR both come off this one struct, which
// is what stops them naming different logins (#854): a resolver that answered with a
// dir alone would leave the name to be settled somewhere else, and somewhere else is
// where routing already answers it.
//
// An exact match on Name, and exactly one. A name two entries share matches nothing:
// which login it means is precisely what a pin exists to settle, and picking the first
// would inject one dir under a name the caller may have meant for the other — the
// divergence this lookup exists to close, re-introduced by the thing closing it.
// (`atrium doctor` is where a config that ambiguous gets reported.) An empty name never
// matches, so "not asked for" cannot resolve to an entry that also declares none.
func (c *Config) ClaudeAccountNamed(name string) (ClaudeAccount, bool) {
	if name == "" {
		return ClaudeAccount{}, false
	}
	hit, hits := ClaudeAccount{}, 0
	for _, a := range c.ClaudeAccounts {
		if a.Name != name {
			continue
		}
		hit, hits = a, hits+1
	}
	if hits != 1 {
		return ClaudeAccount{}, false
	}
	return hit, true
}

// ClaudeAccountVocabulary lists the configured account names in config order, as the
// clause an --account refusal quotes back so a caller that misspelled one is told what it
// could have said rather than only that it was wrong. One function because both the CLI's
// courtesy refusal and the drain's authoritative one say it, and two spellings of the
// same list is how they come to disagree about what the config holds.
//
// Duplicates are not collapsed: a name listed twice is why ClaudeAccountNamed refuses it,
// and hiding that would make the message argue against itself. The empty case is named
// rather than rendered as an empty list — with no claude_accounts the feature is dormant
// and no name would have worked, which is a different mistake from a misspelling.
func (c *Config) ClaudeAccountVocabulary() string {
	if len(c.ClaudeAccounts) == 0 {
		return "no claude_accounts (the feature is off)"
	}
	names := make([]string, 0, len(c.ClaudeAccounts))
	for _, a := range c.ClaudeAccounts {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
