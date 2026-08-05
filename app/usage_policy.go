package app

// Who is allowed to hold a context-window reading (#596).
//
// The chip's safety property is "absent rather than wrong", and two conditions
// make a reading wrong regardless of how carefully the transcript was parsed.
// Both are facts about the fleet or the config, not about the session, so
// neither can be decided inside Instance.ComputeUsage — hence a policy object
// built once per tick on the main thread and consulted by the poll goroutines.
//
// It gates the READ, not the render. An earlier draft suppressed the chip in
// the row renderer instead, which looks equivalent and is not: a suppressed row
// still computed and stored its poisoned reading, so killing the neighbour that
// caused the suppression made the survivor's row paint the dead session's token
// count — and the stamp memo kept it there until the survivor took another
// turn. Declining to read is what makes the value absent instead of hidden.

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
)

// usagePolicy decides, for one tick, which sessions may take a context reading.
// The zero value allows nothing, which is the safe default for a caller that
// forgot to build one.
type usagePolicy struct {
	// enabled mirrors config.GetContextIndicator() != off. A chip nobody can see
	// should also cost nothing: UsageInfo has exactly one consumer (the row
	// renderer), so with the chip off every reading is a directory walk taken for
	// a value that is never displayed.
	enabled bool
	// ambiguous holds the transcript project directories claimed by more than one
	// session (see session.Instance.ContextSourceKey). nil when there is no
	// collision, which is the common case and makes the lookup a nil-map read.
	ambiguous map[string]bool
}

// newUsagePolicy builds the per-tick policy from the config flag and the WHOLE
// fleet — every instance the list holds, not just this tick's poll targets and
// not just the visible ones. A collision is a fact about what is on disk: a
// session excluded by the tick throttle, hidden by a filter, or scrolled out of
// view is writing into the shared directory just the same.
func newUsagePolicy(enabled bool, instances []*session.Instance) usagePolicy {
	p := usagePolicy{enabled: enabled}
	if !enabled {
		// Nothing will be read, so the collision set would never be consulted.
		return p
	}
	seen := make(map[string]bool, len(instances))
	for _, inst := range instances {
		key := inst.ContextSourceKey()
		if key == "" {
			continue
		}
		if seen[key] {
			if p.ambiguous == nil {
				p.ambiguous = make(map[string]bool, 1)
			}
			p.ambiguous[key] = true
			continue
		}
		seen[key] = true
	}
	return p
}

// allows reports whether inst may take a context reading this tick. A false
// answer means the poll layer clears whatever the session was holding — see
// session.Instance.ClearUsage for why a stale reading is not left in place.
func (p usagePolicy) allows(inst *session.Instance) bool {
	if !p.enabled {
		return false
	}
	key := inst.ContextSourceKey()
	return key != "" && !p.ambiguous[key]
}

// usagePolicy snapshots the policy on the main thread, where reading config and
// walking the instance list can't race the poll goroutines that consult it.
func (m *home) usagePolicy() usagePolicy {
	return newUsagePolicy(
		m.appConfig.GetContextIndicator() != config.ContextIndicatorOff,
		m.list.GetInstances())
}
