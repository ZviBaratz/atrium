package overlay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/config"
)

// sizedOverlay is what the width contract needs from an overlay: accept the
// fitted size, render — the same two calls the app's resize walk and
// viewContent make.
type sizedOverlay interface {
	SetSize(width, height int)
	Render() string
}

// TestEverySizerHonorsItsSpec is the width contract over every overlay that
// declares a SizeSpec: built with real content and sized to spec.Fit at the
// 80x24 floor and at a wide terminal that puts the caps in play, a box-filler
// renders every line at exactly the claimed outer width, a content-hugger
// never exceeds it, and no height-aware box renders more lines than the
// claimed height. Widths are measured with ansi.PrintableRuneWidth, not
// lipgloss — measuring Lip Gloss's output with its own measurer is a
// tautology that stays green when the emitter and the measurer move together.
// This is the assertion that catches the ±2 class: a Width(w+2) regression in
// any Render, or an inner width re-derived against the wrong chrome, moves a
// rendered line off the claim and fails its row.
func TestEverySizerHonorsItsSpec(t *testing.T) {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{Argv: strings.Repeat("git status --porcelain --ahead-behind origin/main ", 4),
		Session: "s", Start: time.Now()})
	longPrompt := strings.Repeat("refactor the frobnicator until it hums ", 8)

	cases := []struct {
		name  string
		spec  SizeSpec
		build func() sizedOverlay
		// exact: every rendered line is the claimed width. Content-huggers
		// (false) size themselves inside the claim instead.
		exact bool
		// hAware: the spec claims a height and the box windows to it.
		hAware bool
		// reach, when non-nil, is how far (in printable columns) the widest
		// content row must extend before its right padding and border, given
		// content long enough to fill the row. A box whose inner arithmetic
		// loses columns still renders at the claimed width — the style pads
		// the difference — so the exact-width assertion alone cannot see the
		// other half of the ±2 class; this can.
		reach func(w int) int
	}{
		{"queue", HistoryPickerSize, func() sizedOverlay {
			q := NewQueueOverlay("parity")
			q.SetQueue([]string{longPrompt, "short"}, true)
			return q
		}, true, false,
			// The in-flight head fills its whole row: border+pad (3) + cursor
			// (2) + numbering (3) + the truncated prompt (inner-7) + the mark
			// (2), with inner = w-6.
			func(w int) int { return w - 3 }},
		{"history", HistoryPickerSize, func() sizedOverlay {
			return NewPromptHistoryOverlay([]string{longPrompt, "short"})
		}, true, false,
			// border+pad (3) + cursor (2) + the truncated prompt (inner-4),
			// with inner = w-6.
			func(w int) int { return w - 5 }},
		{"confirm", ConfirmSize, func() sizedOverlay {
			return NewConfirmationOverlay("Push changes from session 'a-rather-long-session-name' to origin?")
		}, true, false, nil},
		{"welcome", WelcomeSize, func() sizedOverlay {
			w := NewWelcomeOverlay()
			w.SetDetected(detectedFixture())
			return w
		}, true, false, nil},
		{"cmdlog", CmdLogSize, func() sizedOverlay { return NewCmdLogOverlay("s") }, true, true, nil},
		{"commandPalette", CommandPaletteSize, func() sizedOverlay {
			return NewCommandPaletteOverlay([]PaletteAction{
				{Key: "m", Label: "merge PR", Detail: strings.Repeat("merge the pull request ", 6)},
				{Key: "d", Label: "diff", Detail: "open the diff tab"},
			})
		}, true, true, nil},
		{"customCommands", CustomCommandsSize, func() sizedOverlay {
			return NewCustomCommandsOverlay([]CustomCommandRow{
				{Key: "x", Description: strings.Repeat("run the deploy script ", 10)},
			})
		}, true, true, nil},
		{"checkpoints", CheckpointSize, func() sizedOverlay {
			c := NewCheckpointOverlay("alpha")
			c.SetRows(checkpointRows(6))
			return c
		}, true, true, nil},
		{"image", ImageSize, func() sizedOverlay {
			return NewImageOverlay(Image{Path: "/tmp/shots/screenshot.png",
				Pixels: testImage(64, 32), Width: 64, Height: 32}, renderMode())
		}, true, true, nil},
		{"textOverlay", Fullscreen, func() sizedOverlay {
			return NewTextOverlay(strings.Repeat("the quick brown fox jumps over the lazy dog\n", 40))
		}, false, true, nil},
		{"settings", Fullscreen, func() sizedOverlay {
			return NewSettingsOverlay(config.DefaultConfig())
		}, false, true, nil},
		{"accounts", Fullscreen, func() sizedOverlay {
			return NewAccountsOverlay(&config.Config{}, config.DefaultState())
		}, false, true, nil},
		{"textInput", TextInputSize, func() sizedOverlay {
			return NewTextInputOverlay("New prompt", "")
		}, false, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, term := range [2][2]int{{80, 24}, {200, 50}} {
				w, h := tc.spec.Fit(term[0], term[1])
				o := tc.build()
				o.SetSize(w, h)
				lines := strings.Split(o.Render(), "\n")
				widest := 0
				for i, l := range lines {
					got := ansi.PrintableRuneWidth(l)
					if got > widest {
						widest = got
					}
					if tc.exact {
						assert.Equalf(t, w, got, "term %dx%d: line %d must be exactly the claimed %d columns\n%q",
							term[0], term[1], i, w, l)
					} else {
						assert.LessOrEqualf(t, got, w, "term %dx%d: line %d exceeds the claimed %d columns\n%q",
							term[0], term[1], i, w, l)
					}
				}
				if !tc.exact && w == term[0] {
					// A hugger given the whole terminal obeys the inset rule
					// (#695, SnapFullBleed): the full width, or a gap of at
					// least two cells per side — never the doubled-border
					// sliver between.
					assert.Truef(t, widest == w || widest <= w-4,
						"term %dx%d: a %d-wide box inside a %d-wide terminal reads as a doubled border (#695)",
						term[0], term[1], widest, w)
				}
				if tc.hAware {
					assert.LessOrEqualf(t, len(lines), h, "term %dx%d: %d lines exceed the claimed height %d",
						term[0], term[1], len(lines), h)
				}
				if tc.reach != nil {
					assert.Equalf(t, tc.reach(w), contentReach(lines),
						"term %dx%d: the widest content row must use the full inner width of the %d-column box",
						term[0], term[1], w)
				}
			}
		})
	}
}

// TestTextOverlayFullBleedGolden photographs the #695 rule: content wide
// enough to hit the terminal cap renders a box that meets both terminal edges
// — no one-cell halo of background for the frame underneath to double
// against. The content is deliberately synthetic (rulers wider than the
// terminal), so the golden pins the geometry without inheriting the
// cheatsheet's copy; TestHelpOverlayFullBleedsAtTheFloor in app holds the
// same rule over the real cheatsheet.
func TestTextOverlayFullBleedGolden(t *testing.T) {
	ruler := strings.Repeat("0123456789", 9) // 90 columns: capped at 80, snapped full
	o := NewTextOverlay(ruler + "\n" + ruler)
	w, h := Fullscreen.Fit(80, 24)
	o.SetSize(w, h)
	compareOverlayGolden(t, filepath.Join("testdata", "textoverlay-fullbleed-80x24.txt"),
		xansi.Strip(o.Render())+"\n")
}

// contentReach is how far the widest content row extends before its right
// padding and border: each side-bordered line, ANSI-stripped, loses its
// closing border and trailing spaces, and the widest remainder is the reach.
// Top and bottom border lines carry no content and are skipped.
func contentReach(lines []string) int {
	widest := 0
	for _, l := range lines {
		s := stripANSI(l)
		if !strings.HasSuffix(s, "│") {
			continue
		}
		s = strings.TrimRight(strings.TrimSuffix(s, "│"), " ")
		if w := ansi.PrintableRuneWidth(s); w > widest {
			widest = w
		}
	}
	return widest
}
