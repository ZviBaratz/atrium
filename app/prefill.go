package app

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ZviBaratz/atrium/session"
)

// PrefillResult is the outcome of parsing a smart-dispatch line against the known
// repo candidates. It feeds the new-session form: Path/Title pre-fill the project
// and title, Prompt seeds the agent's initial message. Confident is true only for an
// unambiguous, exact project match — the gate for opt-in auto-dispatch.
type PrefillResult struct {
	Path      string // matched candidate path, "" when nothing matched
	Title     string // a bounded title derived from the line, "" when none
	Prompt    string // the original line, trimmed, verbatim
	Confident bool   // exactly one candidate matched by an exact basename token

	// TitleIsRough is true when the project-stripped phrase still reads as prose
	// (more than 4 words) and would benefit from an LLM rewrite. It lets a confident
	// match — which routes deterministically and skips the LLM — still upgrade a
	// sentence-like title, while leaving a clean short title (e.g. "Review #243")
	// untouched. Only meaningful alongside a non-empty Title.
	TitleIsRough bool
}

// titleRoughWordCount is the word count above which a project-stripped title reads
// as prose rather than a title (a good session title is 2-4 words).
const titleRoughWordCount = 4

// issueRefRe captures the project name from an issue reference like "box#123" or
// "hub-123" (non-greedy name so "box-123" yields "box"). The "-" form is ambiguous
// with a real basename ending in a number, so the captured name is offered only as
// one more match candidate — exact basename matching decides whether it counts.
var issueRefRe = regexp.MustCompile(`^([a-z][a-z0-9_.-]*?)[#-](\d+)$`)

// prefillTrimChars is the set of surrounding punctuation stripped from each word
// before matching, so "hub." or "(box)" still match the repo basename.
const prefillTrimChars = "#.,!?:;()[]{}\"'`"

// ParsePrefill parses a free-form line against an ordered list of candidate repo
// paths (most-relevant first). Matching is deterministic and offline: tokens are
// compared to candidate basenames by exact equality (the confident tier) or a
// length-gated prefix (a hint, never confident). It never shells out — basename
// comparison stands in for the repo group key, keeping the function pure.
func ParsePrefill(line string, candidates []string) PrefillResult {
	prompt := strings.TrimSpace(line)
	res := PrefillResult{Prompt: prompt}
	if prompt == "" {
		return res
	}

	tokens := prefillTokens(prompt)

	var exact, prefix []string
	for _, path := range candidates {
		base := strings.ToLower(filepath.Base(path))
		switch matchTier(base, tokens) {
		case tierExact:
			exact = append(exact, path)
		case tierPrefix:
			prefix = append(prefix, path)
		}
	}

	switch {
	case len(exact) > 0:
		res.Path = exact[0]
		// Confident only when a single repo claims the line: more than one exact match
		// (a duplicated basename, or two different repos named) is genuinely ambiguous,
		// so it prefills a best guess but still routes through the form.
		res.Confident = len(exact) == 1
	case len(prefix) > 0:
		res.Path = prefix[0]
	}

	// Drop the matched project name from the title — it is redundant with the repo
	// group the session is filed under. Only an exact-tier match is stripped: a prefix
	// match (e.g. "atri"→"atrium") is not a clean project mention.
	matchedBase := ""
	if len(exact) > 0 {
		matchedBase = strings.ToLower(filepath.Base(res.Path))
	}
	res.Title, res.TitleIsRough = buildTitle(prompt, matchedBase)
	return res
}

// buildTitle derives the bounded session title from the line. When matchedBase is set
// it removes that project name: a bare word equal to the basename is dropped, and an
// issue-ref naming the project keeps only its number ("box#123" with project "box" →
// "#123", so "Review box#123" → "Review #243"-style). '#' is preserved — the branch
// slug strips it anyway (session/git.sanitizeBranchName). If stripping empties the
// title (the line was only the project name), it falls back to the unstripped line so
// the title is never blank. The second result reports whether the kept phrase reads as
// prose (more than titleRoughWordCount words), measured before the 32-char cap.
func buildTitle(prompt, matchedBase string) (string, bool) {
	words := strings.Fields(prompt)
	var kept []string
	if matchedBase != "" {
		for _, w := range words {
			wl := strings.ToLower(strings.Trim(w, prefillTrimChars))
			if wl == matchedBase {
				continue // the project name itself
			}
			if m := issueRefRe.FindStringSubmatch(wl); m != nil && m[1] == matchedBase {
				kept = append(kept, "#"+m[2]) // keep the issue number, drop the name
				continue
			}
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		kept = words // no match, or the strip emptied the title: keep the line
	}
	return session.SlugTitle(strings.Join(kept, " ")), len(kept) > titleRoughWordCount
}

const (
	tierNone = iota
	tierPrefix
	tierExact
)

// matchTier returns the strongest match between any token and the repo basename:
// exact equality, else a prefix match where the shorter side is at least 4 chars
// (so "atri"→"atrium" counts but "is"→"issues" does not).
func matchTier(base string, tokens []string) int {
	best := tierNone
	for _, t := range tokens {
		if t == base {
			return tierExact
		}
		if len(t) >= 4 && strings.HasPrefix(base, t) {
			best = tierPrefix
		}
		if len(base) >= 4 && strings.HasPrefix(t, base) {
			best = tierPrefix
		}
	}
	return best
}

// prefillTokens lowercases the line and returns the de-duplicated set of match
// candidates: each whitespace word (stripped of surrounding punctuation) plus, for
// an issue-ref word, the captured project name.
func prefillTokens(line string) []string {
	seen := make(map[string]bool)
	var tokens []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		tokens = append(tokens, s)
	}
	for _, raw := range strings.Fields(strings.ToLower(line)) {
		// Trim surrounding punctuation first: the issue-ref regex is $-anchored, so a
		// trailing "." ("nanoclaw#247.") would otherwise fail to parse and the clean
		// project name would never be extracted — silently degrading an exact match to a
		// prefix one (no strip, not confident).
		word := strings.Trim(raw, prefillTrimChars)
		if m := issueRefRe.FindStringSubmatch(word); m != nil {
			add(m[1])
		}
		add(word)
	}
	return tokens
}
