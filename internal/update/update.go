package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ZviBaratz/atrium/log"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// repoSlug is the canonical release source. Updates always come from this
// repository regardless of any fork the binary was built from.
const repoSlug = "ZviBaratz/atrium"

// ErrNotUpdatable reports a check on a non-release build (dev, git-describe,
// or SHA-stamped — see IsUpdatableVersion). It is a typed outcome rather than
// a silent nil: a caller that forgets to gate gets an error it can present,
// never a false "you're on the latest version".
var ErrNotUpdatable = errors.New("not a release build; self-update requires a release version")

// Release is a newer-than-current release. One resolved from the network
// carries the library handles Apply needs to download and swap; one served
// from the cache knows only its version (see Resolved).
type Release struct {
	// Version is the release's clean semver, without a leading "v".
	Version string

	updater *selfupdate.Updater
	release *selfupdate.Release
}

// Resolved reports whether Apply has the handles it needs — true only for a
// Release that came from the network. A cache-served Release carries just the
// version: enough for a hint, never for an install.
func (r *Release) Resolved() bool {
	return r.updater != nil && r.release != nil
}

// checkRemote queries the release source. It is a package var so tests can
// fake the network (same pattern as app.copyToClipboard).
var checkRemote = realCheck

// realCheck asks GitHub for the latest release and returns it only when it is
// strictly newer than current; nil means up to date. The checksum validator
// pins every later download to GoReleaser's checksums.txt.
func realCheck(ctx context.Context, current string) (*Release, error) {
	// A non-release version (dev build, git-describe string) can never match a
	// release asset, and the library's semver comparison would panic on it.
	if !IsUpdatableVersion(current) {
		return nil, ErrNotUpdatable
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize updater: %w", err)
	}
	latest, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(repoSlug))
	if err != nil {
		return nil, fmt.Errorf("failed to query the latest release: %w", err)
	}
	if !found {
		// Finding no release at all is a failure, not "up to date": a release
		// build always finds at least its own published release, so this means
		// the platform's asset is missing or the release pipeline broke. The
		// CLI must report it (exit 1), not claim the binary is current.
		return nil, fmt.Errorf("no release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if latest.LessOrEqual(current) {
		return nil, nil
	}
	return &Release{Version: latest.Version(), updater: updater, release: latest}, nil
}

// Check queries the network unconditionally — the `atrium update` path. It
// returns nil when the running version is already the latest, and
// ErrNotUpdatable for a non-release version (see IsUpdatableVersion).
func Check(ctx context.Context, current string) (*Release, error) {
	return checkRemote(ctx, current)
}

// CheckCached is the TUI-startup check: while the 24h cache is fresh it never
// touches the network — an up-to-date verdict short-circuits to nil, and a
// known-newer release is served as an unresolved (version-only) Release, which
// is all the notify hint needs. The network runs only when the cache expires;
// a failed attempt is itself recorded so the retry happens after
// failureBackoff, not on every launch.
func CheckCached(ctx context.Context, current string) (*Release, error) {
	now := time.Now()
	e, ok := loadCache()
	if ok && (e.fresh(now) || e.failedRecently(now)) {
		// Answer from the cache. Inside the failure backoff the entry may be
		// stale, but a previously seen newer release still hints rather than
		// going silent until the network recovers.
		if isNewer(e.Latest, current) {
			return &Release{Version: e.Latest}, nil
		}
		return nil, nil
	}
	rel, err := checkRemote(ctx, current)
	if err != nil {
		// Keep the last successful CheckedAt/Latest; only stamp the failure.
		e.FailedAt = now
		if serr := saveCache(e); serr != nil {
			log.WarningLog.Printf("failed to save update-check cache: %v", serr)
		}
		return nil, err
	}
	latest := current
	if rel != nil {
		latest = rel.Version
	}
	if serr := saveCache(cacheEntry{CheckedAt: now, Latest: latest}); serr != nil {
		log.WarningLog.Printf("failed to save update-check cache: %v", serr)
	}
	return rel, nil
}

// Apply downloads the release archive, validates its checksum, and atomically
// replaces the running executable. Running processes keep the old inode; the
// new version takes effect on the next launch. On any failure the old binary
// stays in place (the library rolls back a partial swap). A cross-process lock
// serializes appliers: the swap renames through fixed .old/.new names next to
// the binary, and two concurrent rename dances can clobber a fresh install.
func (r *Release) Apply(ctx context.Context) error {
	if !r.Resolved() {
		return errors.New("release was not resolved from the network")
	}
	unlock, err := acquireUpdateLock()
	if err != nil {
		return fmt.Errorf("cannot start the update: %w", err)
	}
	defer unlock()
	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return fmt.Errorf("could not locate the running executable: %w", err)
	}
	if err := canReplaceExecutable(exe); err != nil {
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	if err := r.updater.UpdateTo(ctx, r.release, exe); err != nil {
		return fmt.Errorf("update to v%s failed: %w", r.Version, err)
	}
	return nil
}

// canReplaceExecutable verifies the swap can succeed before any download: the
// atomic replace writes a temp file next to exe and renames it into place, so
// write permission on the directory is the real requirement (e.g. a
// package-manager-owned path would fail here, before wasting a download).
func canReplaceExecutable(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".atrium-update-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
