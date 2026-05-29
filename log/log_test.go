package log

import "testing"

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
