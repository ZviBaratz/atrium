package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// SchemeResult is what doctor can determine about terminal-polarity detection
// without a Bubble Tea loop.
//
// OSC11Probed is always false, and that is the point rather than a stub: doctor is a
// one-shot command, not a TUI, so it cannot send the query and wait for the reply.
// Reporting "not probed here" is honest; omitting the rung would let a user read "no
// answer" as "your terminal does not support it", which is the opposite conclusion
// and the one that would send them to change terminals.
type SchemeResult struct {
	Scheme      theme.Scheme // what the rungs doctor CAN read resolve to
	ColorFGBG   string       // the raw value, "" when unset
	OSC11Probed bool         // always false; see the type comment
}

// CheckScheme resolves the detection rungs available outside the TUI.
//
// environ is a parameter rather than an os.Environ() call so the rule is a pure
// function of its input, matching every other Check in this package and
// theme.NoColorRequested, whose doc explains the cost of the alternative: a
// predicate reading its input at package load cannot be reached by t.Setenv at all.
// Later entries win, matching os.Environ semantics for a duplicated name.
func CheckScheme(environ []string) SchemeResult {
	var colorfgbg string
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok && name == "COLORFGBG" {
			colorfgbg = value
		}
	}
	return SchemeResult{
		Scheme:    theme.ResolveScheme(nil, colorfgbg),
		ColorFGBG: colorfgbg,
	}
}

// RenderScheme formats the detection report under a "Terminal background
// detection:" header, parallel to RenderCapacity.
func RenderScheme(r SchemeResult) string {
	var b strings.Builder
	b.WriteString("Terminal background detection:\n")

	switch r.Scheme {
	case theme.SchemeLight:
		fmt.Fprintf(&b, "  %-18s light (from COLORFGBG=%s)\n", "resolved", r.ColorFGBG)
	case theme.SchemeDark:
		fmt.Fprintf(&b, "  %-18s dark (from COLORFGBG=%s)\n", "resolved", r.ColorFGBG)
	case theme.SchemeUnknown:
		fmt.Fprintf(&b, "  %-18s dark (no answer from any rung doctor can read)\n", "resolved")
	}

	if r.ColorFGBG == "" {
		fmt.Fprintf(&b, "  %-18s unset\n", "COLORFGBG")
	}
	if !r.OSC11Probed {
		// The wrapped half aligns under the value column because it continues a VALUE;
		// the actionable line below is a hint, and hints are the 9-space arrow form
		// every other section uses (capacity.go, deps.go, oom.go).
		fmt.Fprintf(&b, "  %-18s not probed here — it outranks COLORFGBG but needs the\n", "OSC 11")
		fmt.Fprintf(&b, "  %-18s running TUI, which asks at startup, on refocus and after a detach\n", "")
	}
	b.WriteString("         → set theme: auto to follow whichever rung answers\n")
	return b.String()
}
