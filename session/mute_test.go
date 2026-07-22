package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstance_SetMuted(t *testing.T) {
	i := &Instance{Title: "t"}
	require.False(t, i.Muted(), "a fresh session is not muted")
	i.SetMuted(true)
	require.True(t, i.Muted())
	i.SetMuted(false)
	require.False(t, i.Muted())
}

func TestToInstanceData_CarriesMuted(t *testing.T) {
	i := &Instance{Title: "t"}
	i.SetMuted(true)
	require.True(t, i.ToInstanceData().Muted)
}

// TestMuted_RoundTrip covers AC #5: a muted session's mute survives a restart —
// through ToInstanceData → JSON → FromInstanceData.
func TestMuted_RoundTrip(t *testing.T) {
	inst := &Instance{Title: "m", status: Paused, started: true, direct: true, Path: t.TempDir(), Program: "claude"}
	inst.SetMuted(true)

	data := inst.ToInstanceData()
	require.True(t, data.Muted, "mute must be persisted")

	blob, err := json.Marshal(data)
	require.NoError(t, err)
	var decoded InstanceData
	require.NoError(t, json.Unmarshal(blob, &decoded))

	restored, err := FromInstanceData(context.Background(), decoded, "session/")
	require.NoError(t, err)
	require.True(t, restored.Muted(), "mute must survive restart")
}

func TestInstanceData_MutedJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(InstanceData{Title: "t", Muted: true})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"muted":true`)

	// omitempty: an unmuted session is byte-identical to before the field existed.
	b, err = json.Marshal(InstanceData{Title: "t"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"muted"`)

	// A legacy state.json with no muted key decodes to false.
	var d InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t"}`), &d))
	assert.False(t, d.Muted)
}
