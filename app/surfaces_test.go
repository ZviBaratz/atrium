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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEverySurfaceSpecIsComplete requires every state's slot in surfaceSpecs to
// be filled and self-identifying: st equals the index (a forgotten slot is
// zero-valued and fails here before its false bar bit or nil handler can ship;
// a whole entry moved under another state's key never reaches this guard,
// because every index is claimed and the keyed literal makes that a
// duplicate-index compile error — what compiles and lands here is a swapped
// pair of keys or a mistyped st), and fixture is non-empty, unique, and names
// an existing golden under testdata/frames — the same name frameStates hands
// the parity, fingerprint, bounds and hardtab sweeps. The reverse direction
// closes the walk: every file under testdata/frames is claimed by some state,
// so a renamed fixture cannot leave its old golden behind reading as current.
//
// Mutation-verified: deleting an entry (stateDefault's fails on the empty
// fixture, since its st is the zero value; any other state's fails on st),
// changing an entry's st to another state's, duplicating a fixture name,
// pointing a fixture at a missing golden, and dropping a stray file into
// testdata/frames each fail here.
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

	// The reverse direction: the frames directory holds nothing the table does
	// not claim. An unclaimed golden is a renamed fixture's abandoned twin (or
	// a surface that lost its entry), and it would read as current forever —
	// CS_UPDATE_GOLDEN only ever regenerates the claimed names. A non-.txt
	// file fails too, on purpose: the directory holds goldens and nothing else.
	entries, err := os.ReadDir(filepath.Join("testdata", "frames"))
	require.NoError(t, err)
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".txt")
		if _, claimed := seen[name]; !claimed {
			t.Errorf("testdata/frames/%s is claimed by no state — delete the orphan or fix the fixture name that abandoned it", entry.Name())
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
