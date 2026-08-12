package testutil

import "time"

// The budget every bubblezone retry loop waits on. Shared from here — beside
// MouseClick, which most of those loops use to deliver a click — so the zone tests in
// `app` and `ui` move together instead of each carrying its own literal.
//
// The pattern these bound is #434's: re-render and resolve the zone inside one
// require.Eventually, because zone.Scan hands bounds to an async worker and the zone
// manager is package-global, so a Get can return a zero rect or one an earlier frame
// registered. Most loops fold the click in too, so a miss just re-renders and retries;
// app/wheel_test.go's waitAppZone folds in a whole-frame consistency check instead and
// returns bounds for the caller to click. Same budget, since both are waiting on the
// same worker.
//
// A generous timeout costs nothing in strength, and that is the point of naming it
// here rather than trimming it. None of these loops can pass on the clock: each exits
// only when the state it waits for is actually reached, so extra time buys retries and
// never buys a pass — a genuinely broken click never sets that state and fails at the
// deadline either way. A short one, by contrast, turns a slow render into a failure:
// one second was short enough that a -race build on a loaded CI runner blew it,
// failing at exactly 1.00s with the click never landing (#621).
const (
	// ZoneClickTimeout bounds one such loop.
	ZoneClickTimeout = 5 * time.Second
	// ZoneClickPoll is the gap between attempts, i.e. between whole re-renders.
	ZoneClickPoll = 5 * time.Millisecond
)
