// Package cmdlog is a bounded, in-memory, thread-safe record of the external
// subprocesses Atrium runs on the user's behalf (tmux, git, gh). It exists so the
// TUI can show "what did Atrium just run against my repo, and what did it say?" —
// the command-log overlay reads live from the package ring (#372).
//
// It deliberately depends on nothing inside Atrium (only the standard library),
// so the lowest-level packages — cmd, session/git, session/tmux — can call
// cmdlog.RecordCmd without an import cycle. Recording never blocks the caller: Add
// takes a short mutex and returns; there is no I/O on the hot path.
package cmdlog

import (
	"errors"
	"os/exec"
	"sync"
	"time"
)

// maxRecords bounds the ring. Old records are dropped once it fills, so a
// long-running session can never grow the log without limit.
const maxRecords = 500

// stderrTailCap bounds the stored failure output per record (bytes), so a single
// pathological command cannot balloon memory.
const stderrTailCap = 4096

// Record is one finished subprocess: its (redacted) argv, the session it was run
// for (empty when none is in scope), when it started, how long it took, how much
// CPU it burned, its exit status, and — on failure — a bounded tail of its
// stderr/output.
type Record struct {
	Argv string
	// Verb is the "binary subcommand" pair Argv aggregates under, resolved when the
	// record is built rather than re-derived from Argv on every read. Argv is
	// rendered log text and joining has already thrown away where each token ended,
	// so a prelude flag value containing a space cannot be recovered from it — see
	// verbOf. Add fills this from Argv for a record that arrives without one.
	Verb    string
	Session string
	Start   time.Time
	Dur     time.Duration
	// CPU is the user+system CPU the child consumed, as the OS reported it on reap.
	// It is deliberately separate from Dur: a command that waits on the network or
	// on a contended tmux server has a large Dur and a small CPU, and only CPU
	// belongs in a "what is heating this laptop" total.
	//
	// This is the half of Atrium's cost no Go profiler can see. While a child runs,
	// Atrium is blocked in wait4 and contributes nothing to its own profile, so a
	// pprof capture attributes zero to it — measured at 19.4% of a core against
	// 37.2% in-process on a 14-session fleet (#546). Zero when the process never
	// started, and on platforms that report no rusage.
	CPU    time.Duration
	Exit   int
	Err    bool
	Stderr string
}

// ring is a fixed-capacity FIFO of Records guarded by a mutex.
type ring struct {
	mu   sync.Mutex
	buf  []Record
	next int
	full bool
}

func (r *ring) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf == nil {
		r.buf = make([]Record, maxRecords)
	}
	r.buf[r.next] = rec
	r.next = (r.next + 1) % maxRecords
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns the records newest-first (most recent at index 0), keep-filtered
// by keep (nil keeps all). It copies out under the lock, so callers can iterate
// without holding it.
func (r *ring) snapshot(keep func(Record) bool) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	count := maxRecords
	if !r.full {
		count = r.next
		n = r.next
	}
	out := make([]Record, 0, count)
	// Walk backwards from the most-recently-written slot.
	for i := 0; i < count; i++ {
		idx := (n - 1 - i + maxRecords) % maxRecords
		rec := r.buf[idx]
		if keep == nil || keep(rec) {
			out = append(out, rec)
		}
	}
	return out
}

// defaultRing is the process-wide command log, mirroring the log package's
// singleton style so low-level packages can record without threading a handle.
var defaultRing = &ring{}

// Add records rec into the process-wide log.
func Add(rec Record) {
	if rec.Verb == "" {
		rec.Verb = verb(rec.Argv)
	}
	defaultRing.add(rec)
}

// Snapshot returns every recorded command, newest first.
func Snapshot() []Record { return defaultRing.snapshot(nil) }

// Failures returns only the commands that exited non-zero, newest first.
func Failures() []Record { return defaultRing.snapshot(func(r Record) bool { return r.Err }) }

// ForSession returns the commands attributed to session, newest first. This is the
// per-session chronological view (#372 AC5).
func ForSession(session string) []Record {
	return defaultRing.snapshot(func(r Record) bool { return r.Session == session })
}

// Reset clears the log. Intended for tests.
func Reset() {
	defaultRing.mu.Lock()
	defer defaultRing.mu.Unlock()
	defaultRing.buf = nil
	defaultRing.next = 0
	defaultRing.full = false
}

// RecordCmd builds a Record from a finished subprocess and Adds it. cmd must have
// been waited on (Run/Output/Wait have returned), because that is when the kernel
// fills in the rusage this reads; its argv is redacted before storage. failTail is
// any captured output used as the failure tail (only kept when err != nil). The
// exit code and, when present, the OS-captured stderr are read from
// *exec.ExitError.
func RecordCmd(cmd *exec.Cmd, session string, start time.Time, failTail []byte, err error) {
	if cmd == nil {
		return
	}
	rec := Record{
		Argv:    Redact(cmd.Args),
		Verb:    verbOf(cmd.Args),
		Session: session,
		Start:   start,
		Dur:     time.Since(start),
		CPU:     childCPU(cmd),
		Err:     err != nil,
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rec.Exit = ee.ExitCode()
			if len(ee.Stderr) > 0 {
				failTail = ee.Stderr
			}
		} else {
			rec.Exit = -1 // launch/context failure, not a process exit code
		}
		rec.Stderr = tail(failTail, stderrTailCap)
	}
	Add(rec)
}

// childCPU sums the user and system CPU the reaped child consumed.
//
// ProcessState is nil until the process has been waited on, and stays nil when the
// launch itself failed (a missing binary, a cancelled context before exec), so an
// absent state reports zero rather than panicking. On platforms with no rusage the
// stdlib returns zero durations, which is the same answer.
func childCPU(cmd *exec.Cmd) time.Duration {
	if cmd.ProcessState == nil {
		return 0
	}
	return cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
}

func tail(b []byte, limit int) string {
	if len(b) > limit {
		b = b[len(b)-limit:]
	}
	return string(b)
}
