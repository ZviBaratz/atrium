package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// capDirWith writes the named files into a fresh temp dir and returns it (absolute).
// A nil body means "do not write this file at all", which is the case that must not
// be confused with an empty one.
func capDirWith(t *testing.T, files map[string]*string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if body == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(*body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func body(s string) *string { return &s }

func TestReadDirCapabilities(t *testing.T) {
	cases := []struct {
		name     string
		settings *string
		claude   *string
		want     DirCapabilities
		ok       bool
	}{{
		name: "both files, every field",
		settings: body(`{"enabledPlugins":{"b@m":true,"a@m":true},
		                 "extraKnownMarketplaces":{"m":{"source":{"source":"github"}}},
		                 "deniedMcpServers":["evil"],
		                 "disableClaudeAiConnectors":true}`),
		claude: body(`{"mcpServers":{"linear":{"type":"http"},"slack":{"type":"http"}}}`),
		want: DirCapabilities{
			EnabledPlugins:   []string{"a@m", "b@m"},
			Marketplaces:     []string{"m"},
			MCPServers:       []string{"linear", "slack"},
			DeniedMCPServers: []string{"evil"},
			ConnectorsOff:    true,
		},
		ok: true,
	}, {
		// The whole point of the probe: a dir configured with nothing is a readable
		// answer, not an unreadable one. Only then can it be diffed against a sibling.
		name:     "settings present but empty",
		settings: body(`{}`),
		want:     DirCapabilities{},
		ok:       true,
	}, {
		// An absent settings.json genuinely enables no plugins, so the dir is still
		// answerable off .claude.json alone.
		name:   "only .claude.json",
		claude: body(`{"mcpServers":{"linear":{}}}`),
		want:   DirCapabilities{MCPServers: []string{"linear"}},
		ok:     true,
	}, {
		name:     "only settings.json",
		settings: body(`{"enabledPlugins":{"a@m":true}}`),
		want:     DirCapabilities{EnabledPlugins: []string{"a@m"}},
		ok:       true,
	}, {
		// enabledPlugins maps a name to a bool, so a key carrying false is not a
		// plugin the dir has. Counting it would credit the dir with a capability it
		// does not have and, worse, hide the drift where one member has it on and
		// another has it off.
		name:     "a plugin set to false is not enabled",
		settings: body(`{"enabledPlugins":{"on@m":true,"off@m":false}}`),
		want:     DirCapabilities{EnabledPlugins: []string{"on@m"}},
		ok:       true,
	}, {
		name:     "connectors absent reads as on",
		settings: body(`{"enabledPlugins":{"a@m":true}}`),
		want:     DirCapabilities{EnabledPlugins: []string{"a@m"}},
		ok:       true,
	}, {
		name:     "connectors explicitly false reads as on",
		settings: body(`{"disableClaudeAiConnectors":false}`),
		want:     DirCapabilities{},
		ok:       true,
	}, {
		// Names arrive from a map, whose iteration order is randomized per run, so an
		// unsorted result would make two dirs holding the same plugins compare
		// unequal at random. Eight names make an accidental pass vanishingly unlikely.
		name: "keys come back sorted",
		settings: body(`{"enabledPlugins":{"h":true,"c":true,"a":true,"g":true,
		                 "d":true,"b":true,"f":true,"e":true}}`),
		want: DirCapabilities{EnabledPlugins: []string{"a", "b", "c", "d", "e", "f", "g", "h"}},
		ok:   true,
	}, {
		name:     "names are trimmed and deduplicated",
		settings: body(`{"deniedMcpServers":[" evil ","evil","","   "]}`),
		want:     DirCapabilities{DeniedMCPServers: []string{"evil"}},
		ok:       true,
	}, {
		// A container reshaped between claude versions still yields its names.
		name:     "an array-shaped plugin field still yields names",
		settings: body(`{"enabledPlugins":["a@m","b@m"]}`),
		want:     DirCapabilities{EnabledPlugins: []string{"a@m", "b@m"}},
		ok:       true,
	}, {
		// One reshaped field degrades that field alone; it must not take the readable
		// ones down with it, because a silent parity section reads as "in parity".
		name:     "a reshaped field loses only itself",
		settings: body(`{"enabledPlugins":7,"extraKnownMarketplaces":{"m":{}}}`),
		want:     DirCapabilities{Marketplaces: []string{"m"}},
		ok:       true,
	}, {
		name:     "null fields read as empty",
		settings: body(`{"enabledPlugins":null,"deniedMcpServers":null}`),
		want:     DirCapabilities{},
		ok:       true,
	}, {
		name:     "malformed settings.json",
		settings: body(`{"enabledPlugins":`),
		claude:   body(`{"mcpServers":{"linear":{}}}`),
		ok:       false,
	}, {
		name:     "malformed .claude.json",
		settings: body(`{"enabledPlugins":{"a@m":true}}`),
		claude:   body(`not json at all`),
		ok:       false,
	}, {
		name:     "settings.json is a top-level array",
		settings: body(`["nope"]`),
		ok:       false,
	}, {
		// Neither file: claude was never onboarded here. Reporting "has nothing"
		// would accuse every pool containing this dir of drift the user cannot fix.
		name: "neither file",
		ok:   false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := capDirWith(t, map[string]*string{
				"settings.json": tc.settings,
				".claude.json":  tc.claude,
			})
			got, ok := ReadDirCapabilities(dir)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (caps %+v)", ok, tc.ok, got)
			}
			if !tc.ok {
				if !reflect.DeepEqual(got, DirCapabilities{}) {
					t.Errorf("failed read returned %+v, want zero value", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("caps = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadDirCapabilitiesMissingDir(t *testing.T) {
	if _, ok := ReadDirCapabilities(filepath.Join(t.TempDir(), "absent")); ok {
		t.Error("a nonexistent dir read as capability-bearing")
	}
}

// A relative or empty dir must be refused, not joined against the process's working
// directory. "" is the routing value for "inherit the ambient env", so resolving it
// to ./settings.json would report on a dir no session has any relationship to.
//
// The cwd is stocked with readable files so a regression has something to find:
// without them this would pass just as well against a broken guard.
func TestReadDirCapabilitiesRefusesRelativeDir(t *testing.T) {
	t.Chdir(capDirWith(t, map[string]*string{
		"settings.json": body(`{"enabledPlugins":{"bait@m":true}}`),
		".claude.json":  body(`{"mcpServers":{"bait":{}}}`),
	}))

	// Control: the bait is real and readable when addressed absolutely.
	abs, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if _, ok := ReadDirCapabilities(abs); !ok {
		t.Fatal("bait files are not readable; the rest of this test proves nothing")
	}

	for _, dir := range []string{"", ".", "./", "relative/sub"} {
		if got, ok := ReadDirCapabilities(dir); ok {
			t.Errorf("ReadDirCapabilities(%q) read the working directory: %+v", dir, got)
		}
	}
}

// The probe is strictly read-only: it must not seed a settings.json, a .claude.json
// or anything else in a dir it merely inspected. LoadConfig in this same package
// does seed, which is exactly why this needs asserting rather than assuming.
func TestReadDirCapabilitiesCreatesNothing(t *testing.T) {
	empty := t.TempDir()
	if _, ok := ReadDirCapabilities(empty); ok {
		t.Fatal("an empty dir read as capability-bearing")
	}
	if names := entries(t, empty); len(names) != 0 {
		t.Errorf("reading an empty dir left %v behind", names)
	}

	partial := capDirWith(t, map[string]*string{
		"settings.json": body(`{"enabledPlugins":{"a@m":true}}`),
	})
	before := entries(t, partial)
	if _, ok := ReadDirCapabilities(partial); !ok {
		t.Fatal("a dir with settings.json read as unreadable")
	}
	if after := entries(t, partial); !reflect.DeepEqual(before, after) {
		t.Errorf("dir contents changed: %v → %v", before, after)
	}
	// Specifically the file the probe looked for and did not find.
	if _, err := os.Stat(filepath.Join(partial, ".claude.json")); err == nil {
		t.Error("the probe created the .claude.json it failed to read")
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	sort.Strings(names)
	return names
}

// ReadCapabilities resolves the account's own config_dir, and an inherit-env account
// has none: it injects no CLAUDE_CONFIG_DIR, so there is no dir of its own whose
// capabilities could be compared against a pool sibling's.
func TestClaudeAccountReadCapabilities(t *testing.T) {
	dir := capDirWith(t, map[string]*string{
		"settings.json": body(`{"enabledPlugins":{"a@m":true}}`),
	})

	got, ok := ClaudeAccount{Name: "work", ConfigDir: dir}.ReadCapabilities()
	if !ok {
		t.Fatal("an account naming a readable dir reported ok=false")
	}
	if !reflect.DeepEqual(got.EnabledPlugins, []string{"a@m"}) {
		t.Errorf("EnabledPlugins = %v, want [a@m]", got.EnabledPlugins)
	}

	// A trailing slash is not a different dir — NormalizedConfigDir cleans it, so the
	// same dir spelled two ways reads identically and cannot be reported as two.
	slashed, ok := ClaudeAccount{Name: "work", ConfigDir: dir + "/"}.ReadCapabilities()
	if !ok || !reflect.DeepEqual(slashed, got) {
		t.Errorf("trailing slash read differently: %+v (ok=%v) vs %+v", slashed, ok, got)
	}

	if _, ok := (ClaudeAccount{Name: "ambient"}).ReadCapabilities(); ok {
		t.Error("an inherit-env account reported capabilities")
	}
}
