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
// This file reads that state out of <configDir>/settings.json and
// <configDir>/.claude.json. Like ReadAccountIdentity beside it, and unlike
// LoadConfig/LoadState in this same package, it is a pure, strictly READ-ONLY probe:
// it creates nothing, rewrites nothing, and so can run beside a live TUI or a live
// agent.
//
// # Local file reads only
//
// No network, no bearer token, no subprocess. The authoritative source of claude.ai
// connector GRANT state is an HTTP call carrying the dir's own OAuth token, and
// three things rule it out here: it would be Atrium's first outbound request, its
// first handling of token material, and it would not work on macOS at all, where
// claude keeps credentials in the Keychain rather than in a readable file. What this
// probe measures is therefore file-level parity — what each dir is CONFIGURED with —
// which is the layer rotation can actually be aligned on.
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

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DirCapabilities is what one config dir is configured to bring to a session.
// Every name list is sorted and deduplicated, so two dirs are comparable by value
// and the rendered diff does not reorder between runs.
//
// ConnectorsOff is stated in the negative because the file is: the setting is spelled
// disableClaudeAiConnectors, and inverting it here would put a zero value
// ("connectors on") in front of a dir that never mentioned them, which is the same
// thing the field's absence already means.
type DirCapabilities struct {
	EnabledPlugins   []string // settings.json  enabledPlugins
	Marketplaces     []string // settings.json  extraKnownMarketplaces
	MCPServers       []string // .claude.json   mcpServers
	DeniedMCPServers []string // settings.json  deniedMcpServers
	ConnectorsOff    bool     // settings.json  disableClaudeAiConnectors
}

// ReadDirCapabilities returns what configDir is configured with, or ok=false when
// that question cannot be answered there.
//
// ok=false is reserved for "unknown", never for "has nothing". Three things produce
// it: a relative (or empty) dir, a file that is present but malformed or unreadable,
// and a dir holding neither file. The last is the case worth being deliberate about:
// a dir claude was never onboarded in is not a dir with no plugins, and reporting it
// as one would accuse every pool containing it of drift the user cannot fix. A dir
// holding one readable file and not the other IS answerable — an absent settings.json
// genuinely enables no plugins — so it reads ok=true.
//
// The safe direction for a caller is to skip an ok=false member rather than compare
// it: a missed warning costs a diagnosis the user did not have anyway, while a false
// one sends them aligning two dirs that already agree.
//
// A relative dir is refused rather than joined against the working directory, for
// the reason ReadAccountIdentity refuses one: "" is the routing value meaning
// "inherit the ambient env", and resolving it to ./settings.json would report on a
// dir no session has any relationship to.
func ReadDirCapabilities(configDir string) (DirCapabilities, bool) {
	if !filepath.IsAbs(configDir) {
		return DirCapabilities{}, false
	}

	// Every field is json.RawMessage so that only genuinely malformed JSON (or a
	// top-level value that is not an object) fails the read. A single field claude
	// reshapes in a future version then degrades that field alone instead of taking
	// the four still-readable ones down with it. ReadAccountIdentity makes the
	// opposite call for the same reason: there the reshaped object IS the whole
	// answer, so there is nothing left to report once it stops parsing.
	var settings struct {
		EnabledPlugins   json.RawMessage `json:"enabledPlugins"`
		Marketplaces     json.RawMessage `json:"extraKnownMarketplaces"`
		DeniedMCPServers json.RawMessage `json:"deniedMcpServers"`
		ConnectorsOff    json.RawMessage `json:"disableClaudeAiConnectors"`
	}
	settingsFound, ok := readJSONIfPresent(filepath.Join(configDir, "settings.json"), &settings)
	if !ok {
		return DirCapabilities{}, false
	}

	var claude struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	claudeFound, ok := readJSONIfPresent(filepath.Join(configDir, ".claude.json"), &claude)
	if !ok {
		return DirCapabilities{}, false
	}

	if !settingsFound && !claudeFound {
		return DirCapabilities{}, false // not a claude config dir; nothing to compare
	}

	return DirCapabilities{
		EnabledPlugins:   nameSet(settings.EnabledPlugins),
		Marketplaces:     nameSet(settings.Marketplaces),
		MCPServers:       nameSet(claude.MCPServers),
		DeniedMCPServers: nameSet(settings.DeniedMCPServers),
		ConnectorsOff:    isJSONTrue(settings.ConnectorsOff),
	}, true
}

// readJSONIfPresent unmarshals path into v. found reports whether the file exists;
// ok is false only when the file could not be read for a reason other than absence,
// or held JSON v cannot parse. An absent file is (false, true): missing is an
// answer, unreadable is not.
func readJSONIfPresent(path string, v any) (found, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Only "no such file" means the dir genuinely lacks this file. A permission
		// error or a directory in the file's place is an unanswered question, and
		// must not read as an empty capability set.
		return false, errors.Is(err, os.ErrNotExist)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, false
	}
	return true, true
}

// nameSet reduces one capability field to the set of names it grants, sorted and
// deduplicated. Two spellings are accepted — an object keyed by name, which is how
// settings.json holds enabledPlugins and extraKnownMarketplaces, and a plain array of
// names — so a field written either way, or reshaped from one to the other by a
// future claude, still yields its names rather than nothing.
//
// In the object form a value of exactly false EXCLUDES the key: enabledPlugins maps a
// plugin name to a bool, so a key carrying false is not a plugin the dir has.
// Counting it would do both kinds of damage at once — credit a dir with a capability
// it does not have, and hide the precise drift this probe exists to find, one dir
// with a plugin on and another with it off.
//
// Any other shape yields no names. That is the false-empty direction, taken for
// these fields only because the alternative — failing the whole read — would silence
// the fields that are still perfectly readable, and a silent parity section reads as
// "these accounts agree".
func nameSet(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		for name, enabled := range obj {
			if !isJSONFalse(enabled) {
				names = append(names, name)
			}
		}
		return sortedUnique(names)
	}
	if err := json.Unmarshal(raw, &names); err == nil {
		return sortedUnique(names)
	}
	return nil
}

// sortedUnique trims, drops empties and duplicates, and sorts. Sorting is not
// cosmetic: map iteration order is randomized per run, so an unsorted list would
// make two dirs holding the same plugins compare unequal at random.
func sortedUnique(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// isJSONTrue and isJSONFalse test a raw value against the JSON literal, so a field
// claude reshapes reads as neither rather than as a parse failure.
func isJSONTrue(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
}

func isJSONFalse(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("false"))
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
func (a ClaudeAccount) ReadCapabilities() (DirCapabilities, bool) {
	return ReadDirCapabilities(a.NormalizedConfigDir())
}
