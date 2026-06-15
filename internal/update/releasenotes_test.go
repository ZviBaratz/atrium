package update

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// IsNewer is the exported wrapper the app uses to detect a version transition;
// it must agree with the private isNewer (v-prefix tolerant, bad data is never
// newer).
func TestIsNewer_Exported(t *testing.T) {
	assert.True(t, IsNewer("0.7.0", "0.6.0"))
	assert.True(t, IsNewer("v0.7.0", "0.6.0"))
	assert.False(t, IsNewer("0.6.0", "0.6.0"))
	assert.False(t, IsNewer("0.5.0", "0.6.0"))
	assert.False(t, IsNewer("garbage", "0.6.0"))
}

// FetchVersion is the "what's new" path. A non-release running version can have
// no release to describe, so it must refuse before touching the network — same
// gate as realCheck.
func TestRealFetchVersion_NonReleaseVersionRefuses(t *testing.T) {
	for _, v := range []string{"dev", "0.6.0-5-gabc123", "1cd6ba3"} {
		rel, err := realFetchVersion(context.Background(), v)
		assert.ErrorIs(t, err, ErrNotUpdatable, "realFetchVersion(%q)", v)
		assert.Nil(t, rel)
	}
}

// FetchVersion delegates through the fetchVersion package var so callers (and
// the app) can fake the network in tests, mirroring Check/checkRemote.
func TestFetchVersion_DelegatesToPackageVar(t *testing.T) {
	orig := fetchVersion
	called := ""
	fetchVersion = func(_ context.Context, version string) (*Release, error) {
		called = version
		return &Release{Version: version, Notes: "notes", URL: "u"}, nil
	}
	t.Cleanup(func() { fetchVersion = orig })

	rel, err := FetchVersion(context.Background(), "0.7.0")
	assert.NoError(t, err)
	assert.Equal(t, "0.7.0", called)
	assert.Equal(t, "notes", rel.Notes)
	assert.Equal(t, "u", rel.URL)
}
