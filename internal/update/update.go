package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/log"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// repoSlug is the canonical release source. Updates always come from this
// repository regardless of any fork the binary was built from.
const repoSlug = "ZviBaratz/atrium"

// Release is a newer-than-current release resolved from the network. The
// embedded library handles are what Apply needs to download and swap.
type Release struct {
	// Version is the release's clean semver, without a leading "v".
	Version string

	updater *selfupdate.Updater
	release *selfupdate.Release
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
		return nil, nil
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
	if !found || latest.LessOrEqual(current) {
		return nil, nil
	}
	return &Release{Version: latest.Version(), updater: updater, release: latest}, nil
}

// Check queries the network unconditionally — the `atrium update` path. It
// returns nil when the running version is already the latest. current should
// be a clean release version (see IsUpdatableVersion); non-release versions
// are inert and return nil.
func Check(ctx context.Context, current string) (*Release, error) {
	return checkRemote(ctx, current)
}

// CheckCached is the TUI-startup check: it consults the 24h cache first so the
// common up-to-date startup never touches the network. A fresh cache that
// already knows about a newer release still queries — Apply needs the resolved
// release handle — but that path only recurs while an available update stays
// uninstalled. The caller is responsible for gating on IsUpdatableVersion.
func CheckCached(ctx context.Context, current string) (*Release, error) {
	now := time.Now()
	if e, ok := loadCache(now); ok && !isNewer(e.Latest, current) {
		return nil, nil
	}
	rel, err := checkRemote(ctx, current)
	if err != nil {
		return nil, err
	}
	latest := current
	if rel != nil {
		latest = rel.Version
	}
	if err := saveCache(latest, now); err != nil {
		log.WarningLog.Printf("failed to save update-check cache: %v", err)
	}
	return rel, nil
}

// Apply downloads the release archive, validates its checksum, and atomically
// replaces the running executable. Running processes keep the old inode; the
// new version takes effect on the next launch. On any failure the old binary
// stays in place (the library rolls back a partial swap).
func (r *Release) Apply(ctx context.Context) error {
	if r.release == nil || r.updater == nil {
		return errors.New("release was not resolved from the network")
	}
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
