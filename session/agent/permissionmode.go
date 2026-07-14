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

// claudePermissionModeLabels maps a --permission-mode enum value to its display
// label — the single source of truth for every chip that shows a mode: the
// create form's offered row and the session-list chip (including live-detected
// modes the form doesn't offer, like bypassPermissions). Modes with uppercase
// letters are kebab-cased for visual consistency with the other chip rows;
// bypassPermissions is shortened to a clean "bypass". A mode absent here has no
// special label and renders as its raw enum value (see ClaudePermissionModeLabel).
var claudePermissionModeLabels = map[string]string{
	"plan":              "plan",
	"acceptEdits":       "accept-edits",
	"auto":              "auto",
	"bypassPermissions": "bypass",
}

// ClaudePermissionModeLabel returns the display label for a --permission-mode
// value, falling back to the raw value for a mode with no special label (e.g.
// dontAsk). The one place mode→label knowledge lives, so the create form and
// the list chip can never disagree.
func ClaudePermissionModeLabel(mode string) string {
	if label, ok := claudePermissionModeLabels[mode]; ok {
		return label
	}
	return mode
}

// ClaudePermissionModeLabels are the display labels for ClaudePermissionModes,
// in the same order — derived from the shared label map so the two never drift.
var ClaudePermissionModeLabels = func() []string {
	labels := make([]string, len(ClaudePermissionModes))
	for i, m := range ClaudePermissionModes {
		labels[i] = ClaudePermissionModeLabel(m)
	}
	return labels
}()

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
// ("" = none), the extraction counterpart of WithPermissionModeFlag. An invalid
// or unrecognised value returns "" — unlike --model and --effort, claude rejects
// an unknown mode at argv parse time, so a value outside the enum is not a mode
// the session could be running in.
func PermissionModeFlag(program string) string {
	value := flagValue(program, "--permission-mode")
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
// Detection is confined to footerBelowBox — the live chrome below the input
// box's bottom border — so a mode phrase quoted in the scrolled-back transcript
// can never false-match. Crucially it gates on the box border being present:
// with no border on screen (a busy frame whose box is hidden, a pre-box startup
// capture) the bottom lines can't be proven to be live chrome rather than
// transcript, so detection stays indeterminate and the caller keeps its last
// value rather than trust a phrase that may be conversation text.
func claudePermissionMode(content string) (mode string, known bool) {
	footer, ok := footerBelowBox(content)
	if !ok {
		return "", false
	}
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
