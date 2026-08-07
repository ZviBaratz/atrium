package keys

import "testing"

// The synthesizer has to agree with the hand-authored labels, or a rebound
// action's key would render in a different style from the ones beside it in the
// same bar — "ctrl+x" next to "ctrl-p", "up" next to "↓".
//
// Every dispatched entry, not a sample: the point is that Label could stand in
// for any of them. DocOnly entries are excluded because they are outside the
// remap namespace (Action == ""), so Label is never asked to replace their
// labels — and one of them could not be reproduced anyway: ctrl+pgup/ctrl+pgdown
// is written "ctrl-pgup/pgdn", which folds the shared modifier and abbreviates
// the second key. That compression is only safe because a human checked those
// two keys read unambiguously together; a generator applying it blindly would
// turn unrelated pairs into nonsense.
func TestLabelReproducesEveryDefault(t *testing.T) {
	for _, e := range Registry {
		if e.DocOnly {
			continue
		}
		want := e.Binding.Help().Key
		if got := Label(e.Binding.Keys()); got != want {
			t.Errorf("Label(%q) = %q, but %q is labelled %q by hand",
				e.Binding.Keys(), got, e.Binding.Help().Desc, want)
		}
	}
}

// The exclusion above is a claim about one specific entry, so pin it: if that
// label is ever regularised, the comment explaining the exclusion is stale and
// the exclusion itself may no longer be needed.
func TestLabel_DocumentedOnlyExceptionIsStillTheOnlyOne(t *testing.T) {
	for _, e := range Registry {
		if !e.DocOnly {
			continue
		}
		want := e.Binding.Help().Key
		got := Label(e.Binding.Keys())
		if e.Name == KeySessionCycle {
			if got == want {
				t.Errorf("session-cycle now labels mechanically as %q — the exclusion "+
					"in TestLabelReproducesEveryDefault has gone stale", got)
			}
			continue
		}
		if got != want {
			t.Errorf("Label(%q) = %q, want %q — only session-cycle is expected to differ",
				e.Binding.Keys(), got, want)
		}
	}
}

// LabelOf and PrimaryKey are the two accessors prose and dispatch read instead
// of a literal, so an unbound action must give an empty string rather than
// panic or return a stale spelling.
func TestLabelOfAndPrimaryKey_ReadTheAppliedBinding(t *testing.T) {
	if got := LabelOf(KeyResume); got != "r" {
		t.Errorf("LabelOf(resume) = %q, want %q", got, "r")
	}
	if got := PrimaryKey(KeyEnter); got != "enter" {
		t.Errorf("PrimaryKey(open) = %q, want the first of its keys, %q", got, "enter")
	}
	if got := KillKey(); got != "ctrl+x" {
		t.Errorf("KillKey() = %q, want %q", got, "ctrl+x")
	}
	// KeyScreensaver has no Registry entry, so it stands in for any name with no
	// binding — the shape an unbound action takes once overrides land.
	if got := PrimaryKey(KeyScreensaver); got != "" {
		t.Errorf("PrimaryKey of an unbound action = %q, want empty", got)
	}
}
