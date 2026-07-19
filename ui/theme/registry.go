package theme

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// DefaultThemeName is used when the configured theme is empty or unknown.
const DefaultThemeName = "tokyo-night"

// Nerd-Font codepoints, expressed numerically so the source stays ASCII-clean.
// All are private-use-area glyphs that render at width 1 in a Nerd-Font
// terminal and measure width 1 under go-runewidth.
const (
	nfBranch = 0xe0a0 // nf-pl-branch
	nfPencil = 0xf040 // nf-fa-pencil
	nfBolt   = 0xf0e7 // nf-fa-bolt
	nfPR     = 0xf407 // nf-oct-git_pull_request
	nfNote   = 0xf249 // nf-fa-sticky_note
)

// miniDotFrames are the Braille spinner frames (each width 1). They render on a
// patched Nerd Font and most modern monospaces but tofu under stock DejaVu, so they
// drive the nerd rung only; the plain rung uses blockSpinnerFrames.
var miniDotFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// blockSpinnerFrames are a btop-style pulsing bar drawn from the Unicode Block
// Elements — each width 1 and present in stock DejaVu and virtually every
// monospace. This is the plain rung's spinner, so the default install never tofus
// the busy marker (the Braille frames it replaces did, on a bare font).
var blockSpinnerFrames = []string{"▁", "▃", "▄", "▅", "▆", "▇", "▆", "▅", "▃"}

// Glyph-fidelity rungs, highest to lowest. The set descends from vendor Nerd-Font
// icons (need a patched font), through plain non-PUA Unicode (the safe default),
// to a 7-bit-ASCII floor that renders on literally any terminal/font/locale — the
// answer to the plain set still showing tofu under stock monospaces (⧗, ⎇, ⇄; the
// spinner is handled separately — the plain rung uses block bars, not Braille).
// These strings are the on-disk config vocabulary too: they match config.GlyphSet*
// verbatim, so app can pass config.GetGlyphSet() straight to SetGlyphSet.
const (
	GlyphSetNerd  = "nerd"
	GlyphSetPlain = "plain"
	GlyphSetASCII = "ascii"
)

// plainGlyphs is the safe default glyph set: every icon is non-PUA Unicode that
// measures width 1 (or empty for AutoBadge) and needs no patched Nerd Font. The
// spinner is block bars (blockSpinnerFrames) so the busy marker never tofus on a
// stock monospace; a few status marks (⧗ ⎇ ⇄) can still tofu on a sparse font,
// which is what the ascii rung is the floor for.
func plainGlyphs() Glyphs {
	return Glyphs{
		SpinnerFrames: blockSpinnerFrames,
		SpinnerFPS:    time.Second / 10, // matches the 100ms preview repaint tick so frames never lag a paint
		Ready:         "●",
		ReadySeen:     "○",
		Waiting:       "◆",
		Pending:       "⧗", // still hourglass: pending autonomous work (#290) — a process glyph, not a disk, so it reads as "still churning," not a read-state
		Paused:        "‖",
		Branch:        "⎇",
		Ahead:         "⇡",
		Warn:          "⚠",
		Behind:        "⇣",
		Dirty:         "*",
		Note:          "✎",
		Queued:        "↦", // plain-unicode "queued to send" marker
		PR:            "⇄", // plain-unicode pull-request marker
		AutoBadge:     "",  // text-only "AUTO" chip
		FoldOpen:      "▾",
		FoldClosed:    "▸",
		SelectionMark: "▎",
		MarkChecked:   "✓",
		DiffAdd:       "+",
		DiffDel:       "-",
		TextCursor:    "▌",
	}
}

// nerdGlyphs is plainGlyphs with the five vendor icons overlaid from the Nerd-Font
// private-use area, plus the Braille spinner: a patched font renders it cleanly, so
// the nerd rung keeps the finer motion the plain rung trades away for coverage.
// These render only on a patched Nerd Font, so this set is chosen solely when the
// nerd-font preference is on (see current.go). Everything else stays shared with
// plainGlyphs so the sets can never drift apart.
func nerdGlyphs() Glyphs {
	g := plainGlyphs()
	g.SpinnerFrames = miniDotFrames
	g.Branch = string(rune(nfBranch))
	g.Dirty = string(rune(nfPencil))
	g.Note = string(rune(nfNote))
	g.PR = string(rune(nfPR))
	g.AutoBadge = string(rune(nfBolt))
	return g
}

// asciiGlyphs is the bottom fidelity rung: every icon is a 7-bit ASCII character
// that renders identically on any terminal, font, or locale — the floor for
// environments where even the plain set (⧗, ⎇, ⇄, and any non-ASCII spinner) shows
// tofu. It is built from plainGlyphs so a glyph added later inherits a Unicode
// default here until it is given an explicit ASCII form.
//
// The values are the #378 "Set A" (iconic) choice: symbolic marks picked to stay
// distinct within each render context (status gutter / git line / badges never
// reuse a glyph ambiguously). Every value is width 1 (guarded by TestGlyphWidths).
func asciiGlyphs() Glyphs {
	g := plainGlyphs()
	g.SpinnerFrames = []string{"|", "/", "-", "\\"}
	g.Ready = "*"
	g.ReadySeen = "o"
	g.Waiting = "?"
	g.Pending = "~"
	g.Paused = "="
	g.Branch = "Y"
	g.Ahead = "^"
	g.Warn = "!"
	g.Behind = "v"
	g.Dirty = "%"
	g.Note = "#"
	g.Queued = ">"
	g.PR = "&"
	g.FoldOpen = "v"
	g.FoldClosed = ">"
	g.SelectionMark = "|"
	g.MarkChecked = "x"
	g.TextCursor = "_"
	// AutoBadge, DiffAdd, DiffDel are already ASCII/empty in plainGlyphs — inherited.
	return g
}

// glyphsFor returns the glyph set for a given fidelity rung. An unrecognized rung
// resolves to the plain set — the safe default that never renders tofu.
func glyphsFor(set string) Glyphs {
	switch set {
	case GlyphSetNerd:
		return nerdGlyphs()
	case GlyphSetASCII:
		return asciiGlyphs()
	default:
		return plainGlyphs()
	}
}

var tokyoNight = &Theme{
	Name: "tokyo-night",
	Palette: Palette{
		Bg:          lipgloss.Color("#1a1b26"),
		BgElevated:  lipgloss.Color("#24283b"),
		BarBg:       lipgloss.Color("#414868"),
		Fg:          lipgloss.Color("#c0caf5"),
		FgDim:       lipgloss.Color("#565f89"),
		FgFaint:     lipgloss.Color("#414868"),
		Accent:      lipgloss.Color("#7aa2f7"),
		AccentMuted: lipgloss.Color("#3d59a1"),
		Purple:      lipgloss.Color("#bb9af7"),
		Success:     lipgloss.Color("#9ece6a"),
		SuccessDim:  lipgloss.Color("#6a8a4a"),
		Working:     lipgloss.Color("#565f89"), // matches FgDim: working rows recede
		Pending:     lipgloss.Color("#7dcfff"), // calm cyan: pending autonomous work, distinct from Working/Success/Attention
		Attention:   lipgloss.Color("#e0af68"),
		Danger:      lipgloss.Color("#f7768e"),
		Cyan:        lipgloss.Color("#7dcfff"),
		BadgeBg:     lipgloss.Color("#bb9af7"),
		BadgeFg:     lipgloss.Color("#1a1b26"),
	},
	Glyphs:  plainGlyphs(),
	Borders: Borders{Style: lipgloss.RoundedBorder()},
}

var catppuccinMocha = &Theme{
	Name: "catppuccin-mocha",
	Palette: Palette{
		Bg:          lipgloss.Color("#1e1e2e"),
		BgElevated:  lipgloss.Color("#313244"),
		BarBg:       lipgloss.Color("#45475a"),
		Fg:          lipgloss.Color("#cdd6f4"),
		FgDim:       lipgloss.Color("#6c7086"),
		FgFaint:     lipgloss.Color("#45475a"),
		Accent:      lipgloss.Color("#89b4fa"),
		AccentMuted: lipgloss.Color("#74c7ec"),
		Purple:      lipgloss.Color("#cba6f7"),
		Success:     lipgloss.Color("#a6e3a1"),
		SuccessDim:  lipgloss.Color("#6c9168"),
		Working:     lipgloss.Color("#6c7086"), // matches FgDim: working rows recede
		Pending:     lipgloss.Color("#89dceb"), // calm cyan: pending autonomous work, distinct from Working/Success/Attention
		Attention:   lipgloss.Color("#f9e2af"),
		Danger:      lipgloss.Color("#f38ba8"),
		Cyan:        lipgloss.Color("#89dceb"),
		BadgeBg:     lipgloss.Color("#cba6f7"),
		BadgeFg:     lipgloss.Color("#1e1e2e"),
	},
	Glyphs:  plainGlyphs(),
	Borders: Borders{Style: lipgloss.RoundedBorder()},
}

// unicodeFallback reuses the Tokyo Night palette (colors are fine without a
// patched font) but uses square borders. Its glyphs are the plain set; since the
// colored themes now also default to plainGlyphs(), this theme's distinction is the
// square border. It stays registered for back-compat with configs set to "unicode".
var unicodeFallback = &Theme{
	Name:    "unicode",
	Palette: tokyoNight.Palette,
	Glyphs:  plainGlyphs(),
	Borders: Borders{Style: lipgloss.NormalBorder()},
}

// registry maps theme names to themes. Adding a theme is one var + one entry.
var registry = map[string]*Theme{
	"tokyo-night":      tokyoNight,
	"catppuccin-mocha": catppuccinMocha,
	"unicode":          unicodeFallback,
}

// Get resolves a theme name (case/space-insensitive), falling back to the
// default for empty or unknown names. It never returns nil.
func Get(name string) *Theme {
	if t, ok := registry[strings.ToLower(strings.TrimSpace(name))]; ok {
		return t
	}
	return registry[DefaultThemeName]
}

// Names returns the registered theme names (unordered); useful for docs/help.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
