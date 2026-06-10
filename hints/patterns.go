// hints/patterns.go
package hints

import "regexp"

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
// substring; otherwise the whole match is copied.
type pattern struct {
	name string
	re   *regexp.Regexp
	kind Kind
}

// builtinPatterns is the curated set, in priority order: when two patterns
// match at the same column, the earlier entry wins (url beats path, uuid
// beats sha). Regexes follow tmux-fingers/tmux-thumbs, adapted to RE2.
var builtinPatterns = []pattern{
	{"markdown-url", regexp.MustCompile(`\[[^]]*\]\((?P<match>[^)]+)\)`), KindURL},
	{"url", regexp.MustCompile(`(?P<match>(https?://|git://|ssh://|ftp://|file:///)[^\s()"']+|git@[^\s()"']+)`), KindURL},
	{"diff-path", regexp.MustCompile(`(---|\+\+\+) [ab]/(?P<match>.+)`), KindPath},
	{"git-status", regexp.MustCompile(`(modified|deleted|new file): +(?P<match>.+)`), KindPath},
	{"path", regexp.MustCompile(`(?P<match>([.\w\-@~]+)?(/[.\w\-@]+)+(:\d+(:\d+)?)?)`), KindPath},
	{"uuid", regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), KindText},
	{"sha", regexp.MustCompile(`[0-9a-f]{7,64}`), KindText},
	{"ipv4", regexp.MustCompile(`\d{1,3}(\.\d{1,3}){3}`), KindText},
	{"hex", regexp.MustCompile(`0x[0-9a-fA-F]+`), KindText},
	{"color", regexp.MustCompile(`#[0-9a-fA-F]{6}`), KindText},
}
