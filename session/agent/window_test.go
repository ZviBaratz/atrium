package agent

import "testing"

// TestClaudeContextWindow_KnownModels pins the table's entries. The values are
// the models' declared windows; getting one wrong is not a cosmetic defect,
// because the denominator is what turns a true token count into a false
// percentage.
func TestClaudeContextWindow_KnownModels(t *testing.T) {
	for model, want := range map[string]int{
		"claude-fable-5":            1_000_000,
		"claude-mythos-5":           1_000_000,
		"claude-opus-5":             1_000_000,
		"claude-opus-4-8":           1_000_000,
		"claude-opus-4-7":           1_000_000,
		"claude-opus-4-6":           1_000_000,
		"claude-sonnet-5":           1_000_000,
		"claude-sonnet-4-6":         1_000_000,
		"claude-haiku-4-5":          200_000,
		"claude-haiku-4-5-20251001": 200_000,
	} {
		got, ok := ClaudeContextWindow(model)
		if !ok {
			t.Errorf("ClaudeContextWindow(%q): not found", model)
			continue
		}
		if got != want {
			t.Errorf("ClaudeContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}

// TestClaudeContextWindow_IsExactMatchNotPrefixMatch is the guard the whole
// design rests on.
//
// The tempting implementation is strings.HasPrefix(model, "claude-opus") → 1M,
// and it reads like sensible future-proofing. It is the opposite: it would make
// the next model ship silently mis-measured, reporting a confident wrong
// fraction with nothing on screen to say so. Exact matching means an unknown
// model falls back to a raw count instead, so a stale table degrades visibly.
//
// Every case below shares a prefix with a real entry and must still miss. The
// invented ids are the point — claude-opus-99 is what a future release looks
// like from here, and this test fails the day someone "helpfully" loosens the
// lookup.
func TestClaudeContextWindow_IsExactMatchNotPrefixMatch(t *testing.T) {
	for _, model := range []string{
		"claude-opus-99",            // a future model
		"claude-opus-4-9",           // a future point release
		"claude-opus",               // the bare family prefix
		"claude-opus-5-20260401",    // a dated form the table does not carry
		"claude-sonnet-6",           // a future Sonnet
		"claude-haiku-5",            // a future Haiku
		"claude-haiku-4-5-20991231", // a different dated Haiku build
		"claude-opus-5-fast",        // a suffixed deployment id
		"opus",                      // a --model alias, not a transcript id
		"",                          // no model at all
	} {
		if tokens, ok := ClaudeContextWindow(model); ok {
			t.Errorf("ClaudeContextWindow(%q) matched (%d tokens) — the lookup must be exact, "+
				"so an unknown model degrades to a count rather than a wrong percentage", model, tokens)
		}
	}
}
