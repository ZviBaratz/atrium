package config

import "time"

// SelectPoolMember picks the next available member of a rotation pool, starting
// from cursor and scanning forward with wraparound. cursor is normalized with
// Euclidean modulo (((cursor % n) + n) % n) so a negative or stale oversized
// persisted cursor never panics or indexes out of range.
//
// It returns the chosen index and whether every member was unavailable. On an
// all-limited pool the returned index is the normalized cursor's own member
// (the caller's existing fallback), not 0 and not the soonest-reset member —
// callers that want a "resets soonest" hint for that case should combine this
// with SoonestResetMember themselves. An empty members slice returns (-1,
// false): there is nothing to select and nothing to report as exhausted.
func SelectPoolMember(members []ClaudeAccount, avail map[string]AccountAvailability, cursor int, now time.Time) (idx int, allLimited bool) {
	n := len(members)
	if n == 0 {
		return -1, false
	}
	start := ((cursor % n) + n) % n
	chosen := start // defensive fallback: an all-limited pool returns the cursor's own member
	allLimited = true
	for k := 0; k < n; k++ {
		i := (start + k) % n
		if AccountAvailable(avail[members[i].Name], now) {
			chosen = i
			allLimited = false
			break
		}
	}
	return chosen, allLimited
}

// SoonestResetMember returns the index of the member whose limit resets soonest.
// Members with a parseable Until sort by that time; indefinite or unparseable
// sort last; all-indefinite returns 0 (the cursor's natural start).
func SoonestResetMember(members []ClaudeAccount, avail map[string]AccountAvailability) int {
	best, bestSet := 0, false
	var bestT time.Time
	for i, mem := range members {
		av := avail[mem.Name]
		if av.Until == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, av.Until)
		if err != nil {
			continue
		}
		if !bestSet || t.Before(bestT) {
			best, bestT, bestSet = i, t, true
		}
	}
	return best
}
