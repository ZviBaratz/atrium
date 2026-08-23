//go:build !linux

package tmux

// procCostSupported is false off Linux, which has no /proc to price a process
// through. The one caller is the #800 fanout measurement, which reports the platform
// unsupported rather than printing zeros — the same shape orphanScanSupported and
// internal/doctor's pressure check already use.
//
// A ps(1) fallback was considered and rejected. ps can report RSS but not Pss, and
// the whole point of the memory half of this measurement is the marginal cost of one
// more client, which shared pages make RSS answer several times over. A number that
// is wrong by an order of magnitude in the direction of alarm is worse here than no
// number, because the measurement exists to decide whether to spend engineering on
// the fanout at all.
const procCostSupported = false

// readProcCost prices nothing off Linux. ok is false, which callers must read as
// "drop this sample", never as "the cost was zero" — the distinction procCost's own
// doc draws, and the one that keeps an unsupported platform from reporting a fleet
// that costs nothing.
func readProcCost(int) (procCost, bool) { return procCost{}, false }

// countFDClasses counts nothing off Linux, for the same reason: without /proc/<pid>/fd
// there is no way to tell a pty master from a socket, and a partial inventory would
// understate exactly the fanout being measured.
func countFDClasses(int) (fdCounts, bool) { return fdCounts{}, false }
