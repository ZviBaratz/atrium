package overlay

import (
	"strings"
	"testing"
	"time"

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
	}{
		{"queue", HistoryPickerSize, func() sizedOverlay {
			q := NewQueueOverlay("parity")
			q.SetQueue([]string{longPrompt, "short"}, true)
			return q
		}, true, false},
		{"history", HistoryPickerSize, func() sizedOverlay {
			return NewPromptHistoryOverlay([]string{longPrompt, "short"})
		}, true, false},
		{"confirm", ConfirmSize, func() sizedOverlay {
			return NewConfirmationOverlay("Push changes from session 'a-rather-long-session-name' to origin?")
		}, true, false},
		{"welcome", WelcomeSize, func() sizedOverlay {
			w := NewWelcomeOverlay()
			w.SetDetected(detectedFixture())
			return w
		}, true, false},
		{"cmdlog", CmdLogSize, func() sizedOverlay { return NewCmdLogOverlay("s") }, true, true},
		{"commandPalette", CommandPaletteSize, func() sizedOverlay {
			return NewCommandPaletteOverlay([]PaletteAction{
				{Key: "m", Label: "merge PR", Detail: strings.Repeat("merge the pull request ", 6)},
				{Key: "d", Label: "diff", Detail: "open the diff tab"},
			})
		}, true, true},
		{"customCommands", CustomCommandsSize, func() sizedOverlay {
			return NewCustomCommandsOverlay([]CustomCommandRow{
				{Key: "x", Description: strings.Repeat("run the deploy script ", 10)},
			})
		}, true, true},
		{"checkpoints", CheckpointSize, func() sizedOverlay {
			c := NewCheckpointOverlay("alpha")
			c.SetRows(checkpointRows(6))
			return c
		}, true, true},
		{"image", ImageSize, func() sizedOverlay {
			return NewImageOverlay(Image{Path: "/tmp/shots/screenshot.png",
				Pixels: testImage(64, 32), Width: 64, Height: 32}, renderMode())
		}, true, true},
		{"textOverlay", Fullscreen, func() sizedOverlay {
			return NewTextOverlay(strings.Repeat("the quick brown fox jumps over the lazy dog\n", 40))
		}, false, true},
		{"settings", Fullscreen, func() sizedOverlay {
			return NewSettingsOverlay(config.DefaultConfig())
		}, false, true},
		{"accounts", Fullscreen, func() sizedOverlay {
			return NewAccountsOverlay(&config.Config{}, config.DefaultState())
		}, false, true},
		{"textInput", TextInputSize, func() sizedOverlay {
			return NewTextInputOverlay("New prompt", "")
		}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, term := range [2][2]int{{80, 24}, {200, 50}} {
				w, h := tc.spec.Fit(term[0], term[1])
				o := tc.build()
				o.SetSize(w, h)
				lines := strings.Split(o.Render(), "\n")
				for i, l := range lines {
					got := ansi.PrintableRuneWidth(l)
					if tc.exact {
						assert.Equalf(t, w, got, "term %dx%d: line %d must be exactly the claimed %d columns\n%q",
							term[0], term[1], i, w, l)
					} else {
						assert.LessOrEqualf(t, got, w, "term %dx%d: line %d exceeds the claimed %d columns\n%q",
							term[0], term[1], i, w, l)
					}
				}
				if tc.hAware {
					assert.LessOrEqualf(t, len(lines), h, "term %dx%d: %d lines exceed the claimed height %d",
						term[0], term[1], len(lines), h)
				}
			}
		})
	}
}
