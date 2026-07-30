package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// Every metadata sweep classifies each polled session's pane, and classification
// walks the whole capture many times over: one ANSI-stripping regexp pass and
// three full string copies in cleanForDetection, a SHA-256 of the result, then a
// dozen-odd whole-pane strings.Split calls across the claude matcher chain. At 14
// sessions that runs ~28 times a second (#546). These benchmarks put a number on
// one pane's worth of it, so a change to the sweep has a baseline.
//
// Run with `just bench`; `go test` ignores them without -bench.

// benchPane builds a capture that looks like what tmux actually returns: SGR
// sequences on most lines, box-drawing borders, and a claude footer — because the
// cost being measured is dominated by escape handling and rune walking, and a
// plain-ASCII fixture would understate it by a wide margin.
func benchPane(rows, cols int) string {
	var b strings.Builder
	body := strings.Repeat("lorem ipsum dolor sit amet ", 1+cols/26)
	for i := range rows - 4 {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&b, "\x1b[38;5;%dm%s\x1b[0m\n", 240+i%16, body[:cols])
		case 1:
			fmt.Fprintf(&b, "\x1b[1m● \x1b[0m%s   \n", body[:cols-4])
		default:
			fmt.Fprintf(&b, "  %s\n", body[:cols-2])
		}
	}
	b.WriteString("╭" + strings.Repeat("─", cols-2) + "╮\n")
	b.WriteString("│ > " + strings.Repeat(" ", cols-6) + "│\n")
	b.WriteString("╰" + strings.Repeat("─", cols-2) + "╯\n")
	b.WriteString("\x1b[2m  ⏵⏵ accept edits on · esc to interrupt\x1b[0m\n")
	return b.String()
}

// BenchmarkCleanForDetection measures the ANSI strip plus per-line trim that every
// poll pays before any matcher has looked at the pane.
func BenchmarkCleanForDetection(b *testing.B) {
	for _, size := range []struct {
		name       string
		rows, cols int
	}{
		{"80x24", 24, 80},
		{"200x50", 50, 200},
	} {
		b.Run(size.name, func(b *testing.B) {
			pane := benchPane(size.rows, size.cols)
			b.ReportAllocs()
			b.SetBytes(int64(len(pane)))
			b.ResetTimer()
			for b.Loop() {
				sink = cleanForDetection(pane)
			}
		})
	}
}

// BenchmarkPaneHash measures the SHA-256 that turns a cleaned pane into the
// change signal, including the full string-to-bytes copy its own comment
// acknowledges.
func BenchmarkPaneHash(b *testing.B) {
	m := newStatusMonitor("claude")
	pane := cleanForDetection(benchPane(50, 200))
	b.ReportAllocs()
	b.SetBytes(int64(len(pane)))
	b.ResetTimer()
	for b.Loop() {
		byteSink = m.hash(pane)
	}
}

var (
	sink     string
	byteSink []byte
)
