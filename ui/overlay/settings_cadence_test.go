package overlay

import (
	"strconv"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cadenceRowKeys is the closed list of #799's knobs, in the order they appear in the
// panel. It is spelled out rather than derived from the schema so that a row silently
// dropped — or a fifth knob added without the recorded decision the issue asks for —
// fails here.
var cadenceRowKeys = []string{
	"context_warn_percent",
	"context_danger_percent",
	"diff_refresh_seconds",
	"notify_throttle_seconds",
	"pending_watchdog_minutes",
}

// TestCadenceRowsExist pins the four knobs' presence and shape. The reflection guards
// prove every scalar Config field has SOME row; they cannot say it is an int row, live, or
// resettable, all of which the issue's "done means" depends on.
func TestCadenceRowsExist(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, key := range cadenceRowKeys {
		t.Run(key, func(t *testing.T) {
			r := rowByKey(t, cfg, key)
			assert.Equal(t, kindInt, r.kind, "a cadence knob is a number, so the panel edits it as one")
			assert.Equal(t, timingLive, r.timing,
				"every cadence knob is wired to apply live; a restart badge here would be a lie")
			require.NotNil(t, r.reset, "a knob with a built-in default must be resettable to it")
			require.NotNil(t, r.defaultDisplay, "and must advertise that default")
			assert.Contains(t, r.detail, cadenceNote,
				"each knob's detail records why only these four are exposed")
		})
	}
}

// TestCadenceRowsRefuseOutOfRange is the row-level half of the bounds contract. The
// accessor clamps, because a hand-edited config.json has nobody to tell; the row refuses,
// because the panel does. Echoing back a number the accessor would silently rewrite is the
// failure this prevents — the user reads their typed value in the cell and a different one
// in force.
func TestCadenceRowsRefuseOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		key string
		// lo/hi are the row's declared range: lo-1 and hi+1 must be refused.
		lo, hi int
		// topAdmitted is the largest value the row actually accepts. It equals hi for
		// every knob except context_warn_percent, whose extra validator also holds it
		// under the configured danger band — so a table assuming hi is always admissible
		// would assert the wrong thing for exactly the row carrying the extra rule.
		topAdmitted int
	}{
		{"context_warn_percent", 1, 100, config.DefaultContextDangerPercent()},
		{"context_danger_percent", 1, 100, 100},
		{"diff_refresh_seconds", 1, config.MaxDiffRefreshSeconds(), config.MaxDiffRefreshSeconds()},
		{"notify_throttle_seconds", 0, config.MaxNotifyThrottleSeconds(), config.MaxNotifyThrottleSeconds()},
		{"pending_watchdog_minutes", 1, config.MaxPendingWatchdogMinutes(), config.MaxPendingWatchdogMinutes()},
	} {
		t.Run(tc.key, func(t *testing.T) {
			cfg := config.DefaultConfig()
			r := rowByKey(t, cfg, tc.key)

			assert.Error(t, r.set(cfg, strconv.Itoa(tc.lo-1)), "below the floor is refused")
			assert.Error(t, r.set(cfg, strconv.Itoa(tc.hi+1)), "above the ceiling is refused")
			assert.Error(t, r.set(cfg, "not a number"), "a non-number is refused")

			// The bounds themselves are admitted, and land in the config where the
			// accessor reads them back unchanged — the round trip, not just the return.
			require.NoError(t, r.set(cfg, strconv.Itoa(tc.topAdmitted)))
			assert.Equal(t, strconv.Itoa(tc.topAdmitted), r.get(cfg), "the top admitted value is legal")
			require.NoError(t, r.set(cfg, strconv.Itoa(tc.lo)))
			assert.Equal(t, strconv.Itoa(tc.lo), r.get(cfg), "so is the floor")

			// And reset really clears the stored value rather than merely rendering the
			// default: the row's value must return to the advertised default.
			r.reset(cfg)
			assert.Equal(t, r.defaultDisplay(), r.get(cfg),
				"reset restores the built-in default the row advertises")
		})
	}
}

// TestContextWarnRowRefusesInversion covers the one cross-field rule at the surface that
// has a user to tell, in BOTH directions. The accessor's silent collapse is the right
// answer for a file nobody is reading; in the panel it would look like the value simply
// did not take.
//
// The downward direction was the one missing: lowering danger under a stored warn was
// allowed, and the warn row then rendered the collapsed value — so the number the user had
// set was neither displayed nor reachable from the panel, and raising danger again made a
// band reappear that the panel had never shown.
func TestContextWarnRowRefusesInversion(t *testing.T) {
	cfg := config.DefaultConfig()
	warn := rowByKey(t, cfg, "context_warn_percent")
	danger := rowByKey(t, cfg, "context_danger_percent")

	require.NoError(t, danger.set(cfg, "60"))
	assert.Error(t, warn.set(cfg, "80"), "a warn band above the danger band is refused, not collapsed")
	require.NoError(t, warn.set(cfg, "60"), "equal is allowed: the warn band may narrow to nothing")
	require.NoError(t, warn.set(cfg, "59"))

	assert.Error(t, danger.set(cfg, "30"), "a danger band under the stored warn is refused too")
	assert.Equal(t, 60, cfg.GetContextDangerPercent(), "and the refusal did not store it")
	require.NoError(t, danger.set(cfg, "59"), "equal is allowed from this side as well")
	require.NoError(t, danger.set(cfg, "95"), "raising it is unconstrained")

	// The refusal reads the STORED warn, so it constrains nothing while that row is unset —
	// which is the case TestWarnRowIsNotMarkedModifiedBySibling covers.
	cfg.ContextWarnPercent = nil
	require.NoError(t, danger.set(cfg, "30"), "an unset warn band still follows danger down")
	assert.Equal(t, 30, cfg.GetContextWarnPercent(), "and the accessor holds it under")
}

// TestWarnRowIsNotMarkedModifiedBySibling covers the one row here whose displayed value
// another field can move. Lowering context_danger_percent collapses the warn band onto it,
// so the warn row's effective value diverges from its advertised default while the user has
// touched nothing — and the plain value comparison isModified would otherwise make reads
// that as "changed from default".
//
// The marker is not cosmetic in that state: the way back is `r`, whose reset clears a field
// that is already clear. A user would press it, watch nothing happen, and press it again.
//
// TestNoRowIsModifiedOnAFreshConfig and TestResetRestoresTheDefault both start from a
// default config, where the two bands are ordered and the collapse never fires, so neither
// can see this.
func TestWarnRowIsNotMarkedModifiedBySibling(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)

	warn := rowIndex(t, o, "context_warn_percent")
	require.False(t, o.isModified(warn), "an untouched pair is not modified")

	// 40 is below the built-in warn band, so the collapse fires.
	low := 40
	cfg.ContextDangerPercent = &low
	require.Equal(t, low, cfg.GetContextWarnPercent(),
		"fixture check: the warn band must actually have collapsed, or this proves nothing")
	assert.False(t, o.isModified(warn),
		"a warn band moved by its sibling is not a value the user changed")
	assert.True(t, o.isModified(rowIndex(t, o, "context_danger_percent")),
		"the field the user DID set is still marked — the gate must not suppress both")

	// And the marker still works for its own field, so the gate is not a blanket off switch.
	half := 20
	cfg.ContextWarnPercent = &half
	assert.True(t, o.isModified(warn), "a warn band the user set is marked")
}

// rowIndex finds a row's position in the overlay's own slice, which is what isModified
// takes — rowByKey returns the row itself and cannot answer for the overlay.
func rowIndex(t *testing.T, o *SettingsOverlay, key string) int {
	t.Helper()
	for i, r := range o.rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("no row keyed %q", key)
	return -1
}
