package agent

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

// WithPermissionModeFlag returns program with `--permission-mode mode`
// applied: verbatim append when the program carries no pin, replace when it
// does (see withFlag for when the replace path applies).
func WithPermissionModeFlag(program, mode string) string {
	return withFlag(program, "--permission-mode", mode)
}
