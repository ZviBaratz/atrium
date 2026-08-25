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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// ext is the extension a user theme file must have. Anything else in the directory is
// ignored rather than refused: an editor's swap file or a README next to the themes is
// not a broken theme.
//
// Matched case-insensitively, because a file copied off a case-insensitive filesystem
// arrives as .JSON and silently doing nothing is the worst answer available. The stem
// is NOT: nameRE demands lowercase, so midnight.json and midnight.JSON resolve to one
// name, and Load registers NEITHER — see the collision branch there for why dropping
// both is the only outcome that does not depend on sort order.
//
// Reachable on a case-SENSITIVE filesystem only. Where the filesystem folds case the two
// names are one file, so the collision cannot arise and the branch is dead; that is why
// its guard skips rather than fails on such a filesystem.
const ext = ".json"

// maxSize bounds a single theme file. A whole palette (theme.TokenNames) is under a
// kilobyte, so
// this is not a budget anyone can reach by writing a theme — it is the answer to a
// multi-gigabyte file that acquired a .json name, which Load would otherwise read whole
// into memory during startup, before any UI exists to say what it is waiting on.
const maxSize = 64 << 10

// nameRE is the shape a theme name (the filename stem) must have.
//
// It is narrow on purpose. The name becomes a config.json value the user types, a key
// in a map, and a row in the settings picker; allowing spaces, case or punctuation
// would mean three places deciding independently how to fold it. Lowercase, digits and
// dashes is what every built-in already uses.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// hexRE is canonical "#rrggbb". Shorthand (#fff) and colour words are refused rather than
// accepted-and-expanded, because what is downstream of a palette is not only lipgloss.
//
// The splash renders from five of these tokens through fresco, which does NOT reject a
// bad anchor at runtime — it falls back and paints something slightly wrong, silently
// (ui/splash_test.go's TestSplashPalettesAreCanonicalHex says so, and exists because
// Atrium's own palettes are compile-time constants that no runtime check would ever see).
// That test iterates theme.Names(), so it covers the built-ins; a user theme is not
// registered in its binary and never will be. Refusing here at the boundary that has a
// filename and a key to name is the only place a user's `#fff` can be a message rather
// than a slightly-off field of colour nobody can account for.
//
// And a colour WORD is worse than shorthand: lipgloss expands #fff to white but answers
// NoColor for "red", which theme.Hex renders as the empty string and the contrast oracle
// measures as luminance 0 — so it would be judged as black and drawn as the terminal's
// own foreground.
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
		base := e.Name()
		// A dot-prefixed name is a sidecar, never a theme: Emacs writes .#midnight.json
		// beside the file the user is editing, and macOS zips carry ._midnight.json.
		// filepath.Ext returns .json for both, so without this they reach nameRE and are
		// REFUSED — a modal accusing the user of misnaming a file they never wrote, at the
		// exact moment they are editing a theme, and one that sorts ahead of every real
		// refusal ('.' is 0x2E) into a report that shows five.
		if strings.HasPrefix(base, ".") || !strings.EqualFold(filepath.Ext(base), ext) {
			continue
		}
		// Stat, not e.IsDir(). IsDir is false for a symlink to a directory AND for a FIFO,
		// and os.ReadFile on a FIFO blocks until a writer appears — inside the launch path,
		// before tmux.Init and before app.Run, so `mkfifo themes/x.json` would hang atrium,
		// atrium doctor and the daemon with no output and no timeout. Stat FOLLOWS the link
		// (unlike Lstat) on purpose: a symlinked theme file is how a dotfiles repo ships
		// one, and that has to keep working; what is excluded is everything not a regular
		// file, which is also how a symlinked directory becomes ignored rather than
		// reported as a broken theme.
		info, err := os.Stat(filepath.Join(dir, base))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, base)
	}
	sort.Strings(names)

	loaded := make(map[string]*theme.Theme, len(names))
	claimed := make(map[string]string, len(names))
	var problems []error
	for _, base := range names {
		name, th, err := loadOne(filepath.Join(dir, base))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// Two files can reach one name only through the extension, which is matched
		// case-insensitively while the stem is not (see ext).
		//
		// NEITHER is registered. Refusing only the second would leave the first loaded, and
		// which one that is falls out of sort.Strings — 'J' (0x4A) precedes 'j' (0x6A), so
		// a stale dusk.JSON beats the dusk.json its author is editing, and the symptom is
		// "my edits do nothing" with the message blaming the live file. Dropping the pair
		// costs one palette the user can restore by deleting a file, and makes the outcome
		// independent of byte order, which is the only version of this worth having.
		if first, dup := claimed[name]; dup {
			problems = append(problems, fmt.Errorf("%s: theme name %q is also claimed by %s; neither loads", base, name, first))
			delete(loaded, name)
			continue
		}
		claimed[name] = base
		loaded[name] = th
	}
	return loaded, problems
}

// loadOne reads and validates a single theme file, returning its registered name.
//
// Every error it returns leads with the file's base name, because a report lists
// several and "unknown palette key" on its own does not say which file to open.
//
// What the messages deliberately do NOT carry is a vocabulary: not the palette token
// names (theme.TokenNames owns that count), not the built-in theme names. An earlier draft interpolated both, and the
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
		// The name IS the filename stem, already in this message's prefix, so it is not
		// repeated here — and the rule is spelled out in full because the near misses are
		// "-dark" and "Midnight", both of which read as compliant against a shorter
		// summary like "lowercase letters, digits and dashes".
		return fail("theme name must be lowercase a-z, 0-9 and dashes, not empty or starting with a dash")
	}
	if name == theme.AutoThemeName {
		return fail("%q is the reserved value that follows the terminal background", name)
	}
	if theme.IsBuiltin(name) {
		return fail("%q is a built-in theme; a user theme cannot replace one", name)
	}

	if info, err := os.Stat(path); err == nil && info.Size() > maxSize {
		return fail("the file is %d bytes; a theme is under a kilobyte and this is capped at %d", info.Size(), maxSize)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is a directory listing of the data dir
	if err != nil {
		return fail("%v", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fail("the file is empty")
	}
	// Both halves of "the file says one thing and Atrium did another", which is the class
	// this whole loader exists to make impossible, and neither is caught by decoding:
	//
	//   {"extends": "unicode", "extends": "tokyo-night"}   duplicate key
	//   {"palette": {...}} {"palette": {...}}              a second object
	//
	// encoding/json accepts a repeated key with last-wins for scalars and a MERGE for
	// maps, and DisallowUnknownFields does not change that — so a file whose visible first
	// line says `extends: unicode` loads tokyo-night and reports nothing. And a Decoder
	// reads ONE value and stops, so a duplicated block or a stray paste after the closing
	// brace loads the first half and discards the rest in the same silence.
	if key, dup := duplicateKey(raw); dup {
		return fail("%q is set twice; JSON keeps the last one silently", key)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return fail("%w", err)
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		return fail("there is content after the theme object; a file holds exactly one")
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
		// The KEY before its value. A misspelt token with a shorthand colour is one typo in
		// the reader's mind, and judging the colour first answers with "#fff is not a
		// canonical #rrggbb colour" — a true statement about a line whose real problem is
		// that `foreground` is not a token at all, and one that sends them to fix the half
		// that was closer to right.
		if !slices.Contains(theme.TokenNames(), key) {
			return fail("palette.%s is not a palette token", key)
		}
		if !hexRE.MatchString(f.Palette[key]) {
			return fail("palette.%s is %q, not a canonical #rrggbb colour", key, f.Palette[key])
		}
		// Cannot fail: the name was just checked against the same table SetToken walks.
		theme.SetToken(&pal, key, theme.ParseHex(f.Palette[key]))
	}

	if violations := theme.Validate(pal); len(violations) > 0 {
		return "", nil, &InvalidPaletteError{File: base, Violations: violations}
	}

	th.Name = name
	th.Palette = pal
	return name, &th, nil
}

// InvalidPaletteError is the refusal for a palette that missed a contrast floor. It
// carries EVERY violation Validate measured, not just the one its message spells out,
// so a surface with room for the list can print it: `atrium doctor` does, and that is
// what keeps tuning a palette to one round rather than one round per token.
//
// A typed error rather than a formatted string because the two consumers want different
// amounts of the same fact. The startup modal has one clipped line per file; doctor has
// a page. Formatting the long form and re-parsing it would be the other way to do this,
// and it would be worse in the ordinary way.
type InvalidPaletteError struct {
	// File is the base name of the refused file, so the message is self-locating in a
	// report that lists several.
	File string
	// Violations is every floor the resolved palette missed, in Validate's order.
	Violations []theme.Violation
}

// violationsShown caps how many misses the one-line form spells out.
//
// One. The line is rendered into the startup modal, which clips at app's
// reportLineBudget, and beyond the first violation the clip lands mid-ratio
// ("contrast 1.3…") — a second entry that gets cut in half is worse than a count.
//
// It costs little, because the misses are rarely independent: a single wrong fg fails
// its own floor and both pairs it appears in, so the rest were restating one mistake.
// Nothing is lost either way — Violations carries them all, doctor prints them, and the
// next load shows whatever the first fix uncovers.
const violationsShown = 1

// Error is the one-line form: the file, how many floors it missed, and the first one.
//
// The COUNT comes before the detail, and that ordering is the whole design of this
// string. The modal that shows it clips to a fixed rune budget, and two of the three
// parts are lengths this package does not choose — the filename, and a violation name
// that runs from "fg" to "badge_fg on badge_bg". So the only honest promise is about
// what sits LEFT of the clip: the file and the count. Putting "and N more" at the end,
// where an earlier draft had it, meant the longer the filename the more certainly the
// reader lost the one fact telling them to look further — and that draft's measurement
// used the shortest filename in the repo, so the bound it claimed held only for it.
func (e *InvalidPaletteError) Error() string {
	shown := e.Violations
	if len(shown) > violationsShown {
		shown = shown[:violationsShown]
	}
	msgs := make([]string, 0, len(shown))
	for _, v := range shown {
		msgs = append(msgs, v.Error())
	}
	count := ""
	if len(e.Violations) > len(shown) {
		count = fmt.Sprintf(" (%d misses)", len(e.Violations))
	}
	return fmt.Sprintf("%s: palette is not legible%s: %s", e.File, count, strings.Join(msgs, "; "))
}

// duplicateKey reports the first object key that appears twice within one object, at any
// depth. It is the check encoding/json does not have: RFC 8259 leaves repeated names
// undefined, and Go resolves them silently rather than refusing.
//
// It walks tokens rather than decoding, because the information is gone by the time a
// value exists — the merged palette map and the last-wins extends are both well-formed.
// Tracking "am I expecting a key" per object level is what separates a key from a string
// VALUE that happens to repeat: {"palette":{"fg":"#111","bg":"#111"}} has two identical
// values and no duplicate key.
//
// A malformed document returns false and is left to Decode, whose message names the
// offset and the syntax problem; this function has neither and would only get in front
// of a better error.
func duplicateKey(raw []byte) (string, bool) {
	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]bool
	}
	var stack []*frame
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		top := (*frame)(nil)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if s, isStr := tok.(string); isStr && top != nil && top.object && top.expectKey {
			if top.keys[s] {
				return s, true
			}
			top.keys[s] = true
			top.expectKey = false
			continue
		}
		// Anything else is a value (or a bracket), and a value completes the pair.
		if d, isDelim := tok.(json.Delim); isDelim {
			switch d {
			case '{':
				stack = append(stack, &frame{object: true, expectKey: true, keys: map[string]bool{}})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			default: // '}' or ']' — the value that closes is the parent's
				stack = stack[:len(stack)-1]
			}
		}
		if len(stack) > 0 {
			if parent := stack[len(stack)-1]; parent.object {
				parent.expectKey = true
			}
		}
	}
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
