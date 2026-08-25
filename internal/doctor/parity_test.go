package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureDir is an absolute path to one of the config dirs under testdata. Absolute
// because ReadDirCapabilities refuses a relative dir, which is the guard the fixture
// tests below would otherwise trip over instead of exercising.
func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "capability", name))
	require.NoError(t, err)
	return abs
}

func pooled(t *testing.T, names ...string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	for _, n := range names {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name: n, ConfigDir: fixtureDir(t, n), Pool: "quantivly",
		})
	}
	return cfg
}

// The fixture dirs are read by the REAL config.ReadDirCapabilities, so this table is
// the one place where the parity section and the on-disk file shapes are checked
// against each other rather than against a hand-written fake.
func TestReadDirCapabilitiesFixtures(t *testing.T) {
	cases := []struct {
		dir  string
		want config.DirCapabilities
		ok   bool
	}{{
		dir: "rich",
		want: config.DirCapabilities{
			EnabledPlugins:   []string{"linear@quantivly", "superpowers@obra"},
			Marketplaces:     []string{"obra", "quantivly"},
			MCPServers:       []string{"linear", "slack"},
			DeniedMCPServers: []string{"sketchy"},
		},
		ok: true,
	}, {
		dir: "bare",
		want: config.DirCapabilities{
			EnabledPlugins: []string{"superpowers@obra"},
			Marketplaces:   []string{"obra"},
			ConnectorsOff:  true,
		},
		ok: true,
	}, {
		dir: "malformed",
		ok:  false,
	}, {
		dir: "notadir",
		ok:  false,
	}}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			got, ok := config.ReadDirCapabilities(fixtureDir(t, tc.dir))
			require.Equal(t, tc.ok, ok, "caps %+v", got)
			if tc.ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// The recorded decision, held to the fixture that would break it: the bare dir's
// .claude.json lists four connectors under claudeAiMcpEverConnected and configures
// no MCP server at all. That array is a local client record — a stateless connector
// is written into it off a cached init response, with no upstream grant — so reading
// it would report an unauthorized account as authorized and take the parity section
// quiet on exactly the drift it exists to find.
func TestCapabilitiesIgnoreEverConnected(t *testing.T) {
	caps, ok := config.ReadDirCapabilities(fixtureDir(t, "bare"))
	require.True(t, ok)
	assert.Empty(t, caps.MCPServers, "claudeAiMcpEverConnected leaked into MCPServers")

	// Control: the fixture really does name those four, so a regression that starts
	// reading the array has something to be caught by.
	raw, err := readFixtureFile(t, "bare", ".claude.json")
	require.NoError(t, err)
	for _, connector := range []string{"Gmail", "Google Calendar", "Google Drive", "Linear"} {
		assert.Contains(t, raw, connector,
			"fixture no longer records the connectors this test exists to ignore")
	}
}

// End-to-end over the fixtures: a pool of two real dirs, read by the real reader,
// diffed and rendered. The rich dir has the linear plugin, both marketplaces, two MCP
// servers and connectors on; the bare dir has none of the first three and connectors
// off. Every one of those must be named.
func TestCheckParityOverFixtures(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "bare"), config.ReadDirCapabilities)
	out := RenderParity(warns)

	assert.Contains(t, out, "Account pool parity:")
	assert.Contains(t, out, `plugin "linear@quantivly": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `marketplace "quantivly": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `MCP server "linear": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `MCP server "slack": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `MCP server "sketchy" is denied for "rich" but not for "bare"`)
	assert.Contains(t, out, `claude.ai connectors are on for "rich" but disabled for "bare"`)

	// Shared by both dirs, so not a parity problem and not a line.
	assert.NotContains(t, out, "superpowers@obra")
	assert.NotContains(t, out, `marketplace "obra"`)
	// The plugin the rich fixture carries as false is not enabled there, and the bare
	// fixture does not name it at all: both lack it, so it is not drift.
	assert.NotContains(t, out, "retired@obra")
}

// A member whose dir cannot be read must get a line of its own. The section is
// silent when a pool is in parity, so dropping the unreadable member would make
// "we compared these and they agree" indistinguishable from "we could not look".
func TestCheckParityNamesUnreadableMembers(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "bare", "malformed", "notadir"), config.ReadDirCapabilities)

	var unreadable []ParityWarning
	for _, w := range warns {
		if w.Kind == ParityUnreadable {
			unreadable = append(unreadable, w)
		}
	}
	require.Len(t, unreadable, 2)
	assert.Equal(t, []string{"malformed"}, unreadable[0].Lack)
	assert.Equal(t, []string{"notadir"}, unreadable[1].Lack)
	assert.Empty(t, unreadable[0].Have, "an unreadable member is not evidence of anything")

	out := RenderParity(warns)
	assert.Contains(t, out, `capabilities unreadable for "malformed"`)
	assert.Contains(t, out, `capabilities unreadable for "notadir"`)
	// The line names WHICH directory came up empty, so it is diagnosable without
	// cross-referencing config.json.
	assert.Contains(t, out, fixtureDir(t, "notadir"))

	// Unreadable members do not stop the readable ones being compared: the pool still
	// has two dirs that can be diffed against each other, so the section keeps the
	// remedy for a real difference alongside the two unmeasured members.
	assert.Contains(t, out, `plugin "linear@quantivly"`)
	assert.Contains(t, out, "Align the config dirs")
}

// A pool with only one READABLE member still reports the unreadable ones — that is
// the whole reason they get a line — but has nothing to diff the survivor against,
// so it must not invent a comparison out of one set.
func TestCheckParityOneReadableMemberDiffsNothing(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "malformed"), config.ReadDirCapabilities)
	require.Len(t, warns, 1)
	assert.Equal(t, ParityUnreadable, warns[0].Kind)
	assert.Equal(t, []string{"malformed"}, warns[0].Lack)

	// Nothing was compared, so the remedy is about the unanswered question rather
	// than about two dirs disagreeing — which they were never found to do.
	out := RenderParity(warns)
	assert.Contains(t, out, "these members were not measured")
	assert.NotContains(t, out, "Align the config dirs")
}

// Two members whose sets are the same SIZE but not the same content. A diff that
// compared cardinality would call this pool in parity and print nothing.
func TestCheckParityEqualSizedDifferentSets(t *testing.T) {
	caps := map[string]config.DirCapabilities{
		"/a": {EnabledPlugins: []string{"only-a@m"}, MCPServers: []string{"alpha"}},
		"/b": {EnabledPlugins: []string{"only-b@m"}, MCPServers: []string{"beta"}},
	}
	warns := CheckParity(twoMemberPool("/a", "/b"), staticReader(caps))
	out := RenderParity(warns)

	require.Len(t, warns, 4, "each of the four names is a difference: %s", out)
	assert.Contains(t, out, `plugin "only-a@m": "a" has it, "b" does not`)
	assert.Contains(t, out, `plugin "only-b@m": "b" has it, "a" does not`)
	assert.Contains(t, out, `MCP server "alpha": "a" has it, "b" does not`)
	assert.Contains(t, out, `MCP server "beta": "b" has it, "a" does not`)
}

// Pools appear in the config order of their first member, members keep config order,
// and names within a kind are alphabetical — so an unchanged config renders an
// unchanged section rather than reshuffling between runs.
func TestCheckParityIsOrdered(t *testing.T) {
	caps := map[string]config.DirCapabilities{
		"/a": {EnabledPlugins: []string{"zeta@m", "alpha@m"}},
		"/b": {},
		"/c": {MCPServers: []string{"srv"}},
		"/d": {},
	}
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "b", ConfigDir: "/b", Pool: "second"},
		{Name: "a", ConfigDir: "/a", Pool: "second"},
		{Name: "c", ConfigDir: "/c", Pool: "first"},
		{Name: "d", ConfigDir: "/d", Pool: "first"},
	}}
	warns := CheckParity(cfg, staticReader(caps))

	require.Len(t, warns, 3)
	// "second" is declared first, so it reports first — pool ordering follows the
	// config, not the pool name.
	assert.Equal(t, "second", warns[0].Pool)
	assert.Equal(t, "alpha@m", warns[0].Feature, "names within a kind sort alphabetically")
	assert.Equal(t, []string{"a"}, warns[0].Have)
	assert.Equal(t, []string{"b"}, warns[0].Lack, "members keep config order")
	assert.Equal(t, "second", warns[1].Pool)
	assert.Equal(t, "zeta@m", warns[1].Feature)
	assert.Equal(t, "first", warns[2].Pool)
	assert.Equal(t, "srv", warns[2].Feature)
}

// Dormant cases. A nil reader is how a caller that has not wired one renders exactly
// as it did before this section existed, and an aligned pool prints nothing at all.
func TestCheckParityDormantAndClean(t *testing.T) {
	cfg := pooled(t, "rich", "bare")

	assert.Nil(t, CheckParity(cfg, nil), "a nil reader must report nothing")
	assert.Nil(t, CheckParity(nil, config.ReadDirCapabilities))
	assert.Equal(t, "", RenderParity(nil))

	same := config.DirCapabilities{EnabledPlugins: []string{"a@m"}, MCPServers: []string{"linear"}}
	aligned := CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": same, "/b": same,
	}))
	assert.Empty(t, aligned, "two identically configured dirs are in parity")
	assert.Equal(t, "", RenderParity(aligned))
}

// A pool of one has nothing to be interchangeable with, and an unpooled account is a
// singleton by definition (Pool == ""), so neither is compared — even when the other
// accounts in the config differ wildly from it.
func TestCheckParityIgnoresSingletons(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "lonely", ConfigDir: "/a", Pool: "solo"},
		{Name: "ungrouped", ConfigDir: "/b"},
	}}
	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/a": {EnabledPlugins: []string{"only-a@m"}},
		"/b": {EnabledPlugins: []string{"only-b@m"}},
	}))
	assert.Empty(t, warns)
}

// Two accounts naming one directory cost a single read. Beyond the saving, it means
// they cannot be made to disagree with each other by a file rewritten between two
// reads — and that pair is not hypothetical, it is precisely what CheckPools flags.
func TestCheckParityReadsEachDirOnce(t *testing.T) {
	reads := map[string]int{}
	counting := func(dir string) (config.DirCapabilities, bool) {
		reads[dir]++
		return config.DirCapabilities{EnabledPlugins: []string{"a@m"}}, true
	}
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "one", ConfigDir: "/shared", Pool: "p"},
		{Name: "two", ConfigDir: "/shared/", Pool: "p"},
		{Name: "three", ConfigDir: "/shared", Pool: "p"},
	}}
	assert.Empty(t, CheckParity(cfg, counting))
	// "/shared/" is the same dir spelled differently; NormalizedConfigDir cleans it,
	// so it must not cost a second read or read as a second directory.
	assert.Equal(t, map[string]int{"/shared": 1}, reads)
}

// The rendered hint promises only what Atrium does. Rotation really does not consult
// capability — SelectPoolMember weighs availability alone — so the section tells the
// user to fix their dirs rather than implying Atrium will route around the gap.
func TestRenderParityHintOffersNoRouting(t *testing.T) {
	out := RenderParity(CheckParity(pooled(t, "rich", "bare"), config.ReadDirCapabilities))
	require.NotEmpty(t, out)
	assert.Contains(t, out, "it does not consult capability")
	assert.Contains(t, out, "Align the config dirs, or split the pool.")
	// One hint for the section, however many warnings it holds.
	assert.Equal(t, 1, strings.Count(out, "Align the config dirs"))
}

// staticReader answers from a table, so a test never touches a real config dir.
func staticReader(caps map[string]config.DirCapabilities) config.CapabilityReadFunc {
	return func(dir string) (config.DirCapabilities, bool) {
		c, ok := caps[dir]
		return c, ok
	}
}

// twoMemberPool builds a one-pool config whose member names are the last path
// element of each dir, so assertions read as "a" and "b".
func twoMemberPool(dirs ...string) *config.Config {
	cfg := &config.Config{}
	for _, d := range dirs {
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, config.ClaudeAccount{
			Name: filepath.Base(d), ConfigDir: d, Pool: "p",
		})
	}
	return cfg
}

// readFixtureFile returns one fixture file's raw text, for the control assertions
// that prove a fixture still contains what a test exists to ignore.
func readFixtureFile(t *testing.T, dir, name string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir(t, dir), name))
	return string(data), err
}
