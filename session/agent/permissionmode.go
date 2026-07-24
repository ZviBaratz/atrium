package agent

import "strings"

// ClaudePermissionModes are the modes the create form's permission-mode field
// offers as chips. The CLI's full closed enum (claude 2.1.172 --help) is
// {acceptEdits, auto, bypassPermissions, default, dontAsk, plan}; the offered
// subset deliberately excludes bypassPermissions — its startup acceptance
// dialog ("WARNING… Yes, I accept") would block the session boot, and a user
// who wants it can pin it in a profile program — and dontAsk, the
// non-interactive CI mode that auto-denies anything not allowlisted. The
// field's first chip ("inherit", the shared noOverrideChip) is rendered by
// ModeField itself and contributes no flag — deliberately not "default", which
// names a real member of the full enum below.
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

// PermissionModePin returns the raw value of a --permission-mode pin in program
// ("" = the flag is absent), deliberately unvalidated — the counterpart to
// PermissionModeFlag for a caller that must know a pin *exists* even when its value
// falls outside the enum snapshot above. PermissionModeFlag drops such a value on
// purpose (an unknown mode is not one a session could be running in, so it is not a
// mode to report), but the create form's no-op-chip hint needs the other
// distinction: reporting "a flag Atrium cannot name" as "claude's default" would
// state the opposite of the truth to the user.
func PermissionModePin(program string) string { return flagValue(program, "--permission-mode") }

// WithPermissionModeFlag returns program with `--permission-mode mode`
// applied: verbatim append when the program carries no pin, replace when it
// does (see withFlag for when the replace path applies).
func WithPermissionModeFlag(program, mode string) string {
	return withFlag(program, "--permission-mode", mode)
}

// claudePermissionModeMarkers maps a stable footer token to the enum value of
// the mode it indicates. The tokens are the mode-name words claude renders in
// its status-bar line below the input box — every one captured verbatim from a
// live pane (see permissionmode_detect_test.go fixtures). The cycle and dontAsk
// were captured at 2.1.209 (#333) and re-confirmed at 2.1.210 (#332):
//
//	⏸ manual mode on · ? for shortcuts · ← for agents
//	⏸ plan mode on (shift+tab to cycle) · ← for agents
//	⏵⏵ accept edits on (shift+tab to cycle) · ← for agents
//	⏵⏵ auto mode on (shift+tab to cycle) · ← for agents
//	⏵⏵ don't ask on (shift+tab to cycle) · ← for agents
//
// dontAsk is a one-way door — shift+tab cycles out of it but the cycle
// (default → acceptEdits → plan → auto) never returns, so it is reachable only
// by pinning the flag, which a profile program can do. It still advertises the
// cycle chord, which is why its footer is not the shape a reading of the mode
// table alone would predict.
//
// bypassPermissions was the last token with no live evidence — reaching it means
// accepting the "you accept all responsibility" startup dialog, which Atrium
// does not drive. #332 drove it once by hand (throwaway dir, footer captured,
// no prompt ever sent) and the line the bundle's mode table implied turned out
// to be exactly right, at 2.1.210:
//
//	⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
//
// Note what that does and does not settle. It confirms this token; it does not
// license predicting the next footer from the table — dontAsk above is the
// standing counter-example, where the same reasoning produced the wrong line.
//
// Every mode holds its indicator for the whole turn, busy or idle; only the
// hint beside it swaps. Since the mode is cyclable mid-turn, that persistence is
// what lets a busy footer track a switch instead of stalling on the pre-turn
// value.
//
// Matching the words, not the leading glyph, keeps detection robust to a glyph
// restyle and is what disambiguates at all: ⏸ covers both default and plan, and
// ⏵⏵ covers the other four, so the glyph alone names nothing. The table must
// stay exhaustive over the modes that render an indicator: an unlisted mode
// whose footer also carries the "? for shortcuts" hint would fall through to the
// check below and be misreported as "default" rather than merely go unknown —
// default's own footer is the proof those two can co-occur. A footer no token
// here names yields known=false, which for a claude session now consults the
// hook record's permission_mode before falling back to the pinned flag
// (tmux.Session.Poll's arbitration, #324).
var claudePermissionModeMarkers = []struct{ token, mode string }{
	{"manual mode on", "default"},
	{"plan mode on", "plan"},
	{"accept edits on", "acceptEdits"},
	{"auto mode on", "auto"},
	{"bypass permissions on", "bypassPermissions"},
	{"don't ask on", "dontAsk"},
}

// claudePermissionMode reports the permission mode shown in the live pane
// footer. known=false (mode "") means the footer is indeterminate — it names no
// mode and shows no idle shortcuts hint, or the capture is startup/degenerate —
// so the caller keeps its last known value rather than flicker.
//
// Current claude names the default mode like any other, as "⏸ manual mode on",
// so the marker table recognizes it directly, and reporting it as a real
// "default" lets the chip clear when a session is switched back to normal. The
// "? for shortcuts" fall-through below the loop is what remains of an older
// contract: the 2.1.178-era CLI rendered no mode line for default, and the idle
// hint was the only thing that named it. The changeover landed somewhere in
// (2.1.178, 2.1.206] — 2.1.206 already renders the indicator, so every version
// Atrium has verified against does, up to and including the VerifiedVersion pin.
// Keeping the fall-through keeps detection working against a CLI older than
// that; it is no longer the sole detector for the mode, which matters because
// "? for shortcuts" is not about the mode at all — the footer pushes it only as
// a fallback, when no other hint applies, so a busy turn shows the interrupt
// hint in its place (live 2.1.210, at any width). Keying a mode off it was
// always keying off something that is not about the mode.
//
// #333 described that hint area as a single mutually-exclusive slot selected by
// state (interrupt / ctrl_t / agents / voice / shortcuts). The #332 sweep found
// that reading came from the wrong branch: the bundle has two footer
// implementations behind the remote gate `tengu_copper_thistle`, and the
// single-slot one is the *gated* branch. What we render is the ungated branch,
// which builds a hint LIST — hints concatenate, and "? for shortcuts" is pushed
// only when that list is otherwise empty. The live proof is co-occurrence the
// single-slot branch cannot produce: "⏸ manual mode on · ? for shortcuts · ←
// for agents" (2.1.210) carries the shortcuts hint and the agents hint at once.
// Detection is unaffected — both branches render "<label> on" — but note that a
// remote gate can swap this shape with no version change. VerifiedVersion cannot
// express that; the adapter's VerifiedGates records the branch these captures came
// from, and `atrium doctor` reports the value claude last resolved per account.
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
