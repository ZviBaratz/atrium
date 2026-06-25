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
	nfPause  = 0xf04c // nf-fa-pause
	nfBranch = 0xe0a0 // nf-pl-branch
	nfPencil = 0xf040 // nf-fa-pencil
	nfBolt   = 0xf0e7 // nf-fa-bolt
	nfPR     = 0xf407 // nf-oct-git_pull_request
	nfNote   = 0xf249 // nf-fa-sticky_note
)

// miniDotFrames are the Braille spinner frames (each width 1, widely supported).
var miniDotFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// nfGlyphs is the Nerd-Font glyph set shared by the colored themes.
func nfGlyphs() Glyphs {
	return Glyphs{
		SpinnerFrames: miniDotFrames,
		SpinnerFPS:    time.Second / 10, // matches the 100ms preview repaint tick so frames never lag a paint
		Ready:         "●",
		ReadySeen:     "○",
		Waiting:       "◆",
		Paused:        string(rune(nfPause)),
		Branch:        string(rune(nfBranch)),
		Ahead:         "⇡",
		Warn:          "⚠",
		Behind:        "⇣",
		Dirty:         string(rune(nfPencil)),
		Note:          string(rune(nfNote)),
		PR:            string(rune(nfPR)),
		AutoBadge:     string(rune(nfBolt)),
		FoldOpen:      "▾",
		FoldClosed:    "▸",
		SelectionMark: "▎",
		MarkChecked:   "✓",
		DiffAdd:       "+",
		DiffDel:       "-",
		TextCursor:    "▌",
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
		Attention:   lipgloss.Color("#e0af68"),
		Danger:      lipgloss.Color("#f7768e"),
		Cyan:        lipgloss.Color("#7dcfff"),
		BadgeBg:     lipgloss.Color("#bb9af7"),
		BadgeFg:     lipgloss.Color("#1a1b26"),
	},
	Glyphs:  nfGlyphs(),
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
		Attention:   lipgloss.Color("#f9e2af"),
		Danger:      lipgloss.Color("#f38ba8"),
		Cyan:        lipgloss.Color("#89dceb"),
		BadgeBg:     lipgloss.Color("#cba6f7"),
		BadgeFg:     lipgloss.Color("#1e1e2e"),
	},
	Glyphs:  nfGlyphs(),
	Borders: Borders{Style: lipgloss.RoundedBorder()},
}

// unicodeFallback reuses the Tokyo Night palette (colors are fine without a
// patched font) but avoids Nerd-Font / wide glyphs and uses square borders.
var unicodeFallback = &Theme{
	Name:    "unicode",
	Palette: tokyoNight.Palette,
	Glyphs: Glyphs{
		SpinnerFrames: miniDotFrames,
		SpinnerFPS:    time.Second / 10, // matches the 100ms preview repaint tick so frames never lag a paint
		Ready:         "●",
		ReadySeen:     "○",
		Waiting:       "◆",
		Paused:        "‖",
		Branch:        "⎇",
		Ahead:         "⇡",
		Warn:          "⚠",
		Behind:        "⇣",
		Dirty:         "*",
		Note:          "✎",
		PR:            "⇄", // plain-unicode pull-request marker
		AutoBadge:     "",  // text-only "AUTO" chip
		FoldOpen:      "▾",
		FoldClosed:    "▸",
		SelectionMark: "▎",
		MarkChecked:   "✓",
		DiffAdd:       "+",
		DiffDel:       "-",
		TextCursor:    "▌",
	},
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
