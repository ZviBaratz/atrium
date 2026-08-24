package doctor

import (
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
func CheckThemes() (dir string, loaded []string, problems []error) {
	dir, err := config.ThemesDir()
	if err != nil {
		return "", nil, []error{err}
	}
	themes, problems := themefile.Load(dir)
	for name := range themes {
		loaded = append(loaded, name)
	}
	sort.Strings(loaded)
	return dir, loaded, problems
}

// RenderThemes formats the user-theme report for `atrium doctor` (empty string when
// there is nothing to say — no files loaded and none refused), in the section shape
// RenderRepoScripts established.
//
// When something was refused it also prints the directory and the themes a file may
// extend. That vocabulary used to be interpolated into the refusal itself, where the
// startup modal clipped it mid-list; here there is room for it, and the directory
// answers the one question a refusal structurally cannot — "my file is in neither
// list".
func RenderThemes(dir string, loaded []string, problems []error) string {
	if len(loaded) == 0 && len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("User themes:\n")
	for _, name := range loaded {
		fmt.Fprintf(&b, "  ✓ %s\n", name)
	}
	for _, p := range problems {
		fmt.Fprintf(&b, "  ⚠ %s\n", p.Error())
	}
	if len(problems) > 0 {
		fmt.Fprintf(&b, "  Themes live in %s and may extend: %s\n",
			dir, strings.Join(theme.BuiltinNames(), ", "))
	}
	return b.String()
}
