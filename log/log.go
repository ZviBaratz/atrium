// Package log provides file-backed loggers for the application. The TUI owns
// stdout/stderr, so all diagnostics go to a log file in the OS temp directory
// instead of the terminal.
package log

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Loggers default to discarding output so they are safe to use before
// Initialize() runs (which only happens from main()). Tests and early-startup
// code log without Initialize; a nil logger there panics with a nil-pointer
// dereference. Initialize() reassigns these to the file-backed loggers.
var (
	WarningLog = log.New(io.Discard, "WARNING: ", log.LstdFlags)
	InfoLog    = log.New(io.Discard, "INFO: ", log.LstdFlags)
	ErrorLog   = log.New(io.Discard, "ERROR: ", log.LstdFlags)
)

var logFileName = filepath.Join(os.TempDir(), "atrium.log")

var globalLogFile *os.File

// Initialize redirects the package loggers to the log file in the OS temp
// directory. Call it once at program start and defer Close afterwards; daemon
// selects a "[DAEMON]" prefix so TUI and daemon entries are distinguishable in
// the shared file.
func Initialize(daemon bool) {
	f, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("could not open log file: %s", err))
	}

	// Set log format to include timestamp and file/line number
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	fmtS := "%s"
	if daemon {
		fmtS = "[DAEMON] %s"
	}
	InfoLog = log.New(f, fmt.Sprintf(fmtS, "INFO:"), log.Ldate|log.Ltime|log.Lshortfile)
	WarningLog = log.New(f, fmt.Sprintf(fmtS, "WARNING:"), log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(f, fmt.Sprintf(fmtS, "ERROR:"), log.Ldate|log.Ltime|log.Lshortfile)

	globalLogFile = f
}

// Close closes the log file opened by Initialize and tells the user where the
// logs were written.
func Close() {
	_ = globalLogFile.Close()
	// TODO: maybe only print if verbose flag is set?
	fmt.Println("wrote logs to " + logFileName)
}

// Every is used to log at most once every timeout duration.
type Every struct {
	timeout time.Duration
	timer   *time.Timer
}

// NewEvery returns an Every that allows one log line per timeout window.
func NewEvery(timeout time.Duration) *Every {
	return &Every{timeout: timeout}
}

// ShouldLog returns true if the timeout has passed since the last log.
func (e *Every) ShouldLog() bool {
	if e.timer == nil {
		e.timer = time.NewTimer(e.timeout)
		e.timer.Reset(e.timeout)
		return true
	}

	select {
	case <-e.timer.C:
		e.timer.Reset(e.timeout)
		return true
	default:
		return false
	}
}
