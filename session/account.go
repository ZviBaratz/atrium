package session

// Per-session account pinning: which Claude Code account (CLAUDE_CONFIG_DIR)
// and GitHub CLI account (GH_CONFIG_DIR) a session runs under. The config DIRS
// are fixed at creation (injected into the tmux env at session birth); the
// account names, the default-account flag and the cluster pool are labels
// re-derived from those dirs when config renames an account (see
// RestampClaudeAccount). All read without the lock — see the claude*/gh* fields
// on Instance.

// SetClaudeAccount pins the Claude Code account for this session. Call before
// Start: claudeConfigDir is injected at session birth (into the tmux env) and
// cannot change after.
func (i *Instance) SetClaudeAccount(name, configDir string, isDefault bool) {
	i.claudeAccount = name
	i.claudeConfigDir = configDir
	i.claudeAccountDefault = isDefault
}

// SetClaudeAccountPool records the rotation pool this session belongs to (the list
// cluster key; the badge still shows the concrete member). Empty = singleton/none.
// Call at creation; afterwards the pool is re-derived by RestampClaudeAccount, so
// a config-side rename or re-pooling is not frozen in.
func (i *Instance) SetClaudeAccountPool(pool string) { i.claudeAccountPool = pool }

// RestampClaudeAccount re-derives this session's account LABELS from live config —
// the badge name, the default/fallback flag and the cluster pool — and reports
// whether any of them changed. claudeConfigDir is deliberately untouched: it is the
// CLAUDE_CONFIG_DIR injected at session birth, it is the anchor these labels are
// looked up BY (config.Config.FindClaudeAccount), and leaving it alone is what
// makes the pass idempotent and re-derivable rather than destructive (#470).
//
// Unlike SetClaudeAccount this is safe after Start: nothing in the live session
// reads these three fields. They feed the list badge and account cluster
// (AccountClusterKey), the account: filter, and the persisted row — all written on
// the update goroutine, which is also the only caller of ToInstanceData.
func (i *Instance) RestampClaudeAccount(name string, isDefault bool, pool string) bool {
	if i.claudeAccount == name && i.claudeAccountDefault == isDefault && i.claudeAccountPool == pool {
		return false
	}
	i.claudeAccount, i.claudeAccountDefault, i.claudeAccountPool = name, isDefault, pool
	return true
}

// RestampAgyAccount is RestampClaudeAccount for the Antigravity account name (the
// section has no pool and no default-badge flag). agyConfigDir — the anchor, and
// what bwrap isolates — is likewise untouched.
func (i *Instance) RestampAgyAccount(name string) bool {
	if i.agyAccount == name {
		return false
	}
	i.agyAccount = name
	return true
}

// AccountClusterKey is the key the session list groups accounts by: the pinned
// rotation pool when there is one, else the account name, else "" (no account).
// The rule lives here so the list, the persisted cluster order and anything that
// maps an old cluster key to a new one can never disagree about it — ui.accountKey
// delegates, mirroring GroupKey/ui.repoKey.
func (i *Instance) AccountClusterKey() string {
	if i.claudeAccountPool != "" {
		return i.claudeAccountPool
	}
	return i.claudeAccount
}

// SetGHConfigDir pins the GH_CONFIG_DIR for this session. Call before Start: it is
// injected at session birth (into the tmux env, and onto the worktree for Atrium's
// own gh calls) and cannot change after. It is resolved independently of the
// Claude account, so it may be "" (inherit) while a Claude dir is set, or vice
// versa — hence a setter separate from SetClaudeAccount.
func (i *Instance) SetGHConfigDir(dir string) {
	i.ghConfigDir = dir
}

// SetGitHubTokenEnv pins the env var names this session's gh token is injected
// under at launch (config.GHAccount.TokenEnv). Call before Start: the names are
// forwarded to the tmux session, which resolves the token value at session birth.
// The token value is never stored or persisted — only these names are.
func (i *Instance) SetGitHubTokenEnv(names []string) {
	i.githubTokenEnv = names
}

// ClaudeAccountName is the resolved account's display name ("" = none/dormant).
func (i *Instance) ClaudeAccountName() string { return i.claudeAccount }

// ClaudeConfigDir is the CLAUDE_CONFIG_DIR injected at launch ("" = inherit env).
func (i *Instance) ClaudeConfigDir() string { return i.claudeConfigDir }

// GHConfigDir is the GH_CONFIG_DIR injected at launch ("" = inherit env).
func (i *Instance) GHConfigDir() string { return i.ghConfigDir }

// GitHubTokenEnv are the env var names this session's gh token is injected under
// at launch (nil = inject no token).
func (i *Instance) GitHubTokenEnv() []string { return i.githubTokenEnv }

// ClaudeAccountIsDefault reports whether this session is on the default/fallback
// account (the list renders that badge dim rather than accented).
func (i *Instance) ClaudeAccountIsDefault() bool { return i.claudeAccountDefault }

// ClaudeAccountPool returns the rotation pool this session clusters under ("" when
// none). See AccountClusterKey for the grouping rule that builds on it.
func (i *Instance) ClaudeAccountPool() string { return i.claudeAccountPool }

// AgyAccountName is the resolved Antigravity account's name ("" = none/dormant),
// and AgyConfigDir the directory bwrap isolates for it ("" = none). The dir is the
// anchor RestampAgyAccount re-derives the name from.
func (i *Instance) AgyAccountName() string { return i.agyAccount }

// AgyConfigDir is the Antigravity config dir pinned at creation ("" = none).
func (i *Instance) AgyConfigDir() string { return i.agyConfigDir }
