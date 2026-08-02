package log

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// rotatingFile holds no package state, so these tests construct it directly with
// a cap small enough to cross deliberately. Nothing here waits on the clock: the
// trigger is a byte count, so every case is driven by what is written.
func newTestRotator(t *testing.T, maxBytes int64) (*rotatingFile, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), fileName)
	r, err := openRotating(path, maxBytes)
	if err != nil {
		t.Fatalf("openRotating: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, path
}

func mustWrite(t *testing.T, r *rotatingFile, s string) {
	t.Helper()
	if _, err := r.Write([]byte(s)); err != nil {
		t.Fatalf("writing %q: %v", s, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// The cap is the point: before #566 the log grew forever, ~8.4 MB/day measured.
// A write that would carry the file past the cap rolls the current contents aside
// first, so the live file never exceeds it.
func TestRotatingFile_RollsOverBeforeExceedingTheCap(t *testing.T) {
	r, path := newTestRotator(t, 20)

	mustWrite(t, r, "first-generation\n") // 17 bytes, under the cap
	if got := read(t, path); got != "first-generation\n" {
		t.Fatalf("before any rotation the live file = %q", got)
	}
	if _, err := os.Stat(path + rotationSuffix); !os.IsNotExist(err) {
		t.Fatal("a rollover appeared before the cap was reached")
	}

	mustWrite(t, r, "second-generation\n") // 17 + 17 > 20, so this rotates first

	if got := read(t, path); got != "second-generation\n" {
		t.Errorf("live file = %q, want only the write that followed the rollover", got)
	}
	if got := read(t, path+rotationSuffix); got != "first-generation\n" {
		t.Errorf("rolled-aside file = %q, want the contents from before the rollover", got)
	}
	if got := int64(len(read(t, path))); got > 20 {
		t.Errorf("live file is %d bytes, past the %d-byte cap", got, 20)
	}
}

// The boundary itself: a write landing exactly on the cap fits, so it must not
// rotate. Only the byte after it does. Without this case a threshold of >= reads
// the same as >, and the log would roll one write early forever.
func TestRotatingFile_AWriteEndingExactlyOnTheCapDoesNotRotate(t *testing.T) {
	r, path := newTestRotator(t, 20)

	mustWrite(t, r, strings.Repeat("a", 10))
	mustWrite(t, r, strings.Repeat("b", 10)) // 10 + 10 == 20, exactly the cap

	if _, err := os.Stat(path + rotationSuffix); !os.IsNotExist(err) {
		t.Error("a write ending exactly on the cap rotated; the threshold is off by one")
	}
	if got := read(t, path); got != strings.Repeat("a", 10)+strings.Repeat("b", 10) {
		t.Errorf("live file = %q, want both writes still in it", got)
	}

	mustWrite(t, r, "c") // 20 + 1 > 20, the first byte past the cap

	if got := read(t, path); got != "c" {
		t.Errorf("live file = %q, want only the write that crossed the cap", got)
	}
}

// Exactly one previous generation is kept. A numbered series would accumulate in
// a directory nothing prunes, which is the objection that keeps pprof files out
// of the data dir.
func TestRotatingFile_KeepsExactlyOneGeneration(t *testing.T) {
	r, path := newTestRotator(t, 12)

	for _, line := range []string{"aaaaaaaaaa\n", "bbbbbbbbbb\n", "cccccccccc\n"} {
		mustWrite(t, r, line)
	}

	if got := read(t, path); got != "cccccccccc\n" {
		t.Errorf("live file = %q, want the newest generation", got)
	}
	if got := read(t, path+rotationSuffix); got != "bbbbbbbbbb\n" {
		t.Errorf("%s = %q, want the second-newest generation", rotationSuffix, got)
	}
	for _, orphan := range []string{path + ".2", path + ".3", path + rotationSuffix + rotationSuffix} {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Errorf("%s exists; only one generation may be kept", orphan)
		}
	}
}

// A single write larger than the whole cap must be written, not lost. Without the
// size > 0 test the writer would rotate an already-empty file on every attempt
// and never make progress.
func TestRotatingFile_OversizedSingleWriteIsNotLost(t *testing.T) {
	r, path := newTestRotator(t, 8)
	huge := strings.Repeat("x", 100) + "\n"

	mustWrite(t, r, huge)

	if got := read(t, path); got != huge {
		t.Errorf("live file holds %d bytes, want the whole %d-byte write", len(got), len(huge))
	}
	if _, err := os.Stat(path + rotationSuffix); !os.IsNotExist(err) {
		t.Error("an empty file was rotated aside; the oversize guard is gone")
	}
}

// Another process can rotate the same log: the TUI and the daemon are mutually
// exclusive, but a short-lived `atrium ls` is not. When that happens this
// writer's descriptor points at the other process's rolled-aside generation, and
// renaming it would replace their file with ours and unlink the inode our own
// lines are still going into. Take the path back instead.
func TestRotatingFile_ReopensWhenAnotherProcessRotatedTheFile(t *testing.T) {
	r, path := newTestRotator(t, 20)
	mustWrite(t, r, "ours-before\n")

	// Simulate the other process rotating: our descriptor follows the rename.
	if err := os.Rename(path, path+rotationSuffix); err != nil {
		t.Fatalf("simulating another process's rotation: %v", err)
	}
	if err := os.WriteFile(path, []byte("theirs\n"), fileMode); err != nil {
		t.Fatalf("simulating the other process's fresh log: %v", err)
	}

	// Big enough to trip the cap, so the rotation path runs.
	mustWrite(t, r, strings.Repeat("y", 18)+"\n")

	if got := read(t, path+rotationSuffix); !strings.Contains(got, "ours-before") {
		t.Errorf("%s = %q, want the other process's rollover left intact", rotationSuffix, got)
	}
	live := read(t, path)
	if !strings.Contains(live, "yyyy") {
		t.Errorf("live file = %q, want our write to have landed in the current log", live)
	}
	if !strings.Contains(live, "theirs") {
		t.Errorf("live file = %q, want the other process's line still there — we replaced their log", live)
	}
}

// A rotation that renames the file aside and then cannot reopen the path leaves
// this writer holding the rolled-aside generation with nothing at the live path.
// The next write must take the path back. Without that, every later rotation
// attempt renames a source that no longer exists and fails: the writer appends to
// the rollover for the rest of the process's life, the cap stops binding the file
// actually being written, and the path `atrium debug` reports stays empty.
//
// The same state is reachable without a failed reopen — anything that removes the
// live log out from under a running Atrium — so this is the general recovery.
func TestRotatingFile_TakesThePathBackWhenNothingIsThere(t *testing.T) {
	r, path := newTestRotator(t, 20)
	mustWrite(t, r, "before-the-strand\n")

	// The state left behind by a rotation whose rename succeeded and whose reopen
	// did not: our descriptor followed the rename, and the path is now empty.
	if err := os.Rename(path, path+rotationSuffix); err != nil {
		t.Fatalf("simulating a rotation that could not reopen the path: %v", err)
	}

	// Big enough to trip the cap, so the rotation path runs.
	mustWrite(t, r, strings.Repeat("z", 18)+"\n")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the live path was never taken back: %v", err)
	}
	if got := read(t, path); !strings.Contains(got, "zzzz") {
		t.Errorf("live file = %q, want the write that followed the strand", got)
	}
	if got := read(t, path+rotationSuffix); !strings.Contains(got, "before-the-strand") {
		t.Errorf("%s = %q, want the stranded generation left intact", rotationSuffix, got)
	}
}

// Three loggers share one writer, so writes are concurrent by construction.
// Guards the mutex under -race.
func TestRotatingFile_ConcurrentWritesAreSerialised(t *testing.T) {
	r, path := newTestRotator(t, 64)

	// t.Fatalf may only be called from the test goroutine, so writers report back
	// rather than failing in place.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Write([]byte("0123456789\n")); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}

	// Whatever the interleaving, no line may be torn: every line is the one that
	// was written, and the live file respects the cap.
	for _, line := range strings.Split(strings.TrimSuffix(read(t, path), "\n"), "\n") {
		if line != "" && line != "0123456789" {
			t.Fatalf("torn line %q — writes were not serialised", line)
		}
	}
}
