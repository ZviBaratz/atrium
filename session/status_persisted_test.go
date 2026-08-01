package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusChangedAtRoundTrip is the guarantee that makes the field worth having:
// a session that has been waiting on the user for hours must still say so after the
// TUI restarts. StatusChangedAt lives in memory, stamped by recordStatusChange, and
// before it was persisted every restore re-derived it from the first observed status
// — so the whole fleet read as having just changed, which is indistinguishable from
// a correct answer and wrong for every row.
func TestStatusChangedAtRoundTrip(t *testing.T) {
	inst := &Instance{Title: "d", status: Paused, started: true, direct: true, Path: t.TempDir(), Program: "claude"}
	changed := time.Date(2026, 8, 1, 9, 33, 16, 0, time.UTC)
	inst.statusChangedAt = changed

	data := inst.ToInstanceData()
	require.True(t, data.StatusChangedAt.Equal(changed), "StatusChangedAt must be persisted")

	blob, err := json.Marshal(data)
	require.NoError(t, err)
	var decoded InstanceData
	require.NoError(t, json.Unmarshal(blob, &decoded))

	restored, err := FromInstanceData(context.Background(), decoded, "session/")
	require.NoError(t, err)
	assert.True(t, restored.StatusChangedAt().Equal(changed),
		"the stamp must survive restore, not restart from now")
}

// A state file written before the field existed decodes to the zero time rather than
// to a fabricated one, and recordStatusChange stamps it on first observation — the
// path it already had for a brand-new instance.
func TestStatusChangedAtAbsentFromOlderStateFile(t *testing.T) {
	var decoded InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"d","path":"`+t.TempDir()+`","direct":true,"program":"claude"}`), &decoded))
	require.True(t, decoded.StatusChangedAt.IsZero(), "absence must decode to zero, not to now")

	restored, err := FromInstanceData(context.Background(), decoded, "session/")
	require.NoError(t, err)
	require.True(t, restored.StatusChangedAt().IsZero(), "restore must not invent a stamp")

	before := time.Now()
	restored.SetStatus(Running)
	assert.False(t, restored.StatusChangedAt().IsZero(), "the first observed status stamps it")
	assert.False(t, restored.StatusChangedAt().Before(before))
}

// An unobserved status serializes as the zero time and decodes back to zero — the
// value every reader here tests with IsZero, and the one cli_ls maps to JSON null.
// Pinned because the obvious tag for this (omitempty) does NOT work on a time.Time:
// encoding/json omits only basic types, so a reader who trusts the tag would expect
// an absent key and get "0001-01-01T00:00:00Z" instead.
func TestStatusChangedAtZeroSerializesAsZero(t *testing.T) {
	inst := &Instance{Title: "d", status: Paused, started: true, direct: true, Path: t.TempDir(), Program: "claude"}
	blob, err := json.Marshal(inst.ToInstanceData())
	require.NoError(t, err)

	var decoded InstanceData
	require.NoError(t, json.Unmarshal(blob, &decoded))
	assert.True(t, decoded.StatusChangedAt.IsZero(), "an unobserved status must decode back to zero")
}
