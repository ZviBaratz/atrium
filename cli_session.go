package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/session"
)

// loadStoredInstances reads the persisted session list without touching tmux,
// taking any lock, or writing anything at all.
//
// It deliberately does NOT call config.LoadState(). That helper is a loader, not
// a reader, and every one of its side effects is hostile from a headless process
// running beside a live TUI:
//
//   - it sweeps orphaned "<file>.tmp-*" files, which is exactly another
//     process's in-flight atomic write — deleting one makes the owner's rename
//     fail, silently losing that save
//   - it creates state.json from defaults when the file is absent
//   - it quarantines an unparseable file by renaming it to <file>.corrupt
//
// All three are right for the TUI, which owns the data dir and runs alone. None
// are acceptable here: `atrium ls` in a watch loop is the advertised usage, so
// this runs concurrently with a TUI's saves as a matter of routine.
//
// It also does not go through session.Storage.LoadInstances, which probes the live
// tmux server for every non-paused instance and reattaches or relaunches each one.
// The cost of reading the file directly is that everything here is last-known state,
// which is why `ls` publishes updated_at.
//
// Reading unsynchronised is safe because every write commits by rename, so a
// reader sees the previous file or the new one, never a torn mix.
func loadStoredInstances() ([]session.InstanceData, error) {
	data, err := readStateFile()
	if err != nil || data == nil {
		return nil, err
	}

	// Only the instance list is decoded. The rest of state.json is UI state that
	// a headless command has no business parsing, and must not fail on.
	var state struct {
		Instances json.RawMessage `json:"instances"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", config.StateFileName, err)
	}
	if len(state.Instances) == 0 {
		return nil, nil
	}

	var instances []session.InstanceData
	if err := json.Unmarshal(state.Instances, &instances); err != nil {
		return nil, fmt.Errorf("failed to read stored sessions: %w", err)
	}
	return instances, nil
}

// readStateFile reads state.json's raw bytes, creating and touching nothing —
// (nil, nil) when the file does not exist yet. Every headless read of persisted
// state goes through here, for all the reasons loadStoredInstances documents above.
func readStateFile() ([]byte, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return nil, fmt.Errorf("failed to locate the data directory: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.StateFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No state file yet. That is a fleet with no sessions, not an error —
			// and emphatically not a reason to create one.
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", config.StateFileName, err)
	}
	return data, nil
}

// loadStoredConfig reads config.json the same way loadStoredInstances reads
// state.json: directly, creating and touching nothing.
//
// config.LoadConfig would be the obvious call and is the wrong one here, for the
// first of the reasons above — loadJSONFile sweeps "<file>.tmp-*" before reading, and
// that glob is precisely the name writeFileAtomic gives an in-flight write. A TUI
// saving config.json from the settings panel at the wrong moment loses the save. It
// also seeds a default config.json when none exists, which is a write from a command
// whose whole contract is that it performs none.
//
// What it must NOT do is improve on LoadConfig, in either half. loadJSONFile decodes
// into a zero Config rather than onto the defaults, so a config.json that omits
// branch_prefix yields "" and not a username-derived prefix; decoding onto
// DefaultConfig here would read better and be worse, because this side would compute a
// prefix the draining TUI does not use and the title pre-check would answer about a
// branch nothing will create. And the fallback is SeededDefaultConfig, not
// DefaultConfig, because that is what LoadConfig falls back to: DefaultConfig carries
// no Profiles, so GetProfiles synthesizes a lone "claude" and `--profile codex` is
// refused on the machine where no TUI has ever run — the headless-bootstrap case the
// flag exists for, and the one machine with no config.json to read.
//
// So a present file is decoded exactly as LoadConfig decodes it and an absent or
// unparseable one yields exactly what LoadConfig yields. The one difference is that
// nothing is written back.
func loadStoredConfig() *config.Config {
	dir, err := config.GetConfigDir()
	if err != nil {
		return config.SeededDefaultConfig()
	}
	data, err := os.ReadFile(filepath.Join(dir, config.ConfigFileName))
	if err != nil {
		return config.SeededDefaultConfig()
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config.SeededDefaultConfig()
	}
	return &cfg
}

// loadStoredAccountKeys reads the account/pool-keyed slice of state.json for
// doctor's orphaned-key section: the cluster order, the rate-limit flags and the
// rotation cursors. Read-only, like every other headless state read (doctor can run
// beside a live TUI). A missing or unparseable file yields an empty set rather than
// an error: this is one diagnostic section, not a reason to fail the command.
func loadStoredAccountKeys() doctor.AccountKeyState {
	data, err := readStateFile()
	if err != nil || data == nil {
		return doctor.AccountKeyState{}
	}
	var state struct {
		AccountOrder        []string                   `json:"account_order"`
		AccountAvailability map[string]json.RawMessage `json:"account_availability"`
		AccountRotation     map[string]json.RawMessage `json:"account_rotation"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return doctor.AccountKeyState{}
	}
	return doctor.AccountKeyState{
		Order:        state.AccountOrder,
		Availability: mapKeys(state.AccountAvailability),
		Rotation:     mapKeys(state.AccountRotation),
	}
}

// mapKeys returns a map's keys; only the names matter to the orphaned-key check, so
// the values stay undecoded and a future field on them cannot break this read.
func mapKeys(m map[string]json.RawMessage) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// resolveSession finds the one session a selector names.
//
// Identity is the (Title, Path) pair, not the title: titles are unique only
// within a repo group, so the same title can legitimately exist in two repos —
// which is why session.Storage matches instances on that pair too. When a
// selector hits more than one, this reports every candidate rather than picking
// one, and pathFilter (the --path flag) is how the caller breaks the tie.
//
// Matching runs in tiers and stops at the first tier that hits, so an exact name
// always wins over a substring. Without that, a user with sessions "api" and
// "api-v2" could never address "api" at all — it would be permanently ambiguous.
func resolveSession(instances []session.InstanceData, selector, pathFilter string) (session.InstanceData, error) {
	if len(instances) == 0 {
		return session.InstanceData{}, fmt.Errorf("no sessions — run %s to create one", binName)
	}
	if strings.TrimSpace(selector) == "" {
		return session.InstanceData{}, fmt.Errorf("no session named: give a session name (see `%s ls`)", binName)
	}

	pool := instances
	if pathFilter != "" {
		// Guarded, because filepath.Clean("") is "." — cleaning an unset flag
		// would filter every session against the literal path "." and match none.
		want := filepath.Clean(pathFilter)
		pool = nil
		for _, d := range instances {
			if filepath.Clean(d.Path) == want {
				pool = append(pool, d)
			}
		}
		if len(pool) == 0 {
			return session.InstanceData{}, fmt.Errorf("no sessions in %q (see `%s ls`)", pathFilter, binName)
		}
	}

	lower := strings.ToLower(selector)
	label := func(d session.InstanceData) string {
		if d.DisplayName != "" {
			return d.DisplayName
		}
		return d.Title
	}

	tiers := []func(session.InstanceData) bool{
		func(d session.InstanceData) bool { return d.Title == selector },
		func(d session.InstanceData) bool { return d.TmuxName == selector },
		func(d session.InstanceData) bool {
			return strings.EqualFold(d.Title, selector) || strings.EqualFold(label(d), selector)
		},
		func(d session.InstanceData) bool {
			return strings.Contains(strings.ToLower(d.Title), lower) ||
				strings.Contains(strings.ToLower(label(d)), lower)
		},
	}

	for _, match := range tiers {
		var hits []session.InstanceData
		for _, d := range pool {
			if match(d) {
				hits = append(hits, d)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return session.InstanceData{}, ambiguousError(selector, hits)
		}
	}

	if pathFilter != "" {
		return session.InstanceData{}, fmt.Errorf("no session named %q in %q (see `%s ls`)", selector, pathFilter, binName)
	}
	return session.InstanceData{}, fmt.Errorf("no session named %q (see `%s ls`)", selector, binName)
}

func ambiguousError(selector string, hits []session.InstanceData) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%q is ambiguous — it matches:", selector)
	for _, d := range hits {
		fmt.Fprintf(&b, "\n  %s  (%s)", d.Title, d.Path)
	}
	b.WriteString("\nuse --path to pick one")
	return fmt.Errorf("%s", b.String())
}
