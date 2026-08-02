//go:build unix

package log

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The log's modes are asserted with the umask zeroed, because a umask can only
// clear bits: without this the test would read whatever the developer's umask
// left behind and pass for the wrong reason. Umask is process-global, so no test
// in this package may run in parallel with this one — none does.
//
// The directory is 0755 to match the mode config creates the same directory with
// (config/persist.go, config/state.go). Initialize runs before config.LoadConfig,
// so on a fresh install either can be the one that creates the data dir, and a
// mode that disagreed would leave the result depending on which got there first.
// The file is 0600 because its contents are narrower than the directory's:
// session titles, worktree paths, branch names and repo paths.
func TestInitialize_NarrowsTheFileButMatchesConfigOnTheDirectory(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := filepath.Join(t.TempDir(), "data-dir")
	t.Cleanup(Initialize(dir, false))
	if _, err := Destination(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat on the created directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("directory mode = %04o, want 0755 to match config's MkdirAll", got)
	}

	fileInfo, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("stat on the log file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("log file mode = %04o, want 0600 — the log is not world-readable", got)
	}
}
