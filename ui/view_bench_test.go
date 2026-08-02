package ui

import (
	"context"
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"github.com/stretchr/testify/require"
)

// Cold/warm pairs for the two components #565 memoized, so each memo's worth can be
// read on its own rather than inferred from the whole-frame numbers in app.
//
// They come in pairs for the reason app/view_bench_test.go states: a cache silently
// turns its own benchmark into a measurement of itself, so the cold half drops the
// memo every iteration and the warm half deliberately does not. Read allocs/op —
// ns/op on this hardware swings ±15% between runs of untouched code.
//
// Run with `just bench`. Not part of `just ci`.

// benchWindow is the right pane at a normal terminal size, holding a pane frame
// with real text in it — a blank pane measures far cheaper than a captured one,
// since every line of agent output carries box-drawing runes.
func benchWindow(tb testing.TB) *TabbedWindow {
	tb.Helper()
	// Pin the theme, as the memo fixtures do. The glyph set drives rune widths and
	// the palette drives how many ANSI sequences each styled segment emits, and both
	// move allocs/op — the number this file tells the reader to trust. Unpinned, a
	// run after a test that left GlyphSetASCII selected is not comparable with one
	// from a clean start, and nothing in the output says which happened.
	tb.Cleanup(theme.Set("unicode"))
	w := NewTabbedWindow(NewPreviewPane(), NewDiffPane(), NewTerminalPane(context.Background()))
	w.SetSize(100, 40)
	var text string
	for i := range 38 {
		text += fmt.Sprintf("│ %02d ╭──────────────╮ agent output line with box drawing\n", i)
	}
	w.preview.previewState = previewState{text: text}
	return w
}

func benchList(tb testing.TB, n int) *List {
	tb.Helper()
	tb.Cleanup(theme.Set("unicode")) // see benchWindow
	s := spinner.New()
	l := NewList(&s)
	for i := range n {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: fmt.Sprintf("bench-%02d", i), Path: tb.TempDir(), Program: "echo",
		})
		require.NoError(tb, err)
		l.AddInstance(inst)()
	}
	l.SetSelectedInstance(0)
	l.SetSize(60, 40)
	return l
}

// BenchmarkTabbedWindowString is one full composition of the right pane: the tab
// strip, the Place into the content box, the bordered window and the height clamp
// — every one of which re-measures each line of what it wraps.
func BenchmarkTabbedWindowString(b *testing.B) {
	w := benchWindow(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w.ResetMemo() // COLD: without this every iteration after the first is a hit
		uiSink = w.String()
	}
}

// BenchmarkTabbedWindowStringRepeat is the same window rendered again with its
// inputs unmoved — what an idle Atrium does ~12 times a second, since #563 leaves
// the watched pane still while nothing is happening in it.
func BenchmarkTabbedWindowStringRepeat(b *testing.B) {
	w := benchWindow(b)
	uiSink = w.String() // warm
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		uiSink = w.String()
	}
}

// BenchmarkListString renders the rows and draws the panel chrome around them.
func BenchmarkListString(b *testing.B) {
	l := benchList(b, 14)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		l.ResetMemo() // COLD
		uiSink = l.String()
	}
}

// BenchmarkListStringRepeat re-renders an unchanged list: the rows are rebuilt
// either way, so the gap against the cold half is the panel chrome alone.
func BenchmarkListStringRepeat(b *testing.B) {
	l := benchList(b, 14)
	uiSink = l.String() // warm
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		uiSink = l.String()
	}
}

// uiSink defeats dead-store elimination, so the compiler cannot delete the work
// being measured.
var uiSink string
