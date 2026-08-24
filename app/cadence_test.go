package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

// The app-side half of the four exposed cadence knobs (#799): that each configured value
// reaches the code that acts on it, and that a settings change reaches it without a
// restart — the only thing that makes every row's "live" badge true.

// TestNotifyThrottleHonoursTheConfiguredWindow drives throttled() with windows other than
// the built-in three seconds. The zero window is the interesting one: it is the configured
// meaning of "signal me on every edge", and a comparison written `<=` rather than `<`
// would swallow every repeat while still passing the default-window table in
// TestNotifyStateThrottle.
func TestNotifyThrottleHonoursTheConfiguredWindow(t *testing.T) {
	t.Run("zero never throttles", func(t *testing.T) {
		st := &notifyState{}
		for i := range 5 {
			require.Falsef(t, st.throttled(notify.EventFinished, 0),
				"edge %d must pass with no throttle window", i)
		}
	})

	t.Run("a long window throttles what the default would admit", func(t *testing.T) {
		st := &notifyState{}
		require.False(t, st.throttled(notify.EventFinished, time.Hour), "first edge passes")
		// Older than the built-in window, so the default would let this through; the
		// configured hour must not.
		st.lastFinished = time.Now().Add(-2 * notifyThrottleWindow())
		require.True(t, st.throttled(notify.EventFinished, time.Hour),
			"a window longer than the default is what decides")
	})

	t.Run("an edge one window old is due", func(t *testing.T) {
		// The tightest gap that must still admit. Note what this does NOT pin: `<` and
		// `<=` are indistinguishable here, because throttled() samples its own `now`
		// after the stamp is set, so the difference is never exactly the window. That
		// boundary is unreachable through this API rather than untested.
		st := &notifyState{}
		require.False(t, st.throttled(notify.EventFinished, notifyThrottleWindow()))
		st.lastFinished = time.Now().Add(-notifyThrottleWindow())
		require.False(t, st.throttled(notify.EventFinished, notifyThrottleWindow()),
			"the window has elapsed, so the edge is due")
	})

	t.Run("a short window admits what the default would throttle", func(t *testing.T) {
		st := &notifyState{}
		require.False(t, st.throttled(notify.EventFinished, time.Nanosecond))
		st.lastFinished = time.Now().Add(-time.Millisecond)
		require.False(t, st.throttled(notify.EventFinished, time.Nanosecond),
			"a millisecond gap clears a nanosecond window")
		st.lastFinished = time.Now().Add(-time.Millisecond)
		require.True(t, st.throttled(notify.EventFinished, notifyThrottleWindow()),
			"positive control: the same gap is still inside the default window")
	})
}

// notifyThrottleWindow is the spacing an unset notify_throttle_seconds resolves to, for the
// tests that need a window they can step either side of.
//
// It is read from config rather than declared here. app used to hold its own 3-second
// constant; since #799 nothing in the app reads it — maybeNotify derives the window from
// config on every edge — so a second literal would be a mirror of a value config owns,
// and the guard pinning them together would fail a legitimate change to the default
// instead of propagating it.
func notifyThrottleWindow() time.Duration {
	return time.Duration(config.DefaultNotifyThrottleSeconds()) * time.Second
}

// TestDiffContentDueHonoursTheConfiguredFloor pins that the staleness bound is the caller's
// parameter and not a constant the function still reaches for. Each row picks an age on
// OPPOSITE sides of the configured floor and the built-in default, so a function ignoring
// its argument answers wrong rather than coincidentally right.
func TestDiffContentDueHonoursTheConfiguredFloor(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	def := time.Duration(config.DefaultDiffRefreshSeconds()) * time.Second
	// Idle and long settled, so only the floor can make the row due.
	const idle = session.Ready
	settled := ago(time.Hour)

	t.Run("a shorter floor makes a fresh row due", func(t *testing.T) {
		content := ago(2 * time.Second)
		require.False(t, diffContentDue(idle, settled, content, now, def),
			"positive control: 2s is well inside the built-in floor")
		require.True(t, diffContentDue(idle, settled, content, now, time.Second),
			"the configured 1s floor is what decides, not the built-in 15s")
	})

	t.Run("a longer floor holds a row the default would refresh", func(t *testing.T) {
		content := ago(def + time.Second)
		require.True(t, diffContentDue(idle, settled, content, now, def),
			"positive control: past the built-in floor")
		require.False(t, diffContentDue(idle, settled, content, now, time.Hour),
			"an hour-long configured floor holds it")
	})

	t.Run("the floor bounds staleness; it does not gate the other reasons", func(t *testing.T) {
		require.True(t, diffContentDue(session.Running, settled, ago(time.Second), now, time.Hour),
			"a writing status is due whatever the floor says")
	})
}

// TestDiffContentFloorReadsTheConfig pins the home helper that resolves the value once per
// tick. The poll goroutines are handed a plain duration, so a helper reading the wrong
// field — or the right one in the wrong unit — is invisible to the pure-function table
// above.
func TestDiffContentFloorReadsTheConfig(t *testing.T) {
	h, cfg := contextHome(t, config.ContextIndicatorPercent)

	require.Equal(t, time.Duration(config.DefaultDiffRefreshSeconds())*time.Second,
		h.diffContentFloor(), "an unconfigured home uses the built-in floor")

	n := 42
	cfg.DiffRefreshSeconds = &n
	require.Equal(t, 42*time.Second, h.diffContentFloor(),
		"the configured value is read in seconds, live, with no restart")
}

// chipColour reports the palette colour the rendered list painted the 28% context chip
// with, by rebuilding the exact segment ui.rowPaint.seg emits for each candidate and
// looking for it in the raw (unstripped) frame.
//
// Reading the colour off the frame rather than off a getter is the point: it is the only
// way to prove the configured bands travelled all the way from config through
// applySettingChange and the List setter into the paint, which is the chain a
// timingLive badge is a promise about.
func chipColour(t *testing.T, h *home) theme.Color {
	t.Helper()
	raw := h.list.String()
	th := theme.Current()
	var found theme.Color
	var hits int
	for _, c := range []theme.Color{th.Palette.Danger, th.Palette.Attention, th.Palette.FgDim} {
		for _, bg := range []theme.AnyColor{theme.NoColor(), th.Palette.BgElevated} {
			seg := lipgloss.NewStyle().Foreground(c).Background(bg).Render(" 28%")
			if strings.Contains(raw, seg) {
				found, hits = c, hits+1
			}
		}
	}
	require.Equalf(t, 1, hits, "the 28%% chip must be painted in exactly one candidate colour, found %d", hits)
	return found
}

// TestContextThresholdsSeededFromConfig covers app_construct.go's seeding call, the link
// TestContextThresholdsApplyLive cannot reach: that test starts from an unconfigured home,
// so its baseline frame is correct whether the constructor seeds the bands or skips them
// entirely. This one configures the bands first and looks at the FIRST frame.
//
// The failure it exists for is the same shape as the context-indicator one it mirrors: the
// renderer's zero bands mean "use the built-in 75/90", so a home that never seeds looks
// exactly right for the default user and silently ignores everyone else — until they open
// the settings panel and cycle a value, which is when applySettingChange finally pushes.
func TestContextThresholdsSeededFromConfig(t *testing.T) {
	warn, danger := 20, 25
	h, _ := contextHomeWithBands(t, config.ContextIndicatorPercent, &warn, &danger)
	require.Equal(t, theme.Current().Palette.Danger, chipColour(t, h),
		"a 28%% reading is past a configured danger band of 25 on the very first frame")
}

// TestContextThresholdsApplyLive is what makes the two context rows' "live" badge honest.
//
// The reflection guards prove the rows exist and the README documents them; both pass
// against an applySettingChange switch that silently ignores the keys — the same hole
// TestContextIndicatorAppliesLive exists for. So this changes the config, drives the real
// handler, and reads the colour back off the painted row.
//
// The session's reading is 28% of its window, which is FgDim on the built-in 75/90 ladder.
// Every band below is chosen so that ladder gives a DIFFERENT answer than the configured
// one, so no assertion can pass on a frame that ignored the change.
func TestContextThresholdsApplyLive(t *testing.T) {
	h, cfg := contextHome(t, config.ContextIndicatorPercent)
	th := theme.Current()

	require.Equal(t, th.Palette.FgDim, chipColour(t, h),
		"28%% is unremarkable on the built-in ladder — the baseline the changes below move off")

	warn, danger := 20, 50
	cfg.ContextWarnPercent, cfg.ContextDangerPercent = &warn, &danger
	_ = h.applySettingChange("context_warn_percent")
	require.Equal(t, th.Palette.Attention, chipColour(t, h),
		"a warn band at 20 must repaint the 28%% chip amber without a restart")

	// The danger key re-seeds BOTH bands, which is what stops a danger change from
	// stranding the warn band the accessor just moved under it.
	lower := 25
	cfg.ContextDangerPercent = &lower
	_ = h.applySettingChange("context_danger_percent")
	require.Equal(t, th.Palette.Danger, chipColour(t, h),
		"lowering danger to 25 must repaint the 28%% chip red without a restart")

	// And back: a settings change is not one-way.
	cfg.ContextWarnPercent, cfg.ContextDangerPercent = nil, nil
	_ = h.applySettingChange("context_danger_percent")
	require.Equal(t, th.Palette.FgDim, chipColour(t, h),
		"clearing both bands restores the built-in ladder live")
}

// TestPendingWatchdogAppliesLive is the same promise for the Advanced row, whose consumer
// is the session package rather than the list. session.PendingWatchdog is the setter's
// counterpart, so what is asserted is the value actually in force for every caller that
// reaches applyPending — this loop, the attach keeper's goroutine, and the daemon.
//
// The unconfigured rungs are asserted as ZERO, and that is the load-bearing half. A cap
// installed for a user who never asked for one is not a harmless default: it is positive,
// so session's ladder stops at its first rung and agent.Adapter.PendingWatchdog can never
// run again. Seeding the accessor's value here — the obvious spelling — is exactly that
// defect, and every assertion about a CONFIGURED cap passes under it.
func TestPendingWatchdogAppliesLive(t *testing.T) {
	t.Cleanup(func() { session.SetPendingWatchdog(0) })
	h, cfg := contextHome(t, config.ContextIndicatorPercent)

	require.Zero(t, session.PendingWatchdog(),
		"an unconfigured launch installs nothing, leaving the adapter rung reachable")

	mins := 7
	cfg.PendingWatchdogMinutes = &mins
	_ = h.applySettingChange("pending_watchdog_minutes")
	require.Equal(t, 7*time.Minute, session.PendingWatchdog(),
		"the configured cap is installed on the package that runs the watchdog, live")

	// Reset is the path back. It clears the field rather than writing the default, so the
	// install must clear too — otherwise resetting a row leaves the fleet pinned at 30
	// minutes with nothing in config.json to explain it.
	cfg.PendingWatchdogMinutes = nil
	_ = h.applySettingChange("pending_watchdog_minutes")
	require.Zero(t, session.PendingWatchdog(),
		"clearing the row hands the decision back to the adapter, live")
}

// TestMaybeNotifyHonoursTheConfiguredThrottle drives the whole notification path rather
// than throttled() alone, which is the only way to see the value maybeNotify DERIVES from
// config. A window computed in the wrong unit — minutes for seconds — passes every
// assertion in TestNotifyThrottleHonoursTheConfiguredWindow, because those hand the window
// in ready-made.
//
// One second configured, two seconds elapsed: due under seconds, still throttled under any
// larger unit. The default-window control alongside it means a path that ignored the
// config entirely fails too.
func TestMaybeNotifyHonoursTheConfiguredThrottle(t *testing.T) {
	var buf bytes.Buffer
	h, list := newNotifyHome(t, &buf)
	target := notifyTarget(t, list)

	one := 1
	h.appConfig.NotifyThrottleSeconds = &one

	finishOnce(h, target)
	require.Equal(t, "\a", buf.String(), "the first finish rings")

	// Two seconds since the last signal: inside the built-in three-second window, outside
	// the configured one-second window.
	h.notifySeen[target].lastFinished = time.Now().Add(-2 * time.Second)
	target.SetStatus(session.Running)
	finishOnce(h, target)
	require.Equal(t, "\a\a", buf.String(),
		"a 2s gap clears the configured 1s window, in seconds — a minute-scaled window would swallow it")

	// Control: restore the default and the same gap is swallowed again, so the assertion
	// above is about the configuration and not about the gap.
	h.appConfig.NotifyThrottleSeconds = nil
	h.notifySeen[target].lastFinished = time.Now().Add(-2 * time.Second)
	target.SetStatus(session.Running)
	finishOnce(h, target)
	require.Equal(t, "\a\a", buf.String(),
		"the same 2s gap is inside the built-in 3s window, so nothing new rings")
}
