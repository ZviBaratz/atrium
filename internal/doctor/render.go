package doctor

import (
	"fmt"
	"strings"
)

func (s Status) label() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusDrifted:
		return "⚠ drifted"
	case StatusNotInstalled:
		return "not installed"
	default:
		return "unknown"
	}
}

// Render formats drift results as an aligned, newline-terminated table.
func Render(results []Result) string {
	var b strings.Builder
	b.WriteString("Agent heuristics:\n")
	for _, r := range results {
		installed := r.Installed
		if installed == "" {
			installed = "-"
		}
		verified := r.Verified
		if verified == "" {
			verified = "-"
		}
		fmt.Fprintf(&b, "  %-12s installed %-10s verified %-10s %s\n",
			r.Name, installed, verified, r.Status.label())
	}
	return b.String()
}
