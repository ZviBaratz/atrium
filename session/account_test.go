package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func accountInstance(t *testing.T) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: "s", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	return inst
}

// The whole point of restamping is that it moves the LABELS and leaves the anchor
// alone: claudeConfigDir is the CLAUDE_CONFIG_DIR the session was born with, the
// key the labels are looked up by, and the reason a later config fix can re-heal.
func TestRestampClaudeAccount_MovesLabelsNotTheAnchor(t *testing.T) {
	inst := accountInstance(t)
	inst.SetClaudeAccount("work", "/home/tester/.claude-work", false)
	inst.SetClaudeAccountPool("")

	require.True(t, inst.RestampClaudeAccount("zvi.baratz", true, "quantivly"))
	assert.Equal(t, "zvi.baratz", inst.ClaudeAccountName())
	assert.True(t, inst.ClaudeAccountIsDefault())
	assert.Equal(t, "quantivly", inst.ClaudeAccountPool())
	assert.Equal(t, "/home/tester/.claude-work", inst.ClaudeConfigDir(),
		"the injected config dir must survive every restamp")

	assert.False(t, inst.RestampClaudeAccount("zvi.baratz", true, "quantivly"),
		"an unchanged restamp reports no change, so a clean launch persists nothing")
}

// Un-pooling is a real config edit too: the pool empties and the cluster key falls
// back to the account name.
func TestRestampClaudeAccount_Unpooling(t *testing.T) {
	inst := accountInstance(t)
	inst.SetClaudeAccount("zvi.baratz", "/home/tester/.claude-work", false)
	inst.SetClaudeAccountPool("quantivly")

	require.True(t, inst.RestampClaudeAccount("zvi.baratz", false, ""))
	assert.Equal(t, "zvi.baratz", inst.AccountClusterKey())
}

func TestRestampAgyAccount(t *testing.T) {
	inst := accountInstance(t)
	inst.SetAgyAccount("agy-old", "/home/tester/.agy-work")

	require.True(t, inst.RestampAgyAccount("agy-new"))
	assert.Equal(t, "agy-new", inst.AgyAccountName())
	assert.Equal(t, "/home/tester/.agy-work", inst.AgyConfigDir())
	assert.False(t, inst.RestampAgyAccount("agy-new"))
}

// One rule for account clustering, so the list and the persisted cluster order
// cannot disagree about what a session's key is.
func TestAccountClusterKey(t *testing.T) {
	inst := accountInstance(t)
	assert.Equal(t, "", inst.AccountClusterKey(), "no account configured")

	inst.SetClaudeAccount("zvi.baratz", "/home/tester/.claude-work", false)
	assert.Equal(t, "zvi.baratz", inst.AccountClusterKey(), "ungrouped: the account name")

	inst.SetClaudeAccountPool("quantivly")
	assert.Equal(t, "quantivly", inst.AccountClusterKey(), "pooled: the pool wins")
}
