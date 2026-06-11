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

	rel, err := CheckCached(context.Background(), "0.6.0", false)

	require.NoError(t, err)
	assert.Nil(t, rel)
	assert.Zero(t, *calls, "a fresh up-to-date cache must skip the network")
}

func TestCheckCached_NoCacheQueriesNetworkAndSavesResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0", false)

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

	rel, err := CheckCached(context.Background(), "0.6.0", false)

	require.NoError(t, err)
	assert.Nil(t, rel)
	e, ok := loadCache()
	require.True(t, ok)
	assert.Equal(t, "0.6.0", e.Latest, "up to date caches the current version")
}

// A fresh cache that already knows about a newer release answers from the
// cache: an unresolved (version-only) Release, which is all the notify hint
// needs — so in notify mode a pending update costs zero API calls per
// startup. (An install wants resolved handles; that is resolve=true.)
func TestCheckCached_PendingNewerReleaseServedFromCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{CheckedAt: time.Now(), Latest: "0.7.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0", false)

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

	_, err := CheckCached(context.Background(), "0.6.0", false)
	require.Error(t, err, "the first failure propagates")
	assert.Equal(t, 1, *calls)
	e, ok := loadCache()
	require.True(t, ok, "the failure must be recorded")
	assert.False(t, e.FailedAt.IsZero())

	rel, err := CheckCached(context.Background(), "0.6.0", false)
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

	rel, err := CheckCached(context.Background(), "0.6.0", false)

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.Zero(t, *calls)
}

// When the cache has gone stale and the refresh attempt fails, a release the
// cache already knows about must still hint — otherwise an offline machine
// loses the hint on exactly the launches that retry the network.
func TestCheckCached_StaleCacheNetworkFailureServesKnownHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	checkedAt := time.Now().Add(-2 * cacheTTL) // stale, and no recent failure
	require.NoError(t, saveCache(cacheEntry{CheckedAt: checkedAt, Latest: "0.7.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("offline")
	})

	rel, err := CheckCached(context.Background(), "0.6.0", false)

	require.NoError(t, err, "a justified hint is served, not the error")
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.False(t, rel.Resolved())
	assert.Equal(t, 1, *calls, "the stale cache still attempts a refresh")
	e, ok := loadCache()
	require.True(t, ok)
	assert.False(t, e.FailedAt.IsZero(), "the failure engages the backoff")
	assert.Equal(t, "0.7.0", e.Latest, "the known release survives the failure")
	assert.True(t, e.CheckedAt.Equal(checkedAt), "only the failure is stamped")
}

// The hint-past-failure fallback applies only when the cache justifies one:
// with nothing newer on record, a failed refresh still propagates its error.
func TestCheckCached_StaleCacheNetworkFailureUpToDatePropagatesError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{
		CheckedAt: time.Now().Add(-2 * cacheTTL),
		Latest:    "0.6.0",
	}))
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("offline")
	})

	rel, err := CheckCached(context.Background(), "0.6.0", false)

	require.Error(t, err)
	assert.Nil(t, rel)
}

// resolve=true with a pending release re-queries even though the cache is
// fresh: an install needs the handles only a live query carries, and waiting
// out the TTL would defer it up to a day after a failed or interrupted
// install (or a notify→auto switch).
func TestCheckCached_ResolveRequeriesPendingRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	freshCheckedAt := time.Now().Add(-time.Hour) // well inside cacheTTL
	require.NoError(t, saveCache(cacheEntry{CheckedAt: freshCheckedAt, Latest: "0.7.0"}))
	remote := &Release{Version: "0.7.0"}
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return remote, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0", true)

	require.NoError(t, err)
	assert.Same(t, remote, rel, "the live query's release is returned as-is")
	assert.Equal(t, 1, *calls)
	e, ok := loadCache()
	require.True(t, ok)
	assert.True(t, e.CheckedAt.After(freshCheckedAt), "the re-query refreshes the cache")
}

// The failure backoff gates the resolving re-query too: frequent relaunches
// are this app's normal workflow, and a pending install must not turn each of
// them into an API call while the network is known to be failing.
func TestCheckCached_ResolveRespectsFailureBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{
		CheckedAt: time.Now(),
		Latest:    "0.7.0",
		FailedAt:  time.Now(),
	}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0", true)

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.False(t, rel.Resolved(), "inside the backoff the hint is cache-served")
	assert.Zero(t, *calls)
}

// A failed resolving re-query degrades to the cached hint and engages the
// backoff — same fallback as the stale-cache failure path.
func TestCheckCached_ResolveRequeryFailureFallsBackToHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{CheckedAt: time.Now(), Latest: "0.7.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("rate limited")
	})

	rel, err := CheckCached(context.Background(), "0.6.0", true)

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.False(t, rel.Resolved())
	assert.Equal(t, 1, *calls)
	e, ok := loadCache()
	require.True(t, ok)
	assert.False(t, e.FailedAt.IsZero(), "the failed re-query engages the backoff")
}

// resolve=true must not query when nothing is pending: the common up-to-date
// startup stays a zero-network operation in auto mode too.
func TestCheckCached_ResolveFreshUpToDateSkipsNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache(cacheEntry{CheckedAt: time.Now(), Latest: "0.6.0"}))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0", true)

	require.NoError(t, err)
	assert.Nil(t, rel)
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
