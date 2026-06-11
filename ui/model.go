package ui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// modelChipMaxWidth caps the model chip so a weird or vendor-prefixed id can't
// eat the row (the chip is a fixed segment; only the name column flexes).
const modelChipMaxWidth = 14

// shortModelName compacts a model id mechanically — deliberately no name
// lookup table, so new model releases render reasonably without a code change:
// strip a leading "claude-", drop a trailing 8-digit date token, and peel
// trailing digit tokens into a dot-joined version.
//
//	claude-opus-4-7           → "opus 4.7"
//	claude-haiku-4-5-20251001 → "haiku 4.5"
//	claude-fable-5            → "fable 5"
//	fable (bare alias)        → "fable"
//
// Legacy ids whose version leads the name (claude-3-5-sonnet-20241022 →
// "3-5-sonnet") pass through the same rules; they predate the feature.
func shortModelName(id string) string {
	s := strings.TrimSpace(id)
	s = strings.TrimPrefix(s, "claude-")
	tokens := strings.Split(s, "-")
	if last := tokens[len(tokens)-1]; len(last) == 8 && allDigits(last) {
		tokens = tokens[:len(tokens)-1] // date suffix
	}
	var version []string
	for len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		if last == "" || !allDigits(last) {
			break
		}
		version = append([]string{last}, version...)
		tokens = tokens[:len(tokens)-1]
	}
	name := strings.Join(tokens, "-")
	switch {
	case name == "" && len(version) == 0:
		s = id // nothing survived the transform; show the raw id
	case name == "":
		s = strings.Join(version, ".")
	case len(version) == 0:
		s = name
	default:
		s = name + " " + strings.Join(version, ".")
	}
	return runewidth.Truncate(s, modelChipMaxWidth, "…")
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
