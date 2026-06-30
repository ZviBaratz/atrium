package session

// Per-session account pinning: which Claude Code account (CLAUDE_CONFIG_DIR)
// and GitHub CLI account (GH_CONFIG_DIR) a session runs under. All fixed at
// creation (injected into the tmux env at session birth) and read without the
// lock — see the claude*/gh* fields on Instance.

// SetClaudeAccount pins the Claude Code account for this session. Call before
// Start: claudeConfigDir is injected at session birth (into the tmux env) and
// cannot change after.
func (i *Instance) SetClaudeAccount(name, configDir string, isDefault bool) {
	i.claudeAccount = name
	i.claudeConfigDir = configDir
	i.claudeAccountDefault = isDefault
}

// SetGHConfigDir pins the GH_CONFIG_DIR for this session. Call before Start: it is
// injected at session birth (into the tmux env, and onto the worktree for Atrium's
// own gh calls) and cannot change after. It is resolved independently of the
// Claude account, so it may be "" (inherit) while a Claude dir is set, or vice
// versa — hence a setter separate from SetClaudeAccount.
func (i *Instance) SetGHConfigDir(dir string) {
	i.ghConfigDir = dir
}

// ClaudeAccountName is the resolved account's display name ("" = none/dormant).
func (i *Instance) ClaudeAccountName() string { return i.claudeAccount }

// ClaudeConfigDir is the CLAUDE_CONFIG_DIR injected at launch ("" = inherit env).
func (i *Instance) ClaudeConfigDir() string { return i.claudeConfigDir }

// GHConfigDir is the GH_CONFIG_DIR injected at launch ("" = inherit env).
func (i *Instance) GHConfigDir() string { return i.ghConfigDir }

// ClaudeAccountIsDefault reports whether this session is on the default/fallback
// account (the list renders that badge dim rather than accented).
func (i *Instance) ClaudeAccountIsDefault() bool { return i.claudeAccountDefault }
