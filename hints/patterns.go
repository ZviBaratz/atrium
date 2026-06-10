package hints

import (
	"regexp"
	"strings"
)

// Kind classifies what a match is, which decides the open-variant's behavior.
type Kind int

const (
	// KindText is copy-only content (SHAs, UUIDs, IPs, hex).
	KindText Kind = iota
	// KindURL opens in the browser on the open variant.
	KindURL
	// KindPath is a filesystem path (open degrades to copy in v1).
	KindPath
)

// pattern is one built-in matcher. A `match` named group selects the copyable
// substring; otherwise the whole match is copied. validate, when set, rejects
// capture texts the regex grammar cannot express (RE2 has no lookaround);
// the scanner drops rejected matches but still advances past them.
type pattern struct {
	name     string
	re       *regexp.Regexp
	kind     Kind
	validate func(text string) bool
}

// builtinPatterns is the curated set, in priority order: when two patterns
// match at the same column, the earlier entry wins (url beats path, uuid
// beats sha). Regexes follow tmux-fingers/tmux-thumbs, adapted to RE2.
var builtinPatterns = []pattern{
	{name: "markdown-url", re: regexp.MustCompile(`\[[^]]*\]\((?P<match>[^)]+)\)`), kind: KindURL},
	{name: "url", re: regexp.MustCompile(`(?P<match>(https?://|git://|ssh://|ftp://|file:///)[^\s()"']+|git@[^\s()"']+)`), kind: KindURL},
	{name: "diff-path", re: regexp.MustCompile(`(---|\+\+\+) [ab]/(?P<match>.+)`), kind: KindPath},
	{name: "git-status", re: regexp.MustCompile(`(modified|deleted|new file): +(?P<match>.+)`), kind: KindPath},
	{name: "path", re: regexp.MustCompile(`(?P<match>([.\w\-@~]+)?(/[.\w\-@]+)+(:\d+(:\d+)?)?)`), kind: KindPath, validate: pathLike},
	{name: "uuid", re: regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), kind: KindText},
	{name: "sha", re: regexp.MustCompile(`[0-9a-f]{7,64}`), kind: KindText, validate: shaLike},
	{name: "ipv4", re: regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}`), kind: KindText},
	{name: "hex", re: regexp.MustCompile(`0x[0-9a-fA-F]+`), kind: KindText},
	{name: "color", re: regexp.MustCompile(`#[0-9a-fA-F]{6}`), kind: KindText},
}

// shaLike rejects hex runs that cannot plausibly be hashes: all-decimal
// (timestamps, PR numbers, line counts) and all-letter (English words like
// "effaced" spelled entirely in hex letters). Real 7+ char hash prefixes
// containing only one class are vanishingly rare, and a word-boundary
// anchor would break git-describe suffixes ("-g5441edb").
func shaLike(text string) bool {
	return strings.ContainsAny(text, "0123456789") && strings.ContainsAny(text, "abcdef")
}

// digitFraction is a relative all-numeric "path" — dates and progress
// fractions ("10/25") rather than filesystem locations.
var digitFraction = regexp.MustCompile(`^\d+(/\d+)+$`)

// pathLike keeps absolute paths unconditionally and rejects relative
// digit-only fractions.
func pathLike(text string) bool {
	return strings.HasPrefix(text, "/") || !digitFraction.MatchString(text)
}

// hasURLScheme reports whether text starts like something a URL handler can
// take: a known scheme or a git SSH shorthand. Markdown links to relative
// paths fail this and demote to KindPath.
func hasURLScheme(text string) bool {
	for _, p := range []string{"http://", "https://", "git://", "ssh://", "ftp://", "file://"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return strings.HasPrefix(text, "git@")
}
