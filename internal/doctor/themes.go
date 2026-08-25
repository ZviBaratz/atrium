package doctor

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/atrium/ui/theme/themefile"
)

// CheckThemes reports the user theme files the loader refuses (#813).
//
// A refused theme is dropped rather than repaired — the correct runtime behaviour,
// since a palette Atrium quietly darkened would no longer be the one its author
// chose — which leaves "why is my theme not in the list?" with no answer anywhere.
// This is that answer, and it runs the same loader the TUI does, so the two can never
// disagree about what is valid.
//
// It returns the loaded names and the directory as well, because the useful report is
// all three: a user whose file is absent from BOTH lists has it in the wrong directory,
// which no refusal can tell them, and only this report knows where the right one is.
//
// It takes the config for the same reason CheckRepoScripts and CheckKeybindings do — the
// interesting failures are joins between what is configured and what is present. Here
// that join is the one failure with no file behind it: `theme` naming a palette that no
// file loads under, which produces no refusal to print because nothing was read and
// rejected.
func CheckThemes(cfg *config.Config) (dir string, loaded []string, problems []error) {
	dir, err := config.ThemesDir()
	if err != nil {
		return "", nil, []error{err}
	}
	themes, problems := themefile.Load(dir)
	for name := range themes {
		loaded = append(loaded, name)
	}
	sort.Strings(loaded)

	// Against what LOADED here rather than theme.IsRegistered: `atrium doctor` reports on
	// the directory it just read, and the process-global registry is a different question
	// that a doctor run has no reason to have answered.
	if name := strings.ToLower(strings.TrimSpace(cfg.GetTheme())); name != theme.AutoThemeName && !theme.IsBuiltin(name) {
		if _, ok := themes[name]; !ok {
			problems = append(problems, fmt.Errorf("theme %q is configured; no file here loads under that name, so the UI falls back to %s", name, theme.DefaultThemeName))
		}
	}
	return dir, loaded, problems
}

// RenderThemes formats the user-theme report for `atrium doctor`, in the section shape
// RenderRepoScripts established.
//
// It prints even when nothing loaded and nothing was refused, unlike its siblings, and
// that empty case is the one it exists for: a file in the wrong directory — themes/
// misspelt, an XDG path assumed, a legacy ~/.claude-squad install — produces no theme
// and no refusal, so every OTHER surface is silent about it by construction. The
// directory line is the answer, and it has to be printed when there is nothing else to
// say or it is missing exactly when it is needed.
//
// The directory line is unconditional; the `extends` vocabulary beneath it is not.
// Someone is only choosing a base theme when a file already failed, and that vocabulary
// used to be interpolated into each refusal, where the startup modal clipped it
// mid-list — this is its home, but a clean run does not need it recited.
//
// A refusal carrying measured violations prints ALL of them, indented under its line.
// The one-line form the modal shows spells out one and counts the rest, which is right
// for a modal and wrong here: a page has room, and reporting every miss at once is the
// whole reason theme.Validate does not stop at the first.
func RenderThemes(dir string, loaded []string, problems []error) string {
	var b strings.Builder
	b.WriteString("User themes:\n")
	for _, name := range loaded {
		fmt.Fprintf(&b, "  ✓ %s\n", name)
	}
	for _, p := range problems {
		var invalid *themefile.InvalidPaletteError
		if errors.As(p, &invalid) && len(invalid.Violations) > 1 {
			// Doctor's own header rather than p.Error(), which spells out the first miss
			// inline — right for a one-line modal, and here it would print that miss twice,
			// once in the header and once at the top of the list below it.
			fmt.Fprintf(&b, "  ⚠ %s: palette is not legible, %d misses:\n", invalid.File, len(invalid.Violations))
			for _, v := range invalid.Violations {
				fmt.Fprintf(&b, "      %s\n", v.Error())
			}
			continue
		}
		fmt.Fprintf(&b, "  ⚠ %s\n", p.Error())
	}
	if len(loaded) == 0 && len(problems) == 0 {
		b.WriteString("  none loaded\n")
	}
	// Not when ThemesDir() itself failed: dir is "" there, and "Themes live in " is a
	// sentence that names nowhere. The error is already printed above it.
	if dir != "" {
		fmt.Fprintf(&b, "  Themes live in %s\n", dir)
	}
	if len(problems) > 0 {
		fmt.Fprintf(&b, "  A theme file may extend: %s\n", strings.Join(theme.BuiltinNames(), ", "))
	}
	return b.String()
}
