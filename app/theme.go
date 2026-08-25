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

// ApplyThemeAtLaunch is applyThemeSelection plus the scheme rung, and it is for LAUNCH
// only — the two processes that start with no scheme recorded at all (main.go ahead of
// tmux.Init, and newHome). It returns one error per theme file that was refused; the
// caller decides the surface.
//
// The scheme is what separates it from the save path, and the separation is the point.
// initialScheme() reads COLORFGBG, which answers SchemeUnknown on most terminals — that
// is why the OSC 11 ladder exists above it. Recording it is right exactly once, before
// any query has been answered; running it again LATER overwrites a detected polarity
// with "nobody replied", which is the value applyDetectedScheme refuses to act on for
// this reason. So the settings panel calls applyThemeSelection instead.
func ApplyThemeAtLaunch(cfg *config.Config) []error {
	problems := applyThemeSelection(cfg)
	// The detection ladder's lower rung, read once, for terminals that will never
	// answer the OSC 11 query Init also sends. It sits here so the ladder's order is
	// visible in one place: Init's query outranks this if an answer arrives.
	//
	// The restore func is discarded on purpose: this is startup, there is no previous
	// scheme to put back, and a test that needs one wraps its own SetScheme.
	theme.SetScheme(initialScheme())
	return problems
}

// applyThemeSelection registers the user's theme files and activates the configured
// palette and glyph set, leaving the scheme axis alone. It is the half that is safe to
// re-run at any moment, which is what the settings panel needs.
//
// The two axes it does set go together because they compose into one published theme
// (ui/theme/current.go): setting the palette without the glyph set would publish a frame
// with the wrong spinner.
//
// User themes are loaded FIRST, because the configured name may be one of them: a
// theme.Set for a palette that is not registered yet falls back to the default and
// nothing later un-falls it.
//
// Idempotent, and deliberately so — it runs twice per launch and again whenever the
// settings panel saves the theme or glyph_set row. Each run re-reads the themes
// directory, which is one os.ReadDir of a directory that is empty on almost every
// install, and is what makes editing a theme file take effect on save without an
// fs-watcher (declined by the program design).
//
// The cost of that idempotence is visible in one place: a refused file is logged once
// per run, so a launch writes each refusal to the log twice (main.go, then newHome).
// Left alone rather than deduplicated, because the alternative is process-global state
// remembering what it has already said, and the user-facing surfaces — the startup
// modal and `atrium doctor` — each read one call's return value and show it once.
func applyThemeSelection(cfg *config.Config) []error {
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
	for _, p := range problems {
		log.WarningLog.Printf("user theme file ignored: %v", p)
	}
	return problems
}
