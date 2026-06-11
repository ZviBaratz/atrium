package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	require.NoError(t, saveCache(cacheEntry{CheckedAt: now, Latest: "0.7.0"}))

	e, ok := loadCache()
	require.True(t, ok)
	assert.Equal(t, "0.7.0", e.Latest)
	assert.True(t, e.fresh(now.Add(time.Hour)), "an hour-old entry is fresh")
}

func TestCache_MissingFileIsAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, ok := loadCache()
	assert.False(t, ok)
}

func TestCache_Freshness(t *testing.T) {
	now := time.Now()
	e := cacheEntry{CheckedAt: now, Latest: "0.7.0"}

	assert.True(t, e.fresh(now.Add(cacheTTL-time.Minute)))
	assert.False(t, e.fresh(now.Add(cacheTTL)),
		"an entry exactly cacheTTL old is stale, not fresh")
	assert.False(t, e.fresh(now.Add(cacheTTL+time.Minute)),
		"an entry past the TTL must force a fresh network check")
	assert.False(t, cacheEntry{CheckedAt: now.Add(48 * time.Hour)}.fresh(now),
		"a clock-skewed future entry must not pin the cache forever")
}

func TestCache_FailureBackoffWindow(t *testing.T) {
	now := time.Now()
	e := cacheEntry{FailedAt: now}

	assert.True(t, e.failedRecently(now.Add(failureBackoff-time.Minute)))
	assert.False(t, e.failedRecently(now.Add(failureBackoff)),
		"the backoff window is half-open, like the TTL")
	assert.False(t, cacheEntry{FailedAt: now.Add(48 * time.Hour)}.failedRecently(now),
		"a clock-skewed future failure must not suppress checks forever")
	assert.False(t, cacheEntry{}.failedRecently(now),
		"no recorded failure means no backoff")
}

func TestCache_CorruptFileIsAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0o644))

	_, ok := loadCache()
	assert.False(t, ok)
}
