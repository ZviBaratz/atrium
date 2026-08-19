//go:build !windows

package handover

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// maxPayloadBytes caps what Held reads back. The payload is a two-field JSON object
// whose only unbounded member is a session title (session.MaxTitleLen runes) or a
// custom command's name, so this is slack rather than a limit — its job is to stop a
// reader from pulling an arbitrary amount into memory if something else ever writes
// to this path.
const maxPayloadBytes = 4096

// Hold takes the handover lock and records what the terminal was handed to, so a
// headless command can name it. The returned release clears the payload and drops
// the lock; the caller must invoke it, and should defer it, since the whole point is
// that the lock is held for exactly as long as the terminal is gone.
//
// An error means the lock was NOT taken — nothing to release, and the caller should
// carry on rather than refuse to hand over the terminal. Attaching must not depend
// on a lock file: the cost of failing open is that a headless command reads the lock
// as free and stays quiet, which is exactly the behaviour that predates this.
func Hold(p Payload) (release func(), err error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	// Unlike Held, which must be able to answer "unknown" without side effects, Hold
	// is called from a process that owns this data dir — mirroring acquireUpdateLock.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Truncate before writing, not after: a shorter payload than the last holder's
	// would otherwise leave that one's tail behind and decode as neither.
	writePayload(f, encodePayload(p))
	return func() {
		// Clear before closing so the next reader in the gap between one Hold's
		// release and the next one's write sees an empty payload rather than a
		// session that is no longer attached (see the package doc).
		writePayload(f, nil)
		// Closing the descriptor releases the flock.
		_ = f.Close()
	}, nil
}

// writePayload replaces the lock file's contents in place. Best-effort throughout:
// the payload only decorates a warning, and no failure here is worth aborting a
// terminal handover for, or worth a second error path in attachCommand.Run.
func writePayload(f *os.File, b []byte) {
	if err := f.Truncate(0); err != nil {
		return
	}
	if len(b) == 0 {
		return
	}
	_, _ = f.WriteAt(b, 0)
}

// Held reports whether a TUI currently has its terminal handed to a child, by
// try-acquiring the lock and immediately releasing it, and returns what it recorded.
//
// The answer is advisory and inherently racy: a handover can begin or end the
// instant after this returns, so callers must use it only to phrase a message. That
// is the same contract tuiRunning states, and for the same reason — the two are read
// together, since a held handover lock only means "parked" alongside a held tui.lock
// (with no TUI at all, this lock is free and the caller has a different thing to say).
//
// known is false when the question could not be answered at all — an unresolvable
// data dir, or a lock that cannot be opened — so a caller can stay silent rather
// than assert something wrong. A missing data dir lands here rather than being
// created: a headless probe has no business materializing one to ask a question.
func Held() (held bool, p Payload, known bool) {
	path, err := Path()
	if err != nil {
		return false, Payload{}, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No TUI has ever handed its terminal over in this data dir. That is an
			// answer, not a failure to get one.
			return false, Payload{}, true
		}
		return false, Payload{}, false
	}
	defer func() { _ = f.Close() }()
	// LOCK_SH, not LOCK_EX: this only needs to discover whether an exclusive holder
	// exists, and a shared request is refused by one just the same. Two concurrent
	// probes then cannot refuse each other and read one another as a live handover.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, readPayload(f), true
		}
		return false, Payload{}, false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, Payload{}, true
}

// readPayload reads the payload out of an already-open lock file. Read from the
// descriptor the probe already holds rather than by path, so a rename between the
// two could not have the caller reporting one file's lock and another's payload.
func readPayload(f *os.File) Payload {
	b, err := io.ReadAll(io.LimitReader(f, maxPayloadBytes))
	if err != nil {
		return Payload{}
	}
	return decodePayload(b)
}
