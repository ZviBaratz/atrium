package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
)

// loadStoredInstances reads the persisted session list without touching tmux or
// taking any lock.
//
// It deliberately does not go through session.Storage.LoadInstances, which calls
// reattach() on every instance and so probes the live tmux server: the headless
// commands must stay read-only and must work with no server running at all. The
// cost is that everything read here is last-known state, which is why `ls`
// publishes updated_at.
//
// Reading state.json unsynchronised is safe because every write commits by
// rename (config.writeFileAtomic), so a reader sees the previous file or the new
// one, never a torn mix.
func loadStoredInstances() ([]session.InstanceData, error) {
	raw := config.LoadState().GetInstances()
	if len(raw) == 0 {
		return nil, nil
	}
	var data []session.InstanceData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("failed to read stored sessions: %w", err)
	}
	return data, nil
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
