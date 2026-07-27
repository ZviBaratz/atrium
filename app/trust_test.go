package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedClaudeJSON writes a minimal .claude.json into dir. The trust shim treats a
// missing file as "claude not onboarded" and never creates one, so a fixture that
// skips this would observe nothing.
func seedClaudeJSON(t *testing.T, dir string) (claudeJSON string) {
	t.Helper()
	claudeJSON = filepath.Join(dir, ".claude.json")
	require.NoError(t, os.WriteFile(claudeJSON, []byte(`{"projects": {}}`), 0600))
	return claudeJSON
}

// setupTrustFixture sandboxes HOME with a seeded ~/.claude.json and no ambient
// CLAUDE_CONFIG_DIR, so the gate can be observed end-to-end without touching the
// developer's real file. Both variables are pinned: CLAUDE_CONFIG_DIR outranks
// HOME in claude's own resolution, so leaving it to the ambient environment would
// make these tests depend on the developer's shell.
func setupTrustFixture(t *testing.T) (claudeJSON string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return seedClaudeJSON(t, home)
}

// worktreesRootTrusted reports whether the .claude.json at path trusts the
// config-derived worktrees root.
func worktreesRootTrusted(t *testing.T, claudeJSON string) bool {
	t.Helper()
	root, err := config.WorktreesDir()
	require.NoError(t, err)
	data, err := os.ReadFile(claudeJSON)
	require.NoError(t, err)
	var m struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(data, &m))
	return m.Projects[root].HasTrustDialogAccepted
}

func TestMaybeTrustWorktreesRoot(t *testing.T) {
	t.Run("disabled flag leaves claude config untouched", func(t *testing.T) {
		claudeJSON := setupTrustFixture(t)
		cfg := config.DefaultConfig() // trust defaults off

		maybeTrustWorktreesRoot(cfg, "claude")

		assert.False(t, worktreesRootTrusted(t, claudeJSON))
	})

	t.Run("enabled flag with a claude program trusts the worktrees root", func(t *testing.T) {
		claudeJSON := setupTrustFixture(t)
		cfg := config.DefaultConfig()
		on := true
		cfg.TrustWorktreesRoot = &on

		maybeTrustWorktreesRoot(cfg, "claude")

		assert.True(t, worktreesRootTrusted(t, claudeJSON))
	})

	t.Run("enabled flag with a claude profile (non-claude default) still trusts", func(t *testing.T) {
		claudeJSON := setupTrustFixture(t)
		cfg := config.DefaultConfig()
		on := true
		cfg.TrustWorktreesRoot = &on
		cfg.DefaultProgram = "gemini"
		cfg.Profiles = []config.Profile{
			{Name: "gemini", Program: "gemini"},
			{Name: "claude", Program: "/usr/local/bin/claude"},
		}

		maybeTrustWorktreesRoot(cfg, "gemini")

		assert.True(t, worktreesRootTrusted(t, claudeJSON))
	})

	t.Run("enabled flag without any claude program is a no-op", func(t *testing.T) {
		claudeJSON := setupTrustFixture(t)
		cfg := config.DefaultConfig()
		on := true
		cfg.TrustWorktreesRoot = &on
		cfg.DefaultProgram = "gemini"
		cfg.Profiles = []config.Profile{{Name: "gemini", Program: "gemini"}}

		maybeTrustWorktreesRoot(cfg, "gemini")

		assert.False(t, worktreesRootTrusted(t, claudeJSON))
	})

	// #359: an unrouted session's claude reads $CLAUDE_CONFIG_DIR/.claude.json
	// when that variable is exported — from a shell profile, or by the enclosing
	// Claude Code session when Atrium is launched from one. Pre-accepting in
	// ~/.claude.json instead left the dialog up with no indication why, which
	// #488's probe reproduced against a live claude 2.1.220.
	t.Run("ambient CLAUDE_CONFIG_DIR outranks home", func(t *testing.T) {
		homeJSON := setupTrustFixture(t)
		ambient := t.TempDir()
		ambientJSON := seedClaudeJSON(t, ambient)
		t.Setenv("CLAUDE_CONFIG_DIR", ambient)
		cfg := config.DefaultConfig()
		on := true
		cfg.TrustWorktreesRoot = &on

		maybeTrustWorktreesRoot(cfg, "claude")

		// Presence first: without this the absence below would also pass for a
		// build that trusted nothing at all.
		assert.True(t, worktreesRootTrusted(t, ambientJSON),
			"trust must land in the file claude actually reads")
		assert.False(t, worktreesRootTrusted(t, homeJSON),
			"$HOME/.claude.json is not the file claude reads when CLAUDE_CONFIG_DIR is set")
	})

	t.Run("each routed account's own config dir is covered", func(t *testing.T) {
		homeJSON := setupTrustFixture(t)
		acct := t.TempDir()
		acctJSON := seedClaudeJSON(t, acct)
		cfg := config.DefaultConfig()
		on := true
		cfg.TrustWorktreesRoot = &on
		cfg.ClaudeAccounts = []config.ClaudeAccount{{Name: "work", ConfigDir: acct}}

		maybeTrustWorktreesRoot(cfg, "claude")

		assert.True(t, worktreesRootTrusted(t, acctJSON), "account-routed sessions read their own dir")
		assert.True(t, worktreesRootTrusted(t, homeJSON), "the ambient/unrouted file is still covered")
	})
}

// claudeTrustDirs is where both halves of #359 live: which dir the unrouted
// session resolves to, and the dedupe that must follow it rather than a hardcoded
// home path.
func TestClaudeTrustDirs(t *testing.T) {
	t.Run("ambient falls back to home when CLAUDE_CONFIG_DIR is unset", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", "")

		assert.Equal(t, []string{home}, claudeTrustDirs(config.DefaultConfig()))
	})

	t.Run("ambient is CLAUDE_CONFIG_DIR when set", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ambient := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", ambient)

		assert.Equal(t, []string{ambient}, claudeTrustDirs(config.DefaultConfig()))
	})

	t.Run("accounts follow the ambient dir, in config order", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cfg := config.DefaultConfig()
		cfg.ClaudeAccounts = []config.ClaudeAccount{
			{Name: "work", ConfigDir: "/c/work"},
			{Name: "oss", ConfigDir: "/c/oss"},
		}

		assert.Equal(t, []string{home, "/c/work", "/c/oss"}, claudeTrustDirs(cfg))
	})

	// The dedupe compares cleaned paths because the two spellings come from
	// different places — the environment and a hand-written config_dir — and a
	// trailing slash is not a different directory. Without cleaning, the same file
	// is rewritten twice.
	t.Run("an account spelling the ambient dir with a trailing slash is one write", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cfg := config.DefaultConfig()
		cfg.ClaudeAccounts = []config.ClaudeAccount{{Name: "same", ConfigDir: home + "/"}}

		assert.Equal(t, []string{home}, claudeTrustDirs(cfg))
	})

	// An inherit-env account names no dir of its own: it reads whatever the
	// unrouted session reads, which is already the ambient entry. Emitting "" would
	// hand the writer a path of ".", i.e. a cwd-relative .claude.json.
	t.Run("inherit-env accounts contribute nothing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cfg := config.DefaultConfig()
		cfg.ClaudeAccounts = []config.ClaudeAccount{{Name: "inherit"}, {Name: "work", ConfigDir: "/c/work"}}

		assert.Equal(t, []string{home, "/c/work"}, claudeTrustDirs(cfg))
	})
}
