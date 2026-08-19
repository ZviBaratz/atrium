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
		// Never "", however little the payload said: a caller must not be one `if` away
		// from dropping the warning over a cosmetic gap. See Describe.
		{"empty label", Payload{Kind: KindAttach}, "has handed its terminal to a session"},
		{"blank label", Payload{Kind: KindAttach, Label: "   "}, "has handed its terminal to a session"},
		{"command, no label", Payload{Kind: KindCommand}, "has handed its terminal to a command"},
		{"unknown kind", Payload{Kind: "who", Label: "fix-auth"}, "has handed its terminal to another program"},
		{"zero", Payload{}, "has handed its terminal to another program"},
		// A custom command's name comes from repo config, and this string is printed by a
		// command an agent runs unattended — so a control rune must not reach a terminal.
		// The LABEL is dropped, not the finding.
		{"control rune", Payload{Kind: KindCommand, Label: "test\x1b[2J"}, "has handed its terminal to a command"},
		{"newline", Payload{Kind: KindAttach, Label: "fix\nauth"}, "has handed its terminal to a session"},
		// Not %q: a non-ASCII title is quoted, never escaped to \uXXXX.
		{"non-ascii", Payload{Kind: KindAttach, Label: "ünïcode"}, `attached to session "ünïcode"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.p.Describe())
		})
	}
}

// TestResumes: the two kinds end differently and only one is something the reader does.
// "Picked up when you detach" is the actionable sentence for an attach and simply false
// of a terminal-mode custom command, which reaches the same attachCommand.Run and takes
// the same lock — so one wording for both would ship a message that is wrong for half
// its callers.
func TestResumes(t *testing.T) {
	assert.Equal(t, "when you detach", Payload{Kind: KindAttach, Label: "fix-auth"}.Resumes())
	assert.Equal(t, "when that command finishes", Payload{Kind: KindCommand, Label: "test"}.Resumes())
	assert.Equal(t, "when it has its terminal back", Payload{}.Resumes(),
		"the fallback is about the lock, which is held whatever the payload says")
}

// TestPathIsInTheDataDir keeps the lock beside tui.lock / daemon.lock / update.lock
// rather than at a hardcoded ~/.atrium, which the identity rules in CLAUDE.md forbid.
func TestPathIsInTheDataDir(t *testing.T) {
	path := sandbox(t)
	assert.Equal(t, LockFilename, filepath.Base(path))
	assert.Contains(t, path, os.Getenv("HOME"))
}
