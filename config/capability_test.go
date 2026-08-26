package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// dimWant is what one dimension should come back as. measured=false is the case the
// whole type exists for, and the names of an unmeasured dimension are never read, so
// a case asserting one asserts the flag rather than an empty list.
type dimWant struct {
	measured bool
	names    []string
}

func measured(names ...string) dimWant { return dimWant{measured: true, names: names} }

var unmeasured = dimWant{}

func (w dimWant) check(t *testing.T, label string, got DimensionState) {
	t.Helper()
	if got.Measured != w.measured {
		t.Errorf("%s: Measured = %v, want %v (names %v)", label, got.Measured, w.measured, got.Names())
		return
	}
	if !w.measured {
		return
	}
	want := w.names
	if want == nil {
		want = []string{}
	}
	if gotNames := got.Names(); !reflect.DeepEqual(gotNames, want) {
		t.Errorf("%s: names = %v, want %v", label, gotNames, want)
	}
}

func TestReadDirCapabilities(t *testing.T) {
	cases := []struct {
		name         string
		settings     *string
		local        *string
		claude       *string
		plugins      dimWant
		marketplaces dimWant
		mcp          dimWant
		connectors   ConnectorState
		ok           bool
	}{{
		name: "every field, from both files",
		settings: body(`{"enabledPlugins":{"b@m":true,"a@m":true},
		                 "extraKnownMarketplaces":{"m":{"source":{"source":"github"}}},
		                 "disableClaudeAiConnectors":true}`),
		claude: body(`{"projects":{"/p":{"mcpServers":{"linear":{"type":"http"},
		                                               "slack":{"type":"http"}}}}}`),
		plugins:      measured("a@m", "b@m"),
		marketplaces: measured("m"),
		mcp:          measured("linear", "slack"),
		connectors:   ConnectorsOff,
		ok:           true,
	}, {
		// The whole point of the probe: a dir configured with nothing is a readable
		// answer, not an unreadable one. Only then can it be diffed against a sibling.
		// MCP servers stay UNMEASURED, because the file that records them is absent.
		name:         "settings present but empty",
		settings:     body(`{}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// The inverse, and the bug this shape was built to fix: mcpServers lives only
		// in .claude.json, so a dir without one has an UNKNOWN MCP set. Reported as
		// empty, such a dir gets accused of lacking every server its sibling has.
		name:         "only settings.json leaves MCP unknown",
		settings:     body(`{"enabledPlugins":{"a@m":true}}`),
		plugins:      measured("a@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "only .claude.json leaves settings dimensions unknown",
		claude:       body(`{"projects":{"/p":{"mcpServers":{"linear":{}}}}}`),
		plugins:      unmeasured,
		marketplaces: unmeasured,
		mcp:          measured("linear"),
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		// A top-level mcpServers key is read as well as the per-project scopes, so a
		// dir written either way answers.
		name:         "mcpServers at the top level and under projects",
		claude:       body(`{"mcpServers":{"top":{}},"projects":{"/a":{"mcpServers":{"a":{}}},"/b":{"mcpServers":{"b":{}}}}}`),
		plugins:      unmeasured,
		marketplaces: unmeasured,
		mcp:          measured("a", "b", "top"),
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		name:         "a project scope with no mcpServers contributes nothing",
		claude:       body(`{"projects":{"/a":{"allowedTools":[]},"/b":{"mcpServers":{}}}}`),
		plugins:      unmeasured,
		marketplaces: unmeasured,
		mcp:          measured(),
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		// enabledPlugins maps a name to a bool, so a key carrying false is not a
		// plugin the dir has. Counting it would credit the dir with a capability it
		// does not have and, worse, hide the drift where one member has it on and
		// another has it off.
		name:         "a plugin set to false or null is not enabled",
		settings:     body(`{"enabledPlugins":{"on@m":true,"off@m":false,"nulled@m":null}}`),
		plugins:      measured("on@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "connectors absent reads as on",
		settings:     body(`{"enabledPlugins":{"a@m":true}}`),
		plugins:      measured("a@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "connectors explicitly false reads as on",
		settings:     body(`{"disableClaudeAiConnectors":false}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// A value that is neither JSON true nor false cannot be read as either state.
		// Guessing "on" here would fabricate a split against a sibling that really is
		// off, and guessing "off" would hide one.
		name:         "a reshaped connector setting is unknown, not a state",
		settings:     body(`{"disableClaudeAiConnectors":"true"}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		// Names arrive from a map, whose iteration order is randomized per run, so an
		// unsorted result would make two dirs holding the same plugins compare
		// unequal at random. Eight names make an accidental pass vanishingly unlikely.
		name: "names come back sorted",
		settings: body(`{"enabledPlugins":{"h":true,"c":true,"a":true,"g":true,
		                 "d":true,"b":true,"f":true,"e":true}}`),
		plugins:      measured("a", "b", "c", "d", "e", "f", "g", "h"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// Names are NOT trimmed. claude looks a plugin up by its literal key, so
		// " x " and "x" are two different plugins; normalising them together invented
		// an equivalence claude does not have, and — because the exclusion of a false
		// key ran before the trim — let a padded true spelling resurrect a plugin the
		// same file explicitly disables.
		name:         "a padded key is a different name, and cannot revive a disabled one",
		settings:     body(`{"enabledPlugins":{"retired@obra":false," retired@obra ":true}}`),
		plugins:      measured(" retired@obra "),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "a blank key names nothing and is skipped",
		settings:     body(`{"enabledPlugins":{"":true,"   ":true,"real@m":true}}`),
		plugins:      measured("real@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "a null field reads as configured with nothing",
		settings:     body(`{"enabledPlugins":null,"extraKnownMarketplaces":null}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// A field reshaped between claude versions makes ITS dimension unknown, not
		// empty, and must not take the still-readable dimensions down with it: a
		// silent parity section reads as "these accounts agree".
		name:         "a reshaped field is unknown and loses only itself",
		settings:     body(`{"enabledPlugins":["a@m","b@m"],"extraKnownMarketplaces":{"m":{}}}`),
		plugins:      unmeasured,
		marketplaces: measured("m"),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "an unrecognised plugin value is unknown, not off",
		settings:     body(`{"enabledPlugins":{"a@m":7}}`),
		plugins:      unmeasured,
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "an unrecognised marketplace value is unknown, not off",
		settings:     body(`{"extraKnownMarketplaces":{"m":7}}`),
		plugins:      measured(),
		marketplaces: unmeasured,
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "a marketplace set to null or false is off, not unknown",
		settings:     body(`{"extraKnownMarketplaces":{"gone":null,"off":false,"on":{"source":{}}}}`),
		plugins:      measured(),
		marketplaces: measured("on"),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "a reshaped projects map makes MCP unknown",
		claude:       body(`{"projects":["/a"]}`),
		plugins:      unmeasured,
		marketplaces: unmeasured,
		mcp:          unmeasured,
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		name:         "a reshaped project mcpServers makes MCP unknown",
		claude:       body(`{"projects":{"/a":{"mcpServers":["linear"]}}}`),
		plugins:      unmeasured,
		marketplaces: unmeasured,
		mcp:          unmeasured,
		connectors:   ConnectorsUnknown,
		ok:           true,
	}, {
		// claude layers settings.local.json over settings.json in the same scope, so
		// a plugin enabled only in the local file really is enabled. Reading the base
		// file alone reported this dir as lacking what it has.
		name:         "settings.local.json is layered over settings.json",
		settings:     body(`{"enabledPlugins":{"base@m":true,"both@m":false}}`),
		local:        body(`{"enabledPlugins":{"both@m":true,"local@m":true}}`),
		plugins:      measured("both@m", "local@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "settings.local.json alone answers the settings dimensions",
		local:        body(`{"enabledPlugins":{"local@m":true},"disableClaudeAiConnectors":true}`),
		plugins:      measured("local@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOff,
		ok:           true,
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
		name:     "malformed settings.local.json",
		settings: body(`{"enabledPlugins":{"a@m":true}}`),
		local:    body(`{`),
		ok:       false,
	}, {
		name:     "settings.json is a top-level array",
		settings: body(`["nope"]`),
		ok:       false,
	}, {
		// The literal null is the one top-level non-object that unmarshals into a map
		// without error, so without an explicit check a truncated-to-null file read as
		// "configured with nothing" and every sibling capability became drift.
		name:     "settings.json is the literal null",
		settings: body(`null`),
		ok:       false,
	}, {
		name:     "settings.json is the literal null with whitespace",
		settings: body("  null\n"),
		ok:       false,
	}, {
		name:   ".claude.json is the literal null",
		claude: body(`null`),
		ok:     false,
	}, {
		// Neither file: claude was never onboarded here. Reporting "has nothing"
		// would accuse every pool containing this dir of drift the user cannot fix.
		name: "no files at all",
		ok:   false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := capDirWith(t, map[string]*string{
				"settings.json":       tc.settings,
				"settings.local.json": tc.local,
				".claude.json":        tc.claude,
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
			tc.plugins.check(t, "plugins", got.Plugins)
			tc.marketplaces.check(t, "marketplaces", got.Marketplaces)
			tc.mcp.check(t, "mcpServers", got.MCPServers)
			if got.Connectors != tc.connectors {
				t.Errorf("connectors = %v, want %v", got.Connectors, tc.connectors)
			}
		})
	}
}

// Two dirs can hold the same names and still not be substitutes: the marketplace or
// MCP server one of them points at can be a different repo, URL or command
// altogether. Comparing names alone certified those as interchangeable.
func TestTargetsCompareTheConfiguredValue(t *testing.T) {
	linear := func(url string) string {
		return `{"projects":{"/p":{"mcpServers":{"linear":{"type":"http","url":"` + url + `"}}}}}`
	}
	genuine, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
		".claude.json": body(linear("https://mcp.linear.app/mcp")),
	}))
	if !ok {
		t.Fatal("the genuine dir did not read")
	}
	evil, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
		".claude.json": body(linear("https://evil.example/mcp")),
	}))
	if !ok {
		t.Fatal("the rewired dir did not read")
	}

	if !genuine.MCPServers.Has("linear") || !evil.MCPServers.Has("linear") {
		t.Fatal("both dirs must configure linear for this to be about the target")
	}
	if genuine.MCPServers.Target("linear") == "" {
		t.Fatal("a server configured with an object must carry a comparable target")
	}
	if genuine.MCPServers.Target("linear") == evil.MCPServers.Target("linear") {
		t.Error("two different URLs produced the same target, so a rewired server compares equal")
	}
}

// Key order and whitespace are not drift. Without canonicalisation, two dirs
// configured identically but written by different claude versions would be reported
// as diverging, and the user would be sent to diff two files that agree.
func TestTargetsIgnoreKeyOrderAndWhitespace(t *testing.T) {
	a, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
		"settings.json": body(`{"extraKnownMarketplaces":{"m":{"source":{"source":"github","repo":"o/p"}}}}`),
	}))
	if !ok {
		t.Fatal("dir a did not read")
	}
	b, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
		"settings.json": body("{\"extraKnownMarketplaces\":{\"m\":{\n  \"source\": {\"repo\":\"o/p\",\n  \"source\":\"github\"}\n}}}"),
	}))
	if !ok {
		t.Fatal("dir b did not read")
	}
	if a.Marketplaces.Target("m") == "" {
		t.Fatal("no target was recorded, so this compares two blanks")
	}
	if a.Marketplaces.Target("m") != b.Marketplaces.Target("m") {
		t.Errorf("reordered keys read as drift:\n a = %s\n b = %s",
			a.Marketplaces.Target("m"), b.Marketplaces.Target("m"))
	}
}

// One server configured under two project scopes has no single target, so the two
// are carried together. Two dirs written the same way must still compare equal —
// otherwise every multi-project dir reports drift against its own twin.
func TestTargetsMergeAcrossProjectScopes(t *testing.T) {
	twoScopes := `{"projects":{
		"/a":{"mcpServers":{"linear":{"url":"https://one.example"}}},
		"/b":{"mcpServers":{"linear":{"url":"https://two.example"}}}}}`
	one, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{".claude.json": body(twoScopes)}))
	if !ok {
		t.Fatal("first dir did not read")
	}
	two, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{".claude.json": body(twoScopes)}))
	if !ok {
		t.Fatal("second dir did not read")
	}
	if one.MCPServers.Target("linear") != two.MCPServers.Target("linear") {
		t.Error("identical multi-scope dirs produced different targets")
	}
	single, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
		".claude.json": body(`{"projects":{"/a":{"mcpServers":{"linear":{"url":"https://one.example"}}}}}`),
	}))
	if !ok {
		t.Fatal("single-scope dir did not read")
	}
	if single.MCPServers.Target("linear") == one.MCPServers.Target("linear") {
		t.Error("a dir configuring linear once matched one configuring it two ways")
	}
}

func TestReadDirCapabilitiesMissingDir(t *testing.T) {
	if _, ok := ReadDirCapabilities(filepath.Join(t.TempDir(), "absent")); ok {
		t.Error("a nonexistent dir read as capability-bearing")
	}
}

// A file that exists but cannot be read is an unanswered question, not an empty
// capability set — the distinction the whole type is built around, and the one no
// fixture can create because git cannot track an unreadable file. Injecting the
// failure is the only way this branch is ever taken.
func TestReadDirCapabilitiesUnreadableFileIsNotEmpty(t *testing.T) {
	t.Run("a directory standing where the file should be", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, ok := ReadDirCapabilities(dir); ok {
			t.Errorf("a dir standing in for settings.json read as answerable: %+v", got)
		}
	})

	t.Run("a file that cannot be opened", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores the permission bits this case turns on")
		}
		if runtime.GOOS == "windows" {
			t.Skip("mode bits do not deny reads here")
		}
		dir := capDirWith(t, map[string]*string{"settings.json": body(`{"enabledPlugins":{}}`)})
		path := filepath.Join(dir, "settings.json")
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		// Control: the file is there, so this is about the read failing and not
		// about an absent file taking the same branch.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the file must exist for this case to mean anything: %v", err)
		}
		if got, ok := ReadDirCapabilities(dir); ok {
			t.Errorf("an unreadable settings.json read as answerable: %+v", got)
		}
	})
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
		".claude.json":  body(`{"projects":{"/p":{"mcpServers":{"bait":{}}}}}`),
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
	// Specifically the files the probe looked for and did not find.
	for _, name := range []string{".claude.json", "settings.local.json"} {
		if _, err := os.Stat(filepath.Join(partial, name)); err == nil {
			t.Errorf("the probe created the %s it failed to read", name)
		}
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

// State is what lets a comparison iterate Dimensions() instead of naming fields, so
// a fourth axis cannot be added without the differ seeing it. That only holds while
// every listed dimension is actually wired to a field: a missing arm returns the
// zero DimensionState, which reads as "unmeasured" and would silently drop the axis
// from every comparison.
func TestStateCoversEveryDimension(t *testing.T) {
	caps := DirCapabilities{
		Plugins:      DimensionState{Measured: true, Targets: map[string]string{"p": ""}},
		Marketplaces: DimensionState{Measured: true, Targets: map[string]string{"m": ""}},
		MCPServers:   DimensionState{Measured: true, Targets: map[string]string{"s": ""}},
	}
	want := map[Dimension]string{
		DimensionPlugin:      "p",
		DimensionMarketplace: "m",
		DimensionMCPServer:   "s",
	}
	if len(Dimensions()) != len(want) {
		t.Fatalf("Dimensions() = %v, but this test knows %d of them", Dimensions(), len(want))
	}
	seen := map[Dimension]bool{}
	for _, d := range Dimensions() {
		if d == DimensionUnspecified {
			t.Error("Dimensions() lists the unspecified zero value")
		}
		if seen[d] {
			t.Errorf("Dimensions() lists %v twice", d)
		}
		seen[d] = true
		if !caps.State(d).Has(want[d]) {
			t.Errorf("State(%v) did not return the field holding %q", d, want[d])
		}
		if d.Noun() == "" || d.Noun() == DimensionUnspecified.Noun() {
			t.Errorf("dimension %v has no noun of its own (%q)", d, d.Noun())
		}
	}
	if caps.State(DimensionUnspecified).Measured {
		t.Error("the unspecified dimension reported a measured state")
	}

	// The denial axis is wired to a field but deliberately left out of Dimensions():
	// a generic name diff over a denial list answers the wrong question, so its
	// comparison is a separate pass. Both halves of that need holding — the field
	// reachable through State, and the dimension absent from the generic walk.
	denied := DirCapabilities{DeniedMCPServers: DimensionState{
		Measured: true, Targets: map[string]string{"evil": ""},
	}}
	if !denied.State(DimensionDeniedMCPServer).Has("evil") {
		t.Error("State(DimensionDeniedMCPServer) is not wired to DeniedMCPServers")
	}
	if seen[DimensionDeniedMCPServer] {
		t.Error("Dimensions() lists the denial axis, which the generic name diff must not walk")
	}
}

// deniedMcpServers is a per-dir key, not managed-settings-only: claude's own
// allowManagedMcpServersOnly says the denylist "still merges from all sources, so
// users can deny servers for themselves". It holds an array, so it needs its own
// shape table — and an entry this build cannot read must make the whole list unknown,
// because a denial it cannot honour reported as absent says the member allows a
// server it blocks.
func TestReadDirCapabilitiesDeniedMCPServers(t *testing.T) {
	cases := []struct {
		name     string
		settings *string
		local    *string
		want     dimWant
	}{
		{"absent key", body(`{}`), nil, measured()},
		{"null", body(`{"deniedMcpServers":null}`), nil, measured()},
		{"a list of names", body(`{"deniedMcpServers":["evil","worse"]}`), nil, measured("evil", "worse")},
		{"blanks skipped, duplicates collapsed", body(`{"deniedMcpServers":["evil","evil","","  "]}`), nil, measured("evil")},
		{"a non-string entry makes the list unknown", body(`{"deniedMcpServers":["ok",7]}`), nil, unmeasured},
		{"an object entry makes the list unknown", body(`{"deniedMcpServers":["ok",{"name":"x"}]}`), nil, unmeasured},
		{"an object instead of a list is unknown", body(`{"deniedMcpServers":{"evil":true}}`), nil, unmeasured},
		{"from settings.local.json", nil, body(`{"deniedMcpServers":["evil"]}`), measured("evil")},
		// No settings file at all: the key's home is absent, so the answer is unknown
		// rather than "denies nothing".
		{"no settings file", nil, nil, unmeasured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := capDirWith(t, map[string]*string{
				"settings.json":       tc.settings,
				"settings.local.json": tc.local,
				// Keeps the dir answerable in the "no settings file" case.
				".claude.json": body(`{}`),
			})
			got, ok := ReadDirCapabilities(dir)
			if !ok {
				t.Fatalf("dir did not read")
			}
			tc.want.check(t, "deniedMcpServers", got.DeniedMCPServers)
		})
	}
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
	if !reflect.DeepEqual(got.Plugins.Names(), []string{"a@m"}) {
		t.Errorf("plugins = %v, want [a@m]", got.Plugins.Names())
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
