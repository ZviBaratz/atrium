// Package themefile loads user-authored palettes from a directory of JSON files and
// turns them into themes ui/theme can register (#813).
//
// It is a separate package, not a file in ui/theme, to keep the L1 veto honoured:
// ui/theme stays a leaf that imports nothing from Atrium and nothing that touches a
// filesystem. This package imports ui/theme, never the other way round, and takes its
// directory as an argument rather than resolving the data dir itself — so it depends on
// stdlib plus ui/theme and nothing else, and a test can point it anywhere.
//
// Every refusal is by name. A palette that misses a contrast floor is REFUSED and
// reported, never silently darkened to fit: the user picked those colours on purpose,
// and a theme system that quietly edits them is worse than one that says no.
package themefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// ext is the extension a user theme file must have. Anything else in the directory is
// ignored rather than refused: an editor's swap file or a README next to the themes is
// not a broken theme.
const ext = ".json"

// nameRE is the shape a theme name (the filename stem) must have.
//
// It is narrow on purpose. The name becomes a config.json value the user types, a key
// in a map, and a row in the settings picker; allowing spaces, case or punctuation
// would mean three places deciding independently how to fold it. Lowercase, digits and
// dashes is what every built-in already uses.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// hexRE is canonical "#rrggbb". Shorthand (#fff) and colour words are refused rather
// than accepted-and-expanded, because ui/splash's fresco.Palette.Validate holds every
// REGISTERED theme to canonical hex — a shorthand accepted here would fail there, in a
// package that has no idea a user file exists.
var hexRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// file is the on-disk shape. Decoded with DisallowUnknownFields, so a misspelt
// top-level key is an error naming it rather than a setting that silently did nothing.
type file struct {
	// Extends names the BUILT-IN theme this palette starts from; empty means the
	// default. Only a built-in, never another user theme: chaining would introduce a
	// load-order and a cycle axis for the sake of a feature nobody asked for, and
	// "start from one of the built-ins" is the whole use case.
	Extends string `json:"extends"`
	// Palette overrides tokens by their on-disk name (theme.TokenNames). Partial by
	// design — a two-token file is a legitimate theme.
	Palette map[string]string `json:"palette"`
}

// Load reads every *.json in dir and returns the themes that passed, keyed by name,
// plus one error per file that did not.
//
// A missing directory is not an error: it is the state of every install that has never
// written a theme. An unreadable one IS, because that is a permissions problem the user
// can act on and would otherwise present as "my themes vanished".
//
// Errors are returned rather than logged so the caller decides the surface — the TUI
// toasts them, `atrium doctor` prints them, the daemon logs them. They are ordered by
// filename so a report does not reshuffle between runs.
func Load(dir string) (map[string]*theme.Theme, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("themes directory %s: %w", dir, err)}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ext) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	loaded := make(map[string]*theme.Theme, len(names))
	var problems []error
	for _, base := range names {
		name, th, err := loadOne(filepath.Join(dir, base))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		loaded[name] = th
	}
	return loaded, problems
}

// loadOne reads and validates a single theme file, returning its registered name.
//
// Every error it returns leads with the file's base name, because a report lists
// several and "unknown palette key" on its own does not say which file to open.
//
// What the messages deliberately do NOT carry is a vocabulary: not the eighteen token
// names, not the five built-in theme names. An earlier draft interpolated both, and the
// startup modal — which hugs its content to the terminal width and clips a report line
// at reportLineBudget — rendered "…is not a palette token (one of bg, bg_elevated,
// bar_bg, fg, fg_dim, fg…", which is a list that stops exactly where it becomes useful.
// A refusal gets one spelling that fits everywhere it is shown (#541's rule); the
// vocabulary lives in the README, which has room for it and is where someone writing a
// theme already is.
func loadOne(path string) (string, *theme.Theme, error) {
	base := filepath.Base(path)
	fail := func(format string, args ...any) (string, *theme.Theme, error) {
		return "", nil, fmt.Errorf("%s: "+format, append([]any{base}, args...)...)
	}

	name := strings.TrimSuffix(base, filepath.Ext(base))
	if !nameRE.MatchString(name) {
		return fail("theme name %q is not lowercase letters, digits and dashes", name)
	}
	if name == theme.AutoThemeName {
		return fail("%q is the reserved value that follows the terminal background", name)
	}
	if theme.IsBuiltin(name) {
		return fail("%q is a built-in theme; a user theme cannot replace one", name)
	}

	raw, err := os.ReadFile(path) // #nosec G304 -- the path is a directory listing of the data dir
	if err != nil {
		return fail("%v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return fail("%v", err)
	}

	baseName := strings.ToLower(strings.TrimSpace(f.Extends))
	if baseName == "" {
		baseName = theme.DefaultThemeName
	}
	if !theme.IsBuiltin(baseName) {
		return fail("extends %q, which is not a built-in theme", f.Extends)
	}

	if len(f.Palette) == 0 {
		return fail("no palette tokens; a theme that overrides nothing is a copy of %s", baseName)
	}

	// A COPY of the base theme, taken through Get, which returns the registry's own
	// pointer — writing into that would repaint the built-in for everyone.
	th := *theme.Get(baseName)
	pal := th.Palette
	for _, key := range sortedKeys(f.Palette) {
		if !hexRE.MatchString(f.Palette[key]) {
			return fail("palette.%s is %q, not a canonical #rrggbb colour", key, f.Palette[key])
		}
		if !theme.SetToken(&pal, key, theme.ParseHex(f.Palette[key])) {
			return fail("palette.%s is not a palette token", key)
		}
	}

	if violations := theme.Validate(pal); len(violations) > 0 {
		return fail("palette is not legible: %s", summarize(violations))
	}

	th.Name = name
	th.Palette = pal
	return name, &th, nil
}

// violationsShown caps how many misses one refusal spells out.
//
// One, and the number was measured rather than chosen. The refusal is rendered as one
// line of the startup modal, which app's clipReportLine cuts at 100 runes; at two
// violations the message reached 143 and the cut landed mid-ratio ("contrast 1.3…"),
// which is worse than not listing the second at all. One violation plus the count is
// 97 for the case above, so it survives the clip and wraps cleanly.
//
// It costs little, because the misses are rarely independent: a single wrong fg fails
// its own floor and both pairs it appears in, so the other two were restating one
// mistake. Validate still reports every one — `atrium doctor` prints the lot, and the
// next load shows whatever the first fix uncovers.
const violationsShown = 1

// summarize renders a refusal's violations, bounded.
func summarize(violations []theme.Violation) string {
	shown := violations
	if len(shown) > violationsShown {
		shown = shown[:violationsShown]
	}
	msgs := make([]string, 0, len(shown)+1)
	for _, v := range shown {
		msgs = append(msgs, v.Error())
	}
	if rest := len(violations) - len(shown); rest > 0 {
		msgs = append(msgs, fmt.Sprintf("and %d more below their floors", rest))
	}
	return strings.Join(msgs, "; ")
}

// sortedKeys orders a palette map so a file with two bad tokens reports the same one
// every run. Map iteration order would make a refusal message a coin flip.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
