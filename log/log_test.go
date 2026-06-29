package log

import (
	"io"
	"os"
	"strings"
	"testing"
)

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

// Close prints the log file path only under --verbose (SetVerbose). Without it a
// normal exit stays quiet. globalLogFile is nil here (no Initialize), which Close
// must tolerate.
func TestClose_VerboseGatesLogPathLine(t *testing.T) {
	t.Cleanup(func() { SetVerbose(false) })

	capture := func() string {
		old := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		Close()
		_ = w.Close()
		os.Stdout = old
		out, _ := io.ReadAll(r)
		return string(out)
	}

	SetVerbose(false)
	if got := capture(); got != "" {
		t.Errorf("Close() without verbose printed %q, want nothing", got)
	}

	SetVerbose(true)
	if got := capture(); !strings.Contains(got, "wrote logs to") {
		t.Errorf("Close() with verbose = %q, want it to mention the log path", got)
	}
}
