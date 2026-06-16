package agent

import "strings"

// ClaudePermissionModes are the modes the create form's permission-mode field
// offers as chips. The CLI's full closed enum (claude 2.1.172 --help) is
// {acceptEdits, auto, bypassPermissions, default, dontAsk, plan}; the offered
// subset deliberately excludes bypassPermissions — its startup acceptance
// dialog ("WARNING… Yes, I accept") would block the session boot, and a user
// who wants it can pin it in a profile program — and dontAsk, the
// non-interactive CI mode that auto-denies anything not allowlisted. The
// field's first chip ("default") is rendered by ModeField itself and
// contributes no flag.
var ClaudePermissionModes = []string{"plan", "acceptEdits", "auto"}

// ClaudePermissionModeLabels are the display labels for ClaudePermissionModes,
// in the same order. Modes with uppercase letters are rendered in kebab-case
// for visual consistency with the other chip rows in the create form.
var ClaudePermissionModeLabels = []string{"plan", "accept-edits", "auto"}

// claudePermissionModeEnum is the CLI's full closed enum (claude 2.1.172
// --help). Unlike --model, claude rejects unknown values at argv parse time —
// anything outside this set would kill the session at launch, so composition
// validates against the whole enum (not just the offered chips, so a future
// caller composing a profile-pinned mode still passes). It is deliberately a
// superset of ClaudePermissionModes — TestValidPermissionMode_CoversOfferedChips
// pins that relation so a chip added to one list but not the other cannot turn
// into a submit-time "invalid permission mode" error on a UI-offered chip.
// The snapshot can also lag the *installed* binary: an older CLI without
// "auto" rejects the flag at launch — the same accepted tradeoff the
// hardcoded chip list embodies, recoverable by killing the instance.
var claudePermissionModeEnum = map[string]bool{
	"acceptEdits": true, "auto": true, "bypassPermissions": true,
	"default": true, "dontAsk": true, "plan": true,
}

// ValidPermissionMode reports whether s is a --permission-mode value the
// claude CLI accepts (exact, case-sensitive match).
func ValidPermissionMode(s string) bool { return claudePermissionModeEnum[s] }

// PermissionModeFlag returns the value of a --permission-mode pin in program
// ("" = none), the extraction counterpart of WithPermissionModeFlag.
// Agent-neutral pure argv parsing; callers gate on the agent where the flag's
// meaning is agent-specific. The last pin wins, matching the CLI's argv
// semantics. An invalid or unrecognised value returns "".
func PermissionModeFlag(program string) string {
	fields := strings.Fields(program)
	value := ""
	for n, f := range fields {
		if v, ok := strings.CutPrefix(f, "--permission-mode="); ok {
			value = v
		}
		if f == "--permission-mode" && n+1 < len(fields) {
			value = fields[n+1]
		}
	}
	if !ValidPermissionMode(value) {
		return ""
	}
	return value
}

// WithPermissionModeFlag returns program with `--permission-mode mode`
// applied: verbatim append when the program carries no pin, replace when it
// does (see withFlag for when the replace path applies).
func WithPermissionModeFlag(program, mode string) string {
	return withFlag(program, "--permission-mode", mode)
}

// claudePermissionModeMarkers maps a stable footer token to the enum value of
// the mode it indicates. The tokens are the mode-name words claude renders in
// its status-bar line below the input box — captured verbatim from a live
// claude 2.1.178 pane (see permissionmode_detect_test.go fixtures):
//
//	⏸ plan mode on (shift+tab to cycle)
//	⏵⏵ accept edits on (shift+tab to cycle)
//	⏵⏵ auto mode on (shift+tab to cycle)
//	⏵⏵ bypass permissions on (shift+tab to cycle)
//
// Matching the words, not the leading glyph, keeps detection robust to a glyph
// restyle and disambiguates the three ⏵⏵ modes. dontAsk has no interactive
// footer indicator and is intentionally absent — it falls back to the pinned
// flag like any unrecognized footer.
var claudePermissionModeMarkers = []struct{ token, mode string }{
	{"plan mode on", "plan"},
	{"accept edits on", "acceptEdits"},
	{"auto mode on", "auto"},
	{"bypass permissions on", "bypassPermissions"},
}

// claudePermissionMode reports the permission mode shown in the live pane
// footer. known=false (mode "") means the footer is indeterminate — a busy turn
// whose footer shows neither a mode indicator nor the idle shortcuts hint, or a
// startup/degenerate capture — so the caller keeps its last known value rather
// than flicker. The default (normal) mode renders no mode line, so it is
// recognized by the idle "? for shortcuts" hint instead; reporting it as a real
// "default" lets the chip clear when a session is switched back to normal.
//
// Detection is confined to footerRegion (the live chrome below the input box)
// so a mode phrase quoted in the scrolled-back transcript can never false-match.
func claudePermissionMode(content string) (mode string, known bool) {
	footer := footerRegion(content)
	for _, m := range claudePermissionModeMarkers {
		if strings.Contains(footer, m.token) {
			return m.mode, true
		}
	}
	if strings.Contains(footer, "? for shortcuts") {
		return "default", true
	}
	return "", false
}
