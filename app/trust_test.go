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

// setupTrustFixture sandboxes HOME and seeds a minimal ~/.claude.json, so the
// gate can be observed end-to-end without touching the developer's real file.
func setupTrustFixture(t *testing.T) (claudeJSON string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeJSON = filepath.Join(home, ".claude.json")
	require.NoError(t, os.WriteFile(claudeJSON, []byte(`{"projects": {}}`), 0600))
	return claudeJSON
}

// worktreesRootTrusted reports whether the sandboxed ~/.claude.json trusts the
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
}
