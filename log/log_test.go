package log

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture swaps one of the process's standard streams for a pipe, runs fn, and
// returns what was written. target is &os.Stdout or &os.Stderr.
func capture(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	old := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	*target = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe: %v", err)
	}
	*target = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading the pipe: %v", err)
	}
	return string(out)
}

// readLog returns the contents of the log file in dir.
func readLog(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("reading the log in %s: %v", dir, err)
	}
	return string(b)
}

// The package loggers must be usable before Initialize() is called. Initialize()
// only runs from main(); tests and early-startup code paths (e.g.
// config.DefaultConfig logging that the claude binary was not found) log without
// it. A nil logger there panics with a nil-pointer dereference — this reproduced
// as a CI segfault in session and session/git tests on runners lacking claude.
func TestLoggersUsableBeforeInitialize(t *testing.T) {
	if InfoLog == nil || WarningLog == nil || ErrorLog == nil {
		t.Fatal("package loggers must be non-nil before Initialize()")
	}

	// Must not panic.
	InfoLog.Printf("info %d", 1)
	WarningLog.Printf("warning %d", 2)
	ErrorLog.Printf("error %d", 3)
}

// A log file that cannot be opened must not be fatal (#567): Initialize returns,
// leaves the loggers discarding, and records why.
//
// "Did not panic" is not the assertion — that would pass on a build where the
// loggers had silently become nil-safe for some other reason. What is asserted is
// that the loggers are still pointed at io.Discard, that nothing reached the file
// Initialize could not open, and that the reason survived for Close to report.
func TestInitialize_UnopenableDestinationFallsBackToDiscard(t *testing.T) {
	cases := []struct {
		name string
		// dir returns the destination to hand Initialize, and the file whose
		// emptiness proves nothing was written (empty when there is no such file).
		dir func(t *testing.T) (dir, unwritable string)
	}{{
		// The reproduction from #567: an atrium.log the user cannot write.
		name: "log file is not writable",
		dir: func(t *testing.T) (string, string) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores the file mode, so this destination is openable")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, fileName)
			if err := os.WriteFile(path, nil, 0o444); err != nil {
				t.Fatalf("seeding an unwritable log: %v", err)
			}
			return dir, path
		},
	}, {
		// Fails with ENOTDIR for every user including root, so this case runs
		// everywhere the one above is skipped.
		name: "destination's parent is a regular file",
		dir: func(t *testing.T) (string, string) {
			parent := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(parent, nil, 0o600); err != nil {
				t.Fatalf("seeding a file where a directory would go: %v", err)
			}
			return filepath.Join(parent, "sub"), ""
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, unwritable := tc.dir(t)

			t.Cleanup(Initialize(dir, false))

			// Must not panic.
			InfoLog.Printf("info %d", 1)
			WarningLog.Printf("warning %d", 2)
			ErrorLog.Printf("error %d", 3)

			for _, l := range []struct {
				name string
				w    io.Writer
			}{
				{"InfoLog", InfoLog.Writer()},
				{"WarningLog", WarningLog.Writer()},
				{"ErrorLog", ErrorLog.Writer()},
			} {
				if l.w != io.Discard {
					t.Errorf("%s writes to %T after a failed Initialize, want io.Discard", l.name, l.w)
				}
			}

			path, err := Destination()
			if err == nil {
				t.Fatal("Destination() reported no error after a failed open")
			}
			if want := filepath.Join(dir, fileName); path != want {
				t.Errorf("Destination() path = %q, want %q — the report must name the file it tried", path, want)
			}

			if unwritable != "" {
				b, readErr := os.ReadFile(unwritable)
				if readErr != nil {
					t.Fatalf("reading the unwritable log: %v", readErr)
				}
				if len(b) != 0 {
					t.Errorf("the unopenable log received %d bytes; the loggers were not left discarding", len(b))
				}
			}

			stderr := capture(t, &os.Stderr, Close)
			for _, want := range []string{"could not open log file", filepath.Join(dir, fileName), "discarded"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("Close() stderr = %q, want it to mention %q", stderr, want)
				}
			}
		})
	}
}

// Two Initialize destinations must produce two log files (#566). Before the
// destination was a parameter it was one fixed path in the shared temp dir, so
// every Atrium on the host wrote into the same file with nothing to tell them
// apart.
func TestInitialize_TwoDestinationsProduceTwoFiles(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()

	restoreFirst := Initialize(first, false)
	InfoLog.Print("belongs to the first instance")
	restoreFirst()

	t.Cleanup(Initialize(second, false))
	InfoLog.Print("belongs to the second instance")
	Close()

	firstLog, secondLog := readLog(t, first), readLog(t, second)
	if !strings.Contains(firstLog, "first instance") {
		t.Errorf("first log = %q, want the first instance's line", firstLog)
	}
	if strings.Contains(firstLog, "second instance") {
		t.Errorf("first log = %q, want it to hold nothing from the second instance", firstLog)
	}
	if !strings.Contains(secondLog, "second instance") {
		t.Errorf("second log = %q, want the second instance's line", secondLog)
	}
	if strings.Contains(secondLog, "first instance") {
		t.Errorf("second log = %q, want it to hold nothing from the first instance", secondLog)
	}
}

// Close prints the log file path only under --verbose (SetVerbose). Without it a
// normal exit stays quiet.
func TestClose_VerboseGatesLogPathLine(t *testing.T) {
	dir := t.TempDir()

	t.Cleanup(Initialize(dir, false))
	t.Cleanup(SetVerbose(false))

	SetVerbose(false)
	if got := capture(t, &os.Stdout, Close); got != "" {
		t.Errorf("Close() without verbose printed %q, want nothing", got)
	}

	t.Cleanup(Initialize(dir, false))
	SetVerbose(true)
	got := capture(t, &os.Stdout, Close)
	if !strings.Contains(got, "wrote logs to") || !strings.Contains(got, dir) {
		t.Errorf("Close() with verbose = %q, want it to name the log path under %s", got, dir)
	}
}

// The "no log file" notice is NOT verbose-gated. It is the only signal that
// diagnostics are being dropped, so a user who never passes --verbose — which is
// every user, by default — must still see it. The negative control is the
// verbose=false half: a notice that only appeared under --verbose would pass a
// test that merely turned verbose on.
func TestClose_ReportsAFailedOpenUnconditionally(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file mode, so the destination is openable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), nil, 0o444); err != nil {
		t.Fatalf("seeding an unwritable log: %v", err)
	}

	t.Cleanup(SetVerbose(false))
	SetVerbose(false)

	t.Cleanup(Initialize(dir, false))
	stderr := capture(t, &os.Stderr, Close)
	if !strings.Contains(stderr, "could not open log file") {
		t.Errorf("Close() stderr with verbose off = %q, want the failure notice", stderr)
	}
}

// Close tolerates being called twice: the second call must not report a
// "file already closed" error from the descriptor the first one closed.
func TestClose_IsIdempotent(t *testing.T) {
	t.Cleanup(Initialize(t.TempDir(), false))

	if got := capture(t, &os.Stderr, Close); got != "" {
		t.Fatalf("first Close() printed %q, want nothing", got)
	}
	if got := capture(t, &os.Stderr, Close); got != "" {
		t.Errorf("second Close() printed %q, want nothing", got)
	}
}

// SetVerbose returns its own undo, mirroring theme.Set.
func TestSetVerbose_RestoresPreviousValue(t *testing.T) {
	t.Cleanup(SetVerbose(false))

	SetVerbose(true)
	restore := SetVerbose(false)
	if verbose {
		t.Fatal("SetVerbose(false) did not take effect")
	}
	restore()
	if !verbose {
		t.Error("the restore returned by SetVerbose did not put the previous value back")
	}
}

// Initialize returns its own undo too, so a test can point the process-global
// loggers somewhere and hand the reversal to t.Cleanup. Asserted structurally:
// after the restore, a write must land in the first destination and not the
// second.
func TestInitialize_RestoreRebindsThePreviousLoggers(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()

	t.Cleanup(Initialize(first, false))

	restoreSecond := Initialize(second, false)
	InfoLog.Print("while the second destination is active")
	restoreSecond()

	InfoLog.Print("after the restore")
	Close()

	if got := readLog(t, first); !strings.Contains(got, "after the restore") {
		t.Errorf("first log = %q, want the post-restore line — the restore did not rebind the loggers", got)
	}
	if got := readLog(t, second); strings.Contains(got, "after the restore") {
		t.Errorf("second log = %q, want nothing written after the restore", got)
	}
}

// The daemon prefix is what distinguishes TUI from daemon lines in a shared file.
func TestInitialize_DaemonPrefixesEveryLine(t *testing.T) {
	cases := []struct {
		name       string
		daemon     bool
		wantPrefix string
	}{
		{"tui", false, "INFO:"},
		{"daemon", true, "[DAEMON] INFO:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(Initialize(dir, tc.daemon))
			InfoLog.Print("a line")
			Close()

			got := readLog(t, dir)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("log = %q, want it to start with %q", got, tc.wantPrefix)
			}
			if !tc.daemon && strings.Contains(got, "[DAEMON]") {
				t.Errorf("log = %q, want no daemon marker on a TUI run", got)
			}
		})
	}
}

// Initialize creates the destination directory. config.GetConfigDir resolves a
// path without creating it, and Initialize runs before config.LoadConfig — the
// call that would otherwise create the data dir — so on a fresh install nothing
// exists yet and a first run would silently have no log.
func TestInitialize_CreatesTheDestinationDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")

	t.Cleanup(Initialize(dir, false))

	if _, err := Destination(); err != nil {
		t.Fatalf("Initialize into a missing directory failed: %v", err)
	}
	InfoLog.Print("a line")
	Close()

	if got := readLog(t, dir); !strings.Contains(got, "a line") {
		t.Errorf("log = %q, want the line written after the directory was created", got)
	}
}
