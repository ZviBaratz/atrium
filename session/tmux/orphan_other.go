//go:build !linux

package tmux

import "context"

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
