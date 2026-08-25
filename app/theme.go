package app

import (
	"fmt"

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
// Idempotent, and deliberately so — it runs twice per launch and again each time the
// settings panel OPENS. Each run re-reads the themes directory, which is one os.ReadDir
// of a directory that is empty on almost every install, and is what makes editing a theme
// file take effect without a restart and without an fs-watcher (declined by the program
// design).
//
// Opening the panel is the event rather than saving the row, and the difference is not a
// detail. The picker's options ARE theme.SelectableNames(), read live from this registry,
// so a file written after launch is missing from the list a save would have to choose
// from: cycling could not reach it, and the first keypress would select and persist some
// other palette on the way past. Reloading when the panel opens is what makes the row's
// vocabulary describe the directory the user just edited. It also takes the work off the
// keypress — left/right cycling calls applySettingChange on EVERY press, which would put
// an os.ReadDir plus a decode per file on the update loop for as long as an arrow key is
// held.
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
	// The failure with no file behind it, and so the one every other surface is silent
	// about by construction. A refusal needs a file that was READ and rejected; a theme
	// whose file was deleted, renamed, or never arrived on this machine produces no
	// refusal, no log line and no picker entry — just the default palette, permanently,
	// with nothing said. For a light-terminal user configured onto a light theme that
	// also means silently losing polarity.
	//
	// PREPENDED, because the modal shows the first five and this is the only entry that
	// describes what the user is looking at rather than a file they may not care about
	// today. It overlaps the refusal when the configured theme is itself the broken file —
	// two lines for one cause — and that is the accepted cost of neither entry having to
	// parse the other's message to find out.
	if name := cfg.GetTheme(); !theme.IsRegistered(name) {
		problems = append([]error{fmt.Errorf("theme %q is configured; no file loads under that name", name)}, problems...)
	}
	for _, p := range problems {
		log.WarningLog.Printf("user theme file ignored: %v", p)
	}
	return problems
}

// reloadUserThemes re-reads the themes directory so the settings picker's options
// describe what is on disk right now, and is called when that panel opens.
//
// It buffers only what this run has not already said. flushThemeProblems CLEARS the
// buffer as it opens the modal, so an unfiltered refill would put the same report in
// front of the user every time they touched any setting, with no way to stop it short of
// fixing or deleting the file — and a report that cannot be dismissed is one nobody
// reads. Keyed on the message, so a file broken a second way is news again.
//
// It APPENDS rather than assigning. A launch-time problem can still be waiting here: the
// buffer is only drained in stateDefault, and the settings overlay is not that, so an
// assignment would drop whatever the launch found on the way past.
// It reports whether the tmux BAND's colours moved, which is a question the caller has to
// ask because this call can repaint the whole UI. Re-reading the directory recomposes the
// active palette — the user edited the file their `theme` names, or deleted it and fell
// back — and the band is not part of this frame: it is a status-style baked into the
// managed conf and a server option on the live tmux server. Repainting the TUI and not
// the band leaves them on different palettes, which is #574's symptom arriving by a new
// road. Compared on the two hexes barStyleColours actually pushes rather than on the
// palette as a whole, so a change no session can see does not cost a conf rewrite.
func (m *home) reloadUserThemes() (bandChanged bool) {
	before := bandColours()
	if m.themeProblemsSeen == nil {
		m.themeProblemsSeen = map[string]bool{}
	}
	for _, p := range applyThemeSelection(m.appConfig) {
		if m.themeProblemsSeen[p.Error()] {
			continue
		}
		m.themeProblemsSeen[p.Error()] = true
		m.pendingThemeProblems = append(m.pendingThemeProblems, p)
	}
	return bandColours() != before
}

// bandColours is the pair session/tmux's barStyleColours resolves for the status band,
// read here only to tell whether it moved. It restates WHICH two tokens those are rather
// than calling into that package, because barStyleColours is unexported and everything
// exported beside it shells out to tmux — not something to do on the update loop merely
// to compare two strings.
//
// So it is a claim about another package, and what holds it is
// TestManagedConfCarriesTheConfiguredPalette (theme_launch_test.go), which renders the
// managed conf for a real palette and asserts the band reads
// "bg=<BarBg>,fg=<Fg>". Changing which tokens the band uses fails there; this function
// then has to follow, and TestReloadPushesTheBandWhenThePaletteMoves is what notices.
func bandColours() [2]string {
	th := theme.Current()
	return [2]string{theme.Hex(th.Palette.BarBg), theme.Hex(th.Palette.Fg)}
}
