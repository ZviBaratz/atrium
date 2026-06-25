package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

func instWithStatus(t *testing.T, title string, st session.Status) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(st)
	return inst
}

func TestRender_AutoBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode")) // AutoBadge glyph is empty here, so the chip is plain "AUTO"
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(60)

	working := instWithStatus(t, "auto", session.Running)
	working.AutoYes = true
	require.Contains(t, r.Render(working, 1, false, false), "AUTO", "auto-accepting session shows the badge")

	paused := instWithStatus(t, "auto-paused", session.Paused)
	paused.AutoYes = true
	require.NotContains(t, r.Render(paused, 1, false, false), "AUTO", "paused session never shows the badge")
}

// The state word is gone — the leading gutter glyph carries the signal. Assert
// the glyph renders and the word does not.
func TestRender_StatusGutterNoWord(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(60)

	cases := []struct {
		st    session.Status
		glyph string
		word  string
	}{
		{session.NeedsInput, th.Glyphs.Waiting, "waiting"},
		{session.Paused, th.Glyphs.Paused, "paused"},
	}
	for _, c := range cases {
		out := r.Render(instWithStatus(t, "s", c.st), 1, false, false)
		require.Contains(t, out, c.glyph, "status %v should render its gutter glyph", c.st)
		require.NotContains(t, out, c.word, "the state word %q must no longer render", c.word)
	}
}

// guard the row keeps its diff stat right-aligned within the inner width
func TestRender_DiffStatPresent(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(60)
	inst := instWithStatus(t, "s", session.Ready)
	inst.SetDiffStats(&git.DiffStats{Added: 9, Removed: 2})
	out := r.Render(inst, 1, false, false)
	require.True(t, strings.Contains(out, "+9") && strings.Contains(out, "-2"), "diff stat should render")
}

// TestListGolden is a deterministic snapshot of the panelized list (no color,
// unicode glyphs, fixed size) guarding against unintended layout/content
// changes. Regenerate with CS_UPDATE_GOLDEN=1 go test ./ui/ -run TestListGolden.
func TestListGolden(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii) // strip color → stable bytes
	t.Cleanup(func() { lipgloss.SetColorProfile(prof) })

	s := spinner.New()
	l := NewList(&s)
	l.SetBranchPrefix("zvi/") // exercise prefix stripping on the one shown branch

	// A renamed, active session: its label differs from the title, so the branch
	// renders (prefix-stripped to "visual-overhaul") next to the agent glyph, git
	// chips, and diff. Driven through the real Running→Ready edge so it carries the
	// unread (bright ●) look — instWithStatus's bare SetStatus(Ready) is a
	// Ready→Ready no-op that would render as seen (○).
	readyInst := instWithStatus(t, "overhaul", session.Running)
	readyInst.SetStatus(session.Ready)
	readyInst.SetDisplayName("Visual overhaul")
	readyInst.Branch = "zvi/visual-overhaul"
	readyInst.Program = "claude" // exercises the agent identity glyph (✻) in the row
	readyInst.SetDiffStats(&git.DiffStats{Added: 142, Removed: 31, Commits: 3, Dirty: true})
	l.AddInstance(readyInst)()

	// A fresh, idle session with no work and no rename: the branch is suppressed
	// (a slug echo of the name) and line 2 has nothing else, so it falls back to
	// the age.
	bounds := instWithStatus(t, "bounds", session.NeedsInput)
	bounds.Branch = "fix-bounds"
	bounds.CreatedAt = time.Now().Add(-2 * time.Hour)
	l.AddInstance(bounds)()

	// A non-renamed session with changes: branch still suppressed, so line 2 leads
	// with the git state instead.
	markers := instWithStatus(t, "markers", session.Paused)
	markers.Branch = "pane-markers"
	markers.SetDiffStats(&git.DiffStats{Added: 8, Removed: 3, Commits: 1})
	l.AddInstance(markers)()
	l.SetSize(40, 14)

	// Rows carry bubblezone click-region markers; Scan strips them just as
	// home.View() does before the frame is shown, so the golden stays the visible
	// output rather than the marked intermediate.
	got := zone.Scan(l.String())
	golden := filepath.Join("testdata", "list_golden.txt")
	if os.Getenv("CS_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "missing golden; regenerate with CS_UPDATE_GOLDEN=1")
	require.Equal(t, string(want), got)
}
