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

	require.NoError(t, saveCache("0.7.0", now))

	e, ok := loadCache(now.Add(time.Hour))
	require.True(t, ok, "an hour-old entry is fresh")
	assert.Equal(t, "0.7.0", e.Latest)
}

func TestCache_MissingFileIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, ok := loadCache(time.Now())
	assert.False(t, ok)
}

func TestCache_StaleEntryIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	require.NoError(t, saveCache("0.7.0", now))

	_, ok := loadCache(now.Add(cacheTTL + time.Minute))
	assert.False(t, ok, "an entry past the TTL must force a fresh network check")
}

func TestCache_ExactTTLBoundaryIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	require.NoError(t, saveCache("0.7.0", now))

	_, ok := loadCache(now.Add(cacheTTL))
	assert.False(t, ok, "an entry exactly cacheTTL old is stale, not fresh")
}

func TestCache_FutureTimestampIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	require.NoError(t, saveCache("0.7.0", now.Add(48*time.Hour)))

	_, ok := loadCache(now)
	assert.False(t, ok, "a clock-skewed future entry must not pin the cache forever")
}

func TestCache_CorruptFileIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0o644))

	_, ok := loadCache(time.Now())
	assert.False(t, ok)
}
