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

// capacityRow returns the rendered line whose text contains label, so a value
// assertion tests the value in its own row rather than anywhere in the block — the
// "8" of "hardware threads" must not be satisfiable by an unrelated figure elsewhere.
func capacityRow(t *testing.T, out, label string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, label) {
			return line
		}
	}
	t.Fatalf("no %q row in RenderCapacity output:\n%s", label, out)
	return ""
}

// RenderCapacity names the host size, the recommended cap, and how to override it —
// each value on its own labeled row.
func TestRenderCapacity_ShowsHostAndOverride(t *testing.T) {
	out := RenderCapacity(CapacityResult{Threads: 8, RAMBytes: 32 * (1 << 30), RAMKnown: true, RecommendedCap: 4})
	if !strings.Contains(out, "Host capacity") {
		t.Errorf("RenderCapacity output missing the \"Host capacity\" header:\n%s", out)
	}
	for _, tc := range []struct{ label, want string }{
		{"hardware threads", "8"},
		{"total RAM", "32.0 GiB"},
		{"recommended cap", "4 sessions"},
	} {
		if row := capacityRow(t, out, tc.label); !strings.Contains(row, tc.want) {
			t.Errorf("%q row = %q, want it to contain %q", tc.label, row, tc.want)
		}
	}
	if row := capacityRow(t, out, "max_sessions"); !strings.Contains(row, "unlimited") {
		t.Errorf("the override row must explain the escape hatch, got %q", row)
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
