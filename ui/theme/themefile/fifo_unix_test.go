//go:build unix

package themefile

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// TestLoadDoesNotBlockOnAFIFO is the reason the entry filter stats rather than trusting
// DirEntry.IsDir, and it is a LIVENESS test, which is why it drives Load off the test
// goroutine and bounds it.
//
// A FIFO reports IsDir()==false and Ext()==".json", so it used to reach os.ReadFile,
// whose os.Open blocks until a writer appears — with no timeout and nothing written. That
// happens inside main.go's initAppearanceAndTmux, BEFORE tmux.Init and before app.Run, so
// `mkfifo ~/.atrium/themes/x.json` wedged `atrium`, `atrium doctor` and the autoyes daemon
// identically: no output, no diagnostic, no way to tell it apart from a hung terminal.
//
// Asserting on the returned values as well is deliberate. A Load that skipped the whole
// directory on meeting one would also return promptly, and that is a different bug: the
// user's real theme has to still load.
func TestLoadDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "wedge.json"), 0o600))
	writeInto(t, dir, "midnight.json", `{"palette": {"attention": "#ffb454"}}`)

	type result struct {
		loaded   map[string]*theme.Theme
		problems []error
	}
	done := make(chan result, 1)
	go func() {
		loaded, problems := Load(dir)
		done <- result{loaded, problems}
	}()

	select {
	case got := <-done:
		assert.Empty(t, got.problems, "a FIFO is not a theme file; ignoring it is not reporting it")
		assert.Contains(t, got.loaded, "midnight", "the real theme beside it must still load")
	case <-time.After(10 * time.Second):
		t.Fatal("Load blocked on a FIFO; on the launch path this is a hang with no output")
	}
}
