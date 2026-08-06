package ui

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
)

// portedInstance returns an instance holding a managed port, and the port itself.
//
// It goes through the real allocation path rather than setting a field, because there
// is no setter to set: a port exists only because a repo declared a range and the
// registry granted one, and a test that could skip that would also pass if the row
// rendered a number no session actually holds.
func portedInstance(t *testing.T) (*session.Instance, int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".atrium"), 0o755))

	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	cfg := config.DefaultConfig()
	cfg.RepoScripts = []config.RepoScript{{Name: "any", PortRange: fmt.Sprintf("%d-%d", port, port)}}
	require.NoError(t, config.SaveConfig(cfg))

	dir := t.TempDir()
	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	// No setup_script in the config above, so this allocates the port and runs nothing.
	inst.RunSetupScript(dir)
	require.Equal(t, port, inst.Port(), "the fixture must hold the port it configured")
	// Give it back when the test ends. The allocator's registry is process-wide and a
	// session only releases on kill, so without this each test here leaves an owner
	// behind — and the ephemeral range these numbers come from is re-handed out freely
	// enough that a later fixture can be refused the port it was just told was free.
	// Kill is the production release path; with no worktree and no tmux session it does
	// nothing else.
	t.Cleanup(func() { require.NoError(t, inst.Kill()) })
	return inst, port
}

// The port is on the row because it is the answer to "which of my four sessions is the
// one on 3001" — a question the user asks with a browser open, not with the preview
// pane focused.
func TestRender_PortChipShowsTheManagedPort(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := ansi.Strip(r.Render(inst, 0, false, false))

	assert.Contains(t, out, fmt.Sprintf(":%d", port))
}

// Line 2, not line 1: line 1's right cluster already carries up to eight chips and
// every one of them steals from the name's flex budget.
func TestRender_PortChipIsOnLineTwo(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	lines := splitRowLines(t, ansi.Strip(r.Render(inst, 0, false, false)))

	require.Len(t, lines, 2)
	assert.NotContains(t, lines[0], fmt.Sprintf(":%d", port))
	assert.Contains(t, lines[1], fmt.Sprintf(":%d", port))
}

// The chip is a link, which costs zero display width (OSC 8 lives outside the visible
// text) and saves the user from retyping a port into a browser.
func TestRender_PortChipLinksToTheServer(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := r.Render(inst, 0, false, false)

	assert.Contains(t, out, fmt.Sprintf("http://localhost:%d", port))
	assert.NotContains(t, ansi.Strip(out), "http://localhost",
		"the URL must not cost the row any columns")
}

// A session whose repo declares no port_range renders exactly as it did before the
// feature: no chip, no separator, no reserved space.
func TestRender_NoChipWithoutAManagedPort(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	dir := t.TempDir()
	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := ansi.Strip(r.Render(inst, 0, false, false))

	assert.NotContains(t, out, ":")
}

// A running dev command turns the port chip into a state signal, and it says so by
// SHAPE rather than by colour alone: the row is read under NO_COLOR and under the
// desaturation guard, where an accent-vs-dim distinction is no distinction at all.
func TestRender_PortChipMarksARunningDevCommand(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	idle := ansi.Strip(r.Render(inst, 0, false, false))
	require.Contains(t, idle, fmt.Sprintf(":%d", port))
	assert.NotContains(t, idle, runGlyph, "an idle session's chip is the port alone")

	inst.ApplyRunState(session.RunState{Live: true, LiveKnown: true})
	running := ansi.Strip(r.Render(inst, 0, false, false))

	assert.Contains(t, running, runGlyph+fmt.Sprintf(":%d", port))
}

// A repo can declare a run_command with no port_range, and that session still has to be
// able to say its server is up — so the chip falls back to a bare "dev" rather than
// disappearing along with the number.
func TestRender_DevChipWithoutAManagedPort(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	dir := t.TempDir()
	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	require.Zero(t, inst.Port())

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	require.NotContains(t, ansi.Strip(r.Render(inst, 0, false, false)), "dev",
		"a session with neither a port nor a server spends no columns on either")

	inst.ApplyRunState(session.RunState{Live: true, LiveKnown: true})

	assert.Contains(t, ansi.Strip(r.Render(inst, 0, false, false)), runGlyph+"dev")
}

// The running chip is one column wider than the idle one, so it has to face the same
// width fold — measured at a width where the idle chip fits exactly, which is what makes
// this an assertion about the glyph rather than about the rest of the line.
func TestRender_RunningChipIsDroppedWhenTheRowCannotHoldIt(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)
	require.Equal(t, inst.DisplayName(), inst.Title, "an un-renamed session: line 2 has no flex segment")
	inst.SetDiffStats(&git.DiffStats{Added: 12, Removed: 3, Commits: 2, Behind: 1})

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}

	// The narrowest width at which the idle chip still fits.
	width := 0
	for w := 20; w <= 80; w++ {
		r.setWidth(w)
		if strings.Contains(ansi.Strip(r.Render(inst, 1, false, false)), fmt.Sprintf(":%d", port)) {
			width = w
			break
		}
	}
	require.NotZero(t, width, "the idle chip must fit at some width in range")

	inst.ApplyRunState(session.RunState{Live: true, LiveKnown: true})
	r.setWidth(width)
	row := r.Render(inst, 1, false, false)

	assert.NotContains(t, ansi.Strip(row), fmt.Sprintf(":%d", port),
		"one column wider than the idle chip, so at the idle chip's exact width it is dropped")
	for _, ln := range strings.Split(row, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(ln), width, "no line may exceed the row width")
	}
}

// The run glyph shares prCheckGlyph's contract: an inline literal, outside the Glyphs
// table and therefore outside the `?` legend, which makes width the one thing nothing
// else checks. A two-cell glyph breaks the column math line2Fits does and wraps the row.
func TestRunGlyphIsSingleCell(t *testing.T) {
	assert.Equal(t, 1, ansi.StringWidth(runGlyph))
}

// splitRowLines splits a rendered row into its two lines.
func splitRowLines(t *testing.T, out string) []string {
	t.Helper()
	var lines []string
	start := 0
	for i := 0; i < len(out); i++ {
		if out[i] == '\n' {
			lines = append(lines, out[start:i])
			start = i + 1
		}
	}
	return append(lines, out[start:])
}

// The width fold: a port chip beside a long branch and the git chips must not overflow
// the row, which would wrap and desync the renderer. The branch absorbs its cost.
func TestRender_PortChipWidthBudget(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)
	inst.SetDisplayName("web ui")
	inst.Branch = "zvi/a-rather-long-branch-name-that-overflows"
	inst.SetDiffStats(&git.DiffStats{Added: 12, Removed: 3, Commits: 2})
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 1234, CI: git.CIFailing})

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	const width = 44
	r.setWidth(width)

	row := r.Render(inst, 1, false, false)

	assert.Contains(t, ansi.Strip(row), fmt.Sprintf(":%d", port), "the port survives at the expense of the branch")
	for _, ln := range strings.Split(row, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(ln), width, "row must fit width with a port chip present")
	}
}

// A narrow list drops the chip rather than overflowing the row.
//
// Line 2 has a flex segment only for a RENAMED session — the branch — so on an ordinary
// row every segment is fixed width and composeLine has nothing to shrink: an overlong
// line is not truncated but rendered overlong, wrapping into a ghost row that desyncs
// the renderer. Measured at a width where the same row without a port fits exactly, so
// the assertion is about the chip and not about the rest of the line.
func TestRender_PortChipIsDroppedWhenTheRowCannotHoldIt(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst, port := portedInstance(t)
	require.Equal(t, inst.DisplayName(), inst.Title, "an un-renamed session: line 2 has no flex segment")
	inst.SetDiffStats(&git.DiffStats{Added: 12, Removed: 3, Commits: 2, Behind: 1})
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 1234, CI: git.CIFailing})

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	const width = 30
	r.setWidth(width)

	row := r.Render(inst, 1, false, false)

	assert.NotContains(t, ansi.Strip(row), fmt.Sprintf(":%d", port), "the chip is dropped, not squeezed")
	for _, ln := range strings.Split(row, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(ln), width, "no line may exceed the row width")
	}
}
