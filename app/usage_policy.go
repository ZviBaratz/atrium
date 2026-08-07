package app

// Who is allowed to hold a transcript reading, and which one (#596, #392).
//
// The chip's safety property is "absent rather than wrong", and two conditions
// make a reading wrong regardless of how carefully the transcript was parsed.
// Both are facts about the fleet or the config, not about the session, so
// neither can be decided inside Instance.ComputeUsage — hence a policy object
// built once per tick on the main thread and consulted by the poll goroutines.
//
// Since #392 the config half also decides WHICH reading is taken. The occupancy
// chip and the cost chip share one column, so at most one of them is on screen,
// and reading for the other would be a directory walk taken for a value nothing
// displays.
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

// usagePolicy decides, for one tick, which sessions may take a transcript
// reading and WHICH reading that is. The zero value allows nothing, which is the
// safe default for a caller that forgot to build one.
type usagePolicy struct {
	// mode is the normalized config.ContextIndicator for this tick. The empty
	// string is the zero value's "read nothing" and is deliberately NOT the same
	// as config's empty string, which normalizes to percent — a policy nobody
	// built must not turn out to be the default policy.
	//
	// It is the mode rather than a bool because the chip's column is shared: the
	// occupancy reading and the cost reading are two different walks over the same
	// directory, and only the one the user is looking at should be paid for. A
	// chip nobody can see costs nothing at all, since UsageInfo and CostInfo have
	// exactly one consumer each — the row renderer.
	mode string
	// ambiguous holds the transcript project directories claimed by more than one
	// session (see session.Instance.ContextSourceKey). nil when there is no
	// collision, which is the common case and makes the lookup a nil-map read.
	ambiguous map[string]bool
}

// newUsagePolicy builds the per-tick policy from the configured chip mode and
// the WHOLE fleet — every instance the list holds, not just this tick's poll
// targets and not just the visible ones. A collision is a fact about what is on
// disk: a session excluded by the tick throttle, hidden by a filter, or scrolled
// out of view is writing into the shared directory just the same.
func newUsagePolicy(mode string, instances []*session.Instance) usagePolicy {
	p := usagePolicy{mode: mode}
	if !p.readsContext() && !p.readsCost() {
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

// readsContext reports whether this tick's mode wants a context-occupancy
// reading, and readsCost whether it wants a spend estimate. Exactly one can be
// true; "off" and the zero value make both false.
func (p usagePolicy) readsContext() bool {
	switch p.mode {
	case config.ContextIndicatorCount, config.ContextIndicatorPercent, config.ContextIndicatorBar:
		return true
	default:
		return false
	}
}

func (p usagePolicy) readsCost() bool { return p.mode == config.ContextIndicatorCost }

// allowsContext reports whether inst may take a context reading this tick, and
// allowsCost whether it may take a cost reading. A false answer means the poll
// layer clears whatever the session was holding — see session.Instance.ClearUsage
// and ClearCost for why a stale reading is not left in place.
//
// The ambiguity rule is shared because the hazard is: both readings key on the
// same project directory, so two sessions reading one directory poison both. It
// is if anything worse for cost, where a wrong attribution is not a momentary
// misreading but a total that stays wrong for the session's whole life.
func (p usagePolicy) allowsContext(inst *session.Instance) bool {
	return p.readsContext() && p.unambiguous(inst)
}

func (p usagePolicy) allowsCost(inst *session.Instance) bool {
	return p.readsCost() && p.unambiguous(inst)
}

// unambiguous reports whether inst reads a transcript directory no other session
// claims. "" means the session reads nothing at all — unstarted, a non-claude
// program — so it can neither hold a reading nor spoil anyone else's.
func (p usagePolicy) unambiguous(inst *session.Instance) bool {
	key := inst.ContextSourceKey()
	return key != "" && !p.ambiguous[key]
}

// usagePolicy snapshots the policy on the main thread, where reading config and
// walking the instance list can't race the poll goroutines that consult it.
func (m *home) usagePolicy() usagePolicy {
	return newUsagePolicy(m.appConfig.GetContextIndicator(), m.list.GetInstances())
}
