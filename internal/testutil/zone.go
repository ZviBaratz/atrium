package testutil

import "time"

// The budget every bubblezone click-retry loop waits on. Shared from here — beside
// MouseClick, which those same loops use to deliver the click — so the zone-click
// tests in `app` and `ui` move together instead of each carrying its own literal.
//
// The pattern these bound is #434's: render, resolve the zone, and click inside one
// require.Eventually, because zone.Scan hands bounds to an async worker and the zone
// manager is package-global, so a Get can return a zero rect or one an earlier
// frame registered. A miss re-renders and retries.
//
// A generous timeout costs nothing in strength, and that is the point of naming it
// here rather than trimming it. None of those loops can pass vacuously: a click on
// stale bounds leaves the state it asserts on unset, so the loop only exits early on
// a hit. The timeout is therefore paid in full exactly once — on a genuinely broken
// click, which fails either way — while a short one turns a slow render into a
// failure. One second was short enough that a -race build on a loaded CI runner blew
// it, failing at exactly 1.00s with the click never landing (#621).
const (
	// ZoneClickTimeout bounds one such loop.
	ZoneClickTimeout = 5 * time.Second
	// ZoneClickPoll is the gap between attempts, i.e. between whole re-renders.
	ZoneClickPoll = 5 * time.Millisecond
)
