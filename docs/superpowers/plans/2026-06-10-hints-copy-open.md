# Hint-Based Copy/Open ("Fingers Mode") Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Press `f` over the preview pane to overlay tmux-fingers-style hint labels on URLs/paths/SHAs; typing a hint copies the match, typing it UPPERCASE also opens URLs in the browser.

**Architecture:** A new pure `hints/` package (scan → assign labels → render decorated screen) plugs into the existing `PreviewPane` as a third frozen mode (sibling of scroll mode), with a new `stateHints` key-dispatch state in `app/`. No overlay compositing — the preview renders its own decorated capture, so positions are correct by construction.

**Tech Stack:** Go, Bubble Tea, lipgloss. Spec: `docs/superpowers/specs/2026-06-10-hints-copy-open-design.md`.

**Toolchain note (this machine):** `go` is not on the Bash-tool PATH. Use:
- Tests/builds: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test` and `just build` likewise, or invoke `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./...` directly.
- Commits (pre-commit hooks run gofmt/go vet): prefix with `PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH"`.

All paths below are relative to the repo root (the `zvi/copy-pasting` worktree).

---

### Task 1: `hints` package — label assignment

**Files:**
- Create: `hints/assign.go`
- Test: `hints/assign_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// hints/assign_test.go
package hints

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Up to 26 matches every label is a single character, in alphabet order, so
// the most common screens stay one-keystroke.
func TestAssignLabels_SingleCharsUpToAlphabetSize(t *testing.T) {
	labels := assignLabels(26)
	require.Len(t, labels, 26)
	for i, l := range labels {
		assert.Len(t, l, 1, "label %d", i)
		assert.Equal(t, string(Alphabet[i]), l)
	}
}

// Past the alphabet size, tail characters are expanded into two-char combos.
// The result must stay prefix-free: no label may be a prefix of another, or
// typing the shorter one would shadow the longer.
func TestAssignLabels_PrefixFreeWhenExpanded(t *testing.T) {
	labels := assignLabels(40)
	require.Len(t, labels, 40)
	for i, a := range labels {
		for j, b := range labels {
			if i == j {
				continue
			}
			assert.False(t, strings.HasPrefix(b, a),
				"label %q is a prefix of label %q", a, b)
		}
	}
}

// The exact count requested comes back, for any n the screen can produce.
func TestAssignLabels_ExactCount(t *testing.T) {
	for _, n := range []int{0, 1, 25, 26, 27, 51, 52, 100} {
		assert.Len(t, assignLabels(n), n, "n=%d", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -run TestAssignLabels -v`
Expected: FAIL (package does not exist / `assignLabels` undefined)

- [ ] **Step 3: Write the implementation**

```go
// hints/assign.go

// Package hints implements the matcher, hint-label assignment, and renderer
// behind fingers mode: detecting actionable strings (URLs, paths, SHAs, …) in
// a captured pane screen and labeling them with short keyboard hints.
//
// The package is deliberately pure — no tmux, UI, or app dependencies — so the
// same engine can later power hints inside attached sessions (see the design
// doc's "Attached sessions" section).
package hints

// Alphabet orders hint characters by keyboard ergonomics (home row first),
// following tmux-thumbs' qwerty layout. Hint keys are matched case-insensitively;
// an uppercase press selects the copy+open variant.
const Alphabet = "asdfqwerzxcvjklmiuopghtybn"

// assignLabels returns n prefix-free hint labels over Alphabet: all single
// characters first; when more are needed, the tail characters are expanded
// into two-character combinations (tmux-thumbs' expansion). A character used
// as a prefix never appears alone, so no label is a prefix of another.
func assignLabels(n int) []string {
	chars := []rune(Alphabet)
	singles := make([]string, len(chars))
	for i, c := range chars {
		singles[i] = string(c)
	}
	var expanded []string
	for n > len(singles)+len(expanded) && len(singles) > 0 {
		last := singles[len(singles)-1]
		singles = singles[:len(singles)-1]
		group := make([]string, 0, len(chars))
		for _, c := range chars {
			group = append(group, last+string(c))
		}
		expanded = append(group, expanded...)
	}
	labels := append(append([]string{}, singles...), expanded...)
	if n < len(labels) {
		labels = labels[:n]
	}
	return labels
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -run TestAssignLabels -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add hints/ && git commit -m "feat(hints): prefix-free hint label assignment"
```

---

### Task 2: `hints` package — patterns and scanning

**Files:**
- Create: `hints/patterns.go`
- Create: `hints/scan.go`
- Test: `hints/scan_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// hints/scan_test.go
package hints

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func textsOf(ms []Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Text
	}
	return out
}

// The curated patterns against realistic agent-session output. Each case is
// one stripped line; expected is the matched texts in left-to-right order.
func TestScan_Patterns(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		expected []string
		kinds    []Kind
	}{
		{
			name:     "url in prose, trailing period trimmed",
			line:     "PR opened at https://github.com/x/y/pull/9.",
			expected: []string{"https://github.com/x/y/pull/9"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "markdown link captures the url only",
			line:     "see [the docs](https://example.com/docs) for details",
			expected: []string{"https://example.com/docs"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "path with line and column",
			line:     "error in app/app_update.go:412:7",
			expected: []string{"app/app_update.go:412:7"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "git status captures the filename only",
			line:     "        modified:   session/instance.go",
			expected: []string{"session/instance.go"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "diff header captures the path only",
			line:     "+++ b/ui/preview.go",
			expected: []string{"ui/preview.go"},
			kinds:    []Kind{KindPath},
		},
		{
			name:     "uuid wins over sha on overlap",
			line:     "id 123e4567-e89b-42d3-a456-426614174000 ok",
			expected: []string{"123e4567-e89b-42d3-a456-426614174000"},
			kinds:    []Kind{KindText},
		},
		{
			name:     "url wins over path (contains slashes)",
			line:     "git@github.com:x/y.git cloned",
			expected: []string{"git@github.com:x/y.git"},
			kinds:    []Kind{KindURL},
		},
		{
			name:     "sha in a commit line",
			line:     "commit 6912021ab3 (HEAD)",
			expected: []string{"6912021ab3"},
			kinds:    []Kind{KindText},
		},
		{
			name:     "no matches",
			line:     "Thinking about the problem",
			expected: nil,
			kinds:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := scanLine(tc.line, 0)
			require.Equal(t, tc.expected, textsOf(ms))
			for i, m := range ms {
				assert.Equal(t, tc.kinds[i], m.Kind, "kind of %q", m.Text)
			}
		})
	}
}

// Rows and rune columns must locate the copyable text exactly — Col points at
// the capture group's first rune, not the full pattern's.
func TestScan_RowsAndCols(t *testing.T) {
	text := "line one\nsee /tmp/x.go here\nmodified:   foo/bar.go"
	ms := Scan(text)
	require.Len(t, ms, 2)
	assert.Equal(t, Match{Text: "/tmp/x.go", Kind: KindPath, Row: 1, Col: 4}, ms[0])
	assert.Equal(t, Match{Text: "foo/bar.go", Kind: KindPath, Row: 2, Col: 12}, ms[1])
}

// Matching always operates on stripped text; StripANSI removes the SGR
// sequences tmux capture-pane -e embeds.
func TestStripANSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m /tmp/a"
	assert.Equal(t, "red /tmp/a", StripANSI(in))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -run 'TestScan|TestStripANSI' -v`
Expected: FAIL (`Match`, `Scan`, `scanLine`, `StripANSI`, `Kind` undefined)

- [ ] **Step 3: Write the pattern table**

```go
// hints/patterns.go
package hints

import "regexp"

// Kind classifies what a match is, which decides the open-variant's behavior.
type Kind int

const (
	// KindText is copy-only content (SHAs, UUIDs, IPs, hex).
	KindText Kind = iota
	// KindURL opens in the browser on the open variant.
	KindURL
	// KindPath is a filesystem path (open degrades to copy in v1).
	KindPath
)

// pattern is one built-in matcher. A `match` named group selects the copyable
// substring; otherwise the whole match is copied.
type pattern struct {
	name string
	re   *regexp.Regexp
	kind Kind
}

// builtinPatterns is the curated set, in priority order: when two patterns
// match at the same column, the earlier entry wins (url beats path, uuid
// beats sha). Regexes follow tmux-fingers/tmux-thumbs, adapted to RE2.
var builtinPatterns = []pattern{
	{"markdown-url", regexp.MustCompile(`\[[^]]*\]\((?P<match>[^)]+)\)`), KindURL},
	{"url", regexp.MustCompile(`(?P<match>(https?://|git://|ssh://|ftp://|file:///)[^\s()"']+|git@[^\s()"']+)`), KindURL},
	{"diff-path", regexp.MustCompile(`(---|\+\+\+) [ab]/(?P<match>.+)`), KindPath},
	{"git-status", regexp.MustCompile(`(modified|deleted|new file): +(?P<match>.+)`), KindPath},
	{"path", regexp.MustCompile(`(?P<match>([.\w\-@~]+)?(/[.\w\-@]+)+(:\d+(:\d+)?)?)`), KindPath},
	{"uuid", regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), KindText},
	{"sha", regexp.MustCompile(`[0-9a-f]{7,64}`), KindText},
	{"ipv4", regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}`), KindText},
	{"hex", regexp.MustCompile(`0x[0-9a-fA-F]+`), KindText},
	{"color", regexp.MustCompile(`#[0-9a-fA-F]{6}`), KindText},
}
```

- [ ] **Step 4: Write the scanner**

```go
// hints/scan.go
package hints

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Match is one actionable string found on the screen.
type Match struct {
	// Text is the copyable content (the `match` group when the pattern has one).
	Text string
	// Kind decides the open variant's behavior.
	Kind Kind
	// Row and Col locate the first rune of Text on the stripped screen:
	// Row is the 0-based visible line, Col the 0-based rune index within it.
	Row, Col int
	// Label is the assigned hint sequence (set by NewScreen, empty from Scan).
	Label string
}

// ansiRE matches the CSI escape sequences tmux capture-pane -e emits.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;:?]*[A-Za-z]`)

// StripANSI removes ANSI escape sequences so matching and rendering operate
// on plain text. Hint mode re-renders the screen itself with a dim backdrop,
// so original colors are deliberately dropped while the mode is active —
// the contrast effect tmux-fingers applies on purpose.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// Scan finds all matches in stripped multi-line text, top to bottom.
func Scan(text string) []Match {
	var out []Match
	for row, line := range strings.Split(text, "\n") {
		out = append(out, scanLine(line, row)...)
	}
	return out
}

// scanLine finds matches in one stripped line, left to right, non-overlapping.
// All patterns run at each position; the earliest match wins, ties broken by
// pattern priority order. The scanner then advances past the full match (not
// just the capture), so a pattern's consumed prefix ("modified: ") is skipped.
func scanLine(line string, row int) []Match {
	var out []Match
	offset := 0 // byte offset into line
	for offset < len(line) {
		best := -1
		var bestLoc []int
		for i, p := range builtinPatterns {
			loc := p.re.FindStringSubmatchIndex(line[offset:])
			if loc == nil {
				continue
			}
			if best == -1 || loc[0] < bestLoc[0] {
				best, bestLoc = i, loc
			}
		}
		if best == -1 {
			break
		}
		p := builtinPatterns[best]
		text := line[offset+bestLoc[0] : offset+bestLoc[1]]
		textStart := offset + bestLoc[0]
		if gi := p.re.SubexpIndex("match"); gi >= 0 && bestLoc[2*gi] >= 0 {
			text = line[offset+bestLoc[2*gi] : offset+bestLoc[2*gi+1]]
			textStart = offset + bestLoc[2*gi]
		}
		if p.kind == KindURL {
			// Sentence-final URLs in logs: the trailing punctuation is prose,
			// not address.
			text = strings.TrimRight(text, ".,;:")
		}
		if text != "" {
			out = append(out, Match{
				Text: text,
				Kind: p.kind,
				Row:  row,
				Col:  utf8.RuneCountInString(line[:textStart]),
			})
		}
		offset += bestLoc[1]
	}
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -v`
Expected: PASS (all Task 1 + Task 2 tests). If a pattern case fails, fix the regex or the expected value — the test table is the contract.

- [ ] **Step 6: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add hints/ && git commit -m "feat(hints): pattern table and earliest-match-wins scanner"
```

---

### Task 3: `hints` package — Screen (geometry, dedup, resolve)

**Files:**
- Create: `hints/screen.go`
- Test: `hints/screen_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// hints/screen_test.go
package hints

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bottom-most matches get the shortest labels: the match nearest the prompt
// is almost always the wanted one in an agent session.
func TestNewScreen_BottomUpAssignment(t *testing.T) {
	s := NewScreen("/top/one\n\n/bottom/two", 80, 10)
	require.Equal(t, 2, s.MatchCount())
	bottom, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, bottom)
	assert.Equal(t, "/bottom/two", bottom.Text)
}

// Identical text shares one label (tmux-fingers' dedup): one keystroke,
// regardless of how many times the same path is on screen.
func TestNewScreen_DedupSameText(t *testing.T) {
	s := NewScreen("/same/path\n/same/path\n/other/path", 80, 10)
	// 3 visual matches but only 2 distinct labels.
	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	labels := map[string]bool{}
	for _, mm := range s.matches {
		labels[mm.Label] = true
	}
	assert.Len(t, labels, 2)
}

// Geometry clipping: matches beyond the visible rows, or starting past the
// pane's width, must not get hints — a hint must label something visible.
func TestNewScreen_ClipsToGeometry(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		s := NewScreen("/visible/a\n/clipped/b", 80, 1)
		require.Equal(t, 1, s.MatchCount())
		m, _ := s.Resolve("a")
		assert.Equal(t, "/visible/a", m.Text)
	})
	t.Run("width", func(t *testing.T) {
		pad := strings.Repeat("x", 30)
		s := NewScreen(pad+" /far/away", 20, 10)
		assert.Equal(t, 0, s.MatchCount())
	})
}

// Resolve narrows by typed prefix: full label -> the match; proper prefix ->
// nil but valid; a character no label starts with -> invalid.
func TestScreen_Resolve(t *testing.T) {
	// 27 distinct SHAs force two-character labels for the top rows.
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("abcdef%02x", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	require.Equal(t, 27, s.MatchCount())

	m, valid := s.Resolve("a")
	require.True(t, valid)
	require.NotNil(t, m)
	assert.Equal(t, "abcdef1a", m.Text, "bottom row gets label a")

	m, valid = s.Resolve("n") // prefix of na/ns, not a full label
	assert.True(t, valid)
	assert.Nil(t, m)

	m, valid = s.Resolve("nz") // no such label
	assert.False(t, valid)
	assert.Nil(t, m)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -run 'TestNewScreen|TestScreen' -v`
Expected: FAIL (`NewScreen`, `Screen` undefined)

- [ ] **Step 3: Write the implementation**

```go
// hints/screen.go
package hints

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Screen is one frozen, hinted capture of a preview pane: the stripped
// visible lines plus the labeled matches found on them. Immutable after
// NewScreen; Render and Resolve are read-only.
type Screen struct {
	lines   []string
	width   int
	matches []Match
}

// NewScreen strips raw pane content, clips it to the pane's visible geometry
// (rows lines of width columns — the same slice the live preview renders),
// then scans, dedups, and labels the matches. Bottom-most matches get the
// shortest labels; identical text shares one label. A non-positive width or
// negative rows disables that axis of clipping (used by tests).
func NewScreen(raw string, width, rows int) *Screen {
	lines := strings.Split(StripANSI(raw), "\n")
	if rows >= 0 && len(lines) > rows {
		lines = lines[:rows]
	}

	matches := Scan(strings.Join(lines, "\n"))
	// A hint must label something the user can see: drop matches whose first
	// rune is already clipped by the pane's width truncation.
	visible := matches[:0]
	for _, m := range matches {
		if width <= 0 || m.Col < width {
			visible = append(visible, m)
		}
	}
	// Bottom-up: the match nearest the prompt gets the shortest label.
	sort.SliceStable(visible, func(i, j int) bool {
		if visible[i].Row != visible[j].Row {
			return visible[i].Row > visible[j].Row
		}
		return visible[i].Col < visible[j].Col
	})
	labels := assignLabels(countDistinct(visible))
	byText := make(map[string]string)
	next := 0
	for i := range visible {
		if l, ok := byText[visible[i].Text]; ok {
			visible[i].Label = l
			continue
		}
		visible[i].Label = labels[next]
		byText[visible[i].Text] = labels[next]
		next++
	}
	// A label longer than its text would overhang the match (fingers' guard).
	// Dropping after assignment keeps the remaining labels prefix-free.
	kept := visible[:0]
	for _, m := range visible {
		if utf8.RuneCountInString(m.Text) >= len(m.Label) {
			kept = append(kept, m)
		}
	}
	return &Screen{lines: lines, width: width, matches: kept}
}

func countDistinct(ms []Match) int {
	seen := make(map[string]struct{}, len(ms))
	for _, m := range ms {
		seen[m.Text] = struct{}{}
	}
	return len(seen)
}

// MatchCount reports how many labeled matches the screen holds.
func (s *Screen) MatchCount() int { return len(s.matches) }

// Resolve narrows the matches by a typed (lowercased) prefix. It returns the
// selected match when typed equals a full label; match=nil with valid=true
// when typed is a proper prefix of at least one label; valid=false when no
// label starts with typed.
func (s *Screen) Resolve(typed string) (match *Match, valid bool) {
	for i := range s.matches {
		if s.matches[i].Label == typed {
			return &s.matches[i], true
		}
		if strings.HasPrefix(s.matches[i].Label, typed) {
			valid = true
		}
	}
	return nil, valid
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -v`
Expected: PASS (all hints tests so far)

- [ ] **Step 5: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add hints/ && git commit -m "feat(hints): screen model with geometry clipping, dedup, resolve"
```

---

### Task 4: `hints` package — rendering

**Files:**
- Create: `hints/render.go`
- Test: `hints/render_test.go`

- [ ] **Step 1: Write the failing tests**

Tests use zero-value `lipgloss.Style`s (no colors), so rendered output is
plain text and label placement is directly assertable.

```go
// hints/render_test.go
package hints

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainStyles render no escape codes, so position assertions read literally.
func plainStyles() Styles { return Styles{} }

// The label overlays the match's first cells: the hint replaces what it
// covers instead of shifting the line (tmux-thumbs' left position).
func TestRender_LabelOverlaysMatchStart(t *testing.T) {
	s := NewScreen("go to /tmp/file.go now", 80, 10)
	require.Equal(t, 1, s.MatchCount())
	out := s.Render("", plainStyles())
	assert.Equal(t, "go to atmp/file.go now", out,
		"label 'a' must replace the first rune of the match")
}

// Typing a valid prefix consumes it: matching labels show only their
// remaining suffix over the match start, and matches whose labels no longer
// fit the prefix lose their decoration entirely.
func TestRender_TypedPrefixNarrows(t *testing.T) {
	// 27 distinct paths force two-char labels. Bottom-up assignment over
	// Alphabet ("asdf…ybn") gives: row 26 -> "a", …, row 1 -> "na", row 0 -> "ns"
	// ('n' is the popped expansion char; its group follows alphabet order).
	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("/dir/file%02d", i))
	}
	s := NewScreen(strings.Join(lines, "\n"), 80, 27)
	require.Equal(t, 27, s.MatchCount())

	rows := strings.Split(s.Render("n", plainStyles()), "\n")
	// Rows 0 and 1 keep their hints, narrowed to the remaining suffix
	// rendered over the match's first rune.
	assert.Equal(t, "sdir/file00", rows[0], `row 0's label "ns" narrows to "s"`)
	assert.Equal(t, "adir/file01", rows[1], `row 1's label "na" narrows to "a"`)
	// Every other row's label no longer matches the prefix: plain text again.
	assert.Equal(t, "/dir/file02", rows[2])
	assert.Equal(t, "/dir/file26", rows[26])
}

// Lines with no matches are passed through verbatim (modulo styling).
func TestRender_PlainLinesUntouched(t *testing.T) {
	s := NewScreen("no matches here\n/tmp/x.go", 80, 10)
	out := strings.Split(s.Render("", plainStyles()), "\n")
	assert.Equal(t, "no matches here", out[0])
}
```

The test file needs `"fmt"` in its imports alongside `strings` and `testing`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -run TestRender -v`
Expected: FAIL (`Styles`, `Render` undefined)

- [ ] **Step 3: Write the implementation**

```go
// hints/render.go
package hints

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Styles carries the three roles hint rendering needs. The caller builds them
// from the active theme so this package stays theme-agnostic (and tests can
// pass zero values for plain-text output).
type Styles struct {
	// Backdrop dims all non-match text.
	Backdrop lipgloss.Style
	// Match highlights matched text after its label.
	Match lipgloss.Style
	// Label renders the hint characters themselves.
	Label lipgloss.Style
}

// Render draws the frozen screen with hint decorations: every line dimmed,
// matches highlighted, each match's first cells overlaid with its label.
// typed is the already-entered (lowercased) prefix: labels that no longer
// match it lose their decoration; matching labels show only their remaining
// suffix, keeping the next keys to type in front of the user.
//
// Splicing happens at rune indices into the same rune slice the line came
// from, so alignment is self-consistent: the output is exactly the original
// runes with some replaced by ASCII label characters. (All pattern matches
// are ASCII, so the replaced cells are single-width.)
func (s *Screen) Render(typed string, st Styles) string {
	byRow := make(map[int][]Match)
	for _, m := range s.matches {
		byRow[m.Row] = append(byRow[m.Row], m)
	}
	out := make([]string, len(s.lines))
	for row, line := range s.lines {
		ms := byRow[row]
		sort.Slice(ms, func(i, j int) bool { return ms[i].Col < ms[j].Col })
		out[row] = renderLine(line, ms, typed, st)
	}
	return strings.Join(out, "\n")
}

func renderLine(line string, ms []Match, typed string, st Styles) string {
	runes := []rune(line)
	var b strings.Builder
	pos := 0
	for _, m := range ms {
		if m.Col < pos || m.Col > len(runes) {
			continue // overlap or out of range: keep the earlier match
		}
		b.WriteString(st.Backdrop.Render(string(runes[pos:m.Col])))
		end := m.Col + len([]rune(m.Text))
		if end > len(runes) {
			end = len(runes)
		}
		if !strings.HasPrefix(m.Label, typed) {
			// Filtered out by the typed prefix: back to plain backdrop.
			b.WriteString(st.Backdrop.Render(string(runes[m.Col:end])))
			pos = end
			continue
		}
		suffix := m.Label[len(typed):]
		if n := end - m.Col; len(suffix) > n {
			suffix = suffix[:n]
		}
		b.WriteString(st.Label.Render(suffix))
		b.WriteString(st.Match.Render(string(runes[m.Col+len(suffix) : end])))
		pos = end
	}
	if pos < len(runes) {
		b.WriteString(st.Backdrop.Render(string(runes[pos:])))
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./hints/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add hints/ && git commit -m "feat(hints): decorated screen rendering with typed-prefix narrowing"
```

---

### Task 5: browser opener

**Files:**
- Create: `app/open.go`
- Test: `app/open_test.go`

- [ ] **Step 1: Write the failing test**

```go
// app/open_test.go
package app

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chooseOpener: darwin always uses `open`; linux walks the candidate list in
// order and reports a clear error when none exist (headless box).
func TestChooseOpener(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		c, err := chooseOpener("darwin", func(string) (string, error) {
			t.Fatal("lookPath must not be consulted on darwin")
			return "", nil
		})
		require.NoError(t, err)
		assert.Equal(t, "open", c)
	})
	t.Run("linux picks first present", func(t *testing.T) {
		c, err := chooseOpener("linux", func(name string) (string, error) {
			if name == "x-www-browser" {
				return "/usr/bin/x-www-browser", nil
			}
			return "", errors.New("not found")
		})
		require.NoError(t, err)
		assert.Equal(t, "x-www-browser", c)
	})
	t.Run("none found", func(t *testing.T) {
		_, err := chooseOpener("linux", func(string) (string, error) {
			return "", errors.New("not found")
		})
		assert.Error(t, err)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestChooseOpener -v`
Expected: FAIL (`chooseOpener` undefined)

- [ ] **Step 3: Write the implementation**

```go
// app/open.go
package app

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openInBrowser launches the user's opener on a URL, detached from the TUI —
// never via tea.Exec, because the browser doesn't need the terminal. Package
// var so tests can substitute a fake (same pattern as copyToClipboard).
var openInBrowser = openDetached

// linuxOpeners are tried in order on non-darwin systems; wslview (from wslu)
// covers WSL, where xdg-open is typically absent.
var linuxOpeners = []string{"xdg-open", "x-www-browser", "wslview"}

// chooseOpener picks the opener command for goos using lookPath. Split out
// and parameterized so the selection logic is testable without the host's
// actual binaries.
func chooseOpener(goos string, lookPath func(string) (string, error)) (string, error) {
	if goos == "darwin" {
		return "open", nil
	}
	for _, c := range linuxOpeners {
		if _, err := lookPath(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no URL opener found (tried %v)", linuxOpeners)
}

// openDetached starts the opener and reaps it in the background. A failure to
// start surfaces to the caller; the opener's own exit status does not — by
// then the TUI has moved on and the browser owns the outcome.
func openDetached(target string) error {
	opener, err := chooseOpener(runtime.GOOS, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(opener, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestChooseOpener -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add app/open.go app/open_test.go && git commit -m "feat: detached browser opener with per-OS detection"
```

---

### Task 6: key binding, menu state, help row

**Files:**
- Modify: `keys/keys.go` (enum ~line 81, string map ~line 129, bindings ~line 276)
- Modify: `ui/menu.go` (MenuState consts ~line 36, `String()` switch ~line 210)
- Modify: `app/help.go` (Handoff section, after the `y` row ~line 83)
- Test: `ui/menu_hints_test.go`

- [ ] **Step 1: Write the failing menu test**

```go
// ui/menu_hints_test.go
package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// While hint mode is up the bar must teach its three gestures; it is the only
// in-frame documentation the mode has.
func TestMenu_StateHintsLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(120, 1)
	m.SetState(StateHints)
	out := m.String()
	assert.Contains(t, out, "copy")
	assert.Contains(t, out, "copy + open")
	assert.Contains(t, out, "cancel")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/ -run TestMenu_StateHintsLine -v`
Expected: FAIL (`StateHints` undefined)

- [ ] **Step 3: Add the key binding**

In `keys/keys.go`, append to the `KeyName` const block (after `KeyAttachToggle`):

```go
	// KeyHints enters hint (fingers) mode: overlay copy/open hint labels on
	// the preview pane's visible matches (URLs, paths, SHAs, …).
	KeyHints
```

Add to `GlobalKeyStringsMap`:

```go
	"f":          KeyHints,
```

Add to `GlobalkeyBindings`:

```go
	KeyHints: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "copy/open from screen"),
	),
```

- [ ] **Step 4: Add the menu state**

In `ui/menu.go`, append to the `MenuState` const block (after `StateGeneratingName`):

```go
	// StateHints is shown while hint (fingers) mode overlays the preview:
	// the bar teaches the mode's three gestures instead of the usual options.
	StateHints
```

Add a case to the `switch m.state` in `String()` (before `case StateEmpty:`):

```go
	case StateHints:
		line = keyStyle().Render("a–z") + " " + descStyle().Render("copy") +
			sepStyle().Render(separator) +
			keyStyle().Render("A–Z") + " " + descStyle().Render("copy + open") +
			sepStyle().Render(separator) +
			keyStyle().Render("esc") + " " + descStyle().Render("cancel")
```

- [ ] **Step 5: Add the help row**

In `app/help.go`, in the Handoff section after `helpRow("y", "copy branch name to clipboard"),`:

```go
		helpRow("f", "copy/open URLs & paths from the preview"),
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/ ./keys/ ./app/ -run 'TestMenu_StateHintsLine|.' 2>&1 | tail -20`
Expected: `ok` for all three packages (full package runs — the new map entry must not break existing keys tests)

- [ ] **Step 7: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add keys/keys.go ui/menu.go ui/menu_hints_test.go app/help.go && \
  git commit -m "feat: f key binding, hint-mode menu bar, help row"
```

---

### Task 7: PreviewPane hint overlay + TabbedWindow plumbing

**Files:**
- Modify: `ui/preview.go` (struct ~line 53, `UpdateContent` ~line 106, `String` ~line 186)
- Modify: `ui/tabbed_window.go` (delegations, `paneScrolling` ~line 300)
- Test: `ui/preview_hints_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// ui/preview_hints_test.go
package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHintTestInstance(t *testing.T) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "hints", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	return inst
}

// The overlay freezes the pane: String renders the decorated content and
// UpdateContent for the same instance becomes a no-op, so the 100ms poll
// can't repaint over the hints.
func TestPreviewHintOverlay_FreezesAndRenders(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)
	inst := newHintTestInstance(t)

	p.SetHintOverlay(inst, "DECORATED CONTENT")
	assert.True(t, p.InHintMode())
	assert.Contains(t, p.String(), "DECORATED CONTENT")

	require.NoError(t, p.UpdateContent(inst))
	assert.True(t, p.InHintMode(), "same-instance tick must not drop the overlay")
	assert.Contains(t, p.String(), "DECORATED CONTENT")
}

// The overlay belongs to one instance: rendering any other instance drops it
// (the scroll-snapshot ownership rule — a frozen mode must never outlive its
// trigger conditions).
func TestPreviewHintOverlay_DroppedOnInstanceChange(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)
	inst := newHintTestInstance(t)

	p.SetHintOverlay(inst, "DECORATED")
	require.NoError(t, p.UpdateContent(nil))
	assert.False(t, p.InHintMode())
}

// LiveContent gates hint-mode entry: only a live, non-fallback, non-scrolling
// pane with text qualifies.
func TestPreviewLiveContent(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)

	_, ok := p.LiveContent()
	assert.False(t, ok, "empty pane has nothing to hint")

	p.previewState = previewState{fallback: false, text: "some output"}
	got, ok := p.LiveContent()
	assert.True(t, ok)
	assert.Equal(t, "some output", got)

	p.previewState = previewState{fallback: true, text: "splash"}
	_, ok = p.LiveContent()
	assert.False(t, ok, "fallback splash is not hintable")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/ -run 'TestPreviewHint|TestPreviewLiveContent' -v`
Expected: FAIL (`SetHintOverlay` etc. undefined)

- [ ] **Step 3: Add the pane fields and methods**

In `ui/preview.go`, add to the `PreviewPane` struct after the `viewport` field:

```go
	// hintContent, when non-empty, is hint mode's decorated rendering of
	// hintInstance's frozen capture; String() shows it instead of the live
	// text and UpdateContent freezes, mirroring the scroll snapshot's
	// ownership rules (one owning instance, dropped the moment any other
	// instance — or the owner once paused — is rendered).
	hintContent  string
	hintInstance *session.Instance
```

Add methods (after `ResetToNormalMode`):

```go
// LiveContent returns the text the pane is currently rendering live, and
// whether hint mode may act on it: no fallback splash, not scrolling, not
// already in hint mode, and non-empty.
func (p *PreviewPane) LiveContent() (string, bool) {
	if p.previewState.fallback || p.isScrolling || p.hintContent != "" {
		return "", false
	}
	return p.previewState.text, p.previewState.text != ""
}

// SetHintOverlay enters (or refreshes) hint mode: content is the decorated
// frame shown frozen in place of instance's live capture.
func (p *PreviewPane) SetHintOverlay(instance *session.Instance, content string) {
	p.hintInstance = instance
	p.hintContent = content
}

// ClearHintOverlay leaves hint mode; the next UpdateContent tick resumes the
// live view.
func (p *PreviewPane) ClearHintOverlay() {
	p.hintInstance = nil
	p.hintContent = ""
}

// InHintMode reports whether a hint overlay is currently displayed.
func (p *PreviewPane) InHintMode() bool { return p.hintContent != "" }
```

In `UpdateContent`, directly after the scroll-snapshot identity check
(the `if p.isScrolling && …` block at ~line 113), add:

```go
	// The hint overlay belongs to one live instance, exactly like the scroll
	// snapshot above: rendering any other instance, or the owner once paused,
	// drops it. While it is valid the pane is frozen, so the per-tick capture
	// cannot repaint over the hints.
	if p.InHintMode() {
		if instance != p.hintInstance || instance.Paused() {
			p.ClearHintOverlay()
		} else {
			return nil
		}
	}
```

In `String()`, after the fallback branch (`if p.previewState.fallback { … }`)
and before the scroll branch, add:

```go
	// Hint mode: show the frozen decorated frame, clamped exactly like the
	// live view so the layout cannot shift on entry.
	if p.hintContent != "" {
		return previewPaneStyle().MaxWidth(p.width).MaxHeight(p.height).Render(p.hintContent)
	}
```

- [ ] **Step 4: Add TabbedWindow delegations**

In `ui/tabbed_window.go`, after `ResetPreviewToNormalMode`:

```go
// PreviewLiveContent exposes the preview pane's live text for hint mode.
func (w *TabbedWindow) PreviewLiveContent() (string, bool) {
	return w.preview.LiveContent()
}

// SetPreviewHintOverlay shows a frozen hint-decorated frame over instance's
// live preview; ClearPreviewHintOverlay resumes the live view.
func (w *TabbedWindow) SetPreviewHintOverlay(instance *session.Instance, content string) {
	w.preview.SetHintOverlay(instance, content)
}

// ClearPreviewHintOverlay exits hint mode on the preview pane.
func (w *TabbedWindow) ClearPreviewHintOverlay() { w.preview.ClearHintOverlay() }

// InPreviewHintMode reports whether the preview pane shows a hint overlay.
func (w *TabbedWindow) InPreviewHintMode() bool { return w.preview.InHintMode() }
```

Update `paneScrolling` so hint mode lights the pane chrome (it captures
keyboard input, the same reason scroll mode does):

```go
// paneScrolling reports whether any tab pane is in a key-capturing mode
// (scroll or hint) — the state that renders the window's chrome as focused.
// The diff tab scrolls live without a mode, so it never claims focus.
func (w *TabbedWindow) paneScrolling() bool {
	return w.preview.isScrolling || w.preview.InHintMode() || w.terminal.IsScrolling()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/ -v -run 'TestPreviewHint|TestPreviewLiveContent'`
Expected: PASS. Then run the whole package: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./ui/` — expected `ok` (no regressions).

- [ ] **Step 6: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add ui/preview.go ui/preview_hints_test.go ui/tabbed_window.go && \
  git commit -m "feat(ui): hint overlay mode on the preview pane"
```

---

### Task 8: home wiring — stateHints, enter/exit, key dispatch, actions

**Files:**
- Create: `app/app_hints.go`
- Modify: `app/app.go` (state enum ~line 114, home struct ~line 220)
- Modify: `app/app_update.go` (Update cases ~lines 39 & 241; handleKeyPress ~line 592; key switch ~line 631)
- Test: `app/hints_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// app/hints_test.go
package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpener stands in for the browser opener, mirroring fakeClipboard.
type fakeOpener struct {
	called bool
	target string
}

func withFakeOpener(t *testing.T, retErr error) *fakeOpener {
	t.Helper()
	orig := openInBrowser
	t.Cleanup(func() { openInBrowser = orig })
	fo := &fakeOpener{}
	openInBrowser = func(s string) error {
		fo.called = true
		fo.target = s
		return retErr
	}
	return fo
}

// newHintsHome builds a minimal home with a tabbed window (hint mode renders
// into the preview pane) and the given instances, first one selected.
func newHintsHome(t *testing.T, instances ...*session.Instance) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	for _, inst := range instances {
		l.AddInstance(inst)
	}
	return &home{
		ctx:            context.Background(),
		state:          stateDefault,
		list:           l,
		menu:           ui.NewMenu(),
		errBox:         ui.NewErrBox(),
		appConfig:      config.DefaultConfig(),
		appState:       config.LoadState(),
		tabbedWindow:   ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		welcomeChecked: true,
	}
}

func pressRunes(h *home, s string) tea.Cmd {
	var last tea.Cmd
	for _, r := range s {
		_, last = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return last
}

// f with a live selection but an empty preview explains itself instead of
// silently doing nothing.
func TestHints_NothingToHintExplains(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	fc := withFakeClipboard(t, nil)

	pressRunes(h, "f")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, fc.called)
	require.True(t, h.menu.HasNotice())
}

// Content with no pattern matches: stay in default state with an explanation.
func TestHints_NoMatchesExplains(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()

	_, _ = h.startHints(inst, "thinking about words\nnothing actionable")

	assert.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice())
}

// The core flow: one match -> label "a" -> pressing a copies it and returns
// to default with an acknowledgment. The opener must NOT run for lowercase.
func TestHints_LowercaseCopies(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "PR: https://github.com/x/y/pull/9\n")
	require.Equal(t, stateHints, h.state)
	require.True(t, h.tabbedWindow.InPreviewHintMode())

	pressRunes(h, "a")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
	require.True(t, fc.called)
	assert.Equal(t, "https://github.com/x/y/pull/9", fc.value)
	assert.False(t, fo.called, "lowercase must not open")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "copied")
}

// Uppercase = copy + open for URLs.
func TestHints_UppercaseOpensURL(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "PR: https://github.com/x/y/pull/9\n")
	pressRunes(h, "A")

	require.True(t, fc.called)
	require.True(t, fo.called)
	assert.Equal(t, "https://github.com/x/y/pull/9", fo.target)
}

// Uppercase on a non-URL degrades to plain copy (v1).
func TestHints_UppercaseNonURLJustCopies(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)
	fo := withFakeOpener(t, nil)

	_, _ = h.startHints(inst, "edit /tmp/notes.md:12 please\n")
	pressRunes(h, "A")

	require.True(t, fc.called)
	assert.Equal(t, "/tmp/notes.md:12", fc.value)
	assert.False(t, fo.called)
}

// esc cancels: no copy, overlay gone, back to default.
func TestHints_EscCancels(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	require.Equal(t, stateHints, h.state)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
	assert.False(t, fc.called)
}

// A key outside the hint alphabet (or with no matching label) exits without
// acting — any non-hint key is an exit, per the spec.
func TestHints_InvalidKeyExits(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	pressRunes(h, "5")

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, fc.called)
}

// Two-character labels: the first key narrows (mode stays up), the second
// acts. 27 distinct matches force expansion past single chars.
func TestHints_TwoCharNarrowing(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	fc := withFakeClipboard(t, nil)

	var lines []string
	for i := 0; i < 27; i++ {
		lines = append(lines, fmt.Sprintf("abcdef%02x", i))
	}
	_, _ = h.startHints(inst, strings.Join(lines, "\n"))
	require.Equal(t, stateHints, h.state)

	pressRunes(h, "n")
	assert.Equal(t, stateHints, h.state, "valid prefix keeps the mode up")
	assert.False(t, fc.called)

	pressRunes(h, "a")
	assert.Equal(t, stateDefault, h.state)
	require.True(t, fc.called)
	assert.Equal(t, "abcdef01", fc.value,
		"label na = 26th from the bottom = row 1")
}

// A resize invalidates the frozen geometry: exit hint mode.
func TestHints_ResizeExits(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()

	_, _ = h.startHints(inst, "see /tmp/x.go\n")
	require.Equal(t, stateHints, h.state)

	_, _ = h.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	assert.Equal(t, stateDefault, h.state)
	assert.False(t, h.tabbedWindow.InPreviewHintMode())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestHints_ -v`
Expected: FAIL to compile (`startHints`, `stateHints` undefined)

- [ ] **Step 3: Add state and fields**

In `app/app.go`, append to the `state` const block (after `stateSettings`):

```go
	// stateHints is the state when hint (fingers) mode overlays the preview
	// pane with copy/open labels; every key routes to hint selection.
	stateHints
```

Add to the `home` struct (after `generatingName`):

```go
	// hintScreen is the frozen, labeled capture hint mode is acting on.
	// hintTyped is the entered label prefix, and hintOpenVariant records
	// whether any hint character was typed uppercase (selecting copy+open).
	hintScreen      *hints.Screen
	hintTyped       string
	hintOpenVariant bool
```

Add `"github.com/ZviBaratz/atrium/hints"` to `app/app.go`'s imports.

- [ ] **Step 4: Write the hint-mode logic**

```go
// app/app_hints.go
package app

// Hint (fingers) mode: freeze the preview, label its URLs/paths/SHAs with
// short hints, and let one keystroke copy (or copy+open) a match. The hints
// package owns matching/labels/rendering; this file owns the mode's state
// machine and actions. See docs/superpowers/specs/2026-06-10-hints-copy-open-design.md.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ZviBaratz/atrium/hints"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
)

// hintStyles builds the renderer's three roles from the active theme: dim
// backdrop, success-colored match text, and the label in reverse-video
// attention color so it pops over the match's first cells.
func hintStyles() hints.Styles {
	t := theme.Current()
	return hints.Styles{
		Backdrop: t.DimStyle(),
		Match:    t.SuccessStyle(),
		Label:    t.AttentionStyle().Reverse(true).Bold(true),
	}
}

// enterHintMode validates the f keypress and enters hint mode over the
// selected session's live preview. Guards explain themselves via notices
// instead of silently swallowing the key.
func (m *home) enterHintMode() (tea.Model, tea.Cmd) {
	if !m.tabbedWindow.IsInPreviewTab() {
		return m, m.handleInfoNotice("hints work in the preview tab")
	}
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Paused() {
		return m, m.handleInfoNotice("session is paused — press r to resume")
	}
	content, ok := m.tabbedWindow.PreviewLiveContent()
	if !ok {
		return m, m.handleInfoNotice("nothing to copy from yet")
	}
	return m.startHints(selected, content)
}

// startHints enters hint mode over content, the frozen capture of selected's
// pane. Split from enterHintMode so tests can inject pane content directly.
func (m *home) startHints(selected *session.Instance, content string) (tea.Model, tea.Cmd) {
	width, height := m.tabbedWindow.GetPreviewSize()
	// height-1 mirrors the live preview's reserved ellipsis row, so hints land
	// on exactly the rows the user is looking at (ui.PreviewPane.String).
	screen := hints.NewScreen(content, width, height-1)
	if screen.MatchCount() == 0 {
		return m, m.handleInfoNotice("no copyable matches on screen")
	}
	m.hintScreen = screen
	m.hintTyped = ""
	m.hintOpenVariant = false
	m.state = stateHints
	m.tabbedWindow.SetPreviewHintOverlay(selected, screen.Render("", hintStyles()))
	m.menu.SetState(ui.StateHints)
	m.recomputeLayout() // the hint bar may claim a row, like stateFilter
	return m, nil
}

// exitHintMode returns to the default state and the live preview.
func (m *home) exitHintMode() {
	m.state = stateDefault
	m.hintScreen = nil
	m.hintTyped = ""
	m.hintOpenVariant = false
	m.tabbedWindow.ClearPreviewHintOverlay()
	m.menu.SetState(ui.StateDefault)
	m.recomputeLayout()
}

// handleHintsState consumes every key while hint mode is up: hint characters
// narrow toward a match, anything else exits. An uppercase hint character
// selects the copy+open variant.
func (m *home) handleHintsState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	r := msg.Runes[0]
	lower := unicode.ToLower(r)
	if !strings.ContainsRune(hints.Alphabet, lower) {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	typed := m.hintTyped + string(lower)
	match, valid := m.hintScreen.Resolve(typed)
	if !valid {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	if unicode.IsUpper(r) {
		m.hintOpenVariant = true
	}
	if match == nil {
		// A valid proper prefix: narrow the overlay, wait for the next key.
		m.hintTyped = typed
		m.tabbedWindow.SetPreviewHintOverlay(
			m.list.GetSelectedInstance(), m.hintScreen.Render(typed, hintStyles()))
		return m, nil
	}
	open := m.hintOpenVariant
	selected := *match
	m.exitHintMode()
	return m, tea.Batch(m.actHint(selected, open), m.instanceChanged())
}

// actHint copies the match and, on the open variant, opens URLs in the
// browser. Non-URL kinds degrade to plain copy in v1 (see the design doc).
func (m *home) actHint(match hints.Match, open bool) tea.Cmd {
	if err := copyToClipboard(match.Text); err != nil {
		return m.handleError(fmt.Errorf("copy hint: %w", err))
	}
	if open && match.Kind == hints.KindURL {
		if err := openInBrowser(match.Text); err != nil {
			return m.handleError(fmt.Errorf("open url: %w", err))
		}
		return m.handleInfoNotice(fmt.Sprintf("copied + opened %s", truncateForNotice(match.Text)))
	}
	return m.handleInfoNotice(fmt.Sprintf("'%s' copied", truncateForNotice(match.Text)))
}

// truncateForNotice keeps toasts one line short; the menu row truncates too,
// but an early cut keeps the "copied" suffix visible.
func truncateForNotice(s string) string {
	const max = 40
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
```

- [ ] **Step 5: Wire the dispatch in `app/app_update.go`**

(a) In `handleKeyPress`, after the `stateFilter` block (which ends ~line 592)
and **before** the `if msg.Type == tea.KeyEsc` block, add:

```go
	// Hint (fingers) mode: every key is either a hint character or an exit.
	// Must run before the global esc/quit handling below so hint letters like
	// q never quit the app.
	if m.state == stateHints {
		return m.handleHintsState(msg)
	}
```

(b) In the key-name `switch`, add a case (next to `case keys.KeyCopyBranch:`):

```go
	case keys.KeyHints:
		// Freeze the preview and overlay copy/open hints on its matches.
		return m.enterHintMode()
```

(c) In `Update`, extend the `tea.WindowSizeMsg` case (~line 241):

```go
	case tea.WindowSizeMsg:
		// A resize invalidates hint mode's frozen geometry; exit rather than
		// redraw stale coordinates (cheap and correct — scroll-mode pragmatism).
		if m.state == stateHints {
			m.exitHintMode()
		}
		m.updateHandleWindowSizeEvent(msg)
```

(d) In `Update`, extend the `previewTickMsg` case (~line 39) before
`m.markSeenAfterDwell(...)`:

```go
	case previewTickMsg:
		// The pane owns hint-overlay validity (a selection change or pause
		// drops it there); if it dropped, follow it back to default so keys
		// stop being captured for a vanished overlay.
		if m.state == stateHints && !m.tabbedWindow.InPreviewHintMode() {
			m.exitHintMode()
		}
		m.markSeenAfterDwell(time.Now())
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestHints_ -v`
Expected: PASS (9 tests). Then the whole package: `/home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/` — expected `ok`.

- [ ] **Step 7: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/latest/bin:$HOME/go/bin:$PATH" \
  git add app/ && git commit -m "feat: fingers mode — f overlays copy/open hints on the preview"
```

---

### Task 9: full verification + manual smoke test

**Files:** none (verification only)

- [ ] **Step 1: Full test suite, race, lint, build**

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test-race
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just lint
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
```

Expected: all pass; `./bin/atrium` produced. (Known flake: `TestWheelOverTabbedWindowDoesNotMoveSelection` fails ~10% under `-shuffle` on main — rerun before treating as a regression.)

- [ ] **Step 2: Manual smoke test (run by the user or via a scratch session)**

1. `./bin/atrium` with at least one running session whose pane shows a URL and a file path.
2. Press `f` → backdrop dims, hints appear, menu bar shows `a–z copy · A–Z copy + open · esc cancel`, pane border lights accent.
3. Press a lowercase hint → toast `'…' copied`, clipboard holds the match, live view resumes.
4. `f` again, press the UPPERCASE hint of a URL → browser opens it, toast `copied + opened …`.
5. `f`, then `esc` → mode exits, nothing copied.
6. `f` on the Diff tab → notice "hints work in the preview tab".
7. While in hint mode, resize the terminal → mode exits cleanly.

- [ ] **Step 3: Final commit (if any fixups) and report**

Report results honestly: paste failing output if anything fails. Done means
`just build` + `just test` green and the smoke list verified.
