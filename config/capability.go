package config

// Reading what a config dir can actually DO.
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
// This file reads that state out of <configDir>/settings.json,
// <configDir>/settings.local.json and <configDir>/.claude.json. Like
// ReadAccountIdentity beside it, and unlike LoadConfig/LoadState in this same
// package, it is a pure, strictly READ-ONLY probe: it creates nothing, rewrites
// nothing, and so can run beside a live TUI or a live agent.
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
// # Local file reads only
//
// No network, no bearer token, no subprocess. The authoritative source of claude.ai
// connector GRANT state is an HTTP call carrying the dir's own OAuth token, and
// three things rule it out here: it would be Atrium's first outbound request, its
// first handling of token material, and it would not work on macOS at all, where
// claude keeps credentials in the Keychain rather than in a readable file. So this
// probe measures file-level parity — what each dir is CONFIGURED with — which is the
// layer rotation can actually be aligned on, and NOT what claude.ai has granted.
// A pool whose members differ only in their upstream grants reads as being in parity
// here; that is a limit of the file layer, not a clean bill.
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
// enabledMcpjsonServers and disabledMcpjsonServers (per project, in .claude.json)
// gate the servers a repo's own .mcp.json offers. They are a real per-dir difference,
// but naming one means reading a file that belongs to the repo rather than to the
// dir, and the same .mcp.json is shared by every member — so a difference there is
// reported as neither present nor absent. deniedMcpServers and allowedMcpServers are
// also unread: they are enterprise managed-settings policy holding URL patterns, not
// per-dir settings.json keys holding server names. A machine-wide policy cannot
// differ between two dirs in one pool, so it is not a parity axis at all.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	// DimensionMarketplace is settings.json's extraKnownMarketplaces.
	DimensionMarketplace
	// DimensionMCPServer is .claude.json's mcpServers, at the top level and under
	// every projects.<path> scope.
	DimensionMCPServer
)

// Dimensions is the fixed order a comparison walks, so a rendered report does not
// reorder between runs on an unchanged config.
func Dimensions() []Dimension {
	return []Dimension{DimensionPlugin, DimensionMarketplace, DimensionMCPServer}
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

// ConnectorState is whether a dir has claude.ai connectors switched on.
type ConnectorState int

const (
	// ConnectorsUnknown means the setting could not be read: settings.json was
	// absent, or held a value for disableClaudeAiConnectors that is neither JSON
	// true nor false. Never evidence of either state. Iota 0 so an unset field, a
	// var and a map miss all read as "no evidence" rather than as "connectors on".
	ConnectorsUnknown ConnectorState = iota
	// ConnectorsOn means connectors are available: disableClaudeAiConnectors is
	// false or absent, which is claude's default.
	ConnectorsOn
	// ConnectorsOff means disableClaudeAiConnectors is true.
	ConnectorsOff
)

// DimensionState is what one config dir holds along one Dimension.
//
// The zero value is the honest default: Measured false means the file that would
// have answered was absent, or held this field in a shape this build does not
// recognise, so the dir contributes no evidence either way. Unmeasured being the
// ZERO value is the point — a companion bool defaulting the other way licenses a
// confident answer from an unset field.
type DimensionState struct {
	// Measured is true when the source file was read and this field's shape was
	// understood. False means unknown, never "configured with nothing".
	Measured bool
	// Targets maps each configured name to a fingerprint of the value it was
	// configured WITH — a marketplace's source, an MCP server's URL or command — or
	// "" when the shape carries no comparable target, as enabledPlugins does not
	// (its values are bools). A name present with target "" means "configured, but
	// there is nothing here to compare beyond the name".
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
	// because they do not share a source file: enabledPlugins and
	// extraKnownMarketplaces come from settings.json (merged with
	// settings.local.json) and mcpServers from .claude.json, so a dir can answer one
	// and not the other. A single dir-wide "readable" flag was the earlier shape and
	// was wrong: it reported "claude was never onboarded here" as "this member
	// configures zero MCP servers".
	Plugins      DimensionState
	Marketplaces DimensionState
	MCPServers   DimensionState
	// Connectors is stated as a tri-state rather than a bool for the same reason.
	Connectors ConnectorState
}

// State is the dimension addressed by d, so a comparison can iterate Dimensions()
// instead of naming fields — the thing that stops a fourth axis from being added
// without the differ noticing.
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
// present but unreadable or unparseable, and a dir holding none of the three files.
// The last is the case worth being deliberate about: a dir claude was never
// onboarded in is not a dir with no plugins, and reporting it as one would accuse
// every pool containing it of drift the user cannot fix.
//
// A dir holding some of the files IS spoken for — ok=true — with the dimensions
// sourced from the absent files left unmeasured. That per-dimension distinction is
// the whole reason DimensionState exists: mcpServers lives only in .claude.json, so
// a dir without one has an unknown MCP set, not an empty one.
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
	// claude layers settings.local.json over settings.json in the same scope, so a
	// dir that enables a plugin only in the local file really does have it. Reading
	// just the base file reported that dir as lacking what it in fact has.
	local, localFound, ok := readJSONObject(filepath.Join(configDir, "settings.local.json"))
	if !ok {
		return DirCapabilities{}, false
	}
	claude, claudeFound, ok := readJSONObject(filepath.Join(configDir, ".claude.json"))
	if !ok {
		return DirCapabilities{}, false
	}

	if !settingsFound && !localFound && !claudeFound {
		return DirCapabilities{}, false // not a claude config dir; nothing to compare
	}

	var caps DirCapabilities
	if settingsFound || localFound {
		merged := mergeObjects(settings, local)
		caps.Plugins = pluginState(merged["enabledPlugins"])
		caps.Marketplaces = namedObjectState(merged["extraKnownMarketplaces"])
		caps.Connectors = connectorState(merged["disableClaudeAiConnectors"])
	}
	if claudeFound {
		caps.MCPServers = mcpServerState(claude)
	}
	return caps, true
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
	if obj == nil {
		// The literal null is the one top-level non-object that unmarshals into a
		// map without error, so it needs saying out loud: the file parsed but names
		// no settings, which is an unanswered question rather than an empty one.
		return nil, true, false
	}
	return obj, true, true
}

// mergeObjects layers over onto base per key, the way claude layers
// settings.local.json onto settings.json.
func mergeObjects(base, over map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(base)+len(over))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range over {
		merged[k] = v
	}
	return merged
}

// pluginState reads enabledPlugins, an object mapping a plugin name to a bool.
//
// A key is enabled only when its value is exactly JSON true. false and null are the
// plugin being off — counting them would credit a dir with a capability it does not
// have and hide the precise drift this probe exists to find, one dir with a plugin
// on and another with it off. Any other value shape makes the whole dimension
// unmeasured, because a build that does not understand the encoding cannot tell on
// from off and must not guess either way.
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
			targets[name] = "" // a bool carries no target to compare
		case isJSONFalse(val), isJSONNull(val):
			continue // explicitly not enabled
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
		fp := fingerprint(val)
		if prev, seen := into[name]; seen && prev != fp {
			// One name configured two ways in one dir — two project scopes naming
			// the same server differently. Neither spelling is the target, so carry
			// both: a sibling configured the same two ways still compares equal, and
			// one configured a third way still compares different.
			into[name] = mergeFingerprints(prev, fp)
			continue
		}
		into[name] = fp
	}
	return true
}

// mcpServerState reads .claude.json's MCP servers.
//
// They are recorded per project, under projects.<path>.mcpServers, which is what
// `claude mcp add` writes by default; a top-level mcpServers key is read too but is
// absent from real onboarded dirs. The result is the union across scopes, so a
// difference means one dir configures the server SOMEWHERE and the other configures
// it nowhere. Two dirs that both have it, under different project paths, compare
// equal — the union deliberately does not claim per-project availability, because
// doctor is not asked about a repo.
func mcpServerState(claude map[string]json.RawMessage) DimensionState {
	targets := map[string]string{}

	top, ok := capabilityObject(claude["mcpServers"])
	if !ok || !collectNamedObjects(top, targets) {
		return DimensionState{}
	}

	projectsRaw := claude["projects"]
	if len(projectsRaw) == 0 || isJSONNull(projectsRaw) {
		return DimensionState{Measured: true, Targets: targets}
	}
	var projects map[string]json.RawMessage
	if err := json.Unmarshal(projectsRaw, &projects); err != nil {
		return DimensionState{}
	}
	for _, raw := range projects {
		var scope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &scope); err != nil {
			return DimensionState{}
		}
		servers, ok := capabilityObject(scope["mcpServers"])
		if !ok || !collectNamedObjects(servers, targets) {
			return DimensionState{}
		}
	}
	return DimensionState{Measured: true, Targets: targets}
}

// connectorState reads disableClaudeAiConnectors. Absent or null is claude's
// default, which is connectors on — a real answer, not an unknown one. Any value
// that is neither JSON true nor false is unknown, because a build that cannot read
// the encoding must not report a state.
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
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// mergeFingerprints combines two fingerprints for one name into a sorted, stable
// composite, so a dir configuring a server two ways has one comparable value.
func mergeFingerprints(a, b string) string {
	parts := append(strings.Split(a, "\n"), strings.Split(b, "\n")...)
	slices.Sort(parts)
	return strings.Join(slices.Compact(parts), "\n")
}

// isJSONTrue, isJSONFalse, isJSONNull and isJSONObject test a raw value against the
// JSON encoding, so a field claude reshapes reads as an unrecognised shape rather
// than as a parse failure for the whole file.
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
// Callers that must tell that account apart from a dir that merely failed to read
// should check NormalizedConfigDir first, the way CheckParity does.
func (a ClaudeAccount) ReadCapabilities() (DirCapabilities, bool) {
	return ReadDirCapabilities(a.NormalizedConfigDir())
}
