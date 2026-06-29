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

// verbose gates the "wrote logs to" line Close prints. It defaults off so a normal
// exit stays quiet; SetVerbose turns it on from the --verbose flag.
var verbose bool

// SetVerbose enables verbose mode (Close prints the log file path). Call it before
// Close (e.g. from a PersistentPreRun) when the user passes --verbose.
func SetVerbose(v bool) { verbose = v }

// Initialize redirects the package loggers to the log file in the OS temp
// directory. Call it once at program start and defer Close afterwards; daemon
// selects a "[DAEMON]" prefix so TUI and daemon entries are distinguishable in
// the shared file.
func Initialize(daemon bool) {
	f, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("could not open log file: %s", err))
	}

	fmtS := "%s"
	if daemon {
		fmtS = "[DAEMON] %s"
	}
	InfoLog = log.New(f, fmt.Sprintf(fmtS, "INFO:"), log.Ldate|log.Ltime|log.Lshortfile)
	WarningLog = log.New(f, fmt.Sprintf(fmtS, "WARNING:"), log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(f, fmt.Sprintf(fmtS, "ERROR:"), log.Ldate|log.Ltime|log.Lshortfile)

	globalLogFile = f
}

// Close closes the log file opened by Initialize. With verbose set (--verbose) it
// also prints where the logs were written; otherwise it exits quietly so a normal
// run leaves no trailing line. A close error goes to stderr — it cannot go to the
// loggers, which write to the file being closed.
func Close() {
	if globalLogFile != nil {
		if err := globalLogFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close log file: %v\n", err)
		}
	}
	if verbose {
		fmt.Println("wrote logs to " + logFileName)
	}
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
