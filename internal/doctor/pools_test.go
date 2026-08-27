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

// config_dir is hand-written, so one directory reaches this check spelled several
// ways. Bucketing the raw string put "/d" and "/d/" in different buckets and the
// section went silent on a pool that is entirely a no-op — the one thing it exists to
// catch. NormalizedConfigDir's own doc names this hazard; this section just never
// used it.
func TestCheckPoolsSeesThroughATrailingSlash(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/d", Pool: "p"},
		{Name: "b", ConfigDir: "/d/", Pool: "p"},
	}}
	warns := CheckPools(cfg)
	require.Len(t, warns, 1, "a trailing slash hid a rotation no-op: %+v", warns)
	assert.Contains(t, warns[0].Detail, `"a" and "b" share /d`)
}

// PoolMembers counts an account with no pool of its own whose NAME is another
// account's pool, because that is what rotation resolves through. Scanning for a
// matching Pool field skipped the anchor, left one member, and reported nothing —
// on a pool where both members are the same login.
func TestCheckPoolsIncludesTheUnpooledAnchor(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "/d"},
		{Name: "work-alt", ConfigDir: "/d", Pool: "work"},
	}}
	warns := CheckPools(cfg)
	require.Len(t, warns, 1, "the unpooled anchor was skipped: %+v", warns)
	assert.Equal(t, "work", warns[0].Pool)
	assert.Contains(t, warns[0].Detail, `"work" and "work-alt" share /d`)
}

// Two members with no config_dir are the same login for a different reason: both
// inherit the ambient one. Naming that as a shared directory printed the sentence
// with a hole where the path should be.
func TestCheckPoolsNamesTheAmbientCollision(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a", Pool: "p"},
		{Name: "b", Pool: "p"},
	}}
	warns := CheckPools(cfg)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0].Detail, `"a" and "b" inherit the ambient CLAUDE_CONFIG_DIR`)
	assert.NotContains(t, warns[0].Detail, "share  ", "an empty dir rendered as a path")
}

// Two separate collisions in one pool must render in a stable order, or the section
// reshuffles between runs on an unchanged config. Ranging over the bucket map did.
func TestCheckPoolsIsOrdered(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a1", ConfigDir: "/one", Pool: "p"},
		{Name: "a2", ConfigDir: "/one", Pool: "p"},
		{Name: "b1", ConfigDir: "/two", Pool: "p"},
		{Name: "b2", ConfigDir: "/two", Pool: "p"},
	}}
	first := CheckPools(cfg)
	require.Len(t, first, 2)
	for range 20 {
		assert.Equal(t, first, CheckPools(cfg))
	}
	assert.Contains(t, first[0].Detail, "/one")
	assert.Contains(t, first[1].Detail, "/two")
}

func TestCheckPoolsNilConfig(t *testing.T) {
	assert.Nil(t, CheckPools(nil))
}
