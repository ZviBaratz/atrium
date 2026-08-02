package log

import (
	"os"
	"sync"
)

// maxLogBytes caps the live log file. At the rate measured for #566 — 97 bytes/s
// sustained on a 14-session fleet, about 8.4 MB/day — 16 MiB is roughly two days
// per generation, so the log and its single rollover hold about four days of
// history for at most 32 MiB on disk. Before #566 there was no cap of any kind
// and a long-lived install accumulated hundreds of MB.
//
// A bound on a diagnostic artifact, not a setting: a Config field would put a
// knob on something no user has a reason to tune (the memo.Enabled and
// diffContentFloor idiom).
const maxLogBytes = 16 << 20

// rotationSuffix names the single previous generation kept beside the live log.
// One generation, not a numbered series, so a stale install cannot accumulate
// rollovers in a directory nothing prunes.
const rotationSuffix = ".1"

// rotatingFile is the io.WriteCloser behind the package loggers: an append-only
// file that renames itself aside once a write would carry it past maxBytes,
// keeping exactly one previous generation.
//
// It holds no package-level state, so its tests need no globals and no cleanup.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
}

// openRotating opens path for appending, rolling it over at maxBytes.
func openRotating(path string, maxBytes int64) (*rotatingFile, error) {
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	return &rotatingFile{path: path, maxBytes: maxBytes, f: f}, nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
}

// Write appends p, rotating first when it would carry the file past the cap.
//
// The size comes from an fstat of this writer's own descriptor rather than a
// running byte count, because a counter cannot see what another process appended
// to the same file — and at the measured rate (97 bytes/s, roughly a line a
// second) one extra syscall per line is unmeasurable. Testing size > 0 keeps a
// single write larger than the whole cap from rotating an empty file on every
// attempt: an oversized line is written whole rather than lost.
//
// The cap binds the live file, not what is rolled aside: a file that was already
// over the cap when this writer opened it — an install predating the cap, say —
// is rolled aside whole on the first write, so the rollover can exceed maxBytes
// once. The generation after it cannot.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if info, err := r.f.Stat(); err == nil {
		if size := info.Size(); size > 0 && size+int64(len(p)) > r.maxBytes {
			r.rotate()
		}
	}
	return r.f.Write(p)
}

// Close closes the underlying file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// rotate renames the live file aside and takes a fresh descriptor on the path.
//
// Every failure here is deliberately swallowed: a rotation problem must never
// cost a log line, so anything that goes wrong leaves the current descriptor in
// place and the next write tries again. The rename happens before the old
// descriptor is closed — POSIX allows renaming an open file — so a failed rename
// can never strand this writer holding a closed file.
func (r *rotatingFile) rotate() {
	mine, errMine := r.f.Stat()
	onDisk, errDisk := os.Stat(r.path)
	if errMine == nil && errDisk == nil && !os.SameFile(mine, onDisk) {
		// Another process already rotated this file: our descriptor now points at
		// their rolled-aside generation, which their next rotation would replace
		// with our lines still going into it. Take the path back rather than
		// renaming a file that is no longer the live log.
		r.replace()
		return
	}
	if err := os.Rename(r.path, r.path+rotationSuffix); err != nil {
		return
	}
	r.replace()
}

// replace swaps in a fresh descriptor on the path and closes the old one. A
// failed open keeps the old descriptor, so writes still land somewhere.
func (r *rotatingFile) replace() {
	f, err := openAppend(r.path)
	if err != nil {
		return
	}
	_ = r.f.Close()
	r.f = f
}
