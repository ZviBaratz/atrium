package doctor

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckPools_SameConfigDir(t *testing.T) {
	// Two members of one pool pointing at the same config_dir are the same login:
	// rotation is a silent no-op. Flag it.
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work", Pool: "work"},
	}}
	warns := CheckPools(cfg)
	require.Len(t, warns, 1)
	assert.Equal(t, "work", warns[0].Pool)
	assert.Contains(t, warns[0].Detail, "work-1")
	assert.Contains(t, warns[0].Detail, "work-2")
	assert.Contains(t, RenderPools(warns), "work")
}

func TestCheckPools_DistinctDirsClean(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}}
	assert.Empty(t, CheckPools(cfg))
	assert.Equal(t, "", RenderPools(nil))
}
