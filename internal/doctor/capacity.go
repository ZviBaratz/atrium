package doctor

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// CapacityResult is the host-capacity snapshot the doctor reports: the hardware
// thread count, total RAM (RAMKnown is false when RAM is unreadable on this
// platform), and the host-derived recommended concurrent-session cap.
type CapacityResult struct {
	Threads        int
	RAMBytes       uint64
	RAMKnown       bool
	RecommendedCap int
}

// CheckCapacity gathers the host-capacity snapshot from the running machine. Like
// the other doctor checks it never fails: an unreadable RAM total is reported as
// unknown, not an error. It reads no config and touches no tmux, so it is safe to
// run beside a live TUI.
func CheckCapacity() CapacityResult {
	ram, ok := hostMemBytes()
	return CapacityResult{
		Threads:        runtime.NumCPU(),
		RAMBytes:       ram,
		RAMKnown:       ok,
		RecommendedCap: config.DefaultSessionCap(),
	}
}

// RenderCapacity formats the host-capacity snapshot under a "Host capacity:"
// header, parallel to RenderDeps. It reports the host size and the recommended
// default session cap, and points at max_sessions for overriding it.
func RenderCapacity(r CapacityResult) string {
	var b strings.Builder
	b.WriteString("Host capacity:\n")
	fmt.Fprintf(&b, "  %-18s %d\n", "hardware threads", r.Threads)
	fmt.Fprintf(&b, "  %-18s %s\n", "total RAM", humanizeRAM(r.RAMBytes, r.RAMKnown))
	fmt.Fprintf(&b, "  %-18s %d sessions\n", "recommended cap", r.RecommendedCap)
	b.WriteString("         → unset max_sessions warns past the recommended cap; " +
		"set N for a hard cap, 0 for unlimited\n")
	return b.String()
}

// humanizeRAM renders a byte count as GiB, or "unknown" when the platform could
// not report a total.
func humanizeRAM(bytes uint64, known bool) string {
	if !known {
		return "unknown"
	}
	return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
}
