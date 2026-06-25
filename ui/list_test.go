package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/theme"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// renderRow renders a single instance row with the given diff stats at a width
// wide enough that the git-context cluster is never dropped for space. It pins
// the unicode theme so glyph assertions (⇣ ⇡ *) are stable across themes.
func renderRow(t *testing.T, branch string, stats *git.DiffStats) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = branch
	inst.SetDiffStats(stats)
	return r.Render(inst, 1, false, false)
}

// The permission-mode chip renders the live mode (PermissionModeInfo), not the
// stale launch flag: a session launched in plan but switched to auto shows
// "auto", and a switch back to default hides the chip entirely.
func TestRender_PermissionModeChipUsesLiveMode(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "t", Path: ".", Program: "claude --permission-mode plan"})
	require.NoError(t, err)

	inst.SetModeMeta("auto") // user cycled plan -> auto in-session
	row := ansi.Strip(r.Render(inst, 1, false, false))
	require.Contains(t, row, "auto", "chip must reflect the live mode")
	require.NotContains(t, row, "plan", "chip must not show the stale launch flag")

	inst.SetModeMeta("default") // cycled back to normal
	row = ansi.Strip(r.Render(inst, 1, false, false))
	require.NotContains(t, row, "plan")
	require.NotContains(t, row, "auto", "a live default hides the chip")
}

// TestList_UpdateBadge: the persistent update badge renders in the panel's
// top border once set — it is panel chrome, independent of rows and overlays.
func TestList_UpdateBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	l := NewList(&s)
	l.SetSize(40, 10)

	require.NotContains(t, ansi.Strip(l.String()), "⇡ v9.9.9", "no badge before the setter")

	l.SetUpdateBadge("⇡ v9.9.9")
	require.Contains(t, ansi.Strip(l.String()), "⇡ v9.9.9", "badge must render after the setter")
}

func TestRender_GitContextCluster(t *testing.T) {
	// Behind, ahead, and dirty all present → all three glyphs render.
	out := renderRow(t, "feat", &git.DiffStats{Added: 5, Removed: 2, Commits: 3, Behind: 2, Dirty: true})
	require.Contains(t, out, "⇣2", "behind count should render")
	require.Contains(t, out, "⇡3", "commit count should render")
	require.Contains(t, out, "*", "dirty marker should render")

	// Clean, all committed, base unchanged → no extra glyphs, just the diff.
	out = renderRow(t, "feat", &git.DiffStats{Added: 5, Removed: 2, Commits: 2})
	require.NotContains(t, out, "⇣", "behind glyph must be absent when not behind")
	require.NotContains(t, out, "*", "dirty marker must be absent when clean")
	require.Contains(t, out, "⇡2", "commit count should still render")
}

// A git session shows its branch name on line 2; a direct (non-git) session
// shows a dim "direct" marker instead. (The branch glyph was removed in the row
// redesign — indentation + the branch name carry the meaning.)
func TestRender_DirectSessionShowsMarkerNotBranch(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	// The branch shows only when a label-only rename has decoupled it from the
	// visible name; give this session a distinct label so its branch renders.
	gitInst, err := session.NewInstance(session.InstanceOptions{Title: "g", Path: ".", Program: "echo"})
	require.NoError(t, err)
	gitInst.Branch = "zvi/feature"
	gitInst.SetDisplayName("Some Label")
	require.Contains(t, r.Render(gitInst, 1, false, false), "zvi/feature",
		"a label-renamed git session row shows its branch name")

	directInst, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	row := r.Render(directInst, 1, false, false)
	require.Contains(t, row, "direct", "a direct session row shows the direct marker")
}

// The configured branch prefix is stripped from the rendered branch label (it
// repeats on every row), but only when it matches exactly — a differently
// namespaced branch keeps its own meaningful prefix.
func TestRender_StripsConfiguredBranchPrefix(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s, branchPrefix: "zvi/"}
	r.setWidth(80)

	// The branch only renders for a label-renamed session, so decouple the label
	// from the title here.
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "zvi/session-row-redesign"
	inst.SetDisplayName("Row redesign")
	out := r.Render(inst, 1, false, false)
	require.Contains(t, out, "session-row-redesign", "the distinguishing branch part still renders")
	require.NotContains(t, out, "zvi/", "the configured prefix is stripped from the label")

	// A branch under a different namespace keeps its prefix — only the exact
	// configured "zvi/" is removed, not any first path segment.
	other, err := session.NewInstance(session.InstanceOptions{Title: "o", Path: ".", Program: "echo"})
	require.NoError(t, err)
	other.Branch = "feature/login"
	other.SetDisplayName("Login work")
	require.Contains(t, r.Render(other, 1, false, false), "feature/login",
		"a non-matching namespace is left intact")
}

// Age is omitted from a git row's dense version-control line but kept on a
// direct session's otherwise-sparse line 2.
func TestRender_GitRowOmitsAgeDirectKeepsIt(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	twoDaysAgo := time.Now().Add(-48 * time.Hour)

	gitInst, err := session.NewInstance(session.InstanceOptions{Title: "g", Path: ".", Program: "echo"})
	require.NoError(t, err)
	gitInst.Branch = "feat"
	gitInst.CreatedAt = twoDaysAgo
	gitInst.SetDiffStats(&git.DiffStats{Added: 5, Removed: 1, Commits: 1})
	require.NotContains(t, r.Render(gitInst, 1, false, false), "2d",
		"a git row no longer carries the age tail")

	directInst, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	directInst.CreatedAt = twoDaysAgo
	require.Contains(t, r.Render(directInst, 1, false, false), "2d",
		"a direct row still shows its age")
}

// On a panel too narrow for the branch name, the branch flex empties and its
// trailing separator collapses — the PR chip / git cluster must not be preceded
// by a dangling "·". (Replaces the old dangling-branch-glyph rule; the glyph no
// longer exists.)
func TestRender_NarrowWidthCollapsesBranchSeparator(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(16) // wide enough for line 1, too narrow for branch + chips

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "zvi/a-rather-long-branch-name"
	inst.SetDisplayName("Renamed") // decouple so the branch flex actually renders
	inst.SetDiffStats(&git.DiffStats{Added: 1, Removed: 1, Commits: 2})

	row := r.Render(inst, 1, false, false)
	// line 2 is the second line; it must not start its content with a stray middot.
	lines := strings.Split(row, "\n")
	require.Len(t, lines, 2)
	require.NotRegexp(t, `^\s*·`, ansi.Strip(lines[1]),
		"a separator must never lead line 2 after the branch is squeezed out")
}

func newTestList(titles ...string) *List {
	s := spinner.New()
	l := NewList(&s)
	for _, t := range titles {
		inst, _ := session.NewInstance(session.InstanceOptions{
			Title:   t,
			Path:    ".",
			Program: "echo",
		})
		l.AddInstance(inst)
	}
	return l
}

// An empty, unfiltered list is the primary first-run surface once the always-on
// hint bar is gone, so it must surface the two essential onboarding keys.
func TestList_EmptyStateHint(t *testing.T) {
	l := newTestList()
	l.SetSize(40, 20)
	out := l.String()
	require.Contains(t, out, "new", "empty list shows the new-session hint")
	require.Contains(t, out, "keys", "empty list shows the help hint")
}

// The onboarding hint is for the genuinely empty list only: it must not appear
// once sessions exist, nor clobber the filter affordances during an active filter.
func TestList_EmptyStateHint_SuppressedWhenNotEmptyOrFiltering(t *testing.T) {
	// Non-empty list: no onboarding hint. ("keys" is the hint's distinctive marker —
	// it appears in neither session rows nor the "no matches" line.)
	l := newTestList("alpha")
	l.SetSize(40, 20)
	require.NotContains(t, l.String(), "keys", "a non-empty list must not show the onboarding hint")

	// Empty but mid-filter (filter bar active, empty query): the filter bar owns the
	// view; the onboarding hint must not overwrite it.
	lf := newTestList()
	lf.SetSize(40, 20)
	lf.SetFilterActive(true)
	require.NotContains(t, lf.String(), "keys", "an active filter must suppress the onboarding hint")

	// Empty result from a non-matching query keeps the existing "no matches" hint,
	// not the onboarding hint.
	lq := newTestList("alpha")
	lq.SetSize(40, 20)
	lq.SetFilterActive(true)
	lq.SetFilter("zzz")
	out := lq.String()
	require.Contains(t, out, "no matches", "a non-matching filter shows the no-matches hint")
	require.NotContains(t, out, "keys", "a filtered list must not show the onboarding hint")
}

func TestMoveUp(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveUp()
	require.True(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "b", l.items[0].Title)
	require.Equal(t, "a", l.items[1].Title)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveUp_AtTop(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(0)

	moved := l.MoveUp()
	require.False(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
}

func TestMoveDown(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveDown()
	require.True(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
	require.Equal(t, "c", l.items[1].Title)
	require.Equal(t, "b", l.items[2].Title)
}

func TestMoveDown_AtBottom(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(2)

	moved := l.MoveDown()
	require.False(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveWithSingleItem(t *testing.T) {
	l := newTestList("only")
	l.SetSelectedInstance(0)

	require.False(t, l.MoveUp())
	require.False(t, l.MoveDown())
}

func TestList_RendersDisplayLabel(t *testing.T) {
	l := newTestList("original")
	l.SetSize(80, 20)

	// With no label set, the list shows the Title.
	require.Contains(t, l.String(), "original", "shows Title when no label is set")

	// Once a cosmetic label is set, the list shows it in place of the Title.
	l.items[0].SetDisplayName("renamed")
	require.Contains(t, l.String(), "renamed", "shows the custom label when set")
}

func TestFmtAge(t *testing.T) {
	require.Equal(t, "", fmtAge(time.Time{}), "zero time returns empty string")
	require.Equal(t, "", fmtAge(time.Now().Add(-30*time.Second)), "sub-minute returns empty string")
	require.Equal(t, "5m", fmtAge(time.Now().Add(-5*time.Minute)), "minutes label")
	require.Equal(t, "2h", fmtAge(time.Now().Add(-2*time.Hour)), "hours label")
	require.Equal(t, "3d", fmtAge(time.Now().Add(-72*time.Hour)), "days label")
}

func TestRender_SessionAge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	// Age renders only on direct (non-git) rows now — a git row drops it from the
	// dense version-control line — so this format ladder uses a direct session.
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)

	// Match an age token (digits followed by m/h/d at a word boundary) so the
	// assertion targets the label specifically, not any incidental "m"/"h"/"d"
	// in a branch name or status word.
	ageToken := regexp.MustCompile(`\d+[mhd]\b`)

	// Brand-new session (CreatedAt = time.Now()) → no age label.
	out := r.Render(inst, 0, false, false)
	require.NotRegexp(t, ageToken, out, "fresh session should not show age")

	// Simulate a 3-hour-old session.
	inst.CreatedAt = time.Now().Add(-3 * time.Hour)
	out = r.Render(inst, 0, false, false)
	require.Contains(t, out, "3h", "3-hour-old session should show '3h'")

	// Simulate a 2-day-old session.
	inst.CreatedAt = time.Now().Add(-48 * time.Hour)
	out = r.Render(inst, 0, false, false)
	require.Contains(t, out, "2d", "2-day-old session should show '2d'")
}

// A direct (non-git) session carries the same right-aligned age label as a git
// session: CreatedAt is just as meaningful without a worktree. Fresh (< 1 min)
// direct sessions stay unlabeled, like their git counterparts.
func TestRender_SessionAge_Direct(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)

	ageToken := regexp.MustCompile(`\d+[mhd]\b`)

	// Brand-new direct session → no age label.
	out := r.Render(inst, 0, false, false)
	require.NotRegexp(t, ageToken, out, "fresh direct session should not show age")

	// Simulate a 3-hour-old direct session: the label renders alongside the
	// direct marker.
	inst.CreatedAt = time.Now().Add(-3 * time.Hour)
	out = r.Render(inst, 0, false, false)
	require.Contains(t, out, "3h", "3-hour-old direct session should show '3h'")
	require.Contains(t, out, "direct", "the direct marker must survive the age label")
}

// TestRender_PRBadge verifies the PR badge renders "<glyph>#<number>" for a
// session with an open PR, stays absent when there is no PR, and never panics on
// a nil status.
func TestRender_PRBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	g := theme.Current().Glyphs

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "feat"
	inst.SetDiffStats(&git.DiffStats{Added: 1, Removed: 1, Commits: 1})

	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 42, CI: git.CIPassing})
	require.Contains(t, r.Render(inst, 1, false, false), g.PR+"#42", "open PR should render the badge")

	inst.SetPRStatus(&git.PRStatus{HasPR: false})
	require.NotContains(t, r.Render(inst, 1, false, false), "#42", "no PR should render no badge")

	inst.SetPRStatus(nil) // must not panic
	_ = r.Render(inst, 1, false, false)
}

// TestRender_PRBadgeWidthBudget guards the width fold: a PR badge alongside a
// long branch name must not overflow the row (which would wrap and desync the
// renderer). The branch absorbs the badge's cost via fixedW.
func TestRender_PRBadgeWidthBudget(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	const width = 36
	r.setWidth(width)

	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = "zvi/a-rather-long-branch-name-that-overflows"
	inst.SetDiffStats(&git.DiffStats{Added: 12, Removed: 3, Commits: 2})
	inst.SetPRStatus(&git.PRStatus{HasPR: true, Number: 1234, CI: git.CIFailing})

	row := r.Render(inst, 1, false, false)
	require.Contains(t, row, "#1234", "the PR badge survives at the expense of the branch")
	for _, ln := range strings.Split(row, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(ln), width, "row must fit width with a PR badge present")
	}
}

// A direct (non-git) session never has a PR, so even a stray status renders no badge.
func TestRender_DirectSessionNoPRBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	direct, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	direct.SetPRStatus(&git.PRStatus{HasPR: true, Number: 99})
	require.NotContains(t, r.Render(direct, 1, false, false), "#99",
		"a direct session must not render a PR badge")
}

// TestRender_SessionAgeBudget locks in the headline width property: the age
// label steals horizontal budget from the branch (the only variable-length
// part) so the rendered line still fits the width exactly. A regression here is
// what reintroduces width-wrap, which desyncs bubbletea's incremental renderer.
func TestRender_SessionAgeBudget(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	const width = 28
	r.setWidth(width)

	lineWidth := func(out string) int {
		// The row is two lines (identity + version-control); measure the wider.
		// ansi.StringWidth ignores escape sequences, giving true display width.
		widest := 0
		for _, ln := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(ln); w > widest {
				widest = w
			}
		}
		return widest
	}

	// A populated git row (it has a diff) never carries an age label — age was
	// dropped from the dense version-control line — but must still fit its width.
	gitInst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	gitInst.SetDisplayName("A long enough label to overflow the row")
	gitInst.Branch = "feature/a-very-long-branch-name-that-overflows"
	gitInst.CreatedAt = time.Now().Add(-3 * time.Hour)
	gitInst.SetDiffStats(&git.DiffStats{Added: 5, Removed: 1, Commits: 1})
	gitRow := r.Render(gitInst, 0, false, false)
	require.NotContains(t, gitRow, "3h", "a populated git row carries no age label")
	require.LessOrEqual(t, lineWidth(gitRow), width, "git row must fit width")

	// Direct (non-git) mode keeps the age: the fixed marker is the only left-hand content, so
	// the age steals budget from it instead of a branch. The row must still fit
	// exactly — including at a width narrower than the marker itself.
	direct, err := session.NewInstance(session.InstanceOptions{Title: "d", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	direct.CreatedAt = time.Now().Add(-3 * time.Hour)
	directRow := r.Render(direct, 0, false, false)
	require.Contains(t, directRow, "3h", "direct-mode age label should render")
	require.LessOrEqual(t, lineWidth(directRow), width, "direct row must fit width with the age label")

	const narrow = 12 // narrower than the "direct · no git isolation" marker
	r.setWidth(narrow)
	require.LessOrEqual(t, lineWidth(r.Render(direct, 0, false, false)), narrow,
		"direct row must fit even when the marker itself must truncate")
}

// renderRowWith builds a single row at width 80 under the unicode theme (so the
// note glyph "✎" is stable), applying setup to the instance before rendering.
func renderRowWith(t *testing.T, setup func(i *session.Instance)) string {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	inst, err := session.NewInstance(session.InstanceOptions{Title: "auth-refactor", Path: ".", Program: "echo"})
	require.NoError(t, err)
	setup(inst)
	return r.Render(inst, 1, false, false)
}

func TestRender_PausedNoteTakesLineTwo(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Paused)
		i.SetNote("blocked on Benoit's review")
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	require.Contains(t, out, "✎", "paused row shows the note glyph")
	require.Contains(t, out, "blocked on Benoit's review")
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 2, "paused-with-note stays two lines (note replaces the VC line)")
	for _, ln := range lines {
		require.Equal(t, 80, ansi.StringWidth(ln), "each line totals r.width (marker col + W)")
	}
}

func TestRender_RunningNoteGetsThirdLine(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Running)
		i.SetNote("risky — double-check before merge")
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 3, "running-with-note gets a third line; live VC line is preserved")
	require.Contains(t, lines[2], "✎")
	require.Contains(t, lines[2], "risky — double-check before merge")
	for _, ln := range lines {
		require.Equal(t, 80, ansi.StringWidth(ln), "every line totals r.width")
	}
}

func TestRender_NoNoteIsUnchanged(t *testing.T) {
	out := renderRowWith(t, func(i *session.Instance) {
		i.SetStatus(session.Running)
		i.SetDiffStats(&git.DiffStats{Added: 5, Removed: 2, Commits: 3})
	})
	require.Len(t, strings.Split(out, "\n"), 2, "no note → exactly two lines, unchanged")
	require.NotContains(t, out, "✎")
}

func TestRender_AccountBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	// Routed account -> badge present.
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.SetClaudeAccount("quantivly", "/home/x/.claude-quantivly", false)
	require.Contains(t, ansi.Strip(r.Render(inst, 1, false, false)), "quantivly",
		"routed account badge should render its name")

	// No account (feature dormant) -> no badge.
	plain, err := session.NewInstance(session.InstanceOptions{Title: "p", Path: ".", Program: "echo"})
	require.NoError(t, err)
	out := ansi.Strip(r.Render(plain, 1, false, false))
	require.NotContains(t, out, "quantivly")
}
