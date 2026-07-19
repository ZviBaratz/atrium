package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The row PR chip encodes CI state by SHAPE (✗/•/✓), not color alone (#384), so it
// survives desaturation. A merged PR (chip already purple) and a PR with no checks
// add no glyph.
func TestPRCheckGlyph(t *testing.T) {
	for _, c := range []struct {
		ci    git.CIStatus
		state string
		want  string
	}{
		{git.CIFailing, "OPEN", "✗"},
		{git.CIPending, "OPEN", "•"},
		{git.CIPassing, "OPEN", "✓"},
		{git.CINone, "OPEN", ""},
		{git.CIFailing, "MERGED", ""}, // merged suppresses the CI glyph
		{git.CIPassing, "MERGED", ""},
	} {
		got := prCheckGlyph(&git.PRStatus{CI: c.ci, State: c.state})
		require.Equalf(t, c.want, got, "CI=%v state=%s", c.ci, c.state)
	}
}

// End-to-end: a failing PR's row chip renders "#7✗", not a bare "#7" whose only
// failing signal is red.
func TestRow_PRChipShowsCIShape(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(120)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "feature"

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", CI: git.CIFailing})
	require.Contains(t, ansi.Strip(r.Render(inst, 1, false, false)), "#7✗",
		"a failing PR's chip reads by shape, not just color")

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "OPEN", CI: git.CIPassing})
	require.Contains(t, ansi.Strip(r.Render(inst, 1, false, false)), "#7✓")

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 7, State: "MERGED", CI: git.CIPassing})
	row := ansi.Strip(r.Render(inst, 1, false, false))
	require.Contains(t, row, "#7", "a merged-but-kept session still shows its chip")
	require.NotContains(t, row, "#7✓", "a merged chip carries no CI glyph")
}
