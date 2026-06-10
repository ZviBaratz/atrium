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
