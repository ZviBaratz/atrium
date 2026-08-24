package app

// Guards on the surface registry (surfaceSpecs): the table the five per-state
// readers select through. TestEverySurfaceSpecIsComplete is the completeness
// half of the drift guard; behavior stays pinned by the reader-level suites
// (frame parity + colour fingerprints for render/size/barVisible, the
// frame-restore walk and per-surface suites for keys, the paste suites for
// paste), which is why nothing here restates which states carry nil fields —
// that second copy would drift.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEverySurfaceSpecIsComplete requires every state's slot in surfaceSpecs to
// be filled and self-identifying: st equals the index (a forgotten slot is
// zero-valued and fails here before its false bar bit or nil handler can ship,
// and a misplaced entry fails the same way), and fixture is non-empty, unique,
// and names an existing golden under testdata/frames — the same name
// frameStates hands the parity, fingerprint, bounds and hardtab sweeps.
//
// Mutation-verified: deleting an entry (stateDefault's fails on the empty
// fixture, since its st is the zero value; any other state's fails on st),
// changing an entry's st to another state's, duplicating a fixture name, and
// pointing a fixture at a missing golden each fail here.
func TestEverySurfaceSpecIsComplete(t *testing.T) {
	seen := make(map[string]state, len(surfaceSpecs))
	for st := stateDefault; st < numStates; st++ {
		spec := surfaceSpecs[st]
		require.Equalf(t, st, spec.st,
			"surfaceSpecs[%d] does not self-identify (st=%d): the slot was forgotten or its entry is keyed under the wrong state", int(st), int(spec.st))
		require.NotEmptyf(t, spec.fixture,
			"surfaceSpecs[%d] has no fixture name — every surface needs a golden for the frame sweeps", int(st))
		if prev, dup := seen[spec.fixture]; dup {
			t.Errorf("fixture %q is claimed by states %d and %d — frameStates would render one golden twice and another never", spec.fixture, int(prev), int(st))
		}
		seen[spec.fixture] = st
		golden := filepath.Join("testdata", "frames", spec.fixture+".txt")
		if _, err := os.Stat(golden); err != nil {
			t.Errorf("surfaceSpecs[%d].fixture = %q names no golden: %v — the name must match a file the parity sweep reads", int(st), spec.fixture, err)
		}
	}
}

// TestSurfaceSpecHasNotGrownAField pins surfaceSpec's field count so a new
// per-state fact cannot ship classified for no state: the completeness walk
// above cannot see a field that was added and left zero in every entry, so
// growing the struct (the layout-budget SizeSpec of #802 is the expected next
// customer) must come back here, classify the field for every state, and bump
// the pin — a new field fails under any name, and the author must decide.
func TestSurfaceSpecHasNotGrownAField(t *testing.T) {
	require.Equal(t, 7, reflect.TypeOf(surfaceSpec{}).NumField(),
		"surfaceSpec grew or lost a field — classify it for every state in surfaceSpecs, check whether TestEverySurfaceSpecIsComplete must assert it, then update this pin")
}
