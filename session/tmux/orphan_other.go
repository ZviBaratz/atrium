//go:build !linux

package tmux

import (
	"context"
	"time"
)

// orphanScanSupported is false off Linux, which has no /proc to inventory processes
// through. Callers render the section as unavailable rather than failing — the same
// shape internal/doctor's OOM check already uses.
//
// A ps-based fallback was considered and rejected: it could supply neither the bound
// socket path (the only thing that names an orphan whose socket file is gone) nor
// the cwd-deleted signal, so it would report processes it could not classify.
const orphanScanSupported = false

// inventoryCandidates finds nothing off Linux; ScanServers never calls it, since it
// returns early on orphanScanSupported. The stub keeps the shared code compiling.
func inventoryCandidates(context.Context) []candidate { return nil }

// ProcessStartTime is unavailable off Linux. Nothing reaps there — ScanServers
// reports the platform unsupported — so no caller can reach a state where this
// answering "unknown" hides a live process.
func ProcessStartTime(int) (time.Time, bool) { return time.Time{}, false }

// ProcessIsZombie is answered conservatively off Linux: with no /proc there is no
// way to tell a zombie from a live process, and nothing reaps off Linux anyway.
func ProcessIsZombie(int) bool { return false }
