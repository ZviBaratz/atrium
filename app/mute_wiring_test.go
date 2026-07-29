package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Pressing M toggles the selected session's mute (and persists it — the wired home
// has real storage, so a persist error would surface).
func TestMuteKeyTogglesSelectedSession(t *testing.T) {
	h, insts := newNoteWiringHome(t, "alpha")
	inst := insts[0]
	require.False(t, inst.Muted(), "starts unmuted")

	press(t, h, textMsg("M"))
	require.True(t, inst.Muted(), "M mutes the selected session")

	press(t, h, textMsg("M"))
	require.False(t, inst.Muted(), "M again unmutes it")
}
