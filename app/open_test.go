// app/open_test.go
package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chooseOpener: darwin always uses `open`; linux walks the candidate list in
// order and reports a clear error when none exist (headless box).
func TestChooseOpener(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		c, err := chooseOpener("darwin", func(string) (string, error) {
			t.Fatal("lookPath must not be consulted on darwin")
			return "", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "open", c)
	})
	t.Run("linux picks first present", func(t *testing.T) {
		c, err := chooseOpener("linux", func(name string) (string, error) {
			if name == "x-www-browser" {
				return "/usr/bin/x-www-browser", nil
			}
			return "", errors.New("not found")
		})
		require.NoError(t, err)
		assert.Equal(t, "x-www-browser", c)
	})
	t.Run("none found", func(t *testing.T) {
		_, err := chooseOpener("linux", func(string) (string, error) {
			return "", errors.New("not found")
		})
		assert.Error(t, err)
	})
}

// A target that parses as a flag must never reach the opener's argv: pane
// content is untrusted (a crafted markdown link can put anything in a URL).
func TestOpenDetached_RejectsFlagLikeTarget(t *testing.T) {
	assert.Error(t, openDetached("-evil"))
	assert.Error(t, openDetached("--new-window=https://x"))
}

// Control bytes in a target mean an escape-stripping gap upstream; refuse to
// hand them to the opener rather than launching something mangled (the PR #97
// smoke test opened ...pull/97%1B/#97%1B]8;;%1B).
func TestOpenDetached_RejectsControlBytes(t *testing.T) {
	assert.Error(t, openDetached("https://x.com/\x1b]8;;\x1b\\"))
	assert.Error(t, openDetached("https://x.com/a\nb"))
}

// Only web/file URLs are worth handing to a browser opener; ssh/git URLs and
// scp-style remotes open nothing useful and must degrade to copy upstream.
func TestOpenableURL(t *testing.T) {
	assert.True(t, openableURL("https://github.com/x/pull/9"))
	assert.True(t, openableURL("http://localhost:8080"))
	assert.True(t, openableURL("file:///tmp/report.html"))
	assert.False(t, openableURL("ssh://host/repo"))
	assert.False(t, openableURL("git@github.com:x/y.git"))
	assert.False(t, openableURL("/tmp/notes.md"))
}
