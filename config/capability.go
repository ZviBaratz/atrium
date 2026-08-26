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
// reported as neither present nor absent.
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

	// dimensionLast is the highest real axis, and it is what Dimensions() ranges to
	// rather than a hand-written slice — so a const added above it is walked without
	// anyone remembering to list it. A const added BELOW it is still missed, which is
	// what TestDimensionsIsTheWholeConstRange holds by asserting nothing past this
	// sentinel has a Noun or a State.
	dimensionLast = DimensionMCPServer
)

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
// onboarded in is not a dir with no plugins, and reporting it as one would accuse
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
func marketplaceField(settings map[string]json.RawMessage) json.RawMessage {
	if raw, ok := settings["extraKnownMarketplaces"]; ok {
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
			var constraints []string
			if err := json.Unmarshal(val, &constraints); err != nil {
				return DimensionState{} // an array of something other than strings
			}
			targets[name] = fingerprint(val)
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
// Two consequences of that schema are encoded rather than guessed. A list holding any
// entry that is not one of those three shapes is rejected by claude in its ENTIRETY
// ("deniedMcpServers was present but invalid and was dropped; its entries cannot be
// enforced"), so such a list denies nothing: measured and empty, matching what claude
// enforces rather than what the file appears to ask for. And a valid entry keyed on
// serverCommand or serverUrl is enforced but cannot be expressed as a server name, so
// it makes the list unmeasured rather than short — treating it as no denial would
// report a member as allowing a server it blocks.
//
// Validity is decided over the whole list before any name is collected, because
// claude's rejection is wholesale: a command-keyed entry followed by a malformed one
// is a dropped key, not an unnameable denial.
func denialNames(raw json.RawMessage) DimensionState {
	if len(raw) == 0 || isJSONNull(raw) {
		return DimensionState{Measured: true, Targets: map[string]string{}}
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return deniesNothing()
	}

	names := map[string]string{}
	unnameable := false
	for _, entry := range entries {
		if !isJSONObject(entry) {
			return deniesNothing()
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(entry, &obj); err != nil {
			return deniesNothing()
		}
		nameRaw, hasName := obj["serverName"]
		_, hasCommand := obj["serverCommand"]
		_, hasURL := obj["serverUrl"]
		if countSet(hasName, hasCommand, hasURL) != 1 {
			return deniesNothing() // the schema refines to exactly one of the three
		}
		if !hasName {
			unnameable = true
			continue
		}
		var name string
		if err := json.Unmarshal(nameRaw, &name); err != nil {
			return deniesNothing()
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		names[name] = ""
	}
	if unnameable {
		return DimensionState{}
	}
	return DimensionState{Measured: true, Targets: names}
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

// mcpServerState reads .claude.json's configured MCP servers, before denials.
//
// They are recorded per project, under projects.<path>.mcpServers — measured on a
// live onboarded dir, where the top-level mcpServers key was absent and every
// projects entry carried one. The top-level key is read too, since claude's scopes
// (local, user, project) are not all per-project and only the local one was
// observed. The result is the union across scopes, so a difference means one dir
// configures the server SOMEWHERE and the other configures it nowhere. Two dirs that
// both have it, under different project paths, compare equal — the union
// deliberately does not claim per-project availability, because doctor is not asked
// about a repo.
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
		// A null scope is a project entry with nothing configured under it, which is
		// an answer. This arm is explicit because without it the value fell through
		// json.Unmarshal into a nil map and read the same way by accident, making
		// `null` the one unrecognised shape that did not force the dimension
		// unmeasured.
		if isJSONNull(raw) {
			continue
		}
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
// Callers that must tell that account apart from a dir that merely failed to read
// should check NormalizedConfigDir first, the way CheckParity does.
func (a ClaudeAccount) ReadCapabilities() (DirCapabilities, bool) {
	return ReadDirCapabilities(a.NormalizedConfigDir())
}
