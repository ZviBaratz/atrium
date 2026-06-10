// Package hints implements the matcher, hint-label assignment, and renderer
// behind fingers mode: detecting actionable strings (URLs, paths, SHAs, …) in
// a captured pane screen and labeling them with short keyboard hints.
//
// The package is deliberately pure — no tmux, UI, or app dependencies — so the
// same engine can later power hints inside attached sessions (see the design
// doc's "Attached sessions" section).
package hints

// Alphabet orders hint characters by keyboard ergonomics (home row first),
// following tmux-thumbs' qwerty layout. Hint keys are matched case-insensitively;
// an uppercase press selects the copy+open variant.
const Alphabet = "asdfqwerzxcvjklmiuopghtybn"

// assignLabels returns n prefix-free hint labels over Alphabet: all single
// characters first; when more are needed, the tail characters are expanded
// into two-character combinations (tmux-thumbs' expansion). A character used
// as a prefix never appears alone, so no label is a prefix of another.
func assignLabels(n int) []string {
	chars := []rune(Alphabet)
	singles := make([]string, len(chars))
	for i, c := range chars {
		singles[i] = string(c)
	}
	var expanded []string
	for n > len(singles)+len(expanded) && len(singles) > 0 {
		last := singles[len(singles)-1]
		singles = singles[:len(singles)-1]
		group := make([]string, 0, len(chars))
		for _, c := range chars {
			group = append(group, last+string(c))
		}
		expanded = append(group, expanded...)
	}
	labels := append(append([]string{}, singles...), expanded...)
	if n < len(labels) {
		labels = labels[:n]
	}
	return labels
}
