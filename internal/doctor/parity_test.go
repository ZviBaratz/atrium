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

// pooled builds one pool whose members are the named fixture dirs, read by the real
// reader. Member names are the fixture names, so assertions read as "rich" and "bare".
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

// named is a measured dimension holding these names and no comparable targets.
func named(names ...string) config.DimensionState {
	targets := map[string]string{}
	for _, n := range names {
		targets[n] = ""
	}
	return config.DimensionState{Measured: true, Targets: targets}
}

// measuredCaps is a dir that answered every dimension, so a test that means to be
// about one difference is not also about three unmeasured ones.
func measuredCaps(plugins, marketplaces, mcp []string) config.DirCapabilities {
	return config.DirCapabilities{
		Plugins:          named(plugins...),
		Marketplaces:     named(marketplaces...),
		MCPServers:       named(mcp...),
		DeniedMCPServers: named(),
		Connectors:       config.ConnectorsOn,
	}
}

// The fixture dirs are read by the REAL config.ReadDirCapabilities, so the shapes
// this section depends on are checked against files on disk rather than against a
// hand-written fake. Several tests below do the same; this one pins what each fixture
// holds, so a change to a fixture fails here rather than somewhere downstream.
func TestReadDirCapabilitiesFixtures(t *testing.T) {
	cases := []struct {
		dir          string
		plugins      []string
		marketplaces []string
		mcp          []string
		denied       []string
		mcpKnown     bool
		connectors   config.ConnectorState
		ok           bool
	}{{
		dir:          "rich",
		plugins:      []string{"linear@quantivly", "superpowers@obra"},
		marketplaces: []string{"obra", "quantivly"},
		mcp:          []string{"linear", "slack"},
		denied:       []string{"sketchy"},
		mcpKnown:     true,
		connectors:   config.ConnectorsOn,
		ok:           true,
	}, {
		dir:          "bare",
		plugins:      []string{"superpowers@obra"},
		marketplaces: []string{"obra"},
		mcp:          []string{},
		denied:       []string{},
		mcpKnown:     true,
		connectors:   config.ConnectorsOff,
		ok:           true,
	}, {
		// The same names as rich, every one of them pointing somewhere else — and one
		// of the two servers denied.
		dir:          "rewired",
		plugins:      []string{"linear@quantivly", "superpowers@obra"},
		marketplaces: []string{"obra", "quantivly"},
		mcp:          []string{"linear", "slack"},
		denied:       []string{"slack"},
		mcpKnown:     true,
		connectors:   config.ConnectorsOn,
		ok:           true,
	}, {
		// settings.json and no .claude.json: three dimensions answerable, the MCP one
		// unknown rather than empty.
		dir:          "nomcp",
		plugins:      []string{"linear@quantivly", "superpowers@obra"},
		marketplaces: []string{"obra", "quantivly"},
		denied:       []string{},
		mcpKnown:     false,
		connectors:   config.ConnectorsOn,
		ok:           true,
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
			if !tc.ok {
				return
			}
			require.True(t, got.Plugins.Measured)
			require.True(t, got.Marketplaces.Measured)
			assert.Equal(t, tc.plugins, got.Plugins.Names())
			assert.Equal(t, tc.marketplaces, got.Marketplaces.Names())
			assert.Equal(t, tc.mcpKnown, got.MCPServers.Measured, "MCP measured flag")
			if tc.mcpKnown {
				assert.Equal(t, tc.mcp, got.MCPServers.Names())
			}
			require.True(t, got.DeniedMCPServers.Measured)
			assert.Equal(t, tc.denied, got.DeniedMCPServers.Names())
			assert.Equal(t, tc.connectors, got.Connectors)
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
	require.True(t, caps.MCPServers.Measured,
		"the dir must have answered, or an empty list proves nothing")
	assert.Empty(t, caps.MCPServers.Names(), "claudeAiMcpEverConnected leaked into MCPServers")

	// Control: the fixture really does name those four, in the spelling claude writes
	// them — a "claude.ai " prefix — so a regression that starts reading the array has
	// something to be caught by.
	raw, err := readFixtureFile(t, "bare", ".claude.json")
	require.NoError(t, err)
	for _, connector := range []string{
		"claude.ai Gmail", "claude.ai Google Calendar",
		"claude.ai Google Drive", "claude.ai Linear",
	} {
		assert.Contains(t, raw, connector,
			"fixture no longer records the connectors this test exists to ignore")
	}
}

// End-to-end over the fixtures: a pool of two real dirs, read by the real reader,
// diffed and rendered. The rich dir enables both plugins, knows both marketplaces,
// configures two MCP servers and has connectors on; the bare dir has one plugin, one
// marketplace, no MCP servers and connectors off.
func TestCheckParityOverFixtures(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "bare"), config.ReadDirCapabilities)
	out := RenderParity(warns)

	assert.Contains(t, out, "Account pool parity:")
	assert.Contains(t, out, `plugin "linear@quantivly": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `marketplace "quantivly": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `MCP server "linear": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `MCP server "slack": "rich" has it, "bare" does not`)
	assert.Contains(t, out, `claude.ai connectors are on for "rich" but disabled for "bare"`)

	// Shared by both dirs, and pointing at the same place in both, so not a parity
	// problem and not a line.
	assert.NotContains(t, out, "superpowers@obra")
	assert.NotContains(t, out, `marketplace "obra"`)
	// The plugin the rich fixture carries as false is not enabled there, and the bare
	// fixture does not name it at all: both lack it, so it is not drift.
	assert.NotContains(t, out, "retired@obra")

	// The dimensions are walked in a fixed order, so the block does not reshuffle.
	order := make([]string, 0, len(warns))
	for _, w := range warns {
		order = append(order, w.Dimension.Noun()+"/"+w.Feature)
	}
	assert.Equal(t, []string{
		"plugin/linear@quantivly",
		"marketplace/quantivly",
		"MCP server/linear",
		"MCP server/slack",
		"capability/", // the connector warning is about no single dimension
	}, order)
}

// Two dirs can hold every capability under the same name and still not be
// substitutes: the rewired fixture points the same two marketplaces and the same two
// MCP servers at a different repo, a local path, a different URL and a different
// command. Comparing names alone certified that pool as interchangeable.
func TestCheckParityReportsDivergentTargets(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "rewired"), config.ReadDirCapabilities)
	out := RenderParity(warns)

	assert.Contains(t, out, `marketplace "obra" is configured differently across "rich" and "rewired"`)
	assert.Contains(t, out, `marketplace "quantivly" is configured differently across "rich" and "rewired"`)
	assert.Contains(t, out, `MCP server "linear" is configured differently across "rich" and "rewired"`)
	assert.Contains(t, out, `MCP server "slack" is configured differently across "rich" and "rewired"`)
	assert.Contains(t, out, "same name, different target")

	// Both dirs enable the same plugins, and a plugin's value is a bool with nothing
	// to point at, so there is no target to diverge and no line about one.
	assert.NotContains(t, out, "plugin")
	// Nothing is missing on either side, so no member is told it lacks anything.
	assert.NotContains(t, out, "has it")

	// rewired denies a server both members configure, which is a real difference in
	// what a session placed there can do.
	assert.Contains(t, out, `MCP server "slack" is denied for "rewired" but not for "rich"`)
	// rich denies a server NEITHER member configures. Ranging over the denial lists
	// instead of over the configured servers reported that as drift, and the only way
	// to silence it was to copy the denial into every member.
	assert.NotContains(t, out, "sketchy")
	assert.Len(t, warns, 5)
}

// A denial only matters for a server the member actually configures. A member that
// does not configure it is not denying it — that gap belongs to the MCP-server
// dimension, and charging it to two lines in two vocabularies is worse than one.
func TestCheckParityDenialsRangeOverConfiguredServers(t *testing.T) {
	denies := measuredCaps(nil, nil, nil)
	denies.DeniedMCPServers = named("linear")
	configures := measuredCaps(nil, nil, []string{"linear"})

	// Neither member configures linear, though one denies it: nothing to report.
	assert.Empty(t, CheckParity(twoMemberPool("/a", "/b"), staticReader(
		map[string]config.DirCapabilities{"/a": denies, "/b": measuredCaps(nil, nil, nil)})))

	// One configures it, the other denies without configuring: the difference is that
	// only one has it, reported once, by the server dimension.
	warns := CheckParity(twoMemberPool("/a", "/b"), staticReader(
		map[string]config.DirCapabilities{"/a": configures, "/b": denies}))
	require.Len(t, warns, 1)
	assert.Equal(t, config.DimensionMCPServer, warns[0].Dimension)
	assert.Contains(t, RenderParity(warns), `MCP server "linear": "a" has it, "b" does not`)

	// Both configure it and one denies it: now the denial is the difference.
	both := measuredCaps(nil, nil, []string{"linear"})
	both.DeniedMCPServers = named("linear")
	warns = CheckParity(twoMemberPool("/a", "/b"), staticReader(
		map[string]config.DirCapabilities{"/a": configures, "/b": both}))
	require.Len(t, warns, 1)
	assert.Equal(t, config.DimensionDeniedMCPServer, warns[0].Dimension)
	assert.Contains(t, RenderParity(warns), `MCP server "linear" is denied for "b" but not for "a"`)
}

// A denial list nothing could read leaves the pool unverified on that axis. But a
// member whose SERVER list is the unreadable half is already named by the MCP-server
// dimension, so it must not be charged a second line.
func TestCheckParityUnreadableDenialList(t *testing.T) {
	blindDenials := measuredCaps(nil, nil, []string{"linear"})
	blindDenials.DeniedMCPServers = config.DimensionState{}
	warns := CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps(nil, nil, []string{"linear"}),
		"/b": blindDenials,
	}))
	require.Len(t, warns, 1)
	assert.Equal(t, ParityUnmeasured, warns[0].Kind)
	assert.Equal(t, config.DimensionDeniedMCPServer, warns[0].Dimension)
	assert.Contains(t, RenderParity(warns),
		`MCP server denials are unverified: "b" does not report a readable denial list`)

	// The other half: an unreadable SERVER list is one absent file and one line.
	blindServers := measuredCaps(nil, nil, nil)
	blindServers.MCPServers = config.DimensionState{}
	warns = CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps(nil, nil, []string{"linear"}),
		"/b": blindServers,
	}))
	require.Len(t, warns, 1, "rendered: %s", RenderParity(warns))
	assert.Equal(t, config.DimensionMCPServer, warns[0].Dimension)
}

// The false positive this shape exists to prevent. mcpServers lives only in
// .claude.json, so the nomcp dir has an UNKNOWN MCP set, not an empty one. Reported
// as empty it was accused of lacking both of its sibling's servers, and the remedy
// told the user to align two dirs when the honest answer was that one of them had
// never been onboarded.
func TestCheckParityUnmeasuredDimensionIsNotDrift(t *testing.T) {
	warns := CheckParity(pooled(t, "rich", "nomcp"), config.ReadDirCapabilities)
	require.Len(t, warns, 1, "only the unanswered dimension: %s", RenderParity(warns))

	assert.Equal(t, ParityUnmeasured, warns[0].Kind)
	assert.Equal(t, config.DimensionMCPServer, warns[0].Dimension)
	assert.Equal(t, []string{"nomcp"}, warns[0].Lack)
	assert.Empty(t, warns[0].Have, "an unmeasured dimension is not evidence of anything")

	out := RenderParity(warns)
	assert.Contains(t, out, `MCP server parity is unverified: "nomcp" does not report one`)
	// The two dirs configure the same plugins and marketplaces, so those are silent —
	// and critically, nomcp is never said to LACK a server.
	assert.NotContains(t, out, `MCP server "linear"`)
	assert.NotContains(t, out, "has it")
	// The remedy is about the unanswered question, not about aligning two dirs that
	// were never found to disagree.
	assert.Contains(t, out, "not evidence of parity")
	assert.NotContains(t, out, "Align the config dirs")
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

	// Unreadable members do not stop the readable ones being compared, and the report
	// carries both remedies: one for the real difference, one for the two dirs nothing
	// could be read from.
	assert.Contains(t, out, `plugin "linear@quantivly"`)
	assert.Contains(t, out, "Align the config dirs")
	assert.Contains(t, out, "not evidence of parity")
}

// A pool with only one READABLE member still reports the unreadable ones — that is
// the whole reason they get a line — but has nothing to diff the survivor against,
// so it must not invent a comparison out of one set. The survivor here has an
// unmeasured dimension, which is what makes this exercise the guard: without it the
// pool would additionally be told its MCP parity is unverified, repeating in a second
// sentence what the unreadable line already said.
func TestCheckParityOneReadableMemberDiffsNothing(t *testing.T) {
	warns := CheckParity(pooled(t, "nomcp", "malformed"), config.ReadDirCapabilities)
	require.Len(t, warns, 1, "rendered: %s", RenderParity(warns))
	assert.Equal(t, ParityUnreadable, warns[0].Kind)
	assert.Equal(t, []string{"malformed"}, warns[0].Lack)

	out := RenderParity(warns)
	assert.NotContains(t, out, "unverified")
	assert.NotContains(t, out, "Align the config dirs")
	assert.Contains(t, out, "not evidence of parity")
}

// Membership comes from config.PoolMembers, the function rotation itself resolves
// through — which also counts an account with no pool of its own whose NAME another
// account uses as its pool. Scanning locally for Pool != "" skipped that anchor, left
// one member, and printed nothing for the one pool where rotation was live.
func TestCheckParityIncludesTheUnpooledAnchor(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "/a"},
		{Name: "work-alt", ConfigDir: "/b", Pool: "work"},
	}}
	// Control: rotation really does treat these two as one pool, so the section is
	// obliged to have an opinion about them.
	require.Len(t, cfg.PoolMembers("work"), 2)

	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"linear@quantivly"}, nil, nil),
		"/b": measuredCaps(nil, nil, nil),
	}))
	require.Len(t, warns, 1)
	assert.Equal(t, "work", warns[0].Pool)
	assert.Equal(t, "linear@quantivly", warns[0].Feature)
	assert.Equal(t, []string{"work"}, warns[0].Have, "the anchor keeps its own name")
	assert.Equal(t, []string{"work-alt"}, warns[0].Lack)
}

// An unpooled account whose name nothing references is a singleton, and a pool of
// one has nothing to be interchangeable with — neither is compared, however wildly
// the rest of the config differs from it.
func TestCheckParityIgnoresSingletons(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "lonely", ConfigDir: "/a", Pool: "solo"},
		{Name: "ungrouped", ConfigDir: "/b"},
	}}
	// Control: neither account shares a pool with anyone, which is the premise the
	// assertion rests on rather than something it should discover.
	require.Len(t, cfg.PoolMembers("solo"), 1)
	require.Len(t, cfg.PoolMembers("ungrouped"), 1)

	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"only-a@m"}, nil, nil),
		"/b": measuredCaps([]string{"only-b@m"}, nil, nil),
	}))
	assert.Empty(t, warns)
}

// Two members whose sets are the same SIZE but not the same content. A diff that
// compared cardinality would call this pool in parity and print nothing.
func TestCheckParityEqualSizedDifferentSets(t *testing.T) {
	warns := CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"only-a@m"}, nil, []string{"alpha"}),
		"/b": measuredCaps([]string{"only-b@m"}, nil, []string{"beta"}),
	}))
	out := RenderParity(warns)

	require.Len(t, warns, 4, "each of the four names is a difference: %s", out)
	assert.Contains(t, out, `plugin "only-a@m": "a" has it, "b" does not`)
	assert.Contains(t, out, `plugin "only-b@m": "b" has it, "a" does not`)
	assert.Contains(t, out, `MCP server "alpha": "a" has it, "b" does not`)
	assert.Contains(t, out, `MCP server "beta": "b" has it, "a" does not`)
}

// Pools appear in the config order of their first member, members keep config order,
// and names within a dimension are alphabetical — so an unchanged config renders an
// unchanged section rather than reshuffling between runs.
func TestCheckParityIsOrdered(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "b", ConfigDir: "/b", Pool: "second"},
		{Name: "a", ConfigDir: "/a", Pool: "second"},
		{Name: "c", ConfigDir: "/c", Pool: "first"},
		{Name: "d", ConfigDir: "/d", Pool: "first"},
	}}
	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"zeta@m", "alpha@m"}, nil, nil),
		"/b": measuredCaps(nil, nil, nil),
		"/c": measuredCaps(nil, nil, []string{"srv"}),
		"/d": measuredCaps(nil, nil, nil),
	}))

	require.Len(t, warns, 3)
	// "second" is declared first, so it reports first — pool ordering follows the
	// config, not the pool name.
	assert.Equal(t, "second", warns[0].Pool)
	assert.Equal(t, "alpha@m", warns[0].Feature, "names within a dimension sort alphabetically")
	assert.Equal(t, []string{"a"}, warns[0].Have)
	assert.Equal(t, []string{"b"}, warns[0].Lack, "members keep config order")
	assert.Equal(t, "second", warns[1].Pool)
	assert.Equal(t, "zeta@m", warns[1].Feature)
	assert.Equal(t, "first", warns[2].Pool)
	assert.Equal(t, "srv", warns[2].Feature)
}

// Three-member pools, so the verb agreement and the multi-member lists are actually
// rendered rather than resting on one-element slices that agree either way.
func TestCheckParityRendersMultiMemberLists(t *testing.T) {
	caps := map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"shared@m", "lonely@m"}, nil, nil),
		"/b": measuredCaps([]string{"shared@m"}, nil, nil),
		"/c": measuredCaps([]string{"shared@m"}, nil, nil),
	}
	out := RenderParity(CheckParity(twoMemberPool("/a", "/b", "/c"), staticReader(caps)))
	assert.Contains(t, out, `plugin "lonely@m": "a" has it, "b" and "c" do not`)

	caps["/b"] = measuredCaps([]string{"shared@m", "lonely@m"}, nil, nil)
	out = RenderParity(CheckParity(twoMemberPool("/a", "/b", "/c"), staticReader(caps)))
	assert.Contains(t, out, `plugin "lonely@m": "a" and "b" have it, "c" does not`)
}

// A member riding the ambient env has no dir of its own, which is a different fact
// from a dir that would not read: the identity section skips such an account
// outright, and reporting it as an unreadable directory printed an empty
// parenthetical and a remedy for a path that does not exist.
func TestCheckParityNamesAmbientMembers(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "ambient", Pool: "p"},
		{Name: "work", ConfigDir: "/w", Pool: "p"},
	}}
	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/w": measuredCaps([]string{"a@m"}, nil, nil),
	}))
	require.Len(t, warns, 1)
	assert.Equal(t, ParityNoConfigDir, warns[0].Kind)
	assert.Equal(t, []string{"ambient"}, warns[0].Lack)
	assert.Empty(t, warns[0].Dir, "there is no dir here to name")

	out := RenderParity(warns)
	assert.Contains(t, out, `"ambient" injects no CLAUDE_CONFIG_DIR`)
	assert.Contains(t, out, "give that member its own config_dir")
	// Never the unreadable-dir wording, and never an empty parenthetical.
	assert.NotContains(t, out, "unreadable")
	assert.NotContains(t, out, "()")
}

// config.json is hand-editable and the only name-uniqueness guard runs in the
// accounts overlay, so two members can share a name or carry none. Rendered raw they
// produced `"work" has it, "work" does not` — the section that exists to make a bad
// pool diagnosable being the one place a duplicate name made it undiagnosable.
func TestCheckParityDisambiguatesCollidingNames(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work", ConfigDir: "/a", Pool: "p"},
		{Name: "work", ConfigDir: "/b", Pool: "p"},
		{Name: "", ConfigDir: "/c", Pool: "p"},
	}}
	warns := CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/a": measuredCaps([]string{"linear@quantivly"}, nil, nil),
		"/b": measuredCaps(nil, nil, nil),
		"/c": measuredCaps(nil, nil, nil),
	}))
	require.Len(t, warns, 1)

	out := RenderParity(warns)
	assert.Contains(t, out, `"work at /a" has it`)
	assert.Contains(t, out, `"work at /b"`)
	assert.Contains(t, out, `"unnamed account at /c"`)
	// The self-contradictory rendering this test exists to prevent.
	assert.NotContains(t, out, `"work" has it, "work"`)
}

// A connector setting nothing could read is not "on" and not "off". Folded into
// either bucket it would fabricate a split against a member that agrees, or hide a
// real one.
func TestCheckParityConnectorsUnknownIsItsOwnLine(t *testing.T) {
	on := measuredCaps(nil, nil, nil)
	unknown := measuredCaps(nil, nil, nil)
	unknown.Connectors = config.ConnectorsUnknown

	warns := CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": on, "/b": unknown,
	}))
	require.Len(t, warns, 1)
	assert.Equal(t, ParityConnectorsUnknown, warns[0].Kind)
	assert.Equal(t, []string{"b"}, warns[0].Lack)

	out := RenderParity(warns)
	assert.Contains(t, out, `the claude.ai connector setting could not be read for "b"`)
	assert.NotContains(t, out, "connectors are on for")
	assert.Contains(t, out, "not evidence of parity")
	assert.NotContains(t, out, "Align the config dirs")
}

// Dormant cases. A nil reader is how a caller that has not wired one renders exactly
// as it did before this section existed, and an aligned pool prints nothing at all.
func TestCheckParityDormantAndClean(t *testing.T) {
	cfg := pooled(t, "rich", "bare")

	assert.Nil(t, CheckParity(cfg, nil), "a nil reader must report nothing")
	assert.Nil(t, CheckParity(nil, config.ReadDirCapabilities))
	assert.Equal(t, "", RenderParity(nil))

	same := measuredCaps([]string{"a@m"}, nil, []string{"linear"})
	aligned := CheckParity(twoMemberPool("/a", "/b"), staticReader(map[string]config.DirCapabilities{
		"/a": same, "/b": same,
	}))
	assert.Empty(t, aligned, "two identically configured dirs are in parity")
	assert.Equal(t, "", RenderParity(aligned))

	// And the same over real files: a dir compared with itself is in parity, which is
	// what makes the section's silence meaningful.
	assert.Empty(t, CheckParity(pooled(t, "rich", "rich"), config.ReadDirCapabilities))
}

// Two accounts naming one directory cost a single read. Beyond the saving, it means
// they cannot be made to disagree with each other by a file rewritten between two
// reads.
func TestCheckParityReadsEachDirOnce(t *testing.T) {
	reads := map[string]int{}
	counting := func(dir string) (config.DirCapabilities, bool) {
		reads[dir]++
		return measuredCaps([]string{"a@m"}, nil, nil), true
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

// Each remedy is chosen by what the report actually holds, and a report can hold both
// kinds at once. Deciding it all-or-nothing across the whole report meant one real
// difference anywhere suppressed the "check the file parses" line for every member
// nothing could be read from, leaving them with a remedy that did not apply.
func TestRenderParityHintsFollowTheWarnings(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "bad1", ConfigDir: "/bad1", Pool: "brokenpool"},
		{Name: "bad2", ConfigDir: "/bad2", Pool: "brokenpool"},
		{Name: "g1", ConfigDir: "/g1", Pool: "okpool"},
		{Name: "g2", ConfigDir: "/g2", Pool: "okpool"},
	}}
	out := RenderParity(CheckParity(cfg, staticReader(map[string]config.DirCapabilities{
		"/g1": measuredCaps([]string{"p@m"}, nil, nil),
		"/g2": measuredCaps(nil, nil, nil),
	})))

	assert.Contains(t, out, `capabilities unreadable for "bad1" (/bad1)`)
	assert.Contains(t, out, `plugin "p@m": "g1" has it, "g2" does not`)
	// Both remedies, each once however many warnings called for it.
	assert.Contains(t, out, "Align the config dirs")
	assert.Contains(t, out, "not evidence of parity")
	assert.Equal(t, 1, strings.Count(out, "Align the config dirs"))
	assert.Equal(t, 1, strings.Count(out, "not evidence of parity"))
}

// The rendered hint promises only what Atrium does. Rotation really does not consult
// capability — SelectPoolMember weighs availability alone — so the section tells the
// user to fix their dirs rather than implying Atrium will route around the gap.
func TestRenderParityHintOffersNoRouting(t *testing.T) {
	out := RenderParity(CheckParity(pooled(t, "rich", "bare"), config.ReadDirCapabilities))
	require.NotEmpty(t, out)
	assert.Contains(t, out, "it does not consult capability")
	assert.Contains(t, out, "Align the config dirs, or split the pool.")
	assert.Equal(t, 1, strings.Count(out, "Align the config dirs"))
}

// Kind's zero value is not a real warning. Sharing iota 0 with a reportable kind
// meant a warning built without one rendered as that kind's sentence, quietly
// mislabelling whatever it actually held; an obviously wrong line is better than a
// plausible wrong one.
func TestRenderParityUnsetKindIsVisiblyWrong(t *testing.T) {
	out := RenderParity([]ParityWarning{{Pool: "p", Feature: "x", Have: []string{"a"}, Lack: []string{"b"}}})
	assert.Contains(t, out, "internal error: parity warning with no kind")
	assert.NotContains(t, out, "unreadable")
	assert.NotContains(t, out, "has it")
	// It is neither a difference nor an unanswered question, so it earns no remedy.
	assert.NotContains(t, out, "Align the config dirs")
	assert.NotContains(t, out, "not evidence of parity")
}

// staticReader answers from a table, so a test never touches a real config dir.
func staticReader(caps map[string]config.DirCapabilities) config.CapabilityReadFunc {
	return func(dir string) (config.DirCapabilities, bool) {
		c, ok := caps[dir]
		return c, ok
	}
}

// twoMemberPool builds a one-pool config whose member names are the last path
// element of each dir, so assertions read as "a" and "b". It takes any number of
// dirs; the name is about the smallest useful pool, not a limit.
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
