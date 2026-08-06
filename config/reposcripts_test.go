package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRepoScript pins the routing rules against the same table shape the
// account resolvers use, because it is deliberately the same matcher: remote
// substrings first, then target path, per entry in config order, and the first entry
// with no rules at all is the catch-all.
func TestResolveRepoScript(t *testing.T) {
	web := RepoScript{Name: "web", SetupScript: "npm ci",
		RemoteMatches: []string{"acme/web"},
		PathMatches:   []string{"/projects/web/"}}
	fallback := RepoScript{Name: "fallback", SetupScript: "make deps"} // no rules → catch-all

	cases := []struct {
		name    string
		entries []RepoScript
		remote  string
		path    string
		want    string // the resolved entry's Name, "" for no entry
		wantIdx int    // its position in the configured list; -1 when nothing resolved
	}{
		{"unconfigured", nil, "https://github.com/acme/web.git", "/projects/web/x", "", -1},
		{"remote match", []RepoScript{web, fallback}, "https://github.com/acme/web.git", "", "web", 0},
		{"case-insensitive remote", []RepoScript{web, fallback}, "https://github.com/ACME/Web.git", "", "web", 0},
		{"path match for a direct session", []RepoScript{web, fallback}, "", "/home/z/projects/web/app", "web", 0},
		{"no match falls to the rule-less entry", []RepoScript{web, fallback}, "https://github.com/other/x.git", "/tmp/x", "fallback", 1},
		{"no match and no catch-all", []RepoScript{web}, "https://github.com/other/x.git", "/tmp/x", "", -1},
		{"empty remote and path take the catch-all", []RepoScript{web, fallback}, "", "", "fallback", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{RepoScripts: tc.entries}

			got, idx, ok := c.ResolveRepoScript(tc.remote, tc.path)

			if tc.want == "" {
				assert.False(t, ok, "expected no entry, got %q", got.Name)
				assert.Equal(t, -1, idx, "a resolution that found nothing must not report a position")
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.want, got.Name)
			// The index is what a message about this entry is found by: a problem
			// reported as repo_scripts[0] when the broken entry is the second points the
			// user at an innocent one.
			assert.Equal(t, tc.wantIdx, idx)
		})
	}
}

// First match wins when two entries could both claim a repo.
func TestResolveRepoScript_FirstMatchWins(t *testing.T) {
	a := RepoScript{Name: "a", SetupScript: "x", RemoteMatches: []string{"acme"}}
	b := RepoScript{Name: "b", SetupScript: "y", RemoteMatches: []string{"acme"}}

	got, idx, ok := (&Config{RepoScripts: []RepoScript{a, b}}).ResolveRepoScript("https://x/acme/r.git", "")

	require.True(t, ok)
	assert.Equal(t, "a", got.Name)
	assert.Equal(t, 0, idx)
}

func TestRepoScript_IsCatchAll(t *testing.T) {
	assert.True(t, RepoScript{}.IsCatchAll())
	assert.False(t, RepoScript{RemoteMatches: []string{"x"}}.IsCatchAll())
	assert.False(t, RepoScript{PathMatches: []string{"x"}}.IsCatchAll())
}

// The section survives a save/load cycle intact, including the map — the guard that
// a hand-written config is not quietly rewritten into a different one.
func TestRepoScriptsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".atrium"), 0o755))

	want := []RepoScript{{
		Name:          "web",
		RemoteMatches: []string{"acme/web"},
		PathMatches:   []string{"/projects/web/"},
		SetupScript:   "npm ci",
		SessionEnv:    map[string]string{"CACHE_DIR": "/tmp/{{.Session.Name}}"},
	}}
	cfg := DefaultConfig()
	cfg.RepoScripts = want
	require.NoError(t, SaveConfig(cfg))

	got := LoadConfig()

	assert.Equal(t, want, got.RepoScripts)
}

// An absent section decodes to nil, so a config written before the feature existed
// means "no repo scripts" rather than an empty entry that matches everything.
func TestRepoScriptsAbsentDecodesToNil(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".atrium"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium", ConfigFileName),
		[]byte(`{"default_program":"claude"}`), 0o644))

	got := LoadConfig()

	assert.Nil(t, got.RepoScripts)
	_, _, ok := got.ResolveRepoScript("https://github.com/acme/web.git", "/projects/web")
	assert.False(t, ok)
}

// DefaultConfig ships no entries: the feature is dormant until configured.
func TestDefaultConfigHasNoRepoScripts(t *testing.T) {
	assert.Nil(t, DefaultConfig().RepoScripts)
}

// Guard for the json tag itself, which the README table and the settings-panel
// exemption both spell out by hand.
func TestRepoScriptsJSONTag(t *testing.T) {
	b, err := json.Marshal(Config{RepoScripts: []RepoScript{{Name: "web", SetupScript: "x"}}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"repo_scripts":[{"name":"web","setup_script":"x"}]`)
}
