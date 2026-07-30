package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ParseSeconds clamps rather than rejects: a diagnostic knob that refuses to run
// because its value was fat-fingered is worse than one that runs with a default.
func TestParseSeconds(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unset", "", defaultWindow},
		{"plain value", "10", 10 * time.Second},
		{"unparseable", "soon", defaultWindow},
		{"zero", "0", defaultWindow},
		{"negative", "-5", defaultWindow},
		{"below minimum is clamped up", "1", minWindow},
		{"above maximum is clamped down", "99999", maxWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseSeconds(tc.raw); got != tc.want {
				t.Errorf("ParseSeconds(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// Profiles land in the OS temp directory, beside atrium.log — never in the data
// dir, which a live TUI owns and `atrium reset` does not clear.
func TestDirIsTheTempDirNotTheDataDir(t *testing.T) {
	if got := Dir(); got != os.TempDir() {
		t.Errorf("Dir() = %q, want %q", got, os.TempDir())
	}
}

// A full run writes the CPU profile plus its two companion snapshots. The
// goroutine dump is the one that earns its place: every tick loop in the app is
// self-arming, so "did one fork, or fail to die?" is a question only it answers.
//
// Caveat for a future reader: this opens a real CPU profile, so it fails if the
// test binary is already being profiled (`go test -cpuprofile`). That is the
// correct failure — a refused start must be visible — not a flake to paper over.
func TestToggleWritesCPUAndSnapshots(t *testing.T) {
	dir := t.TempDir()
	d := NewDumper(dir, maxWindow) // long window: the second Toggle is what stops it

	d.Toggle()
	if !d.Running() {
		t.Fatal("Running() = false after the first Toggle, want a profile open")
	}
	d.Toggle()
	if d.Running() {
		t.Fatal("Running() = true after the second Toggle, want it stopped")
	}

	for _, want := range []string{"atrium-cpu-", "atrium-heap-", "atrium-goroutine-"} {
		if !hasPrefixedFile(t, dir, want) {
			t.Errorf("no %s*.pprof written into %s", want, dir)
		}
	}
}

// A second start while one is open is refused, not propagated. runtime/pprof
// returns an error from a duplicate StartCPUProfile, and a diagnostic must never
// be able to take the app down — so the guard is "the first run keeps going",
// which also means exactly one CPU profile file exists.
func TestSecondStartIsRefused(t *testing.T) {
	dir := t.TempDir()
	d := NewDumper(dir, maxWindow)

	d.start()
	d.start()
	t.Cleanup(d.finish)

	if n := countPrefixedFiles(t, dir, "atrium-cpu-"); n != 1 {
		t.Errorf("wrote %d CPU profiles, want exactly 1 — the second start must be refused", n)
	}
	if !d.Running() {
		t.Error("Running() = false, want the first profile still open after a refused second start")
	}
}

// finish on a dumper that never started is a no-op, so a stray timer or a stop
// after a failed start cannot double-close or panic.
func TestFinishWithoutStartIsANoOp(t *testing.T) {
	dir := t.TempDir()
	d := NewDumper(dir, maxWindow)
	d.finish()
	if n := countPrefixedFiles(t, dir, "atrium-"); n != 0 {
		t.Errorf("wrote %d files without ever starting, want 0", n)
	}
}

func countPrefixedFiles(t *testing.T, dir, prefix string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && filepath.Ext(e.Name()) == ".pprof" {
			n++
		}
	}
	return n
}

func hasPrefixedFile(t *testing.T, dir, prefix string) bool {
	t.Helper()
	return countPrefixedFiles(t, dir, prefix) > 0
}
