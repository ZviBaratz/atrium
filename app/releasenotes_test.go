package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/internal/update"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapFetchReleaseNotes installs a fake "what's new" fetch for one test.
func swapFetchReleaseNotes(t *testing.T, fake func(context.Context, string) (*update.Release, error)) {
	t.Helper()
	orig := fetchReleaseNotes
	fetchReleaseNotes = fake
	t.Cleanup(func() { fetchReleaseNotes = orig })
}

// newReleaseNotesHome builds a release-version home whose last-shown version is
// preset, so transition detection has something to compare against.
func newReleaseNotesHome(t *testing.T, version, lastShown string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.version = version
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	if lastShown != "" {
		require.NoError(t, h.appState.SetLastNotesVersion(lastShown))
	}
	return h
}

// The toggle off means no notes command at all.
func TestReleaseNotesCmd_OffConfigIsInert(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")
	off := false
	h.appConfig.ShowReleaseNotesAfterUpdate = &off
	assert.Nil(t, h.releaseNotesCmd())
}

// A dev/unstamped build has no release to describe.
func TestReleaseNotesCmd_DevBuildIsInert(t *testing.T) {
	for _, v := range []string{"", "dev", "1cd6ba3"} {
		h := newReleaseNotesHome(t, v, "0.5.0")
		assert.Nil(t, h.releaseNotesCmd(), "version %q", v)
	}
}

// First run with this feature (no last-shown version): seed the current version
// and show nothing — there is no transition to celebrate.
func TestReleaseNotesCmd_EmptyLastSeedsAndReturnsNil(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "")
	require.Equal(t, "", h.appState.GetLastNotesVersion(), "precondition: nothing seen yet")

	assert.Nil(t, h.releaseNotesCmd(), "first run shows nothing")
	assert.Equal(t, "0.6.0", h.appState.GetLastNotesVersion(), "the current version is seeded so no upgrade is detected next launch")
}

// Same or older running version than last shown is not an upgrade.
func TestReleaseNotesCmd_NotNewerIsInert(t *testing.T) {
	for _, last := range []string{"0.6.0", "0.7.0"} {
		h := newReleaseNotesHome(t, "0.6.0", last)
		assert.Nil(t, h.releaseNotesCmd(), "last=%q", last)
	}
}

// A genuine upgrade emits a fetch command yielding the landed version's notes.
func TestReleaseNotesCmd_NewerEmitsFetch(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")
	asked := ""
	swapFetchReleaseNotes(t, func(_ context.Context, version string) (*update.Release, error) {
		asked = version
		return &update.Release{Version: version, Notes: "shiny things", URL: "https://x/v0.6.0"}, nil
	})

	cmd := h.releaseNotesCmd()
	require.NotNil(t, cmd, "an upgrade must produce a fetch command")
	msg := cmd()
	fetched, ok := msg.(releaseNotesFetchedMsg)
	require.True(t, ok)
	assert.Equal(t, "0.6.0", asked, "notes are fetched for the landed version, not the next available one")
	assert.Equal(t, "shiny things", fetched.notes)
	assert.Equal(t, "0.5.0", h.appState.GetLastNotesVersion(), "the version is recorded only on message handling, not at fetch time")
}

// A fetch failure (offline after an auto-update) is silent and leaves the
// last-shown version unchanged so the notes retry on the next launch.
func TestReleaseNotesCmd_FetchFailureIsSilentAndRetries(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")
	swapFetchReleaseNotes(t, func(context.Context, string) (*update.Release, error) {
		return nil, errors.New("offline")
	})

	msg := h.releaseNotesCmd()()
	assert.Nil(t, msg, "a failed fetch yields no message")
	assert.Equal(t, "0.5.0", h.appState.GetLastNotesVersion(), "an unrecorded version retries next launch")
	assert.False(t, h.errBox.HasError(), "update failures never surface as UI errors")
}

// A "not found" release (deleted/yanked, or no asset for this OS/arch — the
// library reports both as a nil release with no error) is permanent, unlike a
// transient fetch error: it must record the landed version so the network is not
// re-queried on every launch. No notes body means no overlay.
func TestReleaseNotesCmd_NotFoundRecordsAndStops(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")
	calls := 0
	swapFetchReleaseNotes(t, func(context.Context, string) (*update.Release, error) {
		calls++
		return nil, nil // found=false: the release isn't there
	})

	msg := h.releaseNotesCmd()()
	fetched, ok := msg.(releaseNotesFetchedMsg)
	require.True(t, ok, "a not-found release still reports back so the version can be recorded")
	assert.Equal(t, "0.6.0", fetched.version, "the landed version is carried, so the handler records it")
	assert.Equal(t, "", fetched.notes, "nothing to show")

	// Feed it through: the version records and no overlay opens.
	h.Update(fetched)
	assert.Equal(t, stateDefault, h.state, "a not-found release opens no modal")
	assert.Equal(t, "0.6.0", h.appState.GetLastNotesVersion(), "the version is recorded so we stop refetching")

	// A second launch at the same version is now inert — no further fetch.
	assert.Nil(t, h.releaseNotesCmd(), "the recorded version is no longer an upgrade")
	assert.Equal(t, 1, calls, "the network is queried once, not on every launch")
}

// Non-empty notes open the dismissible overlay and record the version so it
// shows once and never refetches.
func TestReleaseNotesFetchedMsg_OpensOverlayAndRecords(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")

	h.Update(releaseNotesFetchedMsg{version: "0.6.0", notes: "shiny things", url: "https://x/v0.6.0"})

	assert.Equal(t, stateInfo, h.state, "non-empty notes open the modal")
	require.NotNil(t, h.textOverlay)
	overlay := xansi.Strip(h.textOverlay.Render())
	assert.Contains(t, overlay, "What's new in v0.6.0")
	assert.Contains(t, overlay, "shiny things")
	assert.Contains(t, overlay, "https://x/v0.6.0")
	assert.Equal(t, "0.6.0", h.appState.GetLastNotesVersion(), "shown once: the version is recorded")
}

// Empty notes still record the version (so we don't poll forever) but show no
// overlay.
func TestReleaseNotesFetchedMsg_EmptyRecordsWithoutOverlay(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")

	h.Update(releaseNotesFetchedMsg{version: "0.6.0", notes: "   ", url: ""})

	assert.Equal(t, stateDefault, h.state, "empty notes do not open a modal")
	assert.Equal(t, "0.6.0", h.appState.GetLastNotesVersion(), "the version is still recorded so we stop refetching")
}

// Notes arriving while another overlay owns the screen are buffered and shown by
// the next preview tick, never clobbering the open overlay.
func TestReleaseNotesFetchedMsg_BufferedWhileOverlayOpen(t *testing.T) {
	h := newReleaseNotesHome(t, "0.6.0", "0.5.0")
	h.state = stateHelp // a modal already owns the screen

	h.Update(releaseNotesFetchedMsg{version: "0.6.0", notes: "shiny things", url: ""})

	assert.Equal(t, stateHelp, h.state, "the open overlay is untouched")
	require.NotNil(t, h.pendingReleaseNotes, "the notes are buffered")
	assert.Equal(t, "0.6.0", h.appState.GetLastNotesVersion(), "the version is recorded even while buffered")

	h.state = stateDefault
	h.Update(previewTickMsg{})
	assert.Equal(t, stateInfo, h.state, "the tick flushes the buffered notes")
	assert.Nil(t, h.pendingReleaseNotes)
	require.NotNil(t, h.textOverlay)
	assert.Contains(t, xansi.Strip(h.textOverlay.Render()), "shiny things")
}
