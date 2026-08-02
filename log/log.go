// Package log provides file-backed loggers for the application. The TUI owns
// stdout/stderr, so all diagnostics go to a log file instead of the terminal.
//
// Where that file lives is a parameter, not something this package resolves.
// config imports log, so importing config back would cycle — and that cycle is
// worth avoiding on its own merits: depending only on the standard library is
// what makes the discard defaults below safe to use from inside config while it
// is still resolving the very directory the log lives in. main() owns the
// resolution and passes it in.
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

// fileName is the log's name inside the directory Initialize is given.
const fileName = "atrium.log"

const (
	// dirMode must match the mode config creates the same directory with
	// (config/persist.go and config/state.go both MkdirAll it 0755). Initialize
	// runs before config.LoadConfig, so either of them can be the one that
	// creates the data dir on a fresh install; a mode that disagreed here would
	// leave the result depending on which got there first.
	dirMode os.FileMode = 0o755
	// fileMode is narrower than the directory because the contents are: the log
	// records session titles, worktree paths, branch names and repo paths, and
	// nobody but their owner has any reason to read them.
	fileMode os.FileMode = 0o600
)

// The destination state Initialize replaces and its restore func puts back.
var (
	globalLogFile *rotatingFile
	logPath       string
	initErr       error
)

// verbose gates the "wrote logs to" line Close prints. It defaults off so a normal
// exit stays quiet; SetVerbose turns it on from the --verbose flag.
var verbose bool

// SetVerbose enables verbose mode (Close prints the log file path) and returns a
// function restoring the previous value, mirroring theme.Set so the flip is
// always paired with its undo. Call it before Close (e.g. from a
// PersistentPreRun) when the user passes --verbose; startup discards the return.
func SetVerbose(v bool) (restore func()) {
	prev := verbose
	verbose = v
	return func() { verbose = prev }
}

// Destination reports where Initialize sent the loggers and, if it could not open
// that file, why. A nil error with a non-empty path means the log is live; a
// non-nil error means the loggers are still discarding and nothing is being
// recorded. Both are zero before Initialize runs.
//
// The two are returned together so a caller cannot print the path as though the
// log were being written when it is not.
func Destination() (path string, err error) { return logPath, initErr }

// Initialize redirects the package loggers to atrium.log inside dir, creating dir
// if it does not exist. Call it once at program start and defer Close afterwards;
// daemon selects a "[DAEMON]" prefix so TUI and daemon entries are
// distinguishable in the shared file.
//
// A log that cannot be opened is not fatal. On failure the loggers are left
// discarding — the state they already default to — and the reason is recorded for
// Destination and reported by Close, because a tool that cannot write its log
// should still run (#567). The path is recorded either way, so the report can
// name the file it tried.
//
// It returns a function restoring the previous loggers, mirroring theme.Set so
// the flip is always paired with its undo. Startup discards it, the way
// theme.SetMono's is discarded in main; tests pass it to t.Cleanup.
func Initialize(dir string, daemon bool) (restore func()) {
	prevInfo, prevWarning, prevError := InfoLog, WarningLog, ErrorLog
	prevFile, prevPath, prevErr := globalLogFile, logPath, initErr
	restore = func() {
		if globalLogFile != nil && globalLogFile != prevFile {
			_ = globalLogFile.Close()
		}
		InfoLog, WarningLog, ErrorLog = prevInfo, prevWarning, prevError
		globalLogFile, logPath, initErr = prevFile, prevPath, prevErr
	}

	logPath = filepath.Join(dir, fileName)
	f, err := openDestination(dir, logPath)
	if err != nil {
		initErr = err
		return restore
	}
	initErr = nil

	fmtS := "%s"
	if daemon {
		fmtS = "[DAEMON] %s"
	}
	InfoLog = log.New(f, fmt.Sprintf(fmtS, "INFO:"), log.Ldate|log.Ltime|log.Lshortfile)
	WarningLog = log.New(f, fmt.Sprintf(fmtS, "WARNING:"), log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(f, fmt.Sprintf(fmtS, "ERROR:"), log.Ldate|log.Ltime|log.Lshortfile)

	globalLogFile = f
	return restore
}

// openDestination creates dir and opens the rotating log inside it.
func openDestination(dir, path string) (*rotatingFile, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, err
	}
	return openRotating(path, maxLogBytes)
}

// Close closes the log file opened by Initialize and reports what became of it.
// Closing twice is safe: the second call closes nothing.
//
// A failed open is reported here rather than at Initialize, unconditionally and
// on stderr, because this is the only point that reaches the user in every
// command: all nine call sites defer Close on the line after Initialize, and the
// TUI's defer runs after Bubble Tea has restored the screen, so a line printed at
// Initialize would be wiped by the alt screen in exactly the case that matters
// most. `atrium debug` reports the same fact on demand, via Destination.
//
// With verbose set (--verbose) a run that did open its log also prints where the
// logs went; otherwise it exits quietly so a normal run leaves no trailing line.
// A close error goes to stderr — it cannot go to the loggers, which write to the
// file being closed.
func Close() {
	if globalLogFile != nil {
		if err := globalLogFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close log file: %v\n", err)
		}
		globalLogFile = nil
	}
	switch {
	case initErr != nil:
		fmt.Fprintf(os.Stderr, "atrium: could not open log file %s: %v — diagnostics were discarded\n",
			logPath, initErr)
	case verbose && logPath != "":
		fmt.Println("wrote logs to " + logPath)
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
