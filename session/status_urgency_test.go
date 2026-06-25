package session

import "testing"

// StatusUrgency encodes the action-priority used by the "status" sort mode: a
// blocked prompt is most urgent, then an unread (finished, unseen) Ready, then a
// seen Ready, then working/starting, with Paused last. Lower ranks sort first.
func TestStatusUrgency_Ordering(t *testing.T) {
	needsInput := StatusUrgency(NeedsInput, false)
	unreadReady := StatusUrgency(Ready, true)
	seenReady := StatusUrgency(Ready, false)
	running := StatusUrgency(Running, false)
	loading := StatusUrgency(Loading, false)
	paused := StatusUrgency(Paused, false)

	// The full strict ordering, most urgent (lowest) to least.
	ranks := []int{needsInput, unreadReady, seenReady, running, loading, paused}
	for i := 1; i < len(ranks); i++ {
		if ranks[i-1] >= ranks[i] {
			t.Errorf("rank %d (%d) should be strictly less than rank %d (%d)", i-1, ranks[i-1], i, ranks[i])
		}
	}

	// Unread only matters for Ready: it splits Ready into two adjacent tiers and
	// does not change any other status's rank.
	if StatusUrgency(NeedsInput, true) != needsInput {
		t.Error("unread must not change NeedsInput rank")
	}
	if StatusUrgency(Running, true) != running {
		t.Error("unread must not change Running rank")
	}
}
