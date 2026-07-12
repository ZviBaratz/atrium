package agent

import "strings"

// ClaudeEffortLevels are the reasoning-effort levels the create form offers as
// chips (after the field's own "default" chip). The claude CLI (2.1.207 --help)
// documents exactly these for --effort: "Effort level for the current session
// (low, medium, high, xhigh, max)". Unlike --permission-mode the CLI does not
// reject an unknown value — it prints a warning and falls back to the default
// effort — so this list is the offered set, not a hard gate.
// TestClaudeEffortLevels_MatchInstalledCLI pins it to the installed binary.
var ClaudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ClaudeEffortLabels are the display labels for ClaudeEffortLevels, in the same
// order. Effort labels are identical to their values (no kebab-casing needed);
// the parallel slice mirrors chipRow's options/labels contract used by the mode
// and profile fields.
var ClaudeEffortLabels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEffortEnum is the offered level set as a lookup — the validation backstop
// behind the closed chip set.
var claudeEffortEnum = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ValidEffort reports whether s is an --effort level Atrium offers. A cheap
// backstop behind the chip set (the field is the only source of values):
// composeProgramFlags errors on a miss so UI/enum drift is caught before launch,
// rather than silently handed to the CLI (which would only warn-and-ignore it).
func ValidEffort(s string) bool { return claudeEffortEnum[s] }

// EffortFlag returns the value of an --effort pin in program ("" = none), the
// extraction counterpart of WithEffortFlag. Agent-neutral argv parsing; the last
// pin wins, matching the CLI's argv semantics.
func EffortFlag(program string) string {
	fields := strings.Fields(program)
	value := ""
	for n, f := range fields {
		if v, ok := strings.CutPrefix(f, "--effort="); ok {
			value = v
		}
		if f == "--effort" && n+1 < len(fields) {
			value = fields[n+1]
		}
	}
	return value
}

// WithEffortFlag returns program with `--effort level` applied: verbatim append
// when the program carries no pin, replace when it does (see withFlag).
func WithEffortFlag(program, level string) string {
	return withFlag(program, "--effort", level)
}
