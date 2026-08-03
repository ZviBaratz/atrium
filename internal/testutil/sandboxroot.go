package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A sandbox root is a temp directory a test binary owns for the length of its run:
// the throwaway HOME (SandboxHomeMain) and the private tmux socket directory
// (installSandboxTmuxTmpdir). Both are removed on the way out — and both are
// therefore leaked by every exit that skips defers, which is any abort, any signal,
// and any os.Exit from inside a test. Measured on this repo before the sweep below
// existed: 633 orphaned HOME roots over five days.
//
// So a root records its owner, and each run reaps the roots whose owners are gone.
// That makes the leak self-healing rather than permanent, which matters most for the
// tmux root, where the orphan is not a directory but a running server holding a
// never-exiting $SHELL.
const (
	// ownerMarkerFile records which process owns a root, so a later run can tell an
	// orphan from a sibling package's live root.
	ownerMarkerFile = "owner.pid"
	// rootGrace is how long a root with no owner marker is presumed live. It covers
	// the window between MkdirTemp returning and the marker being written, during
	// which a concurrently starting package must not mistake a brand-new root for an
	// orphan. `go test ./...` runs package binaries in parallel, so that window is
	// genuinely raced. A root that *has* a marker is judged by it alone — see
	// rootIsStale for why age must not override one.
	rootGrace = 5 * time.Minute
)

// markRootOwner stamps root with this process's pid.
//
// Its error is worth acting on rather than discarding: an unmarked root is judged
// by age alone, so a root that outlives rootGrace unmarked is an orphan by that
// measure — and a sibling package's sweep will delete it out from under the run
// that is still using it. A root that cannot be marked is not a sandbox.
func markRootOwner(root string) error {
	return os.WriteFile(filepath.Join(root, ownerMarkerFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600)
}

// rootIsStale reports whether root belongs to a process that is gone.
//
// The owner marker is consulted first and, when it is there, it is the whole
// answer: a root naming a dead pid is stale however recently it was touched. Age is
// the fallback for a root with no marker, which is the only state the grace window
// was ever for — the sliver between MkdirTemp returning and the marker landing.
// Gating on age first instead would make the grace mean something else entirely,
// because a teardown's own cleanup bumps the root's mtime on the way out: a crashed
// run's root, holding the immortal $SHELL server the tmux sweep exists to reap,
// would go unreaped for another rootGrace after every attempt.
func rootIsStale(root string) bool {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(root, ownerMarkerFile))
	if err != nil && !os.IsNotExist(err) {
		// Unreadable is not the same as absent, and only absent is evidence. A root
		// another user owns is 0700, so this user's sweep reads EACCES here — and
		// answering that with the age fallback would let one user's sweep call another
		// user's live root an orphan. Same rule processAlive applies to EPERM one layer
		// down: what you cannot inspect, you do not get to reap.
		return false
	}
	if err != nil {
		// No owner recorded: a teardown already emptied it — or a sibling package binary
		// created it a microsecond ago and has not laid its marker down yet, which is the
		// only other way this state is reachable now that a failed marker write aborts
		// the install outright. Only age separates them, and `go test ./...` runs those
		// binaries in parallel, so that window is genuinely raced.
		return time.Since(info.ModTime()) >= rootGrace
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return true
	}
	return !processAlive(pid)
}

// processAlive reports whether pid names a running process. Signal 0 performs the
// permission and existence checks without delivering anything.
//
// EPERM counts as alive, not dead: it means the process is there but owned by
// another user. Reading it as "gone" would let this user's sweep decide another
// user's live root is an orphan to kill and delete.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// sweepStaleRoots removes the sandbox roots under parent matching prefix whose
// owning process is gone, skipping self.
//
// release runs before each removal and may veto it by returning false. The tmux
// sweep uses that to refuse to delete a root whose server it could not reap:
// removing the directory out from under a live server is the unreachable orphan
// (#547), not the cure for it. A nil release means there is nothing to reap first.
//
// The prefix is doing safety work, not just naming: this deletes directories, so it
// has to be distinctive enough that it cannot match a developer's own scratch dir.
// Globbing bounds the blast radius to parent's immediate children whose names start
// with it, which is why parent itself can never be a candidate.
func sweepStaleRoots(parent, prefix, self string, release func(string) bool) {
	roots, err := filepath.Glob(filepath.Join(parent, prefix+"*"))
	if err != nil {
		return
	}
	for _, root := range roots {
		if root == self || !rootIsStale(root) {
			continue
		}
		if release != nil && !release(root) {
			continue
		}
		_ = os.RemoveAll(root)
	}
}
