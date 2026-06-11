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

// hasModelFlag reports whether program pins a model via `--model value` or
// `--model=value`. A whole-field comparison, so lookalike flags ("--models-dir",
// "--model-context") don't count as pins. Distinct from ModelFlag != "": a bare
// trailing `--model` is a (broken) pin that WithModelFlag must still replace,
// not append after.
func hasModelFlag(program string) bool {
	for _, f := range strings.Fields(program) {
		if f == "--model" || strings.HasPrefix(f, "--model=") {
			return true
		}
	}
	return false
}

// ModelFlag returns the value of a --model pin in program ("" = none), the
// extraction counterpart of WithModelFlag. Agent-neutral pure argv parsing;
// callers gate on the agent where the pin's meaning is agent-specific.
func ModelFlag(program string) string {
	fields := strings.Fields(program)
	for n, f := range fields {
		if v, ok := strings.CutPrefix(f, "--model="); ok {
			return v
		}
		if f == "--model" && n+1 < len(fields) {
			return fields[n+1]
		}
	}
	return ""
}

// WithModelFlag returns program with `--model model` applied. The common case —
// no --model present — appends to the string verbatim, preserving any quoting
// the profile's program carries. When the program already pins a model, the
// flag is replaced instead of duplicated; that path re-joins strings.Fields
// output, which collapses quoted multi-word arguments — acceptable, since it
// only runs for profiles that already embed --model.
func WithModelFlag(program, model string) string {
	if !hasModelFlag(program) {
		return program + " --model " + model
	}
	fields := strings.Fields(program)
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--model":
			i++ // drop the flag and its value
		case strings.HasPrefix(fields[i], "--model="):
			// drop the combined form
		default:
			out = append(out, fields[i])
		}
	}
	return strings.Join(out, " ") + " --model " + model
}
