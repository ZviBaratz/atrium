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
	// cacheTTL bounds how often startups hit the network in the common
	// up-to-date case. It also respects GitHub's 60 req/h unauthenticated API
	// rate limit.
	cacheTTL = 24 * time.Hour
)

// cacheEntry records the last completed network check. Latest is the newest
// release version seen (== the current version when up to date).
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
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

// loadCache returns the cached entry and whether it is fresh: present,
// parseable, younger than cacheTTL, and not from the future (clock skew must
// not pin the cache forever). Any failure reads as "no cache".
func loadCache(now time.Time) (cacheEntry, bool) {
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
	if e.CheckedAt.After(now) || now.Sub(e.CheckedAt) >= cacheTTL {
		return cacheEntry{}, false
	}
	return e, true
}

// saveCache records a completed check. Best-effort consumers may ignore the
// error: a failed write only means the next startup re-checks the network.
// The plain (non-atomic) write is deliberate: a torn write reads as corrupt
// JSON, which loadCache treats as "no cache" — the worst case is one extra
// network check. Adopting the data dir's writeFileAtomic would add temp-file
// sweeping (see config.sweepStaleTempFiles) for no real gain at that stake.
func saveCache(latest string, now time.Time) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheEntry{CheckedAt: now, Latest: latest})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
