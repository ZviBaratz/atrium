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
		claude: body(`{"mcpServers":{"linear":{"type":"http"},
		                             "slack":{"type":"http"}}}`),
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
		// The ordinary onboarded dir: claude writes .claude.json at login and creates
		// settings.json only once something is configured. An ABSENT settings.json is
		// an answer — claude's defaults are no plugins, no marketplaces, connectors
		// on — so these axes are measured and empty. Gating them on the file existing
		// turned the common case into three "unverified" lines against an identical
		// sibling, and masked a real connector split behind one of them.
		name:         "only .claude.json still answers the settings dimensions",
		claude:       body(`{"mcpServers":{"linear":{}}}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          measured("linear"),
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// The compared axis is the dir-wide scope — the top-level key. A
		// projects.<path>.mcpServers entry is claude's LOCAL scope, available in that
		// one checkout, so it is not a capability of the dir; mcpServerState says why
		// counting it measured how much each dir had been used instead.
		name:         "a project scope is local to that project and is not counted",
		claude:       body(`{"mcpServers":{"top":{}},"projects":{"/a":{"mcpServers":{"a":{}}},"/b":{"mcpServers":{"b":{}}}}}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          measured("top"),
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// And a dir whose only MCP servers are local-scope is measured and EMPTY, not
		// unknown: the file was read and it declares nothing dir-wide.
		name:         "local scopes alone leave the dir-wide set empty, not unknown",
		claude:       body(`{"projects":{"/a":{"allowedTools":[]},"/b":{"mcpServers":{"b":{}}}}}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          measured(),
		connectors:   ConnectorsOn,
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
		// The projects key is not read at all now, so no shape it takes can make the
		// dir-wide axis unknown — including one this build would not recognise.
		name:         "a reshaped projects map does not reach the compared axis",
		claude:       body(`{"mcpServers":{"top":{}},"projects":["/a"]}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          measured("top"),
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// The top-level key is the one that still must: a shape this build cannot read
		// there is an unanswered question, never an empty set.
		name:         "a reshaped top-level mcpServers makes MCP unknown",
		claude:       body(`{"mcpServers":["linear"]}`),
		plugins:      measured(),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// settings.local.json in a CONFIG dir is not a claude source. claude resolves
		// its localSettings source to .claude/settings.local.json against the project
		// root and labels it "project, gitignored". An earlier version read it here
		// and layered it over settings.json, which spoke for the dir out of a file
		// claude never consults there — and layered it whole-key, where claude merges
		// enabledPlugins per plugin.
		name:         "settings.local.json in the config dir is not read",
		settings:     body(`{"enabledPlugins":{"base@m":true}}`),
		local:        body(`{"enabledPlugins":{"local@m":true},"disableClaudeAiConnectors":true}`),
		plugins:      measured("base@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// And it cannot make the dir answerable on its own, which is the half that
		// matters: a dir holding only that file is a dir claude was never onboarded
		// in, and it used to be spoken for on all four axes.
		name:  "settings.local.json alone is not a claude config dir",
		local: body(`{"enabledPlugins":{"local@m":true}}`),
		ok:    false,
	}, {
		// enabledPlugins values are typed array<string> | boolean | object: an array
		// carries version constraints and an object is the extended format. Accepting
		// only true/false/null made ONE such entry take the whole dimension
		// unmeasured and discard every readable entry beside it.
		name:         "a version constraint and an extended-format object are enabled",
		settings:     body(`{"enabledPlugins":{"plain@m":true,"pinned@m":["^1.2"],"extended@m":{"version":"2.0"}}}`),
		plugins:      measured("extended@m", "pinned@m", "plain@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		name:         "an array of non-strings is a shape this build cannot read",
		settings:     body(`{"enabledPlugins":{"a@m":[7]}}`),
		plugins:      unmeasured,
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// additionalMarketplaces is claude's documented alias, "read exactly as if it
		// were spelled extraKnownMarketplaces". Reading only the canonical key made
		// two dirs claude treats identically report a marketplace one has and the
		// other lacks.
		name:         "additionalMarketplaces is read as extraKnownMarketplaces",
		settings:     body(`{"additionalMarketplaces":{"obra":{"source":{"repo":"obra/superpowers"}}}}`),
		plugins:      measured(),
		marketplaces: measured("obra"),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
	}, {
		// claude ignores the alias with a warning when both appear in one file, so
		// the canonical key wins here too.
		name:         "the canonical marketplace key wins over the alias",
		settings:     body(`{"extraKnownMarketplaces":{"canonical":{"source":{}}},"additionalMarketplaces":{"alias":{"source":{}}}}`),
		plugins:      measured(),
		marketplaces: measured("canonical"),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
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
		// Unreadable only matters for a file this probe reads. A broken
		// settings.local.json belongs to a checkout, and refusing the whole dir over
		// it would report a dir claude reads fine as unanswerable.
		name:         "a malformed settings.local.json is ignored, not fatal",
		settings:     body(`{"enabledPlugins":{"a@m":true}}`),
		local:        body(`{`),
		plugins:      measured("a@m"),
		marketplaces: measured(),
		mcp:          unmeasured,
		connectors:   ConnectorsOn,
		ok:           true,
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
		return `{"mcpServers":{"linear":{"type":"http","url":"` + url + `"}}}`
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

// Nor is the SPELLING of a number. UseNumber keeps a literal's digits, which is what
// stops two values past 2^53 from colliding — and, left there, made 100 and 1e2
// fingerprint apart, so two dirs configuring one server identically were reported as
// "same name, different target" and the user was sent to align dirs that agree.
func TestTargetsIgnoreNumberSpelling(t *testing.T) {
	target := func(n string) string {
		t.Helper()
		got, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
			".claude.json": body(`{"mcpServers":{"linear":{"timeout":` + n + `}}}`),
		}))
		if !ok {
			t.Fatalf("dir with timeout %s did not read", n)
		}
		if got.MCPServers.Target("linear") == "" {
			t.Fatalf("timeout %s produced no target, which a differ reads as no difference", n)
		}
		return got.MCPServers.Target("linear")
	}

	for _, same := range [][2]string{
		{"100", "1e2"},
		{"100", "1.0e2"},
		{"1", "1.0"},
		{"0.1", "0.10"},
		{"0", "-0"},
		{"0", "0.000"},
		{"-5", "-5.0"},
		{"12345678901234567890", "1.234567890123456789e19"},
	} {
		if a, b := target(same[0]), target(same[1]); a != b {
			t.Errorf("%s and %s are the same number but fingerprinted %q vs %q", same[0], same[1], a, b)
		}
	}
	// Canonicalising must not go through a float, or it re-collides what UseNumber was
	// added to keep apart. TestTargetsDoNotRoundNumbers holds the wider range; these
	// are the pairs one canonical spelling could most easily merge.
	for _, differ := range [][2]string{
		{"1e2", "1e3"},
		{"0.1", "0.01"},
		{"5", "-5"},
		{"9007199254740993", "9007199254740992"},
	} {
		if a, b := target(differ[0]), target(differ[1]); a == b {
			t.Errorf("%s and %s are different numbers but both fingerprinted %q", differ[0], differ[1], a)
		}
	}
}

// The canonical key wins over its alias only when it holds a value. claude's resolver
// promotes the alias unless the canonical key is present AND non-null, so a file
// spelling the canonical key `null` beside a populated alias configures those
// marketplaces — read as "the canonical key is present, so use it", it configured
// none of them and the dir was reported as lacking what a sibling had.
func TestNullCanonicalKeyDoesNotShadowTheAlias(t *testing.T) {
	const market = `{"obra":{"source":{"source":"github","repo":"obra/x"}}}`
	read := func(settings string) DimensionState {
		t.Helper()
		got, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{"settings.json": body(settings)}))
		if !ok {
			t.Fatalf("dir with %s did not read", settings)
		}
		return got.Marketplaces
	}

	canonical := read(`{"extraKnownMarketplaces":` + market + `}`)
	shadowed := read(`{"extraKnownMarketplaces":null,"additionalMarketplaces":` + market + `}`)
	if !reflect.DeepEqual(shadowed.Names(), canonical.Names()) {
		t.Errorf("a null canonical key shadowed the alias: %v, want %v",
			shadowed.Names(), canonical.Names())
	}
	if shadowed.Target("obra") != canonical.Target("obra") {
		t.Errorf("the promoted alias produced a different target: %q vs %q",
			shadowed.Target("obra"), canonical.Target("obra"))
	}

	// And a canonical key that DOES hold a value still wins, which is the case the
	// warning claude emits is about.
	both := read(`{"extraKnownMarketplaces":{"canonical":{"source":{}}},"additionalMarketplaces":` + market + `}`)
	if !reflect.DeepEqual(both.Names(), []string{"canonical"}) {
		t.Errorf("names = %v, want [canonical] — the alias must be ignored, not merged", both.Names())
	}
}

// One server configured under two project scopes has no single target, so the two
// are carried together. Two dirs written the same way must still compare equal —
// otherwise every multi-project dir reports drift against its own twin.
// A local-scope server belongs to one checkout, not to the dir, so it must not enter
// the compared set. Unioning the project scopes in made the axis a record of how much
// each dir had been USED: the dir that had been driven through more repos was reported
// as holding capabilities its sibling lacked, under a remedy naming no place to put
// them. The two dirs below differ in every project scope and agree dir-wide, which is
// the shape that produced that noise.
func TestLocalScopeServersAreNotDirCapabilities(t *testing.T) {
	read := func(claude string) DimensionState {
		t.Helper()
		got, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{".claude.json": body(claude)}))
		if !ok {
			t.Fatalf("dir with %s did not read", claude)
		}
		return got.MCPServers
	}

	busy := read(`{"mcpServers":{"shared":{"url":"https://same.example"}},"projects":{
		"/repo-one":{"mcpServers":{"linear":{"url":"https://one.example"}}},
		"/repo-two":{"mcpServers":{"slack":{"url":"https://two.example"}}}}}`)
	fresh := read(`{"mcpServers":{"shared":{"url":"https://same.example"}},"projects":{}}`)

	if !busy.Measured || !fresh.Measured {
		t.Fatalf("a readable dir came back unmeasured: busy %+v fresh %+v", busy, fresh)
	}
	if !reflect.DeepEqual(busy.Names(), fresh.Names()) {
		t.Errorf("local scopes changed the dir's capability set: busy %v, fresh %v",
			busy.Names(), fresh.Names())
	}
	if busy.Target("shared") != fresh.Target("shared") {
		t.Errorf("the dir-wide target differed: %q vs %q", busy.Target("shared"), fresh.Target("shared"))
	}
	// And the dir-wide key is still compared on its value, not merely on its name.
	elsewhere := read(`{"mcpServers":{"shared":{"url":"https://other.example"}}}`)
	if elsewhere.Target("shared") == busy.Target("shared") {
		t.Error("two dirs pointing shared at different URLs produced one target")
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
	for _, name := range []string{".claude.json"} {
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

// Every Dimension const must be walked by Dimensions() and wired to a field. A
// missing State arm returns the zero DimensionState, which reads as "unmeasured" and
// silently drops the axis from every comparison; a const missing from Dimensions() is
// never compared at all.
//
// Dimensions() ranges over the const values rather than a hand-written slice, so a
// const declared before the sentinel is picked up without anyone remembering to list
// it. The residual hole is a const declared PAST the sentinel, which is what the last
// assertion here closes: nothing beyond dimensionLast may have a noun or a state,
// because a dimension with either is one somebody wired up and left uncompared.
func TestDimensionsIsTheWholeConstRange(t *testing.T) {
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
	nouns := map[string]Dimension{}
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
		// Two dimensions sharing a noun make one rendered line unattributable to the
		// axis that produced it.
		if prev, dup := nouns[d.Noun()]; dup {
			t.Errorf("dimensions %v and %v share the noun %q", prev, d, d.Noun())
		}
		nouns[d.Noun()] = d
	}
	if caps.State(DimensionUnspecified).Measured {
		t.Error("the unspecified dimension reported a measured state")
	}

	// Nothing past the sentinel. A const added below dimensionLast is walked
	// automatically; one added above it is not, and this is what says so.
	past := dimensionLast + 1
	if past.Noun() != DimensionUnspecified.Noun() {
		t.Errorf("Dimension(%d) has the noun %q, so a dimension exists past dimensionLast "+
			"and Dimensions() never walks it", int(past), past.Noun())
	}
	if caps.State(past).Measured {
		t.Errorf("Dimension(%d) is wired to a field but is past dimensionLast, so it is never compared", int(past))
	}
}

// deniedMcpServers decides which of the CONFIGURED servers a dir can actually run, so
// it is asserted through the MCP axis rather than on its own: a denied server is not a
// capability the dir has.
//
// Its entries are objects carrying exactly one of serverName, serverCommand or
// serverUrl — claude matches on r.serverName, on the expanded serverCommand argv, or
// on the expanded serverUrl. An earlier version read bare strings, which is the one
// shape claude REJECTS: it drops the whole key ("was present but invalid and was
// dropped; its entries cannot be enforced"), so such a list denies nothing. Both
// halves of that are pinned below, because the suite was previously green over a list
// claude ignores.
func TestReadDirCapabilitiesDeniedMCPServers(t *testing.T) {
	const configured = `{"mcpServers":{"linear":{"type":"http"},"slack":{"type":"http"}}}`
	both := measured("linear", "slack")
	cases := []struct {
		name     string
		settings string
		want     dimWant
	}{
		{"absent key denies nothing", `{}`, both},
		{"null denies nothing", `{"deniedMcpServers":null}`, both},
		{"a serverName denial removes that server", `{"deniedMcpServers":[{"serverName":"slack"}]}`, measured("linear")},
		{"denying every server leaves an empty set, not an unknown one",
			`{"deniedMcpServers":[{"serverName":"slack"},{"serverName":"linear"}]}`, measured()},
		{"denying a server this dir does not configure changes nothing",
			`{"deniedMcpServers":[{"serverName":"sketchy"}]}`, both},
		{"a blank serverName names nothing", `{"deniedMcpServers":[{"serverName":"   "}]}`, both},
		// The shape claude rejects. Reading it as a denial made the suite green on an
		// axis claude does not enforce; reading it as unknown would print an
		// "unverified" line for a list claude simply ignores.
		{"bare strings are the shape claude drops, so nothing is denied",
			`{"deniedMcpServers":["slack"]}`, both},
		// claude catches each entry on its own, so a malformed neighbour costs the
		// entry and nothing else. Read as dropping the key, this credited the member
		// with every server the file denies.
		{"a malformed entry is ignored alone, and its neighbours still deny",
			`{"deniedMcpServers":[{"serverName":"slack"},7]}`, measured("linear")},
		{"a valid entry beside one breaking exactly-one-of still denies",
			`{"deniedMcpServers":[{"serverName":"slack"},{"serverName":"linear","serverUrl":"https://x"}]}`,
			measured("linear")},
		// The schema refines serverName to equal its own trim, with the message that an
		// untrimmed name "will never match (names are compared verbatim)". Stored
		// verbatim after only a blank check, it subtracted a server the member can run.
		{"an untrimmed serverName is an entry claude ignores",
			`{"deniedMcpServers":[{"serverName":" pad "}]}`, both},
		{"a null serverName is ignored rather than read as blank",
			`{"deniedMcpServers":[{"serverName":null}]}`, both},
		// An unknown key does NOT invalidate the entry: claude's object schema strips
		// what it does not declare, so the denial is still enforced. Measured against the
		// shipped binary, which lists linear alone for exactly this file.
		{"an unknown key beside a valid serverName is stripped, not fatal",
			`{"deniedMcpServers":[{"serverName":"slack","junk":1}]}`, measured("linear")},
		{"two of the three keys in one entry is invalid",
			`{"deniedMcpServers":[{"serverName":"slack","serverUrl":"https://x"}]}`, both},
		{"an entry with none of the three keys is invalid",
			`{"deniedMcpServers":[{"name":"slack"}]}`, both},
		{"a non-string serverName is invalid",
			`{"deniedMcpServers":[{"serverName":7}]}`, both},
		{"an object instead of a list is invalid",
			`{"deniedMcpServers":{"slack":true}}`, both},
		// Enforced, and not expressible as a server name. Unmeasured rather than
		// short: reported as the configured set, this dir would be credited with a
		// server it blocks.
		{"a serverCommand denial cannot be named, so the axis is unknown",
			`{"deniedMcpServers":[{"serverCommand":["/bin/sketchy"]}]}`, unmeasured},
		{"a serverUrl denial cannot be named either",
			`{"deniedMcpServers":[{"serverUrl":"https://sketchy.example/mcp"}]}`, unmeasured},
		// A malformed neighbour does not rescue the axis either: the command-keyed entry
		// is still enforced, and still cannot be named.
		{"an unnameable entry survives a malformed neighbour",
			`{"deniedMcpServers":[{"serverCommand":["/bin/sketchy"]},7]}`, unmeasured},
		// An INVALID command-keyed entry is enforced against nothing, so it must not
		// take the axis with it: claude ignores it and the set stays fully knowable.
		{"a null serverCommand is ignored, not an unnameable denial",
			`{"deniedMcpServers":[{"serverCommand":null}]}`, both},
		{"an empty serverCommand array is below the schema's minimum",
			`{"deniedMcpServers":[{"serverCommand":[]}]}`, both},
		{"a serverCommand of non-strings is ignored",
			`{"deniedMcpServers":[{"serverCommand":[7]}]}`, both},
		{"a non-string serverUrl is ignored",
			`{"deniedMcpServers":[{"serverUrl":7}]}`, both},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := capDirWith(t, map[string]*string{
				"settings.json": body(tc.settings),
				".claude.json":  body(configured),
			})
			got, ok := ReadDirCapabilities(dir)
			if !ok {
				t.Fatal("dir did not read")
			}
			tc.want.check(t, "available MCP servers", got.MCPServers)
		})
	}
}

// A denial cannot be told apart from an absent server once the two are merged, so the
// case that matters is the one where the SERVER list is unknown: an unreadable
// denial list must take the MCP axis with it rather than leaving the configured set
// standing in for the available one.
func TestUnreadableDenialListMakesMCPUnknown(t *testing.T) {
	dir := capDirWith(t, map[string]*string{
		"settings.json": body(`{"enabledPlugins":{"a@m":true},"deniedMcpServers":[{"serverCommand":["/bin/x"]}]}`),
		".claude.json":  body(`{"mcpServers":{"linear":{"type":"http"}}}`),
	})
	got, ok := ReadDirCapabilities(dir)
	if !ok {
		t.Fatal("dir did not read")
	}
	if got.MCPServers.Measured {
		t.Errorf("MCP servers reported as measured (%v) despite a denial this build cannot express",
			got.MCPServers.Names())
	}
	// And only that axis: a dimension that could be read must not be lost with it, or
	// one unreadable field silences the whole section.
	if !got.Plugins.Measured || !got.Plugins.Has("a@m") {
		t.Errorf("the plugin axis was lost with the MCP axis: %+v", got.Plugins)
	}
}

// Numbers must survive fingerprinting exactly. Decoding into `any` without UseNumber
// made every JSON number a float64, which collided values past 2^53 — two dirs
// configuring one server differently compared EQUAL, and the divergence was never
// reported — and produced "" for a value outside float64's range, which the differ
// reads as "no difference" rather than as "not comparable".
func TestTargetsDoNotRoundNumbers(t *testing.T) {
	server := func(n string) string {
		return `{"mcpServers":{"linear":{"type":"http","timeout":` + n + `}}}`
	}
	read := func(n string) DimensionState {
		t.Helper()
		got, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{".claude.json": body(server(n))}))
		if !ok {
			t.Fatalf("dir with timeout %s did not read", n)
		}
		return got.MCPServers
	}

	for _, pair := range [][2]string{
		{"9007199254740993", "9007199254740992"}, // adjacent past 2^53
		{"12345678901234567890", "12345678901234567891"},
		{"1e400", "1e401"}, // outside float64 entirely
	} {
		a, b := read(pair[0]), read(pair[1])
		if a.Target("linear") == "" {
			t.Errorf("timeout %s produced no target, which a differ reads as no difference", pair[0])
			continue
		}
		if a.Target("linear") == b.Target("linear") {
			t.Errorf("timeouts %s and %s produced the same target %q", pair[0], pair[1], a.Target("linear"))
		}
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

// A version constraint is a comparable target, not just a name. Two dirs pinning one
// plugin to different versions are not substitutes, and recording the constraint as
// "enabled, nothing to compare" reported them as agreeing.
func TestPluginVersionConstraintsAreComparable(t *testing.T) {
	read := func(val string) DimensionState {
		t.Helper()
		got, ok := ReadDirCapabilities(capDirWith(t, map[string]*string{
			"settings.json": body(`{"enabledPlugins":{"keep@m":true,"pinned@m":` + val + `}}`),
		}))
		if !ok {
			t.Fatalf("dir with pinned@m = %s did not read", val)
		}
		return got.Plugins
	}

	one, two := read(`["^1.2"]`), read(`["^2.0"]`)
	if one.Target("pinned@m") == "" {
		t.Fatal("a version constraint recorded no target, so two pins compare as equal")
	}
	if one.Target("pinned@m") == two.Target("pinned@m") {
		t.Errorf("^1.2 and ^2.0 produced the same target %q", one.Target("pinned@m"))
	}
	// Same constraint, same target — otherwise every pinned plugin diverges from its
	// own twin.
	if one.Target("pinned@m") != read(`["^1.2"]`).Target("pinned@m") {
		t.Error("identical constraints produced different targets")
	}
	// A bare true still carries nothing to compare, so it cannot diverge.
	if one.Target("keep@m") != "" {
		t.Errorf("a bare true acquired the target %q", one.Target("keep@m"))
	}
	// An extended-format object is comparable for the same reason.
	obj := read(`{"version":"2.0"}`)
	if obj.Target("pinned@m") == "" {
		t.Error("an extended-format object recorded no target")
	}
	// A constraint list is a SET. json.Marshal orders object keys but never array
	// elements, so fingerprinting the raw array reported one dir as diverging from
	// another that pins the same plugin to the same two constraints.
	if a, b := read(`["^1.0",">=1.2"]`), read(`[">=1.2","^1.0"]`); a.Target("pinned@m") != b.Target("pinned@m") {
		t.Errorf("constraint order read as drift: %q vs %q", a.Target("pinned@m"), b.Target("pinned@m"))
	}
	// Sorting must not flatten a genuine difference into agreement.
	if a, b := read(`["^1.0",">=1.2"]`), read(`["^1.0",">=1.3"]`); a.Target("pinned@m") == b.Target("pinned@m") {
		t.Errorf("two different constraint sets both fingerprinted %q", a.Target("pinned@m"))
	}
	// An array of something other than strings is a shape this build does not
	// understand, and null is one: json.Unmarshal reads a null element into a string
	// without error, so `[null]` was accepted as a pin to the constraint "".
	for _, val := range []string{`[null]`, `[7]`, `["1.0",null]`, `[{}]`} {
		if got := read(val); got.Measured {
			t.Errorf("enabledPlugins %s was measured, with targets %v", val, got.Names())
		}
	}
	// An empty list is a shape this build DOES understand: enabled, unconstrained.
	if got := read(`[]`); !got.Measured || !got.Has("pinned@m") {
		t.Errorf("an empty constraint list was not read as enabled: %+v", got)
	}
}
