package themefile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// write drops one file into a fresh directory and returns the directory.
func write(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	return dir
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
			// The one that is the point of the issue: a palette the contrast oracle
			// refuses is reported with the failing token and its measured ratio, never
			// darkened into compliance.
			name: "illegible palette",
			file: "washed.json", body: `{"extends": "tokyo-night", "palette": {"fg": "#111111"}}`,
			// The measured ratio and the floor, not just "invalid": tuning a palette
			// needs the number. Bounded at violationsShown, so the tail is a count —
			// a single wrong fg fails its own floor and both pairs it appears in.
			contains: []string{"washed.json", "not legible", "fg: contrast 1.10, floor 4.50", "and 2 more"},
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
// package, so the constant cannot come the other way. What keeps the two honest is that
// this asserts the bound with room to spare — the filename is the only unbounded part,
// and a name long enough to breach it is one the modal clips for its own reasons.
func TestRefusalsFitTheModalTheyAreShownIn(t *testing.T) {
	const modalClip = 100

	// The worst case the fixed text can produce: a palette missing several floors,
	// under a filename of ordinary length.
	_, problems := Load(write(t, "washed.json", `{"palette": {"fg": "#111111"}}`))
	require.Len(t, problems, 1)
	assert.LessOrEqualf(t, len([]rune(problems[0].Error())), modalClip,
		"the refusal is %d runes; the startup modal clips it mid-word: %v",
		len([]rune(problems[0].Error())), problems[0])
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "midnight.json"),
		[]byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))

	loaded, problems := Load(dir)
	assert.Empty(t, problems)
	assert.Len(t, loaded, 1)
	assert.Contains(t, loaded, "midnight")
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
	assert.Equal(t, theme.Get("catppuccin-mocha").Glyphs, th.Glyphs,
		"glyphs come from the base; a theme file cannot introduce one")
	assert.Equal(t, theme.Get("catppuccin-mocha").Borders, th.Borders)
}
