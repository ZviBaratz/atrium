package ui

import (
	"os"
	"path/filepath"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// untrustedRepoConfigInstance arms the inert state through the REAL enforcement
// path — the gate itself classifies the repo as untrusted when it resolves —
// rather than poking a field a renderer test has no business setting. The
// fixture dir is not even a git repo: the ledger key then derives empty, and an
// empty key is refused by construction (the failing side is the refusing side),
// which is exactly the state this row line exists for.
func untrustedRepoConfigInstance(t *testing.T) *session.Instance {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium.json"),
		[]byte(`{"repo_scripts":[{"name":"web","setup_script":"make deps"}]}`), 0o644))

	inst, err := session.NewInstance(session.InstanceOptions{Title: "web", Path: dir, Program: "echo"})
	require.NoError(t, err)
	inst.RunSetupScript(dir) // resolves, classifies untrusted, runs nothing
	require.Equal(t, session.RepoConfigUntrusted, inst.RepoConfigStatus(),
		"fixture must arm the state through the gate, or this test renders nothing real")
	return inst
}

// A session whose repo declared setup that was withheld must say so ON THE ROW,
// for as long as the state holds — a whole dim line rather than a chip, because
// chips are dropped on narrow panes and a security refusal must not be the
// thing that silently vanishes there (#814).
func TestRender_UntrustedRepoConfigReplacesLineTwo(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst := untrustedRepoConfigInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 60}

	out := ansi.Strip(r.Render(inst, 0, false, false))
	require.Contains(t, out, "repo config ignored · untrusted",
		"the withheld config must be legible on the row")
}

// The narrow-pane property the line was chosen for: at a width where chips
// would be dropped, the hint is still there (truncated at worst, never
// omitted).
func TestRender_UntrustedRepoConfigSurvivesANarrowPane(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	inst := untrustedRepoConfigInstance(t)

	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, width: 24}

	out := ansi.Strip(r.Render(inst, 0, false, false))
	require.Contains(t, out, "repo config",
		"narrowing the pane may truncate the hint but must not drop it")
}

// The row line is for REFUSALS only. AbsentGranted — a granted file simply not
// on this branch — is benign divergence: it gets the one-shot modal at
// materialization, and holding a fixed line for the life of the session would
// displace the git line (ahead/behind, PR, diff stats) over routine branch
// work.
func TestRepoConfigLineFlagsRefusalsOnly(t *testing.T) {
	for state, want := range map[session.RepoConfigState]bool{
		session.RepoConfigUnset:         false,
		session.RepoConfigNone:          false,
		session.RepoConfigActive:        false,
		session.RepoConfigAbsentGranted: false,
		session.RepoConfigUntrusted:     true,
		session.RepoConfigChanged:       true,
		session.RepoConfigInvalid:       true,
	} {
		line, ok := repoConfigLine(state)
		require.Equal(t, want, ok, "state %v", state)
		require.Equal(t, want, line != "", "a flagged state needs copy; an unflagged one must render nothing")
	}
}
