package app

// settings_repolayer_test.go - #815's provenance annotation measured against the
// COMPOSED frame, which is the one contract the overlay's own tests cannot see. The
// chip is a fourth claimant on a column three others already compete for, and
// composeRowLine drops the badge before it touches the value - so a chip that
// overran would misalign every label below it rather than failing loudly, and an
// over-width line desyncs Bubble Tea's incremental renderer into ghost rows.
//
// It is a separate test from TestViewFitsTerminalBoundsEveryState rather than a new
// frameStates entry on purpose: the frame GOLDEN for stateSettings is deliberately
// layer-free (nil is "unknown", the pre-#815 rendering), and adding the layer there
// would re-baseline a golden this change is supposed to leave alone.

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/muesli/ansi"
)

func TestSettingsFrameFitsWithARepoLayer(t *testing.T) {
	// Entries long enough that the badge, the value and the help line all compete
	// for the same slack at the narrow end of the ladder.
	layer := &overlay.RepoLayer{
		Repo: "/home/dev/src/a-project-with-a-long-path",
		Lists: map[string][]string{
			"carry_files": {".dev.vars", ".claude/settings.local.json"},
			"link_paths":  {"node_modules", "container/agent-runner/node_modules", ".venv"},
		},
	}
	fs := frameState{name: "settings-repolayer", st: stateSettings, wire: func(h *home, _ *session.Instance) {
		h.settingsOverlay = overlay.NewSettingsOverlay(h.appConfig)
		h.settingsOverlay.SetRepoLayer(layer)
		// Park the cursor on a layered row, so the help pane renders the provenance
		// line as well as the chip.
		h.settingsOverlay.OpenAt("link_paths")
	}}

	for _, dim := range [][2]int{{200, 50}, {160, 40}, {120, 30}, {96, 30}, {80, 24}} {
		w, h := dim[0], dim[1]
		lines := strings.Split(newParityHome(t, fs, w, h).View().Content, "\n")
		if len(lines) > h {
			t.Errorf("size=%dx%d: View() emitted %d lines, exceeds height %d", w, h, len(lines), h)
		}
		for i, l := range lines {
			if pw := ansi.PrintableRuneWidth(l); pw != w {
				t.Errorf("size=%dx%d: line %d width=%d, expected %d", w, h, i, pw, w)
				break
			}
		}
	}
}
