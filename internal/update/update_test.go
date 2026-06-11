package update

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapRemote installs a fake network check, restores the real one on cleanup,
// and returns a pointer to the call counter.
func swapRemote(t *testing.T, fake func(context.Context, string) (*Release, error)) *int {
	t.Helper()
	calls := 0
	orig := checkRemote
	checkRemote = func(ctx context.Context, current string) (*Release, error) {
		calls++
		return fake(ctx, current)
	}
	t.Cleanup(func() { checkRemote = orig })
	return &calls
}

// The common case: a fresh cache that says we're current must not touch the
// network at all — that is the cache's entire job.
func TestCheckCached_FreshUpToDateCacheSkipsNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{CheckedAt: time.Now(), Latest: "0.6.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	assert.Nil(t, rel)
	assert.Zero(t, *calls, "a fresh up-to-date cache must skip the network")
}

func TestCheckCached_NoCacheQueriesNetworkAndSavesResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.Equal(t, 1, *calls)
	e, ok := loadCache()
	require.True(t, ok, "a completed check must refresh the cache")
	assert.Equal(t, "0.7.0", e.Latest)
	assert.True(t, e.FailedAt.IsZero(), "a success clears any failure stamp")
}

func TestCheckCached_UpToDateResultCachesCurrentVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil // up to date
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	assert.Nil(t, rel)
	e, ok := loadCache()
	require.True(t, ok)
	assert.Equal(t, "0.6.0", e.Latest, "up to date caches the current version")
}

// A fresh cache that already knows about a newer release answers from the
// cache: an unresolved (version-only) Release, which is all the notify hint
// needs. The network — and the resolved handle an install needs — waits for
// the TTL to expire, so a pending update costs zero API calls per startup.
func TestCheckCached_PendingNewerReleaseServedFromCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{CheckedAt: time.Now(), Latest: "0.7.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.False(t, rel.Resolved(), "a cache-served release has no install handles")
	assert.Zero(t, *calls, "a fresh cache answers without the network")
}

// A failed check is itself recorded: the next startup inside the backoff
// window must not retry the network (an offline or rate-limited machine would
// otherwise hammer the API on every launch).
func TestCheckCached_FailureRecordsBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("rate limited")
	})

	_, err := CheckCached(context.Background(), "0.6.0")
	require.Error(t, err, "the first failure propagates")
	assert.Equal(t, 1, *calls)
	e, ok := loadCache()
	require.True(t, ok, "the failure must be recorded")
	assert.False(t, e.FailedAt.IsZero())

	rel, err := CheckCached(context.Background(), "0.6.0")
	require.NoError(t, err, "inside the backoff the check is silently skipped")
	assert.Nil(t, rel)
	assert.Equal(t, 1, *calls, "no network retry inside the backoff window")
}

// Inside the failure backoff, a release learned before the cache went stale
// still hints — the user shouldn't lose a known update because the network
// flaked afterwards.
func TestCheckCached_BackoffPreservesKnownRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{
		CheckedAt: time.Now().Add(-2 * cacheTTL), // stale
		Latest:    "0.7.0",
		FailedAt:  time.Now(), // a just-failed refresh
	}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("still offline")
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.Zero(t, *calls)
}

// realCheck must refuse non-release versions with a typed error: the library's
// semver comparison panics on strings like "dev" or a bare commit SHA, and a
// silent nil would read as "up to date" — a lie.
func TestRealCheck_NonReleaseVersionRefuses(t *testing.T) {
	for _, v := range []string{"dev", "0.6.0-5-gabc123", "1cd6ba3"} {
		rel, err := realCheck(context.Background(), v)
		assert.ErrorIs(t, err, ErrNotUpdatable, "realCheck(%q)", v)
		assert.Nil(t, rel)
	}
}

// A Release constructed without going through the network (e.g. served from
// the cache) cannot be applied; the guard must say so, not panic.
func TestApply_UnresolvedReleaseErrors(t *testing.T) {
	rel := &Release{Version: "9.9.9"}
	err := rel.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not resolved")
}
