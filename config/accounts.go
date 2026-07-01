package config

import "strings"

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
