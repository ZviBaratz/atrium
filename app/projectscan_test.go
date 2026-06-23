package app

import (
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intp(i int) *int { return &i }

// indexOf returns the position of want in paths, or -1.
func indexOf(paths []string, want string) int {
	for i, p := range paths {
		if p == want {
			return i
		}
	}
	return -1
}

// Known projects rank after recents, scanned repos after known projects, and the
// whole list stays deduped with cwd last.
func TestCandidateRepoPaths_KnownThenScannedOrdering(t *testing.T) {
	recent := t.TempDir()
	knownOnly := t.TempDir()
	scanned := t.TempDir()

	h := newTestHomeWithInstances(t)
	require.NoError(t, h.appState.AddRecentPath(recent))
	// A known project that has fallen out of recents: present only in the
	// durable list.
	st := h.appState.(*config.State)
	st.KnownProjects = append(st.KnownProjects, knownOnly)
	h.scannedRepos = []string{scanned, recent} // scan also found the recent repo

	got := h.candidateRepoPaths()

	ri, ki, si := indexOf(got, recent), indexOf(got, knownOnly), indexOf(got, scanned)
	require.NotEqual(t, -1, ri, "recent missing: %v", got)
	require.NotEqual(t, -1, ki, "known project missing: %v", got)
	require.NotEqual(t, -1, si, "scanned repo missing: %v", got)
	assert.Less(t, ri, ki, "recents must rank above known projects: %v", got)
	assert.Less(t, ki, si, "known projects must rank above scanned repos: %v", got)

	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
		assert.Equal(t, 1, seen[p], "path %q duplicated", p)
	}
}

// Known projects whose directory is gone are pruned like stale recents.
func TestCandidateRepoPaths_DropsMissingKnownProjects(t *testing.T) {
	h := newTestHomeWithInstances(t)
	st := h.appState.(*config.State)
	st.KnownProjects = []string{"/does/not/exist/anymore"}

	assert.NotContains(t, h.candidateRepoPaths(), "/does/not/exist/anymore")
}

// A completed scan is stored, persisted, and live-updates an open create form
// without disturbing the typed filter.
func TestProjectScanDone_PersistsAndLiveUpdatesForm(t *testing.T) {
	// Isolate cwd: candidateRepoPaths() includes os.Getwd(), and the picker
	// fuzzy-matches full paths. A worktree path containing the subsequence
	// z-e-b-r-a would otherwise match the "zebra" filter and pin the selection
	// to cwd, masking the scanned repo (issue #169).
	t.Chdir(t.TempDir())

	scannedRepo := t.TempDir() + "/zebra-service"
	require.NoError(t, os.MkdirAll(scannedRepo, 0o755))

	h := newCreateFormHome(t)
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	require.NotNil(t, h.textInputOverlay)
	// The picker has focus in the N flow; type a fragment that matches nothing yet.
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zebra")})

	h.scanGen = 7
	h.scanInFlight = true
	_, _ = h.Update(projectScanDoneMsg{repos: []string{scannedRepo}, gen: 7})

	assert.False(t, h.scanInFlight)
	assert.Equal(t, []string{scannedRepo}, h.scannedRepos)
	persisted, at := h.appState.GetScannedRepos()
	assert.Equal(t, []string{scannedRepo}, persisted, "scan must be persisted to state")
	assert.False(t, at.IsZero())
	// The live update made the scanned repo visible under the preserved filter.
	assert.Equal(t, scannedRepo, h.textInputOverlay.GetSelectedPath(),
		"typed filter should now match the scanned repo")
}

// A result from a superseded scan generation is dropped entirely.
func TestProjectScanDone_StaleGenerationDropped(t *testing.T) {
	h := newCreateFormHome(t)
	h.scanGen = 5
	h.scanInFlight = true

	_, _ = h.Update(projectScanDoneMsg{repos: []string{"/stale"}, gen: 4})

	assert.True(t, h.scanInFlight, "stale result must not clear the in-flight flag")
	assert.Empty(t, h.scannedRepos)
}

// Depth 0 disables the feature: no scan command, ever.
func TestStartProjectScan_DisabledByDepthZero(t *testing.T) {
	h := newCreateFormHome(t)
	h.appConfig.ProjectSearchDepth = intp(0)

	assert.Nil(t, h.startProjectScan())
	assert.False(t, h.scanInFlight)
}

// Only one scan runs at a time.
func TestStartProjectScan_InFlightGuard(t *testing.T) {
	h := newCreateFormHome(t)

	first := h.startProjectScan()
	require.NotNil(t, first)
	assert.True(t, h.scanInFlight)
	assert.Nil(t, h.startProjectScan(), "second scan while one is in flight")
}

// Opening the create form re-scans only when the last completed scan is stale.
func TestOpenCreateForm_RescanOnlyWhenStale(t *testing.T) {
	fresh := newCreateFormHome(t)
	fresh.lastScanAt = time.Now()
	fresh.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	assert.False(t, fresh.scanInFlight, "fresh cache must not re-scan on form open")

	stale := newCreateFormHome(t)
	stale.lastScanAt = time.Now().Add(-2 * projectScanTTL)
	stale.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	assert.True(t, stale.scanInFlight, "stale cache must kick a re-scan on form open")
}
