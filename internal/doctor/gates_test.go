package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain sandboxes HOME (and drops CLAUDE_CONFIG_DIR, which any shell inside
// Claude Code exports) so nothing in this package can reach the developer's real
// ~/.claude.json. gates.go made internal/doctor capable of that for the first
// time: it reads claude's config file and imports config, whose LoadConfig seeds
// config.json when absent. No test calls CheckGatesInstalled — the
// CheckGates/CheckGatesInstalled split is the real seam; this is belt-and-braces
// so a future one cannot silently escape.
func TestMain(m *testing.M) {
	os.Exit(testutil.SandboxHomeMain(m))
}

// fakeGateReader maps a config dir to the gates resolved there. A dir absent from
// the map reads as "no comparable map", the same as a missing file.
type fakeGateReader struct{ m map[string]map[string]any }

func (f fakeGateReader) gates(configDir string) (map[string]any, bool) {
	g, ok := f.m[configDir]
	return g, ok
}

// pinned builds an adapter pinning one gate, standing in for claude.
func pinned(gate string, value bool) *agent.Adapter {
	return &agent.Adapter{
		Key: agent.KeyClaude, DisplayName: "Claude Code",
		VerifiedGates: []agent.VerifiedGate{{Name: gate, Value: value}},
	}
}

func oneDir(dir string) []gateDir { return []gateDir{{Account: defaultAccount, Dir: dir}} }

func TestCheckGatesMatchesPin(t *testing.T) {
	r := fakeGateReader{m: map[string]map[string]any{
		"/cfg": {"tengu_copper_thistle": false},
	}}
	got := CheckGates([]*agent.Adapter{pinned("tengu_copper_thistle", false)}, oneDir("/cfg"), r)

	require.Len(t, got, 1)
	assert.Equal(t, GateMatchesPin, got[0].State)
	assert.False(t, got[0].Actual)
	assert.Equal(t, "tengu_copper_thistle", got[0].Gate)
	assert.Equal(t, defaultAccount, got[0].Account)
}

func TestCheckGatesFlipped(t *testing.T) {
	r := fakeGateReader{m: map[string]map[string]any{
		"/cfg": {"tengu_copper_thistle": true},
	}}
	got := CheckGates([]*agent.Adapter{pinned("tengu_copper_thistle", false)}, oneDir("/cfg"), r)

	require.Len(t, got, 1)
	assert.Equal(t, GateFlipped, got[0].State)
	assert.False(t, got[0].Pinned, "pin is reported alongside the resolved value")
	assert.True(t, got[0].Actual)
	assert.True(t, GatesFlipped(got))
}

// TestCheckGatesUnknown covers every way a value can fail to be comparable. The
// assertion that matters is the second one: none of these may read as a flip. A
// spurious "your heuristics were verified on the other branch" would send someone
// re-driving the whole surface for nothing.
func TestCheckGatesUnknown(t *testing.T) {
	cases := []struct {
		name  string
		gates map[string]map[string]any
	}{
		{"dir absent", map[string]map[string]any{}},
		{"gate absent", map[string]map[string]any{"/cfg": {"tengu_other": true}}},
		{"gate explicitly null", map[string]map[string]any{"/cfg": {"tengu_copper_thistle": nil}}},
		{"value is a number", map[string]map[string]any{"/cfg": {"tengu_copper_thistle": float64(1)}}},
		{"value is a json.Number", map[string]map[string]any{"/cfg": {"tengu_copper_thistle": json.Number("1")}}},
		{"value is an object", map[string]map[string]any{"/cfg": {"tengu_copper_thistle": map[string]any{"on": true}}}},
		{"value is a string", map[string]map[string]any{"/cfg": {"tengu_copper_thistle": "true"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckGates([]*agent.Adapter{pinned("tengu_copper_thistle", false)},
				oneDir("/cfg"), fakeGateReader{m: tc.gates})

			require.Len(t, got, 1)
			assert.Equal(t, GateUnknown, got[0].State)
			assert.False(t, GatesFlipped(got), "an unreadable value must never read as a flip")
		})
	}
}

// TestCheckGatesPerAccount is the case no version pin can express: one claude
// version, two accounts, and only one of them on the branch we verified against.
func TestCheckGatesPerAccount(t *testing.T) {
	r := fakeGateReader{m: map[string]map[string]any{
		"/personal": {"tengu_copper_thistle": false},
		"/work":     {"tengu_copper_thistle": true},
	}}
	dirs := []gateDir{{Account: "personal", Dir: "/personal"}, {Account: "work", Dir: "/work"}}
	got := CheckGates([]*agent.Adapter{pinned("tengu_copper_thistle", false)}, dirs, r)

	require.Len(t, got, 2)
	assert.Equal(t, "personal", got[0].Account)
	assert.Equal(t, GateMatchesPin, got[0].State)
	assert.Equal(t, "work", got[1].Account)
	assert.Equal(t, GateFlipped, got[1].State)
}

func TestCheckGatesIgnoresAdaptersPinningNoGates(t *testing.T) {
	r := fakeGateReader{m: map[string]map[string]any{"/cfg": {"tengu_copper_thistle": true}}}
	adapters := []*agent.Adapter{
		{Key: agent.KeyGemini, DisplayName: "Gemini CLI", VerifiedVersion: "0.55.1"},
		{Key: agent.KeyCodex, DisplayName: "Codex"},
	}
	assert.Empty(t, CheckGates(adapters, oneDir("/cfg"), r),
		"an adapter with no gate-sensitive surface contributes no rows")
}

// TestInstalledGateDirsDedupes pins the labelling rule: an account pointing at the
// ambient dir is reported once, under its own name rather than "default", since
// that is the label a user routing sessions there would recognize.
func TestInstalledGateDirsDedupes(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/personal")
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/personal"},
		{Name: "work", ConfigDir: "/work"},
		{Name: "inherit", ConfigDir: ""},
	}}

	assert.Equal(t, []gateDir{
		{Account: "personal", Dir: "/personal"},
		{Account: "work", Dir: "/work"},
	}, installedGateDirs(cfg))
}

// TestInstalledGateDirsDedupesUncleanPath pairs with the dedupe above: the two
// paths being compared come from different places — doctor's own env and a
// hand-written config_dir — so the same dir arrives spelled two ways. Comparing
// raw strings would print it twice, once as "default" and once under the account,
// as if there were two accounts to reason about.
func TestInstalledGateDirsDedupesUncleanPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/personal")
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/personal/"},
		{Name: "work", ConfigDir: "/work/./"},
	}}

	assert.Equal(t, []gateDir{
		{Account: "personal", Dir: "/personal"},
		{Account: "work", Dir: "/work"},
	}, installedGateDirs(cfg))
}

// TestInstalledGateDirsKeepsEveryAccountSharingADir pins that dedupe never costs an
// account its row. Two accounts on one config dir share one gate state, so the value
// is reported twice — but under both names, because a row missing the name a user
// routes by would leave that account looking unchecked on the command whose whole
// job is saying which accounts were checked.
func TestInstalledGateDirsKeepsEveryAccountSharingADir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient")
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/shared"},
		{Name: "work", ConfigDir: "/shared"},
	}}

	assert.Equal(t, []gateDir{
		{Account: defaultAccount, Dir: "/ambient"},
		{Account: "personal", Dir: "/shared"},
		{Account: "work", Dir: "/shared"},
	}, installedGateDirs(cfg))
}

// TestInstalledGateDirsInheritEnvAccountRidesTheAmbientRow pins the one place the
// "a row per named dir" rule bottoms out. An inherit-env account names no dir, so
// it has no row of its own and rides the ambient one — and if a later account
// claims that row, the inherit account is reported under THAT account's name.
// Pinned because it looks like the label-drop bug this file exists to prevent and
// is not: both accounts read the same dir, so the value is right for both and only
// the name is missing. Giving inherit-env accounts their own row would fix the
// label; nobody has asked, and a wrong name here costs less than machinery.
func TestInstalledGateDirsInheritEnvAccountRidesTheAmbientRow(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/dev")
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "inherit", ConfigDir: ""},
		{Name: "work", ConfigDir: "/home/dev"},
	}}

	assert.Equal(t, []gateDir{{Account: "work", Dir: "/home/dev"}}, installedGateDirs(cfg),
		`"inherit" reads /home/dev too, and is reported by the row now named "work"`)
}

// TestInstalledGateDirsAccountsSharingTheAmbientDir collides the two rules above:
// only the FIRST account claims the ambient stand-in row, and the second is a real
// configured account like any other, so it gets its own row instead of evicting the
// first. The "default" label must not survive alongside them — it is the one label
// here that names nothing the user configured.
func TestInstalledGateDirsAccountsSharingTheAmbientDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/dev")
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/home/dev"},
		{Name: "work", ConfigDir: "/home/dev"},
	}}

	assert.Equal(t, []gateDir{
		{Account: "personal", Dir: "/home/dev"},
		{Account: "work", Dir: "/home/dev"},
	}, installedGateDirs(cfg))
}

func TestInstalledGateDirsAmbientOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/ambient")
	assert.Equal(t, []gateDir{{Account: defaultAccount, Dir: "/ambient"}},
		installedGateDirs(&config.Config{}),
		"with no claude_accounts configured the section stays a single row")
}

// TestFileGateReaderParses is the only test here that touches disk. The fixture
// keeps the real file's shape — the gate map holds bools beside numbers and nested
// objects, and the file holds unrelated top-level keys — so that "reads the map,
// ignores everything else" is pinned rather than assumed.
func TestFileGateReaderParses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{
	  "cachedGrowthBookFeatures": {
	    "tengu_copper_thistle": false,
	    "tengu_willow_sentinel_ttl_hours": 1,
	    "tengu_feedback_survey_config": {"probability": 0.05}
	  },
	  "cachedGrowthBookFeaturesAt": 1782824428458,
	  "projects": {}
	}`), 0o600))

	got, ok := fileGateReader{}.gates(dir)

	require.True(t, ok)
	assert.Equal(t, false, got["tengu_copper_thistle"])
	assert.Len(t, got, 3, "numbers and objects are read, then rejected at comparison time")
}

func TestFileGateReaderUnreadable(t *testing.T) {
	cases := map[string]string{
		"malformed":      `{`,
		"empty":          ``,
		"gates absent":   `{"projects": {}}`,
		"gates null":     `{"cachedGrowthBookFeatures": null}`,
		"gates reshaped": `{"cachedGrowthBookFeatures": ["tengu_copper_thistle"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))

			got, ok := fileGateReader{}.gates(dir)
			assert.False(t, ok)
			assert.Nil(t, got)
		})
	}
}

func TestFileGateReaderMissingFile(t *testing.T) {
	got, ok := fileGateReader{}.gates(t.TempDir())
	assert.False(t, ok, "no .claude.json just means claude is not onboarded in that dir")
	assert.Nil(t, got)
}

// TestFileGateReaderRejectsRelativeDir guards the one path that could manufacture a
// false flip: filepath.Join("", ".claude.json") is "./.claude.json", so an
// unresolvable home would silently report on whatever happens to sit in the
// caller's working directory. A stray true there would print "⚠ flipped" for a dir
// no session reads.
func TestFileGateReaderRejectsRelativeDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"cachedGrowthBookFeatures": {"tengu_copper_thistle": true}}`), 0o600))

	// Use a manual chdir instead of t.Chdir so the test skips gracefully when the
	// environment forbids directory changes (e.g. container restrictions), rather
	// than panicking during cleanup.
	prev, err := os.Getwd()
	if err != nil {
		t.Skip("cannot determine working directory")
	}
	if err := os.Chdir(dir); err != nil {
		t.Skip("chdir not available in this environment")
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	for _, configDir := range []string{"", ".", "relative/dir"} {
		got, ok := fileGateReader{}.gates(configDir)
		assert.False(t, ok, "gates(%q) read a cwd-relative file", configDir)
		assert.Nil(t, got)
	}
}

// TestInstalledGateDirsDropsUnresolvableAmbient pairs with the reader guard: with
// no home and no CLAUDE_CONFIG_DIR there is no ambient dir to speak of, so the row
// is dropped rather than carrying "" into the report.
func TestInstalledGateDirsDropsUnresolvableAmbient(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", "")

	assert.Empty(t, installedGateDirs(&config.Config{}))
}
