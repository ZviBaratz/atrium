package themefile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// write drops one file into a fresh directory and returns the directory.
func write(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeInto(t, dir, name, body)
	return dir
}

// writeInto adds another file to a directory write already made, for the cases that
// need two — a name collision, or two bases whose difference is the assertion.
func writeInto(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

// TestLoadAcceptsAPartialOverride is the happy path and the argument for `extends`: a
// two-token file is a whole theme, because the other sixteen come from the base.
//
// It asserts the inherited tokens by VALUE against the base rather than merely
// asserting the theme loaded — a loader that returned the base palette untouched would
// pass the weaker check while ignoring the file entirely.
func TestLoadAcceptsAPartialOverride(t *testing.T) {
	dir := write(t, "midnight.json", `{
	  "extends": "tokyo-night",
	  "palette": { "bg": "#000000", "attention": "#ffb454" }
	}`)

	// Snapshotted BEFORE the load, as a value: comparing the base to itself afterwards
	// would pass however hard the loader wrote through to the registry's own theme.
	baseBefore := theme.Get("tokyo-night").Palette

	loaded, problems := Load(dir)
	require.Empty(t, problems)
	require.Contains(t, loaded, "midnight")

	got := loaded["midnight"]
	assert.Equal(t, "midnight", got.Name, "the name is the filename stem, not the base's")
	assert.Equal(t, "#000000", theme.Hex(got.Palette.Bg))
	assert.Equal(t, "#ffb454", theme.Hex(got.Palette.Attention))

	assert.Equal(t, theme.Hex(baseBefore.Fg), theme.Hex(got.Palette.Fg), "an unnamed token comes from the base")
	assert.Equal(t, theme.Hex(baseBefore.BadgeBg), theme.Hex(got.Palette.BadgeBg))
	assert.Equal(t, baseBefore, theme.Get("tokyo-night").Palette,
		"loading must not write through to the built-in it extends")
}

// TestLoadDefaultsToTheDefaultBase: `extends` is optional.
func TestLoadDefaultsToTheDefaultBase(t *testing.T) {
	dir := write(t, "plain.json", `{"palette": {"attention": "#ffb454"}}`)
	loaded, problems := Load(dir)
	require.Empty(t, problems)
	require.Contains(t, loaded, "plain")
	assert.Equal(t, theme.Hex(theme.Get(theme.DefaultThemeName).Palette.Fg),
		theme.Hex(loaded["plain"].Palette.Fg))
}

// TestLoadRefusals is the refusal table. Every case asserts the message NAMES what the
// author has to change: a refusal that only says "invalid theme" sends them to read the
// source, which is the failure this whole surface exists to avoid.
func TestLoadRefusals(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		body     string
		contains []string
	}{
		{
			name: "unknown top-level key",
			file: "x.json", body: `{"palete": {"fg": "#ffffff"}}`,
			contains: []string{"x.json", "palete"},
		},
		{
			name: "unknown palette token",
			file: "x.json", body: `{"palette": {"forground": "#ffffff"}}`,
			contains: []string{"x.json", "forground", "not a palette token"},
		},
		{
			name: "shorthand hex",
			file: "x.json", body: `{"palette": {"fg": "#fff"}}`,
			contains: []string{"x.json", "palette.fg", "#fff", "#rrggbb"},
		},
		{
			name: "colour word",
			file: "x.json", body: `{"palette": {"fg": "red"}}`,
			contains: []string{"palette.fg", "red"},
		},
		{
			name: "malformed json",
			file: "x.json", body: `{"palette":`,
			contains: []string{"x.json"},
		},
		{
			name: "empty palette",
			file: "x.json", body: `{"extends": "unicode"}`,
			contains: []string{"x.json", "unicode"},
		},
		{
			name: "extends an unknown theme",
			file: "x.json", body: `{"extends": "dracula", "palette": {"fg": "#ffffff"}}`,
			contains: []string{"dracula", "not a built-in"},
		},
		{
			name: "name collides with a built-in",
			file: "tokyo-night.json", body: `{"palette": {"fg": "#ffffff"}}`,
			contains: []string{"tokyo-night", "built-in"},
		},
		{
			name: "name is the reserved auto",
			file: "auto.json", body: `{"palette": {"fg": "#ffffff"}}`,
			contains: []string{"auto", "reserved"},
		},
		{
			name: "name is not a slug",
			file: "My Theme.json", body: `{"palette": {"fg": "#ffffff"}}`,
			contains: []string{"My Theme", "lowercase"},
		},
		{
			// "-dark" IS lowercase letters, digits and dashes, which is what the message
			// used to say it had to be — so the one refusal a reader could not act on was
			// the one they were most likely to hit by naming a variant.
			name: "name starts with a dash",
			file: "-dark.json", body: `{"palette": {"fg": "#ffffff"}}`,
			contains: []string{"-dark.json", "starting with a dash"},
		},
		{
			// A Decoder reads one value and stops. Without a check for what follows, the
			// second object here is discarded in silence and the file half-applies — the
			// exact no-op DisallowUnknownFields was added to prevent.
			name: "a second object after the first",
			file: "x.json", body: `{"palette": {"accent": "#7aa2f7"}}{"palette": {"accent": "#000000"}}`,
			contains: []string{"x.json", "content after the theme object"},
		},
		{
			name: "garbage after the object",
			file: "x.json", body: `{"palette": {"accent": "#7aa2f7"}} !!! not json at all {`,
			contains: []string{"x.json", "content after the theme object"},
		},
		{
			// json's own word for this is "EOF", which is not a sentence a user can act on.
			name: "empty file",
			file: "x.json", body: "   \n",
			contains: []string{"x.json", "empty"},
		},
		{
			// The one that is the point of the issue: a palette the contrast oracle
			// refuses is reported with the failing token and its measured ratio, never
			// darkened into compliance.
			name: "illegible palette",
			file: "washed.json", body: `{"extends": "tokyo-night", "palette": {"fg": "#111111"}}`,
			// The measured ratio and the floor, not just "invalid": tuning a palette
			// needs the number. Bounded at violationsShown, so the rest is a count —
			// a single wrong fg fails its own floor and both pairs it appears in — and
			// the count comes BEFORE the detail, so a clip cannot eat it.
			contains: []string{"washed.json", "not legible", "3 misses", "fg: contrast 1.10, floor 4.50"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loaded, problems := Load(write(t, tc.file, tc.body))
			assert.Empty(t, loaded, "a refused file must register nothing")
			require.Len(t, problems, 1)
			for _, want := range tc.contains {
				assert.Containsf(t, problems[0].Error(), want,
					"the refusal must name %q so the author knows what to change: %v", want, problems[0])
			}
		})
	}
}

// TestRefusalsFitTheModalTheyAreShownIn pins the width the messages were bounded to.
// A refusal is one line of app's startup modal, which clips at 100 runes — and a clip
// lands mid-word, which is how "…fg on bg_elevated: contrast 1.3…" shipped in review.
//
// 100 is app's reportLineBudget, spelled here rather than imported: app depends on this
// package, so the constant cannot come the other way.
//
// The FILENAME is what makes this a real bound rather than a formality, and an earlier
// version of this test hid that by measuring the shortest name in the repo. A theme is
// named by its file, so the names people actually pick are the names of themes —
// "catppuccin-frappe", "gruvbox-material-dark" — and every one of those breached a
// budget "washed.json" cleared with three runes to spare. So the fixture is a long
// realistic name, and the fixed text was re-cut to fit under one.
//
// It is still not an unbounded promise: a filename can be any length, and past some
// point the clip is the modal doing its job on a name Atrium did not choose. What the
// message owes is that the COUNT survives, which is why it sits ahead of the detail —
// asserted below rather than left to the reading.
func TestRefusalsFitTheModalTheyAreShownIn(t *testing.T) {
	const modalClip = 100

	// The worst case the fixed text can produce: a palette missing several floors,
	// under the longest theme name anyone has plausibly typed.
	dir := write(t, "gruvbox-material-dark.json", `{"palette": {"fg": "#111111"}}`)
	_, problems := Load(dir)
	require.Len(t, problems, 1)
	msg := problems[0].Error()
	assert.LessOrEqualf(t, len([]rune(msg)), modalClip,
		"the refusal is %d runes; the startup modal clips it mid-word: %s", len([]rune(msg)), msg)

	// What holds for ANY filename and any violation, unlike the width above: the file and
	// the count are complete before the budget runs out. That is the part a clip may not
	// take, and it is why they are on the left.
	head := strings.Index(msg, "):")
	require.Positive(t, head, "the count must be present for a multi-miss palette: %s", msg)
	assert.Lessf(t, head, modalClip,
		"the file and the miss count must fit inside the clip budget: %s", msg)

	// The count is what a clip must never take, because it is the only thing telling the
	// reader that fixing the named token will not be the end of it. Asserted as an
	// ORDERING rather than a width: a clip eats the right-hand end, so "survives the
	// clip" and "sits left of the detail" are the same property, and only the second one
	// stays true for a filename longer than this fixture's.
	assert.Lessf(t, strings.Index(msg, "misses"), strings.Index(msg, "contrast"),
		"the miss count must sit ahead of the violation detail, or a clip takes it: %s", msg)
}

// TestExtendsCannotChainThroughAUserTheme pins the no-chaining rule against the state
// that makes it tempting: a user theme that IS registered. Without the built-ins-only
// check this would resolve, and the file's meaning would then depend on load order.
func TestExtendsCannotChainThroughAUserTheme(t *testing.T) {
	dir := write(t, "midnight.json", `{"palette": {"attention": "#ffb454"}}`)
	loaded, problems := Load(dir)
	require.Empty(t, problems)
	restore := theme.SetUserThemes(loaded)
	defer restore()
	require.Equal(t, "midnight", theme.Get("midnight").Name, "precondition: it is resolvable")

	_, problems = Load(write(t, "deeper.json", `{"extends": "midnight", "palette": {"fg": "#ffffff"}}`))
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "not a built-in")
}

// TestLoadIgnoresWhatIsNotAThemeFile: a README, an editor swap file or a subdirectory
// beside the themes is not a broken theme, and reporting it as one would train the user
// to ignore the report.
func TestLoadIgnoresWhatIsNotAThemeFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".midnight.json.swp"), []byte("junk"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "old.json"), 0o750))
	// The sidecars, which are the ones that reach the extension filter: filepath.Ext of
	// both is ".json", so before they were skipped by name their stems hit nameRE and the
	// user got a refusal naming a file they never wrote — while editing a theme, and
	// sorted ('.' is 0x2E) ahead of their own into a report that shows five.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".#midnight.json"), []byte("lock"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "._midnight.json"), []byte("appledouble"), 0o600))
	// A symlink to a DIRECTORY named like a theme. e.IsDir() is false for it, which is
	// what let it through to os.ReadFile and be reported as "is a directory".
	require.NoError(t, os.Symlink(filepath.Join(dir, "old.json"), filepath.Join(dir, "linked.json")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "midnight.json"),
		[]byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))

	loaded, problems := Load(dir)
	assert.Empty(t, problems)
	assert.Len(t, loaded, 1)
	assert.Contains(t, loaded, "midnight")
}

// TestLoadFollowsASymlinkedThemeFile is the other side of the filter above, and the
// reason it is os.Stat rather than os.Lstat or DirEntry.Type(): a dotfiles repo ships a
// theme by symlinking it into place, and either of those would see ModeSymlink, decide it
// is not a regular file, and ignore a theme the user can see sitting in the directory.
func TestLoadFollowsASymlinkedThemeFile(t *testing.T) {
	src := t.TempDir()
	target := filepath.Join(src, "midnight.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))

	dir := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "midnight.json")))

	loaded, problems := Load(dir)
	assert.Empty(t, problems)
	assert.Contains(t, loaded, "midnight")
}

// TestLoadRefusesAnOversizeFile. Without a cap Load reads whatever carries a .json name
// wholly into memory during startup, before any UI exists to say what it is doing.
func TestLoadRefusesAnOversizeFile(t *testing.T) {
	dir := write(t, "huge.json", `{"palette": {"attention": "#ffb454"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "huge.json"),
		append([]byte(`{"palette": {"attention": "#ffb454"}}`), make([]byte, maxSize)...), 0o600))

	loaded, problems := Load(dir)
	assert.Empty(t, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "huge.json")
	assert.Contains(t, problems[0].Error(), "capped")
}

// TestLoadTreatsAMissingDirectoryAsNoThemes. Every install is in this state until
// someone writes a file, so a missing directory reported as a problem would put a modal
// in front of every user who has no themes.
func TestLoadTreatsAMissingDirectoryAsNoThemes(t *testing.T) {
	loaded, problems := Load(filepath.Join(t.TempDir(), "never-created"))
	assert.Empty(t, loaded)
	assert.Empty(t, problems)
}

// TestLoadReportsAnUnreadableDirectory is the other half of the case above: a
// directory that EXISTS and cannot be read is a permissions problem the user can fix,
// and silence would present it as "my themes vanished".
func TestLoadReportsAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0o000 directory regardless of its mode")
	}
	dir := filepath.Join(t.TempDir(), "themes")
	require.NoError(t, os.Mkdir(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	loaded, problems := Load(dir)
	assert.Empty(t, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), dir)
}

// TestLoadKeepsTheGoodOnesWhenOneIsRefused. One broken file must not cost the user the
// themes that are fine — the same partial-failure rule repo_scripts and custom_commands
// follow.
func TestLoadKeepsTheGoodOnesWhenOneIsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.json"),
		[]byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"),
		[]byte(`{"palette": {"fg": "#111111"}}`), 0o600))

	loaded, problems := Load(dir)
	assert.Contains(t, loaded, "good")
	assert.NotContains(t, loaded, "bad")
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "bad.json")
}

// TestLoadedThemesPassEveryRegistryGuard is the reason refusal is worth this much
// care: once registered, a user theme is swept by every guard written against "every
// registered theme". This asserts the loader's output survives the two the oracle does
// not cover — the width-1 glyph invariant and canonical hex — so a user palette can
// never be the reason one of those sweeps fails somewhere else.
func TestLoadedThemesPassEveryRegistryGuard(t *testing.T) {
	loaded, problems := Load(write(t, "midnight.json",
		`{"extends": "catppuccin-mocha", "palette": {"bg": "#000000"}}`))
	require.Empty(t, problems)
	th := loaded["midnight"]
	require.NotNil(t, th)

	assert.Empty(t, theme.Validate(th.Palette))
	// Canonical hex over every field of the palette, by reflection rather than by a
	// hand-listed set: the point is that NO token survived the file as a shorthand, and a
	// list would only cover the ones someone remembered. ui/splash's
	// TestSplashPalettesAreCanonicalHex holds every registered theme to this, in a
	// package that has no idea user themes exist.
	pv := reflect.ValueOf(th.Palette)
	for i := range pv.NumField() {
		c, ok := pv.Field(i).Interface().(theme.Color)
		require.Truef(t, ok, "Palette.%s is not a Color", pv.Type().Field(i).Name)
		assert.Regexpf(t, `^#[0-9a-f]{6}$`, theme.Hex(c),
			"Palette.%s is not canonical hex after loading", pv.Type().Field(i).Name)
	}
	assert.Equal(t, theme.Get("catppuccin-mocha").Borders, th.Borders)
}

// TestExtendsCarriesTheBorderStyleAndNotTheGlyphs pins the half of `extends` that is
// easy to state backwards, and was: the README claimed extends supplies the glyph set
// and then that a theme file cannot change borders, and both halves were the wrong way
// round.
//
// Borders ARE inherited and DO differ by base — `unicode` is the square-cornered
// theme and the other four are rounded — so extending it is the only way a theme file
// reaches square corners. Asserting against two bases is what makes that a real
// discriminator: every built-in ships the same Glyphs table, so an equality against one
// base passes for any base and proves nothing about inheritance either way.
//
// Glyphs are NOT inherited, and the struct field is not where that is decided:
// ui/theme's compose() overwrites Glyphs and agentGlyphs from the active glyph_set for
// every theme it publishes, built-in or user. So what a loaded theme carries in that
// field never reaches a frame, and the assertion that matters is the one through Set().
func TestExtendsCarriesTheBorderStyleAndNotTheGlyphs(t *testing.T) {
	dir := write(t, "square.json", `{"extends": "unicode", "palette": {"bg": "#000000"}}`)
	writeInto(t, dir, "round.json", `{"extends": "catppuccin-mocha", "palette": {"bg": "#000000"}}`)

	loaded, problems := Load(dir)
	require.Empty(t, problems)
	require.Len(t, loaded, 2)

	assert.Equal(t, theme.Get("unicode").Borders, loaded["square"].Borders)
	assert.Equal(t, theme.Get("catppuccin-mocha").Borders, loaded["round"].Borders)
	assert.NotEqual(t, loaded["square"].Borders, loaded["round"].Borders,
		"the two bases must differ here, or inheriting the border style is untested")

	// The glyph half, through the composed theme rather than the loaded struct.
	defer theme.SetUserThemes(loaded)()
	defer theme.SetGlyphSet(theme.GlyphSetASCII)()
	defer theme.Set("square")()
	assert.Equal(t, theme.Get("unicode").Borders, theme.Current().Borders,
		"the border style survives composition")
	assert.NotEqual(t, theme.Get("unicode").Glyphs, theme.Current().Glyphs,
		"glyph_set overrides whatever the base carried; a theme file cannot pin glyphs")
}

// TestDuplicateNameIsRefused. The extension is matched case-insensitively and the stem
// is not, so two files can resolve to one name — and before this they did so silently,
// with sort order picking the winner. The symptom was "every save to one of these files
// does nothing", reported by nobody, because neither the startup modal nor `atrium
// doctor` had a second file to name.
func TestDuplicateNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	requireCaseSensitiveFS(t, dir)
	writeInto(t, dir, "dup.json", `{"palette": {"accent": "#7aa2f7"}}`)
	writeInto(t, dir, "dup.JSON", `{"palette": {"accent": "#89b4fa"}}`)

	loaded, problems := Load(dir)
	// NEITHER, and that is the assertion rather than a detail of it. Keeping one would
	// mean keeping whichever sort.Strings reached first — 'J' (0x4A) before 'j' (0x6A) —
	// so a stale dup.JSON would beat the dup.json its author is editing, with the message
	// blaming the live file for the collision.
	assert.Empty(t, loaded, "the pair is dropped; neither name is registered")
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "dup.JSON")
	assert.Contains(t, problems[0].Error(), "dup.json",
		"the refusal must name BOTH files, or the reader cannot tell which two collided")
	assert.Contains(t, problems[0].Error(), "neither",
		"and must say the surviving file is not loaded either, or the user tunes a palette that is gone")
}

// requireCaseSensitiveFS skips when dir's filesystem folds case.
//
// It has to exist, and be a skip rather than a fixture change, because the collision this
// package refuses is REACHABLE only where case is significant. On APFS and NTFS,
// dup.json and dup.JSON are one inode: the second write truncates the first, os.ReadDir
// returns one entry, and Load correctly returns one theme and no problems. CI runs the
// suite on macos-latest as well as ubuntu-latest (.github/workflows/build.yml), so a test
// that asserts the collision unconditionally is red on half the matrix — and green on the
// half a developer runs, which is how it would have merged.
func requireCaseSensitiveFS(t *testing.T, dir string) {
	t.Helper()
	// No .json extension: Load must ignore the probe even if a failure path leaves it.
	probe := filepath.Join(dir, "CaseProbe")
	require.NoError(t, os.WriteFile(probe, nil, 0o600))
	t.Cleanup(func() { _ = os.Remove(probe) })
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err == nil {
		t.Skip("filesystem folds case, so two spellings of one stem cannot coexist here")
	}
}

// TestDuplicateKeyIsRefused. encoding/json resolves a repeated name silently — last-wins
// for a scalar, and a MERGE for an object — and DisallowUnknownFields does not change
// that. So a file whose visible first line says `extends: unicode` loads tokyo-night, and
// a file with two palette blocks loads neither of them but the union. Both are "the file
// says one thing and Atrium did another", which is what this loader exists to prevent.
func TestDuplicateKeyIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"scalar", `{"extends": "unicode", "extends": "tokyo-night"}`, "extends"},
		{"nested", `{"palette": {"fg": "#111111", "fg": "#222222"}}`, "fg"},
		{"block", `{"palette": {"fg": "#111111"}, "palette": {"bg": "#222222"}}`, "palette"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, problems := Load(write(t, "dupkey.json", tc.body))
			assert.Empty(t, loaded)
			require.Len(t, problems, 1)
			assert.Contains(t, problems[0].Error(), tc.want,
				"the refusal must name the key that is set twice")
		})
	}
}

// TestRepeatedVALUESAreNotADuplicateKey is the false positive the check above must not
// have: two tokens set to the same colour is an ordinary theme, and the string that
// repeats there is a value, not a name.
func TestRepeatedVALUESAreNotADuplicateKey(t *testing.T) {
	loaded, problems := Load(write(t, "twin.json", `{"palette": {"fg": "#c0caf5", "cyan": "#c0caf5"}}`))
	assert.Empty(t, problems)
	assert.Contains(t, loaded, "twin")
}

// TestAMisspeltTokenIsNamedBeforeItsColourIsJudged. A user who writes `foreground` also
// tends to write `#fff`, and both checks are true of that line — but only one of them is
// the mistake worth telling them about. Judging the colour first answers "#fff is not a
// canonical #rrggbb colour", which sends them to fix the half that was closer to right
// and leaves the key they invented in place for the next round.
func TestAMisspeltTokenIsNamedBeforeItsColourIsJudged(t *testing.T) {
	_, problems := Load(write(t, "typo.json", `{"palette": {"foreground": "#fff"}}`))
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "not a palette token")
	assert.NotContains(t, problems[0].Error(), "#rrggbb",
		"the key is the mistake; naming the colour first buys the user another round trip")
}
