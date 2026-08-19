// Package flock holds the one piece of advisory-locking policy Atrium's four lock
// files share: how long an exclusive acquirer waits out a reader before deciding the
// lock is genuinely taken.
//
// It exists because a *probe* and an *acquisition* contend. Atrium reads its locks to
// phrase a message — tuiRunning for tui.lock, handover.Held for handover.lock — and a
// reader must take a shared lock to discover whether an exclusive holder exists. A
// shared lock refuses an exclusive request for as long as it is held, so an acquirer
// that tries once can be refused by a passing probe and conclude the lock is held by a
// peer that does not exist. Retrying rides out the probe.
//
// It shrinks that window; it does not close it. Shared locks stack, so overlapping readers
// can keep an exclusive request refused for longer than any budget, with no exclusive owner
// anywhere — and the acquirer still cannot tell the two apart, because both arrive as
// EWOULDBLOCK. Distinguishing them takes another syscall (a shared request that SUCCEEDS
// proves the blocker was shared); atrium#771 tracks that. Until then a caller reading
// exhaustion as "somebody owns this" is choosing the safe error, not a certainty.
//
// What exhaustion MEANS is the caller's, not this package's: one refuses to start
// (errTUIAlreadyRunning) and one carries on unrecorded (handover.Hold fails open). The
// loop is identical; only the conclusion differs, which is why the loop lives here and
// the conclusion does not.
package flock

import "time"

// Attempts and Delay are the default budget: how long an acquirer waits behind a lock
// it expects to be free. Callers pass them explicitly rather than reading them here, so
// a caller that needs a different budget is visible at its call site.
const (
	Attempts = 20
	Delay    = 5 * time.Millisecond
)
