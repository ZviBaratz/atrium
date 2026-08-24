package app

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/atrium/ui/theme/themefile"
)

// theme.go owns the one function that turns config.json into an active theme.
//
// It exists because the order matters and used to be wrong. tmux.Init renders the
// managed tmux config — including the in-session bar's band colours — from
// theme.Current(), and main.go called it BEFORE app.Run ever reached theme.Set. So
// every session started in a run opened with the default palette's band while the TUI
// itself rendered the palette the user chose (#574). Naming the whole sequence once,
// and calling it from main.go ahead of tmux.Init, is what makes the ordering a fact
// about one function rather than a coincidence about two call sites.

// ApplyThemeAtLaunch registers the user's theme files and then activates the
// configured palette, glyph set and detected scheme. It returns one error per theme
// file that was refused; the caller decides the surface.
//
// The three axes are set together because they compose into one published theme
// (ui/theme/current.go): setting the palette without the glyph set would publish a
// frame with the wrong spinner, and the scheme rung has to be in place before `auto`
// resolves or a light terminal gets the dark default for the first paint.
//
// User themes are loaded FIRST, because the configured name may be one of them: a
// theme.Set for a palette that is not registered yet falls back to the default and
// nothing later un-falls it.
//
// Idempotent, and deliberately so — it runs twice per launch (main.go before tmux.Init,
// then newHome) and again whenever the settings panel saves the theme row. Each run
// re-reads the themes directory, which is one os.ReadDir of a directory that is empty
// on almost every install, and is what makes editing a theme file take effect on save
// without an fs-watcher (declined by the program design).
func ApplyThemeAtLaunch(cfg *config.Config) []error {
	var problems []error
	dir, err := config.ThemesDir()
	if err != nil {
		problems = append(problems, err)
	} else {
		loaded, refused := themefile.Load(dir)
		theme.SetUserThemes(loaded)
		problems = append(problems, refused...)
	}
	theme.Set(cfg.GetTheme())
	theme.SetGlyphSet(cfg.GetGlyphSet())
	// The detection ladder's lower rung, read once, for terminals that will never
	// answer the OSC 11 query Init also sends. It sits here so the ladder's order is
	// visible in one place: Init's query outranks this if an answer arrives.
	theme.SetScheme(initialScheme())
	for _, p := range problems {
		log.WarningLog.Printf("user theme file ignored: %v", p)
	}
	return problems
}
