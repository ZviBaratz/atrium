package agent

// ClaudeEffortLevels are the reasoning-effort levels the create form offers as
// chips (after the field's own "default" chip). The claude CLI (2.1.207 --help)
// documents exactly these for --effort: "Effort level for the current session
// (low, medium, high, xhigh, max)". Unlike --permission-mode the CLI does not
// reject an unknown value — it prints a warning and falls back to the default
// effort — so this list is the offered set, not a hard gate.
// TestClaudeEffortLevels_MatchInstalledCLI pins it to the installed binary.
var ClaudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEffortLabels maps an --effort level to its display label — the single source of
// truth for every chip that shows an effort: the create form's offered row and the
// session-list chip (including levels the form doesn't offer, should a newer CLI resolve
// one). "medium" is abbreviated to "med" purely to keep the form's chip row inside the
// width budget the claude fields share: with full labels the row is 45 cells, over the
// 41-cell budget modelField.go documents for the worst realistic overlay width (80-col
// terminal → 42 inner cells); "med" brings it to 42, so every label stays visible without
// truncation. The value carried into --effort is still the full "medium" (chipRow.selected
// returns options[i], not labels[i]). A level absent here has no special label and renders
// as its raw value (see ClaudeEffortLabel).
var claudeEffortLabels = map[string]string{"medium": "med"}

// ClaudeEffortLabel returns the display label for an --effort level, falling back to the
// raw level for one with no special label. The one place level→label knowledge lives, so
// the create form and the list chip can never disagree — and so a level a newer CLI
// resolves (which the hook would report verbatim) still renders as itself rather than
// being dropped.
func ClaudeEffortLabel(level string) string {
	if label, ok := claudeEffortLabels[level]; ok {
		return label
	}
	return level
}

// ClaudeEffortLabels are the display labels for ClaudeEffortLevels, in the same order —
// derived from the shared label map so the two never drift.
var ClaudeEffortLabels = func() []string {
	labels := make([]string, len(ClaudeEffortLevels))
	for i, l := range ClaudeEffortLevels {
		labels[i] = ClaudeEffortLabel(l)
	}
	return labels
}()

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

// WithEffortFlag returns program with `--effort level` applied: verbatim append
// when the program carries no pin, replace when it does (see withFlag).
func WithEffortFlag(program, level string) string {
	return withFlag(program, "--effort", level)
}

// EffortFlag returns the value of an --effort pin in program ("" = none), the
// extraction counterpart of WithEffortFlag. Deliberately unvalidated, like
// ModelFlag and unlike PermissionModeFlag: ClaudeEffortLevels is the offered set
// rather than a hard gate, so gating extraction on it would drop a level a newer
// CLI supports but Atrium's list hasn't caught up to. A value the CLI does not
// support is warned-and-ignored there, leaving the chip briefly optimistic until
// the first tool-use turn reports the resolved truth — the same self-correcting
// intent-then-truth tradeoff the model chip makes.
func EffortFlag(program string) string { return flagValue(program, "--effort") }
