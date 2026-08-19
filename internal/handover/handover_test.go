package handover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sandbox(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	return path
}

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Payload
		want string
	}{
		{"attach", Payload{Kind: KindAttach, Label: "fix-auth"}, `attached to session "fix-auth"`},
		{"command", Payload{Kind: KindCommand, Label: "test"}, `running the terminal command "test"`},
		{"empty label", Payload{Kind: KindAttach}, ""},
		{"blank label", Payload{Kind: KindAttach, Label: "   "}, ""},
		{"unknown kind", Payload{Kind: "who", Label: "fix-auth"}, ""},
		{"zero", Payload{}, ""},
		// A custom command's name comes from repo config, and this string is printed by
		// a command an agent runs unattended — so a control rune must not reach a terminal.
		{"control rune", Payload{Kind: KindCommand, Label: "test\x1b[2J"}, ""},
		{"newline", Payload{Kind: KindAttach, Label: "fix\nauth"}, ""},
		// Not %q: a non-ASCII title is quoted, never escaped to \uXXXX.
		{"non-ascii", Payload{Kind: KindAttach, Label: "ünïcode"}, `attached to session "ünïcode"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.Describe())
		})
	}
}

// TestPathIsInTheDataDir keeps the lock beside tui.lock / daemon.lock / update.lock
// rather than at a hardcoded ~/.atrium, which the identity rules in CLAUDE.md forbid.
func TestPathIsInTheDataDir(t *testing.T) {
	path := sandbox(t)
	assert.Equal(t, LockFilename, filepath.Base(path))
	assert.Contains(t, path, os.Getenv("HOME"))
}
