package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// The frame is rebuilt from scratch after every message, because Bubble Tea calls
// View() and offers no hook to skip it (#546). That was ~32 times a second at idle
// — three independent 10Hz loops plus the 2Hz metadata tick — until two of the
// three learned to stop: the spinner loop when no row is spinning (armSpinnerTick),
// and the pane-capture chain when there is nothing to capture (armFrameCapture,
// which includes a preview pane that has stopped moving). What is left at idle is
// the preview tick and the metadata tick: ~12/s — rising to ~22/s while the watched
// pane is repainting, and ~32/s once something is Running or Loading.
//
// Three layers of the rebuild are now memoized, each on the bytes it composes:
// frameCached skips the frame stack and zone.Scan, TabbedWindow skips the right
// pane, and List skips its panel chrome (#561, #565). That is why the benchmarks
// below come in cold and warm pairs — see BenchmarkViewContentRepeat — and why the
// cold ones drop all three through resetRenderMemos rather than clearing one field
// by hand. These exist to put a number on what one rebuild costs and how it scales
// with the fleet, so a later change that claims to make idle cheaper has something
// to be measured against.
//
// Run with `just bench`. They are not part of `just ci`: `go test` ignores
// benchmarks unless -bench is passed, so they add no gate time and cannot flake.

// benchFleetSizes are the fleet sizes worth knowing. 1 is the floor, 5 a normal
// day, and 14 the size the #546 measurements were taken at.
var benchFleetSizes = []int{1, 5, 14}

// newBenchHome builds a sized, laid-out home carrying n instances, in the default
// state on the preview tab — the shape an idle Atrium actually renders.
func newBenchHome(tb testing.TB, n int) *home {
	tb.Helper()
	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		program:      "echo",
		spinner:      spinner.New(),
	}
	// The list must borrow THIS home's spinner, as newHome does — not a local copy.
	// With a copy the rows animate off a model the update loop never advances, so a
	// fixture could never reproduce a spinning frame at all.
	h.list = ui.NewList(&h.spinner)
	for i := range n {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   fmt.Sprintf("bench-%02d", i),
			Path:    tb.TempDir(),
			Program: "echo",
		})
		require.NoError(tb, err)
		h.list.AddInstance(inst)()
	}
	h.list.SetSelectedInstance(0)
	h.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	h.state = stateDefault
	return h
}

// BenchmarkViewContent measures one full frame build: every visible list row
// re-rendered from scratch, the tabbed window, the lipgloss joins, and the
// bubblezone scan over the whole frame string.
func BenchmarkViewContent(b *testing.B) {
	for _, n := range benchFleetSizes {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			h := newBenchHome(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				// Drop every render memo so this stays a COLD build, comparable with
				// the numbers taken before they existed. Without this the loop renders
				// an unchanging model and every iteration after the first is a memo
				// hit — the benchmark would report the caches, not the frame.
				h.resetRenderMemos()
				sink = h.viewContent()
			}
		})
	}
}

// BenchmarkViewContentRepeat measures the frame an IDLE Atrium actually builds:
// the same model rendered again, memo warm. That is the steady state — Bubble Tea
// calls View() after every message, and at idle the preview tick and the metadata
// tick produce ~12 of them a second, almost all identical — so the gap against
// BenchmarkViewContent is what the memo is worth in practice.
func BenchmarkViewContentRepeat(b *testing.B) {
	for _, n := range benchFleetSizes {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			h := newBenchHome(b, n)
			sink = h.viewContent() // warm the memo
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				sink = h.viewContent()
			}
		})
	}
}

// BenchmarkView measures the same frame through the exported entry point, so the
// per-frame View-struct work (window title, progress bar, mouse mode) is included
// — that is what Bubble Tea actually calls.
func BenchmarkView(b *testing.B) {
	for _, n := range benchFleetSizes {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			h := newBenchHome(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				sink = h.View().Content
			}
		})
	}
}

// BenchmarkViewContentNoZoneScan builds the same frame with the bubblezone scan
// stubbed out, so the difference against BenchmarkViewContent is zone.Scan's
// share — which is otherwise unattributable, because it is one call inside a
// function that does everything else too.
func BenchmarkViewContentNoZoneScan(b *testing.B) {
	for _, n := range benchFleetSizes {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			h := newBenchHome(b, n)
			restore := scanFrame
			scanFrame = func(s string) string { return s }
			b.Cleanup(func() { scanFrame = restore })
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				h.resetRenderMemos() // cold, like BenchmarkViewContent
				sink = h.viewContent()
			}
		})
	}
}

// sink defeats dead-store elimination, so the compiler cannot delete the work
// being measured.
var sink string

// resetRenderMemos drops every memo the frame build consults, so the next
// viewContent is a COLD build.
//
// It exists because a cache silently converts its own benchmark into a
// measurement of itself: BenchmarkViewContent dropped the zone-scan memo by hand
// from the day #561 landed, and the first run after #565 added the tabbed window's
// reported a 62% fall in allocs/op that was entirely the new cache answering. One
// helper rather than a line per memo at each of the two cold call sites, so the
// next memo has exactly one place to join and cannot be forgotten at one of them.
// TestResetRenderMemos_ForcesEveryLayerToRecompose is what enforces that it joins.
func (m *home) resetRenderMemos() {
	m.frameMemo.Reset()
	m.tabbedWindow.ResetMemo()
	m.list.ResetMemo()
}
