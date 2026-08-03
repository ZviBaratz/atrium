package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// ownerMarkerFile records which process owns a root, so a later run can tell an
// orphan from a sibling package's live root. Its presence is also the sweep's only
// licence to delete anything: see rootIsStale.
const ownerMarkerFile = "owner.pid"

// markRootOwner stamps root with this process's pid.
//
// Its error is worth acting on rather than discarding, though not for the reason
// you might expect: an unmarked root is never swept, so failing to write this
// cannot get the root deleted. It means the opposite — the root becomes permanent
// litter no later run can identify or reclaim. A root that cannot be marked is not
// a sandbox, so the callers give up on it rather than leave one behind.
func markRootOwner(root string) error {
	return os.WriteFile(filepath.Join(root, ownerMarkerFile),
		[]byte(strconv.Itoa(os.Getpid())), 0o600)
}

// rootIsStale reports whether root belongs to a process that is gone.
//
// The owner marker is the whole answer, and its absence is never an answer. A root
// naming a dead pid is stale however recently it was touched; a root with no marker,
// or one this process cannot read, is not stale at any age. Age plays no part —
// deliberately, see the unmarked branch below.
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
		// No owner recorded: never stale, at any age. Absence of evidence is not
		// permission to delete — and this is the guard that decides how bad a bug
		// elsewhere can get, because it is the last thing standing between
		// os.RemoveAll and a directory nothing here created.
		//
		// It replaces an age fallback ("unmarked and older than a grace window ⇒
		// orphan"), which was written for the sliver between MkdirTemp returning and
		// the marker landing. That reasoning was sound and the blast radius was not:
		// with the fallback in place, the only thing keeping this sweep away from the
		// rest of /tmp was its glob prefix, and a one-line change to that prefix
		// deleted the developer's live tmux socket directory, their scratch dirs, and
		// most of /tmp. Requiring positive evidence makes a wrong prefix merely wrong
		// instead of catastrophic: /tmp/tmux-<uid> carries no owner.pid, so no glob
		// can make it reapable.
		//
		// The cost is one permanently-leaked empty directory if a process dies inside
		// that microsecond window. That is the right trade against a recursive delete.
		return false
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
	// A prefix short enough to be wrong is a prefix that can match anything, so this
	// refuses rather than trusting its caller. `atrium-` is 7 characters; nothing
	// legitimate here is shorter, and the empty string — what a bad edit produces —
	// would turn the glob below into "every entry in parent".
	if len(prefix) < 7 || parent == "" {
		return
	}
	roots, err := filepath.Glob(filepath.Join(parent, prefix+"*"))
	if err != nil {
		return
	}
	for _, root := range roots {
		// Re-checked here rather than trusted from the glob. The glob is one
		// expression away from matching the whole parent, and when it did, this loop
		// deleted the developer's live tmux socket directory along with most of /tmp.
		// A guard that only exists in the pattern is a guard one edit can remove.
		if !strings.HasPrefix(filepath.Base(root), prefix) {
			continue
		}
		if root == self || !rootIsStale(root) {
			continue
		}
		if release != nil && !release(root) {
			continue
		}
		_ = os.RemoveAll(root)
	}
}
