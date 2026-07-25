package doctor

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The state a rename leaves behind (#470): a cluster slot for the old account name,
// a rate-limit flag on it, and a rotation cursor for the pool it used to be in.
func TestCheckAccountKeys_ReportsOrphans(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "zvi.baratz", ConfigDir: "~/.claude-work", Pool: "quantivly"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}}
	st := AccountKeyState{
		Order:        []string{"work", "personal"},
		Availability: []string{"work"},
		Rotation:     []string{"oldpool"},
	}

	warns := CheckAccountKeys(cfg, st)
	require.Len(t, warns, 3)
	assert.Equal(t, "work", warns[0].Key)
	assert.Contains(t, warns[0].Detail, "cluster order")
	assert.Equal(t, "work", warns[1].Key)
	assert.Contains(t, warns[1].Detail, "rate-limit")
	assert.Equal(t, "oldpool", warns[2].Key)
	assert.Contains(t, warns[2].Detail, "rotation cursor")

	out := RenderAccountKeys(warns)
	assert.Contains(t, out, "Account state keys:")
	assert.Contains(t, out, `"work"`)
	assert.Contains(t, out, "harmless")
}

// Keys config still declares are clean — including a pool name in the cluster order
// (that is what a pooled cluster's slot looks like) and an account name there.
func TestCheckAccountKeys_LiveKeysClean(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "zvi.baratz", ConfigDir: "~/.claude-work", Pool: "quantivly"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}}
	st := AccountKeyState{
		Order:        []string{"quantivly", "personal", ""}, // "" is the no-account bucket
		Availability: []string{"zvi.baratz"},
		Rotation:     []string{"quantivly"},
	}

	assert.Empty(t, CheckAccountKeys(cfg, st))
	assert.Equal(t, "", RenderAccountKeys(nil))
}

// A rotation cursor may legitimately be keyed by an ungrouped account's name — that
// is how a singleton pool is addressed (config.PoolMembers).
func TestCheckAccountKeys_SingletonPoolCursorIsClean(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "solo", ConfigDir: "~/.c"}}}
	assert.Empty(t, CheckAccountKeys(cfg, AccountKeyState{Rotation: []string{"solo"}}))
}

// Without a configured roster to compare against, every key would look orphaned —
// so the check stays dormant, like the rest of the accounts feature.
func TestCheckAccountKeys_DormantWithoutAccounts(t *testing.T) {
	st := AccountKeyState{Order: []string{"work"}, Availability: []string{"work"}}
	assert.Empty(t, CheckAccountKeys(&config.Config{}, st))
	assert.Empty(t, CheckAccountKeys(nil, st))
}
