package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AmbientClaudeConfigDir is claude's own rule, and the single resolution the
// worktrees-root trust (app) and the gate reader (internal/doctor) now share.
// They implemented it separately until #359, and disagreed: one read $HOME, the
// other $CLAUDE_CONFIG_DIR, so the trust opt-in wrote a file claude never read.
func TestAmbientClaudeConfigDir(t *testing.T) {
	t.Run("CLAUDE_CONFIG_DIR wins when set", func(t *testing.T) {
		t.Setenv("HOME", "/home/someone")
		t.Setenv("CLAUDE_CONFIG_DIR", "/routed/account")

		assert.Equal(t, "/routed/account", AmbientClaudeConfigDir())
	})

	t.Run("falls back to home when unset", func(t *testing.T) {
		t.Setenv("HOME", "/home/someone")
		// t.Setenv first so its cleanup restores whatever the developer's shell
		// had; t.Setenv itself cannot express "unset".
		t.Setenv("CLAUDE_CONFIG_DIR", "/will-be-removed")
		require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))

		assert.Equal(t, "/home/someone", AmbientClaudeConfigDir())
	})

	// An exported-but-empty CLAUDE_CONFIG_DIR is a distinct input from an unset
	// one, and must fall through to home just the same — not to "" (which callers
	// read as "unresolvable") and not to "." (what filepath.Clean would make of an
	// empty path).
	t.Run("set-but-empty behaves as unset", func(t *testing.T) {
		t.Setenv("HOME", "/home/someone")
		t.Setenv("CLAUDE_CONFIG_DIR", "")

		assert.Equal(t, "/home/someone", AmbientClaudeConfigDir())
	})

	// The trailing slash is not normalized here: "" must stay distinguishable as
	// "unresolvable", and filepath.Clean("") is ".". Cleaning is the caller's job,
	// after the empty check.
	t.Run("does not clean the value it returns", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "/routed/account/")

		assert.Equal(t, "/routed/account/", AmbientClaudeConfigDir())
	})
}
