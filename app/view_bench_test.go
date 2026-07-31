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

// The frame is rebuilt from scratch ~32 times a second at idle — three
// independent 10Hz loops plus the 2Hz metadata tick — because Bubble Tea calls
// View() after every message and offers no hook to skip it (#546). Nothing in
// app/ or ui/ memoizes any part of that. These benchmarks exist to put a number
// on what one rebuild costs and how it scales with the fleet, so a later change
// that claims to make idle cheaper has something to be measured against.
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
	s := spinner.New()
	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         ui.NewList(&s),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		program:      "echo",
		spinner:      s,
	}
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
				// Drop the zone-scan memo so this stays a COLD build, comparable with
				// the numbers taken before the memo existed. Without this the loop
				// renders an unchanging model and every iteration after the first is a
				// memo hit — the benchmark would report the cache, not the frame.
				h.lastScanIn = ""
				sink = h.viewContent()
			}
		})
	}
}

// BenchmarkViewContentRepeat measures the frame an IDLE Atrium actually builds:
// the same model rendered again, memo warm. That is the steady state — Bubble Tea
// calls View() after every message and three 10Hz loops plus the metadata tick
// produce ~32 of them a second, almost all identical — so the gap against
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
				h.lastScanIn = "" // cold, like BenchmarkViewContent
				sink = h.viewContent()
			}
		})
	}
}

// sink defeats dead-store elimination, so the compiler cannot delete the work
// being measured.
var sink string
