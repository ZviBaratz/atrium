package daemon

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
)

// A non-positive configured interval would panic time.NewTicker; effectivePollInterval
// must fall back to the built-in default so the daemon keeps running. A positive value
// passes through unchanged.
func TestEffectivePollInterval(t *testing.T) {
	def := time.Duration(config.DefaultDaemonPollIntervalMs) * time.Millisecond
	assert.Equal(t, def, effectivePollInterval(0), "zero must fall back to the default")
	assert.Equal(t, def, effectivePollInterval(-100), "negative must fall back to the default")
	assert.Equal(t, 250*time.Millisecond, effectivePollInterval(250), "positive passes through unchanged")
}

// TestEffectivePendingWatchdog pins the daemon's own conversion of
// pending_watchdog_minutes (#799). The daemon is a separate process with its own config
// load, so the TUI's install of this cap never reaches it — and a unit slip here would
// silently give a headless stretch a different watchdog than the interactive one, which is
// one behaviour under two clocks.
//
// What this does NOT cover: that RunDaemon actually calls it. Nothing here drives
// RunDaemon — it takes the daemon lock, real storage and a poll loop — so deleting the
// SetPendingWatchdog line leaves the suite green, exactly as deleting the
// effectivePollInterval call beside it would. The conversion is guarded; the wiring is
// not, and that is a disclosed gap rather than an oversight.
func TestEffectivePendingWatchdog(t *testing.T) {
	assert.Equal(t,
		time.Duration(config.DefaultPendingWatchdogMinutes())*time.Minute,
		effectivePendingWatchdog(&config.Config{}),
		"an unset field takes the built-in cap, not zero")

	n := 7
	assert.Equal(t, 7*time.Minute, effectivePendingWatchdog(&config.Config{PendingWatchdogMinutes: &n}),
		"the field is minutes — a seconds or milliseconds reading would be off by orders of magnitude")

	// The accessor's clamp is what makes a hand-edited 0 safe here: a zero cap would
	// reconcile every Pending row on its first poll.
	zero := 0
	assert.Positive(t, effectivePendingWatchdog(&config.Config{PendingWatchdogMinutes: &zero}),
		"a non-positive configured cap must not reach the watchdog")
}
