package config

// Reading what a config dir declares it can DO.
//
// Rotation assumes the members of a pool are interchangeable (SelectPoolMember
// picks whichever member is next and available; nothing about the choice consults
// what the dir can do). Nothing has ever measured that assumption. Two dirs can hold
// two genuinely different logins — so ReadAccountIdentity is satisfied and
// CheckPools finds nothing — and still not be substitutes, because plugins,
// marketplaces and MCP servers live in per-dir files that neither of those checks
// reads (ReadAccountIdentity looks at one key, oauthAccount). A session routed to the
// member missing an integration does not fail: it just never has the tool, and the
// only symptom is work that quietly came out worse.
//
// Like ReadAccountIdentity beside it, and unlike LoadConfig/LoadState in this same
// package, this is a pure, strictly READ-ONLY probe: it creates nothing, rewrites
// nothing, and so can run beside a live TUI or a live agent.
//
// # A config dir is one settings source, not the settings
//
// This reads <configDir>/settings.json and <configDir>/.claude.json, the two files a
// CLAUDE_CONFIG_DIR owns. claude resolves settings from five sources in increasing
// precedence — user < project < local < flag < policy — and a config dir is the user
// source and only that. The other four are a checkout's .claude/settings.json, its
// gitignored .claude/settings.local.json, a --settings flag, and the machine's
// managed-settings.json.
//
// So what is compared here is what each dir ITSELF declares. That is the layer
// rotation can be aligned on, and it is deliberately not a claim about the settings a
// session will run under. The distinction cuts both ways, which is why every rendered
// line names the dir rather than the account: a project or managed source is shared by
// both members, so it cannot make two dirs differ, but it CAN make a difference here
// irrelevant by supplying the same capability to both, or by overriding a dir's value
// entirely. A plugin only one dir enables is a real difference between the dirs;
// whether it is a difference between two sessions depends on a checkout this probe is
// not asked about.
//
// <configDir>/settings.local.json is deliberately not read. An earlier version read
// it, on the assumption that it layers over settings.json within the dir. It does not
// exist at that path: claude resolves its localSettings source to
// .claude/settings.local.json against the project root and labels it "project,
// gitignored". Reading it here spoke for a dir out of a file claude never consults
// there, and layered it whole-key when claude merges enabledPlugins per plugin.
//
// # Unknown is never empty
//
// Every answer here distinguishes "this dir configures nothing" from "nothing could
// be read". They are the same value in Go — a nil slice — which is the defect class
// this repo has shipped repeatedly, so the distinction is carried structurally:
// DimensionState's zero value is unmeasured, ConnectorsUnknown is iota 0, and a
// field whose shape this build does not recognise makes its dimension unmeasured
// rather than empty. A caller must not read an unmeasured dimension as evidence of
// anything, in either direction.
//
// An ABSENT settings.json is on the other side of that line: it is an answer, not a
// gap. claude's defaults with no user settings file are no plugins, no extra
// marketplaces and no denials, so a dir holding only .claude.json — the ordinary
// logged-in-never-customised dir — is measured and empty on those axes. Gating them
// on the file existing reported that dir as three unanswered questions and masked the
// connector drift this section exists to find.
//
// # Local file reads only
//
// No network, no bearer token, no subprocess. The authoritative source of claude.ai
// connector GRANT state is an HTTP call carrying the dir's own OAuth token, and
// three things rule THAT out: it would be Atrium's first outbound request, its first
// handling of token material, and it would not work on macOS at all, where claude
// keeps credentials in the Keychain rather than in a readable file.
//
// Shelling out to claude is ruled out by something else, since doctor already runs
// claude elsewhere and none of those three apply to a subprocess: `CLAUDE_CONFIG_DIR=<dir>
// claude mcp list` WRITES. Measured against 2.1.247 on an empty directory, it left a
// .claude.json and a backups/ behind — so asking claude what a dir holds would
// onboard the dir it was asked about, and the read-only property stated above is the
// one this probe cannot give up: it runs beside live agents, against their dirs.
//
// So this probe measures file-level parity — what each dir is CONFIGURED with — which
// is the layer rotation can actually be aligned on, and NOT what claude.ai has
// granted. A pool whose members differ only in their upstream grants reads as being
// in parity here; that is a limit of the file layer, not a clean bill.
//
// # Deliberately does not read claudeAiMcpEverConnected
//
// .claude.json carries a claudeAiMcpEverConnected array, and it looks like the
// answer to "which connectors does this dir have?". It is not. It is a local client
// record written after any successful connect, and a stateless connector connects
// off a cached init response without an upstream grant — so its name is recorded
// whether or not the account was ever authorized. Measured on an account holding
// zero grants, the array still listed four connectors. Reading it would make an
// unauthorized account look authorized, which is worse than not answering: parity
// would go quiet on exactly the drift it exists to find.
//
// # Axes deliberately left unread
//
// enabledMcpjsonServers and disabledMcpjsonServers gate the servers a repo's own
// .mcp.json offers. settings.json types both as flat arrays of server names, declared
// beside the one key this probe already pulls out of that file, so the per-dir value
// is perfectly readable here. What is not readable is what a name on either list
// MEANS: it resolves against a .mcp.json belonging to a checkout, which this probe
// never opens and which every member shares. Two dirs approving different names are
// not thereby different capabilities — the servers those names stand for exist only
// once a repo is named, and doctor is not asked about a repo. So a difference there is
// reported as neither present nor absent.
//
// projects.<path>.mcpServers is left unread for a related reason, which mcpServerState
// states: it is claude's per-project local scope, not a capability of the dir.
//
// # Which claude these shapes were read from
//
// Every claim here about what claude accepts — the enabledPlugins value types, the
// deniedMcpServers entry schema and its per-entry rejection, the marketplace alias
// resolver's non-null check, the mcpServers scopes — was read out of claude 2.1.247's
// own settings schema, not inferred from a config file's contents. That is worth
// recording because the failure mode is silent: capabilityObject reads an ABSENT key
// as measured-and-empty, so if claude renames one of these keys, both members go empty
// on that axis and the section reports a pool as being in parity rather than reporting
// that it can no longer tell. Nothing here detects that; a reader checking these
// claims against a newer claude needs to know which one they were true of.
//
// allowedMcpServers is unread. Its own description makes it an enterprise allowlist,
// and the managed setting allowManagedMcpServersOnly can restrict it to managed
// settings alone, so a per-dir value is not reliably the value in force.
// deniedMcpServers is read, because the same description says the denylist "still
// merges from all sources, so users can deny servers for themselves" — but it is
// folded into the MCP server axis rather than compared on its own, for the reason
// availableMCPServers states.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Dimension is one axis along which two config dirs can fail to be substitutes.
type Dimension int

const (
	// DimensionUnspecified is the zero value and names no axis, so an unset field
	// cannot be mistaken for a claim about a real capability.
	DimensionUnspecified Dimension = iota
	// DimensionPlugin is settings.json's enabledPlugins.
	DimensionPlugin
	// DimensionMarketplace is settings.json's extraKnownMarketplaces (or its alias
	// additionalMarketplaces).
	DimensionMarketplace
	// DimensionMCPServer is the servers a dir can run: .claude.json's mcpServers, at
	// the top level and under every projects.<path> scope, minus what settings.json
	// denies.
	DimensionMCPServer
)

// dimensionLast is the highest real axis, and it is what Dimensions() ranges to
// instead of a hand-written slice, so a new Dimension takes effect without anyone
// remembering to list it separately.
//
// It is declared OUTSIDE the block above on purpose. Inside it, a const appended
// after this line with no value of its own would repeat this expression list rather
// than continue the iota, and silently come out equal to DimensionMCPServer.
//
// The hole it leaves is a Dimension whose value exceeds it, which Dimensions() cannot
// reach. TestDimensionsIsTheWholeConstRange closes that by asserting nothing past
// this sentinel has a noun or a state.
const dimensionLast = DimensionMCPServer

// Dimensions is the fixed order a comparison walks, so a rendered report does not
// reorder between runs on an unchanged config.
func Dimensions() []Dimension {
	dims := make([]Dimension, 0, int(dimensionLast))
	for d := DimensionUnspecified + 1; d <= dimensionLast; d++ {
		dims = append(dims, d)
	}
	return dims
}

// Noun is what one of these is called in a sentence.
func (d Dimension) Noun() string {
	switch d {
	case DimensionPlugin:
		return "plugin"
	case DimensionMarketplace:
		return "marketplace"
	case DimensionMCPServer:
		return "MCP server"
	default:
		return "capability"
	}
}

// ConnectorState is whether a dir's OWN settings.json switches claude.ai connectors
// off. It is not the state in force for a session: claude reads
// disableClaudeAiConnectors as any-source-true ("a project can opt out, but a
// project-level false cannot override a user-level true"), so a project or managed
// source can disable connectors for every member regardless of what a dir says.
type ConnectorState int

const (
	// ConnectorsUnknown means the setting could not be read: settings.json held a
	// value for disableClaudeAiConnectors that is neither JSON true nor false. Never
	// evidence of either state. Iota 0 so an unset field, a var and a map miss all
	// read as "no evidence" rather than as "connectors on".
	ConnectorsUnknown ConnectorState = iota
	// ConnectorsOn means this dir does not disable connectors:
	// disableClaudeAiConnectors is false or absent, which is claude's default.
	ConnectorsOn
	// ConnectorsOff means this dir sets disableClaudeAiConnectors true, which no
	// other source can override back on.
	ConnectorsOff
)

// DimensionState is what one config dir holds along one Dimension.
//
// The zero value is the honest default: Measured false means the file that would
// have answered was unreadable, or held this field in a shape this build does not
// recognise, so the dir contributes no evidence either way. Unmeasured being the
// ZERO value is the point — a companion bool defaulting the other way licenses a
// confident answer from an unset field.
type DimensionState struct {
	// Measured is true when the source was read and this field's shape was
	// understood. False means unknown, never "configured with nothing".
	Measured bool
	// Targets maps each configured name to a fingerprint of the value it was
	// configured WITH — a marketplace's source, an MCP server's URL or command, a
	// plugin's version constraint — or "" when the shape carries no comparable
	// target, as a plugin enabled by a bare `true` does not. A name present with
	// target "" means "configured, but there is nothing here to compare beyond the
	// name".
	Targets map[string]string
}

// Names is the configured names, sorted. Empty for an unmeasured dimension, which
// is why callers must check Measured first rather than reading a short list as a
// short answer.
func (s DimensionState) Names() []string {
	names := make([]string, 0, len(s.Targets))
	for name := range s.Targets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Has reports whether name is configured here. Meaningless unless Measured.
func (s DimensionState) Has(name string) bool {
	_, ok := s.Targets[name]
	return ok
}

// Target is the fingerprint of what name was configured with, or "" when the name
// is absent or carries nothing comparable.
func (s DimensionState) Target(name string) string {
	return s.Targets[name]
}

// DirCapabilities is what one config dir is configured to bring to a session.
type DirCapabilities struct {
	// Plugins, Marketplaces and MCPServers each carry their own Measured flag,
	// because they do not share a source: enabledPlugins and the marketplace key come
	// from settings.json and mcpServers from .claude.json, so a dir can answer one and
	// not the other. A single dir-wide "readable" flag was the earlier shape and was
	// wrong: it reported "claude was never onboarded here" as "this member configures
	// zero MCP servers".
	Plugins      DimensionState
	Marketplaces DimensionState
	MCPServers   DimensionState
	// Connectors is stated as a tri-state rather than a bool for the same reason.
	Connectors ConnectorState
}

// State is the dimension addressed by d, so a comparison can iterate Dimensions()
// instead of naming fields.
//
// This is not by itself a guard against a new axis going uncompared — nothing here
// fails when a Dimension const is added and the differ never walks it. Dimensions()
// ranging over the const values is what makes that automatic, and
// TestDimensionsIsTheWholeConstRange is what catches a const added past the sentinel.
func (c DirCapabilities) State(d Dimension) DimensionState {
	switch d {
	case DimensionPlugin:
		return c.Plugins
	case DimensionMarketplace:
		return c.Marketplaces
	case DimensionMCPServer:
		return c.MCPServers
	default:
		return DimensionState{}
	}
}

// ReadDirCapabilities returns what configDir is configured with, or ok=false when
// the directory as a whole cannot be spoken for.
//
// ok=false is reserved for "this is not a readable claude config dir", never for
// "has nothing". Three things produce it: a relative (or empty) dir, a file that is
// present but unreadable or unparseable, and a dir holding neither of the two files.
// The last is the case worth being deliberate about: a dir claude was never
// onboarded in is not a dir configured with nothing, and reporting it as one accuses
// every member of its pool of drift the user cannot fix. The same distinction runs
// one level down, which is what DimensionState is for — mcpServers lives only in
// .claude.json, so a dir without one has an unknown MCP set, not an empty one.
//
// A relative dir is refused rather than joined against the working directory, for
// the reason ReadAccountIdentity refuses one: "" is the routing value meaning
// "inherit the ambient env", and resolving it to ./settings.json would report on a
// dir no session has any relationship to.
func ReadDirCapabilities(configDir string) (DirCapabilities, bool) {
	if !filepath.IsAbs(configDir) {
		return DirCapabilities{}, false
	}

	settings, settingsFound, ok := readJSONObject(filepath.Join(configDir, "settings.json"))
	if !ok {
		return DirCapabilities{}, false
	}
	claude, claudeFound, ok := readJSONObject(filepath.Join(configDir, ".claude.json"))
	if !ok {
		return DirCapabilities{}, false
	}

	if !settingsFound && !claudeFound {
		return DirCapabilities{}, false // not a claude config dir; nothing to compare
	}

	// These three read an absent settings.json as claude's defaults, not as a gap;
	// see "Unknown is never empty" above. A nil map yields a nil RawMessage, which
	// each reader treats as the field being absent.
	var caps DirCapabilities
	caps.Plugins = pluginState(settings["enabledPlugins"])
	caps.Marketplaces = namedObjectState(marketplaceField(settings))
	caps.Connectors = connectorState(settings["disableClaudeAiConnectors"])
	if claudeFound {
		caps.MCPServers = availableMCPServers(claude, settings["deniedMcpServers"])
	}
	return caps, true
}

// marketplaceField picks the key holding the extra marketplaces. claude accepts
// additionalMarketplaces as an alias read "exactly as if it were spelled
// extraKnownMarketplaces", and ignores the alias with a warning when both appear in
// one file — so the canonical key wins here too, and a dir spelling it either way
// compares equal to a dir spelling it the other.
//
// "Both appear" is decided on the canonical key holding a NON-NULL value, not on the
// key being present. claude's alias resolver ignores the alias only when the
// canonical key is present AND not null, and otherwise promotes the alias into it, so
// a canonical key spelled as an explicit null is not a value that shadows anything.
// Keying on presence alone read such a file as configuring no marketplaces and
// reported two dirs claude resolves identically as drifting apart.
func marketplaceField(settings map[string]json.RawMessage) json.RawMessage {
	if raw, ok := settings["extraKnownMarketplaces"]; ok && !isJSONNull(raw) {
		return raw
	}
	return settings["additionalMarketplaces"]
}

// readJSONObject decodes path as a JSON object. found reports whether the file
// exists; ok is false only when the file could not be read for a reason other than
// absence, or did not hold an object. An absent file is (nil, false, true): missing
// is an answer, unreadable is not.
func readJSONObject(path string) (obj map[string]json.RawMessage, found, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Only "no such file" means the dir genuinely lacks this file. A permission
		// error, or a directory standing where the file should be, is an unanswered
		// question and must not read as an empty capability set.
		return nil, false, errors.Is(err, os.ErrNotExist)
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, true, false
	}
	// A file holding the literal `null` parses into a nil map. It is present and
	// syntactically fine but says nothing, and treating it as an empty object would
	// report the dir as configuring nothing.
	if obj == nil {
		return nil, true, false
	}
	return obj, true, true
}

// pluginState reads enabledPlugins, whose values claude types as
// array<string> | boolean | object: a bool switches a plugin on or off, an array
// carries version constraints, and an object is the documented extended format. An
// earlier version accepted only true/false/null, so a single entry in either of the
// other two shapes made the WHOLE dimension unmeasured and discarded every readable
// entry beside it.
//
// A version constraint or an extended-format object is fingerprinted as the target,
// so two dirs pinning one plugin to different versions register as configured
// differently rather than as both merely having it. A bare true carries no target.
// false and null are the plugin being off — counting them would credit a dir with a
// capability it does not have and hide the precise drift this probe exists to find.
// Any other shape makes the dimension unmeasured, because a build that does not
// understand the encoding cannot tell on from off and must not guess either way.
//
// Names are compared exactly as claude stores them, with no trimming: claude looks a
// plugin up by its literal key, so " x " and "x" are two different plugins and
// normalising them together would invent an equivalence claude does not have. Only a
// blank key — which can name nothing — is skipped.
func pluginState(raw json.RawMessage) DimensionState {
	obj, ok := capabilityObject(raw)
	if !ok {
		return DimensionState{}
	}
	targets := map[string]string{}
	for name, val := range obj {
		if strings.TrimSpace(name) == "" {
			continue
		}
		switch {
		case isJSONTrue(val):
			targets[name] = "" // a bare bool carries no target to compare
		case isJSONFalse(val), isJSONNull(val):
			continue // explicitly not enabled
		case isJSONObject(val):
			targets[name] = fingerprint(val)
		case isJSONArray(val):
			constraints, ok := stringArray(val)
			if !ok {
				return DimensionState{} // an array of something other than strings
			}
			// Sorted before fingerprinting. A constraint list is a set, and
			// json.Marshal orders object keys but never array elements, so the same
			// two constraints listed the other way round registered as drift.
			slices.Sort(constraints)
			targets[name] = fingerprintOf(constraints)
		default:
			return DimensionState{}
		}
	}
	return DimensionState{Measured: true, Targets: targets}
}

// namedObjectState reads a field mapping a name to its configuration object, which
// is how settings.json holds extraKnownMarketplaces and .claude.json holds
// mcpServers.
func namedObjectState(raw json.RawMessage) DimensionState {
	obj, ok := capabilityObject(raw)
	if !ok {
		return DimensionState{}
	}
	targets := map[string]string{}
	if !collectNamedObjects(obj, targets) {
		return DimensionState{}
	}
	return DimensionState{Measured: true, Targets: targets}
}

// collectNamedObjects adds every name in obj whose value is a configuration object,
// fingerprinting that object so two dirs can be compared on WHAT a shared name
// points at and not merely on the name. It reports false when a value has a shape
// this build does not understand, which makes the caller's whole dimension
// unmeasured rather than quietly short.
//
// One call reads one JSON object, whose keys are unique, so a name cannot arrive
// twice and there is no two-spellings case to reconcile. That was not true while the
// MCP axis unioned every project scope; mcpServerState says why it no longer does.
func collectNamedObjects(obj map[string]json.RawMessage, into map[string]string) bool {
	for name, val := range obj {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if isJSONNull(val) || isJSONFalse(val) {
			continue // explicitly not configured
		}
		if !isJSONObject(val) {
			return false
		}
		into[name] = fingerprint(val)
	}
	return true
}

// availableMCPServers is the set of servers a dir can actually run: the union of
// everything .claude.json configures, minus everything settings.json denies.
//
// Denial is folded in here rather than compared on an axis of its own because a
// denied server is not a capability the dir has. A separate pass over the denial
// lists answered the wrong question in both directions: it reported a member that
// configures AND denies a server as having it, and reported two members that both
// deny one as drifting over a server neither can run.
//
// An unreadable denial list makes the whole set unmeasured rather than short. The
// configured names are still known, but which of them are reachable is not, and
// reporting the configured set as the available set would credit a member with a
// server it blocks.
func availableMCPServers(claude map[string]json.RawMessage, denied json.RawMessage) DimensionState {
	configured := mcpServerState(claude)
	if !configured.Measured {
		return DimensionState{}
	}
	denials := denialNames(denied)
	if !denials.Measured {
		return DimensionState{}
	}
	targets := make(map[string]string, len(configured.Targets))
	for name, fp := range configured.Targets {
		if denials.Has(name) {
			continue
		}
		targets[name] = fp
	}
	return DimensionState{Measured: true, Targets: targets}
}

// denialNames reads deniedMcpServers. Its entries are OBJECTS carrying exactly one
// of serverName, serverCommand or serverUrl — not the bare strings an earlier version
// read here. claude matches a denial by `r.serverName === name`, by the expanded
// serverCommand argv, or by the expanded serverUrl.
//
// Rejection is PER ENTRY, not wholesale. claude wraps the list so that each entry
// failing validation is replaced by a sentinel, warned about individually ("Invalid
// entry was ignored"), and filtered out — the surviving entries are still enforced.
// An earlier version here read one bad entry as dropping the whole key, on the
// strength of the "deniedMcpServers was present but invalid and was dropped" message;
// that message comes from the catch OUTSIDE the per-entry wrapper, which a list whose
// entries are each individually caught can only reach by not being a list at all. The
// difference is not cosmetic: it credited a member with every server the file denies
// beside one malformed neighbour, which is the direction that reports a gap as parity.
//
// So an entry claude ignores is skipped, and only a non-list drops the key. A valid
// entry keyed on serverCommand or serverUrl is enforced but cannot be expressed as a
// server name, so it makes the list unmeasured rather than short — treating it as no
// denial would report a member as allowing a server it blocks. An INVALID one is
// enforced against nothing, and must not take the axis with it.
func denialNames(raw json.RawMessage) DimensionState {
	if len(raw) == 0 || isJSONNull(raw) {
		return deniesNothing()
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return deniesNothing() // not a list: the whole key is dropped
	}

	names := map[string]string{}
	unnameable := false
	for _, entry := range entries {
		name, kind := denialEntry(entry)
		switch kind {
		case denialIgnored:
			continue
		case denialUnnameable:
			unnameable = true
		case denialNamed:
			names[name] = ""
		}
	}
	if unnameable {
		return DimensionState{}
	}
	return DimensionState{Measured: true, Targets: names}
}

// denialKind is what one deniedMcpServers entry does to the available set.
type denialKind int

const (
	// denialIgnored is an entry claude drops on its own. It denies nothing, and it
	// leaves every other entry standing.
	denialIgnored denialKind = iota
	// denialNamed is a valid serverName denial, which subtracts that name.
	denialNamed
	// denialUnnameable is a valid serverCommand or serverUrl denial: enforced, but
	// not expressible as a name, so the axis becomes unmeasured.
	denialUnnameable
)

// denialEntry classifies one entry the way claude's schema does.
//
// serverName is refined three times over — non-empty, not whitespace-only, and equal
// to its own trim, the last carrying the message "has leading or trailing whitespace
// and will never match (names are compared verbatim)". An untrimmed name is therefore
// an entry claude ignores, not a denial: stored verbatim after only a blank check, it
// subtracted a server the member can actually run.
func denialEntry(entry json.RawMessage) (string, denialKind) {
	if !isJSONObject(entry) {
		return "", denialIgnored
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(entry, &obj); err != nil {
		return "", denialIgnored
	}
	nameRaw, hasName := obj["serverName"]
	cmdRaw, hasCommand := obj["serverCommand"]
	urlRaw, hasURL := obj["serverUrl"]
	if countSet(hasName, hasCommand, hasURL) != 1 {
		return "", denialIgnored // the schema refines to exactly one of the three
	}
	switch {
	case hasName:
		name, ok := jsonString(nameRaw)
		if !ok || name == "" || name != strings.TrimSpace(name) {
			return "", denialIgnored
		}
		return name, denialNamed
	case hasCommand:
		// The schema is a string array of at least one element (the command).
		if argv, ok := stringArray(cmdRaw); !ok || len(argv) == 0 {
			return "", denialIgnored
		}
		return "", denialUnnameable
	default:
		if _, ok := jsonString(urlRaw); !ok {
			return "", denialIgnored
		}
		return "", denialUnnameable
	}
}

// deniesNothing is the answer for a deniedMcpServers value claude rejects: the key is
// dropped, so nothing is denied. Measured, because that is what claude enforces.
func deniesNothing() DimensionState {
	return DimensionState{Measured: true, Targets: map[string]string{}}
}

// countSet is how many of its arguments are true, so "exactly one of three keys" is
// one expression rather than a chain of comparisons.
func countSet(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// mcpServerState reads the MCP servers .claude.json configures for the WHOLE dir,
// before denials: the top-level mcpServers key, which is claude's `user` scope.
//
// projects.<path>.mcpServers is deliberately not folded in. That is claude's `local`
// scope — it labels the location "local-scope MCP servers for this project" — and a
// server there is available in one checkout and nowhere else. An earlier version
// unioned every project scope into the dir's capability set, which measured how much
// each dir had been USED rather than what it can do: on a real pair, one dir carrying
// ten project entries and a fresh one carrying none, every server the busier dir had
// ever configured locally printed as a capability the other lacked, under a remedy
// ("align the config dirs") that cannot be followed — there is no dir-level place to
// put a local-scope server.
//
// Dropping it loses no signal about the question this section asks. Rotation is about
// the NEXT session, which lands in a path neither member has a local scope for, so a
// local-scope difference cannot change what that session can do. Pool members also
// keep their own dirs and work in their own worktrees, so their project keys are
// different paths and could never have agreed.
func mcpServerState(claude map[string]json.RawMessage) DimensionState {
	top, ok := capabilityObject(claude["mcpServers"])
	if !ok {
		return DimensionState{}
	}
	targets := map[string]string{}
	if !collectNamedObjects(top, targets) {
		return DimensionState{}
	}
	return DimensionState{Measured: true, Targets: targets}
}

// connectorState reads this dir's own disableClaudeAiConnectors. Absent or null is
// claude's default, which is connectors on — a real answer, not an unknown one. Any
// value that is neither JSON true nor false is unknown, because a build that cannot
// read the encoding must not report a state.
//
// This is the dir's declaration, not the state in force; see ConnectorState.
func connectorState(raw json.RawMessage) ConnectorState {
	switch {
	case len(raw) == 0, isJSONNull(raw):
		return ConnectorsOn
	case isJSONTrue(raw):
		return ConnectorsOff
	case isJSONFalse(raw):
		return ConnectorsOn
	default:
		return ConnectorsUnknown
	}
}

// capabilityObject decodes one name-keyed capability field. An absent or null field
// is an empty object and ok — the file was read and simply configures none of these.
// Any other non-object shape is not ok, which makes the dimension unmeasured.
func capabilityObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return map[string]json.RawMessage{}, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// fingerprint canonicalises a configuration value so two spellings of the same
// settings compare equal: json.Marshal sorts object keys, so key order and
// whitespace do not register as drift. "" means the value could not be
// canonicalised, and a caller must then skip the comparison rather than treat two
// blanks as a match.
func fingerprint(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// UseNumber keeps every JSON number as its literal digits. Decoding into `any`
	// without it turns each one into a float64, which silently collided values past
	// 2^53 — two dirs configuring one server with different numbers compared EQUAL —
	// and returned "" for a value outside float64's range, which a differ reads as
	// "no difference" rather than as "not comparable".
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	// Keeping the literal digits is only half of comparing numbers: json.Marshal
	// re-emits a json.Number verbatim, so 100 and 1e2 — the same number, two
	// spellings — fingerprinted apart and reported dirs that agree as drifting. Every
	// number is rewritten into one canonical spelling first, by digit arithmetic
	// rather than through a float, so the 2^53 property above survives it.
	out, err := json.Marshal(canonicalNumbers(v))
	if err != nil {
		return ""
	}
	return string(out)
}

// fingerprintOf canonicalises a value the caller had to reshape — sorting a version
// constraint list — before it could be compared.
func fingerprintOf(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return fingerprint(out)
}

// canonicalNumbers rewrites every json.Number in a decoded value into one spelling
// per numeric value, leaving everything else alone.
func canonicalNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		return json.Number(canonicalNumber(string(t)))
	case map[string]any:
		for k, val := range t {
			t[k] = canonicalNumbers(val)
		}
	case []any:
		for i, val := range t {
			t[i] = canonicalNumbers(val)
		}
	}
	return v
}

// canonicalNumber is one JSON number literal rewritten as <significand>e<exponent>,
// with no leading or trailing zeros in the significand — so 100, 1e2 and 1.0e2 all
// come out "1e2", and 1.0, 1 and 1e0 all come out "1e0".
//
// It is deliberately digit arithmetic and never a float: parsing to float64 to
// re-print is what collided values past 2^53 in the first place, and a literal like
// 1e400 has no float64 to be parsed into at all. Values are only ever compared with
// each other, so the form need not be readable, only unique per number. An input this
// cannot decompose is returned unchanged, which compares as the raw literal did.
func canonicalNumber(lit string) string {
	sign, rest := "", lit
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}

	mantissa, exp := rest, 0
	if i := strings.IndexAny(rest, "eE"); i >= 0 {
		parsed, err := strconv.Atoi(strings.TrimPrefix(rest[i+1:], "+"))
		if err != nil {
			return lit
		}
		mantissa, exp = rest[:i], parsed
	}
	intPart, fracPart := mantissa, ""
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		intPart, fracPart = mantissa[:i], mantissa[i+1:]
	}

	// Fold the fraction into the digit string by charging it to the exponent, so what
	// is left is an integer significand times a power of ten.
	digits := intPart + fracPart
	exp -= len(fracPart)
	if digits == "" || strings.TrimFunc(digits, func(r rune) bool { return r >= '0' && r <= '9' }) != "" {
		return lit
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0" // every spelling of zero, sign included
	}
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	return sign + trimmed + "e" + strconv.Itoa(exp)
}

// jsonString decodes a value claude's schema types as a string, reporting false for
// every other shape — null included, which json.Unmarshal would otherwise accept
// into a string as "".
func jsonString(raw json.RawMessage) (string, bool) {
	if isJSONNull(raw) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// stringArray decodes a value claude's schema types as an array of strings. Each
// element is checked on its own, because json.Unmarshal reads a null element into a
// string without error: `[null]` decoded as [""] and passed for a constraint list.
func stringArray(raw json.RawMessage) ([]string, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := jsonString(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// isJSONTrue, isJSONFalse, isJSONNull, isJSONObject and isJSONArray test a raw value
// against the JSON encoding, so a field claude reshapes reads as an unrecognised
// shape rather than as a parse failure for the whole file.
func isJSONTrue(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
}

func isJSONFalse(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("false"))
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// CapabilityReadFunc reads what a config dir is configured with. A function rather
// than an interface, mirroring IdentityReadFunc, because callers only ever need the
// one call — and because a nil reader is then a usable "feature off": CheckParity
// treats it as having nothing to report, so a caller that has not wired a reader
// renders exactly as it did before this existed.
type CapabilityReadFunc func(configDir string) (DirCapabilities, bool)

// ReadCapabilities is ReadDirCapabilities for a configured account, resolving and
// normalizing its config_dir (expanding ~) first. An inherit-env account — config_dir
// "" — reads nothing and reports ok=false: it injects no CLAUDE_CONFIG_DIR, so it has
// no dir of its own whose capabilities could be compared against a pool sibling's.
//
// The two answers are indistinguishable in the return, so a caller that must tell
// that account apart from a dir which merely failed to read has to check
// NormalizedConfigDir itself first. CheckParity needs exactly that distinction and so
// resolves the dir and calls ReadDirCapabilities directly; this method is the
// convenience form for a caller that does not, and mirrors ReadIdentity beside it.
func (a ClaudeAccount) ReadCapabilities() (DirCapabilities, bool) {
	return ReadDirCapabilities(a.NormalizedConfigDir())
}
