package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// titleRow returns the composed frame's Title row — the line a verdict rides, trailing
// the input. It fails loudly rather than returning "" when the row is missing, so a form
// whose Title line had been pushed out of the frame cannot make the checks below pass
// vacuously.
func titleRow(t *testing.T, h *home) string {
	t.Helper()
	frame := xansi.Strip(h.View().Content)
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "Title") {
			return line
		}
	}
	t.Fatalf("no frame row carries the Title label:\n%s", frame)
	return ""
}

// TestTitleVerdicts_SurviveAn80ColRender is the width guard for the create form's title
// verdicts (#545): every one of them must render whole on the terminal the copy has to
// survive.
//
// It is a table over the named constants rather than three or four driven refusals, and
// that is a deliberate departure from TestVariantRefusals_SurviveAn80ColRender, which
// drives. The reason is titleErrNameTaken: it fires only when a *started* session's tmux
// name collides, and tmuxName is written inside start(), so no hermetic test can reach
// it. A guard covering only the senders it can drive would leave that one unmeasured —
// and it is the second-widest of the four. Enumerating the constants is what makes the
// sender set complete; the existing duplicate-title tests already prove the real code
// paths set these values.
//
// The verdict trails the title input, and renderCreateForm carves its columns out of that
// input so the message never lands past fitOverlay's edge — but the input has a floor of
// 10, so past titleVerdictBudget the row grows instead and the tail is cut in silence.
// Every one of these used to interpolate an unbounded name and every one of them was over
// it: "already used in atrium" was 22 cells against a 21-cell budget, and the shortest
// repo name in the world would not have saved "branch <name> exists in <group>".
func TestTitleVerdicts_SurviveAn80ColRender(t *testing.T) {
	for _, verdict := range []string{
		titleErrAlreadyUsed,
		titleErrBranchExists,
		titleErrNameTaken,
		titleErrNoFreeNames,
	} {
		t.Run(verdict, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := newFanOutHome(t, t.TempDir())
			// The resize is what gives the overlay its width: SetSize takes the
			// overlay's 0.6 share, so 80 cols here is the 48 the form actually gets.
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
			h.textInputOverlay.SetTitleError(verdict)

			require.Equal(t, verdict, h.textInputOverlay.TitleError(), "the verdict must be set")
			row := titleRow(t, h)
			assert.Containsf(t, row, verdict,
				"the verdict is cut at 80 cols — fitOverlay trimmed it to fit.\n"+
					"  verdict (%d cells): %q\n  frame row: %q",
				len(verdict), verdict, strings.TrimSpace(row))
		})
	}
}

// The verdicts must stay free of interpolation, which is what makes their width provable
// rather than a claim about how people name repositories.
//
// This is the property the render check above cannot defend, for the same reason the
// variant refusals needed their own absence check (#541): a reword that puts a name back
// can *fit* for the names a fixture happens to use. "already used in atrium" is 22 cells
// and overflows by one; "already used in x" is 17 and would sail through every render
// assertion here while the next user with a longer repo name loses the tail.
func TestTitleVerdicts_InterpolateNothing(t *testing.T) {
	for _, verdict := range []string{
		titleErrAlreadyUsed,
		titleErrBranchExists,
		titleErrNameTaken,
		titleErrNoFreeNames,
	} {
		assert.NotContainsf(t, verdict, "%",
			"title verdicts must not interpolate: %q. The names they used to carry — a repo "+
				"group, a derived branch, another session's title — have no ceiling, so a "+
				"message carrying one is bounded only by how people happen to name things.",
			verdict)
	}
}
