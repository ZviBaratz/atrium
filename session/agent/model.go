package agent

import "strings"

// ClaudeModelAliases are the model aliases the claude CLI documents for its
// --model flag (claude 2.1.170 --help: "Provide an alias for the latest model
// (e.g. 'fable', 'opus', or 'sonnet') or a full model name"). The list is
// completion sugar for the create form's model field, never a validation
// allowlist: full model names (and aliases added after this list) pass
// ValidModelName and reach the CLI verbatim.
var ClaudeModelAliases = []string{"fable", "haiku", "opus", "sonnet"}

// ValidModelName reports whether s is safe to embed unquoted in the launch
// command. tmux hands the program string to `sh -c`, so the charset excludes
// every shell metacharacter; the leading-alphanumeric rule also rejects
// flag-shaped input ("--foo"). The charset covers aliases ("opus"), full names
// ("claude-opus-4-6"), and provider-prefixed IDs ("us.anthropic.claude-x:0").
func ValidModelName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == ':' || r == '/' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// hasFlag reports whether program pins `name value` or `name=value`. A
// whole-field comparison, so lookalike flags ("--models-dir", "--model-context")
// don't count as pins. Distinct from ModelFlag != "": a bare trailing flag is
// a (broken) pin that withFlag must still replace, not append after.
func hasFlag(program, name string) bool {
	combined := name + "="
	for _, f := range strings.Fields(program) {
		if f == name || strings.HasPrefix(f, combined) {
			return true
		}
	}
	return false
}

// flagValue returns the value of a `name value` or `name=value` pin in program
// ("" = none) — the shared extraction counterpart of withFlag, behind ModelFlag,
// PermissionModeFlag, and EffortFlag. Agent-neutral pure argv parsing; callers
// gate on the agent where the pin's meaning is agent-specific. Comparison is
// whole-field, so a lookalike flag ("--model-context", "--effort-budget") is not
// read as a pin. The last pin wins, matching the CLI's argv semantics (withFlag's
// append path can legitimately leave two pins on a quoted program).
func flagValue(program, name string) string {
	combined := name + "="
	fields := strings.Fields(program)
	value := ""
	for n, f := range fields {
		if v, ok := strings.CutPrefix(f, combined); ok {
			value = v
		}
		if f == name && n+1 < len(fields) {
			value = fields[n+1]
		}
	}
	return value
}

// ModelFlag returns the value of a --model pin in program ("" = none), the
// extraction counterpart of WithModelFlag.
func ModelFlag(program string) string { return flagValue(program, "--model") }

// withFlag returns program with `name value` applied. The common case — no
// pin present — appends to the string verbatim, preserving any quoting the
// profile's program carries. When the program already pins the flag, it is
// replaced instead of duplicated. The replace path re-joins strings.Fields
// output, which cannot see shell quoting: a flag-lookalike token inside a
// quoted argument would be misread as a pin and stripped, mangling the quote
// (and a re-join collapses quoted multi-word arguments regardless). So a
// program carrying any quote character takes the append path instead — the
// CLI's argv parsing is last-wins, so an appended override still beats an
// earlier real pin, and the quoting survives verbatim.
func withFlag(program, name, value string) string {
	if !hasFlag(program, name) || strings.ContainsAny(program, `"'`) {
		return program + " " + name + " " + value
	}
	combined := name + "="
	fields := strings.Fields(program)
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == name:
			i++ // drop the flag and its value
		case strings.HasPrefix(fields[i], combined):
			// drop the combined form
		default:
			out = append(out, fields[i])
		}
	}
	return strings.Join(out, " ") + " " + name + " " + value
}

// WithModelFlag returns program with `--model model` applied (see withFlag).
func WithModelFlag(program, model string) string { return withFlag(program, "--model", model) }
