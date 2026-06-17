package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstance_SetNoteTrims(t *testing.T) {
	i := &Instance{Title: "t"}
	i.SetNote("  blocked on review  ")
	require.Equal(t, "blocked on review", i.Note())
	i.SetNote("   ")
	require.Equal(t, "", i.Note(), "whitespace-only note clears")
}

func TestToInstanceData_CarriesNote(t *testing.T) {
	i := &Instance{Title: "t"}
	i.SetNote("park me")
	require.Equal(t, "park me", i.ToInstanceData().Note)
}

func TestInstanceData_NoteJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(InstanceData{Title: "t", Note: "waiting on CI"})
	require.NoError(t, err)
	require.Contains(t, string(b), `"note":"waiting on CI"`)

	// omitempty: an empty note is not written.
	b, err = json.Marshal(InstanceData{Title: "t"})
	require.NoError(t, err)
	require.NotContains(t, string(b), `"note"`)

	// A legacy state.json with no note key decodes to "".
	var d InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t"}`), &d))
	require.Equal(t, "", d.Note)
}
