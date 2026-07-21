package doctor

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
)

// CheckCapacity reports the live host: its hardware-thread count and the
// host-derived recommended cap (which tracks that thread count).
func TestCheckCapacity(t *testing.T) {
	r := CheckCapacity()
	if r.Threads != runtime.NumCPU() {
		t.Errorf("Threads = %d, want %d", r.Threads, runtime.NumCPU())
	}
	if r.RecommendedCap != config.DefaultSessionCap() {
		t.Errorf("RecommendedCap = %d, want %d", r.RecommendedCap, config.DefaultSessionCap())
	}
	if r.RecommendedCap < 2 {
		t.Errorf("RecommendedCap = %d, want >= 2 (the floor)", r.RecommendedCap)
	}
}

// RenderCapacity names the host size, the recommended cap, and how to override it.
func TestRenderCapacity_ShowsHostAndOverride(t *testing.T) {
	out := RenderCapacity(CapacityResult{Threads: 8, RAMBytes: 32 * (1 << 30), RAMKnown: true, RecommendedCap: 4})
	for _, want := range []string{"Host capacity", "hardware threads", "8", "recommended cap", "4 sessions", "32.0 GiB", "max_sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderCapacity output missing %q\n%s", want, out)
		}
	}
}

// Unknown RAM renders as "unknown", not a bogus 0.0 GiB or an error.
func TestRenderCapacity_UnknownRAM(t *testing.T) {
	out := RenderCapacity(CapacityResult{Threads: 4, RAMKnown: false, RecommendedCap: 2})
	if !strings.Contains(out, "unknown") {
		t.Errorf("unknown RAM must render as \"unknown\":\n%s", out)
	}
	if strings.Contains(out, "GiB") {
		t.Errorf("unknown RAM must not render a GiB figure:\n%s", out)
	}
}

// On Linux (CI) the RAM total is readable and positive.
func TestHostMemBytes_LinuxReadsRAM(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("no RAM reader for %s", runtime.GOOS)
	}
	bytes, ok := hostMemBytes()
	if !ok || bytes == 0 {
		t.Errorf("hostMemBytes() = (%d, %v), want a positive readable total on %s", bytes, ok, runtime.GOOS)
	}
}
