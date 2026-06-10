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
	require.NoError(t, saveCache("0.6.0", time.Now()))
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
	e, ok := loadCache(time.Now())
	require.True(t, ok, "a completed check must refresh the cache")
	assert.Equal(t, "0.7.0", e.Latest)
}

func TestCheckCached_UpToDateResultCachesCurrentVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil // up to date
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	assert.Nil(t, rel)
	e, ok := loadCache(time.Now())
	require.True(t, ok)
	assert.Equal(t, "0.6.0", e.Latest, "up to date caches the current version")
}

// A fresh cache that already knows about a newer release still consults the
// network: Apply needs the resolved release handle, and this path only recurs
// while an available update stays uninstalled.
func TestCheckCached_PendingNewerReleaseStillQueries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache("0.7.0", time.Now()))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, 1, *calls)
}

func TestCheckCached_NetworkErrorPropagates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("rate limited")
	})

	_, err := CheckCached(context.Background(), "0.6.0")

	require.Error(t, err)
	_, ok := loadCache(time.Now())
	assert.False(t, ok, "a failed check must not refresh the cache")
}

// realCheck must be inert for non-release versions: the library's semver
// comparison panics on strings like "dev", so the guard short-circuits before
// any network call.
func TestRealCheck_NonReleaseVersionIsInert(t *testing.T) {
	rel, err := realCheck(context.Background(), "dev")
	require.NoError(t, err)
	assert.Nil(t, rel)
}

// A Release constructed without going through the network (e.g. by a test or a
// future cached path) cannot be applied; the guard must say so, not panic.
func TestApply_UnresolvedReleaseErrors(t *testing.T) {
	rel := &Release{Version: "9.9.9"}
	err := rel.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not resolved")
}
