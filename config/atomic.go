package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ZviBaratz/atrium/log"
)

// loadJSONFile reads and JSON-decodes filename (resolved under the data dir) into a
// *T. It never fails: a missing file is created from makeDefault and saved, a
// read/parse error logs and returns the default, and an unparseable file is
// quarantined for recovery before falling back. label names the file in log lines
// ("config", "state"). This is the shared core behind LoadConfig and LoadState.
func loadJSONFile[T any](filename, label string, makeDefault func() *T, saveDefault func(*T) error) *T {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return makeDefault()
	}

	path := filepath.Join(configDir, filename)
	// Clear any temp files orphaned by a crash mid-write (see writeFileAtomic).
	sweepStaleTempFiles(path)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Create and save the default when the file doesn't exist yet.
			def := makeDefault()
			if saveErr := saveDefault(def); saveErr != nil {
				log.WarningLog.Printf("failed to save default %s: %v", label, saveErr)
			}
			return def
		}
		log.WarningLog.Printf("failed to get %s file: %v", label, err)
		return makeDefault()
	}

	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		logCorruptFile(label, path, data, err)
		return makeDefault()
	}
	return &out
}

// logCorruptFile preserves an unparseable config/state file via quarantineCorruptFile
// — so its bytes survive for recovery instead of being overwritten by the defaults
// the caller falls back to — and logs the outcome. An empty file is not worth
// archiving; it is just reported.
func logCorruptFile(label, path string, data []byte, parseErr error) {
	if len(data) == 0 {
		log.ErrorLog.Printf("failed to parse %s file: %v", label, parseErr)
		return
	}
	if dst, qerr := quarantineCorruptFile(path); qerr != nil {
		log.ErrorLog.Printf("failed to parse %s file and could not preserve it: parse=%v rename=%v", label, parseErr, qerr)
	} else {
		log.ErrorLog.Printf("failed to parse %s file; preserved corrupt copy at %s: %v", label, dst, parseErr)
	}
}

// writeFileAtomic writes data to path by first writing a temp file in the same
// directory, fsyncing it, renaming it over the target, and fsyncing the
// directory. A reader (or a crash, or a full disk) therefore never observes a
// torn or partially written file: the rename is the commit point, and it either
// takes effect whole or not at all. The trailing directory fsync makes the
// rename itself durable, so a power loss right after the swap cannot silently
// roll back to the previous file.
//
// The temp file is created in filepath.Dir(path) so the rename stays on a single
// filesystem (cross-device renames are not atomic). It is named with a leading
// dot and a ".tmp-" infix so a crash between CreateTemp and Rename leaves an
// identifiable orphan that the load path can sweep (see sweepStaleTempFiles).
//
// This mirrors the temp+rename approach already used for ~/.claude.json in
// session/tmux/trust.go, but without that file's "abort if it changed under us"
// guard: state.json and config.json are Atrium's own files, so we always intend
// to complete the write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	// Removed on every failure path; a no-op once the rename has consumed it.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// Flush to disk before the rename so the bytes are durable at the commit point.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	// CreateTemp makes the file 0600; match the caller's intended mode.
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// The write is already committed and visible; fsyncing the directory only
	// hardens the rename against power loss, so it is best-effort. (Directory
	// fsync is unsupported on some platforms — e.g. Windows — where Sync on a
	// directory handle errors; ignoring it there is correct.)
	syncDir(dir)
	return nil
}

// WriteFileAtomic writes data to path atomically (temp file → fsync → rename →
// directory fsync), the same crash-safe primitive used for config.json and
// state.json. It is the exported entry point for callers outside this package —
// e.g. the daemon's PID file — that want the same all-or-nothing guarantee
// instead of a plain os.WriteFile that can leave a torn file on a crash.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm)
}

// syncDir flushes a directory's metadata so a rename into it survives a crash.
// Best-effort: any error is ignored, including the "not supported on a directory
// handle" failure returned on platforms like Windows.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
}

// sweepStaleTempFiles removes orphaned writeFileAtomic temp files for the given
// target left behind by a hard crash between CreateTemp and Rename. It is
// best-effort: any error is ignored, since a leftover temp is harmless clutter,
// never a correctness problem.
func sweepStaleTempFiles(path string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// quarantineCorruptFile moves an unparseable file aside to "<path>.corrupt" so
// its bytes are preserved for recovery instead of being silently overwritten by
// the defaults a caller falls back to. It returns the destination path on
// success. Best-effort: a rename failure is reported to the caller for logging
// but never blocks startup. With atomic writes in place this should never
// trigger; it is a forensic safety net.
func quarantineCorruptFile(path string) (string, error) {
	dst := path + ".corrupt"
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}
