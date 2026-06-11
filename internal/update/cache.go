package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// cacheFileName lives directly in the data dir, next to config.json.
	cacheFileName = "update-check.json"
	// cacheTTL bounds how often startups hit the network. It also respects
	// GitHub's 60 req/h unauthenticated API rate limit.
	cacheTTL = 24 * time.Hour
	// failureBackoff bounds how often startups retry after a failed check.
	// Without it an offline or rate-limited machine would hit the network on
	// every launch — and frequent relaunches are this app's normal workflow —
	// perpetuating the very rate limit that made the check fail.
	failureBackoff = time.Hour
)

// cacheEntry records the outcome of past checks. CheckedAt/Latest describe the
// last completed check (Latest == the current version when up to date);
// FailedAt is the last failed attempt, zero after any success.
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	FailedAt  time.Time `json:"failed_at,omitzero"`
}

// fresh reports whether the last completed check is recent enough to answer
// for the network. A future timestamp reads as stale (clock skew must not pin
// the cache forever).
func (e cacheEntry) fresh(now time.Time) bool {
	return !e.CheckedAt.After(now) && now.Sub(e.CheckedAt) < cacheTTL
}

// failedRecently reports whether the last failed attempt is inside the backoff
// window. A future timestamp reads as no backoff, mirroring fresh.
func (e cacheEntry) failedRecently(now time.Time) bool {
	return !e.FailedAt.After(now) && now.Sub(e.FailedAt) < failureBackoff
}

// cachePath derives the cache location from the data dir — never a hardcoded
// ~/.atrium, because legacy ~/.claude-squad installs keep their directory.
func cachePath() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheFileName), nil
}

// loadCache returns the stored entry and whether one was present and
// parseable. Freshness is the caller's call (fresh/failedRecently): a stale
// entry still carries the last known release and failure state.
func loadCache() (cacheEntry, bool) {
	path, err := cachePath()
	if err != nil {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return cacheEntry{}, false
	}
	return e, true
}

// saveCache records a check outcome. Best-effort consumers may ignore the
// error: a failed write only means the next startup re-checks the network.
// The plain (non-atomic) write is deliberate: a torn write reads as corrupt
// JSON, which loadCache treats as "no cache" — the worst case is one extra
// network check. Adopting the data dir's writeFileAtomic would add temp-file
// sweeping (see config.sweepStaleTempFiles) for no real gain at that stake.
func saveCache(e cacheEntry) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
