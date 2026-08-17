package main

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// positionalCitation matches a path followed by a line number. Keying on the
// extension is what keeps ratios (83:100), clock times (10:00), contrast figures
// (1.44:1) and ports (:3001) out — all of which appear in comments here and none
// of which is a citation.
//
// The colon binds tight on purpose. Allowing space around it also matched
// ordinary prose that names a file and then a number — "CLAUDE.md: 3 rules apply
// here" — and inside a Go block comment, where the whole group arrives as one
// string, the space could span a newline and pair a filename with the next
// sentence's digit.
//
// The set is neither every extension in the tree nor only the Go side. web/ is a
// Next.js site with its own toolchain, but a line number pointed into one of its
// sources rots exactly as one pointed into a Go file does, so its extensions are
// here and the planted comment in TestProseCitationScannerReportsAPlantedCitation
// holds them. The justfile has no extension to key on and is cited by name, so it
// is matched literally.
//
// What is deliberately absent is the two extensions whose paths this repo prints
// as data: a pane capture and a transcript fixture. ui/diff_anchor_test.go has a
// comment quoting a marker row its own code emits, which names a capture file and
// a row number and is a quoted literal rather than a claim about the tree — the
// exact false positive that scoping to comments avoids everywhere else.
var positionalCitation = regexp.MustCompile(
	`(?:[A-Za-z0-9_./-]+\.(?:go|md|yml|yaml|sh|json|tmpl|ts|tsx|css|mjs)|\bjustfile):[0-9]+`)

// proseHit is one positional citation, located well enough to fix.
type proseHit struct {
	rel  string
	line int
	text string
}

// proseScan is what a scan saw, so a caller can prove the scan happened rather
// than infer it from an empty result. A clean tree and a scanner that matched
// nothing produce identical hits; only these counts tell them apart.
//
// The three anomaly lists exist for the same reason. Each names a way a file can
// be listed and still contribute nothing, which is a state no count of *files*
// can distinguish from a file that was read and had nothing to report.
type proseScan struct {
	hits []proseHit
	// unparsed collects .go files go/parser could not read, unreadable those no
	// arm could open, and unbalanced the .md files that end inside a fence —
	// rather than failing on the first one. A scan that stopped early would
	// report a partial result indistinguishable from a clean tree.
	unparsed   []string
	unreadable []string
	unbalanced []string
	goFiles    int
	comments   int
	mdFiles    int
	mdLines    int
	shFiles    int
	shComments int
	// shIndented counts the comments whose `#` is not the line's first byte.
	// shellScanner finds those by a different branch from the ones at column 0,
	// and a total covers whichever branch survives: break the word-start rule and
	// every whole-line comment is still counted, so the arm that finds every
	// trailing and indented comment can die with the count in four figures.
	shIndented int
}

// scanProse looks for positional citations in the durable prose of rels, which
// are paths relative to root. Each surface is scoped to the part of the file that
// makes claims about the tree:
//
//   - .go — comments only, via go/parser. This repo treats a positional reference
//     as data: the hints scanner's `path` pattern captures the trailing `:line`
//     and `:col` so an agent's error output is copyable off the pane, and
//     diffRangeLocation writes the same shape into a review anchor. Their
//     fixtures are string literals doing their job, not claims about the tree,
//     and an unscoped regex would report every one of them.
//
//   - .sh — from the comment `#` to end of line, so a trailing comment counts.
//     Which `#` that is, shellScanner decides, and it reads the file rather than
//     the line: a heredoc body and a quote left open both carry past the newline,
//     and neither is a comment. docs/demos/render.sh embeds a whole bash script in
//     one heredoc and a Python program in another, each with comments of its own,
//     and test/smoke/run.sh passes a multi-line awk program as one quoted argument.
//     Judging each line alone read all of those as comments of this repo's.
//
//     That skips prose as well as programs, and the gap is worth stating: the usage
//     text drive-agent.sh prints comes out of a heredoc, so it is shipped prose this
//     guard does not read. Nothing distinguishes it from the embedded programs
//     mechanically — both are heredoc bodies — and the arm is scoped to comments,
//     which it is not.
//
//   - .md — outside fenced code blocks, which are the markdown equivalent of a
//     string literal: a pasted compiler error or `go test` output is a transcript,
//     not a claim. An indented block is one too, so four columns after a blank line
//     opens the same exemption — columns rather than bytes, because CommonMark
//     reads a tab as four of them and len() reads it as one. That rule is still
//     coarser than the spec, which measures the indent relative to an enclosing
//     list item: a paragraph indented under a bullet goes unscanned here. Missing a
//     line is the direction to err in — a guard that turns CI red for a correctly
//     written file costs more than one that stays quiet about a citation nobody has
//     written yet.
//
// An inline code span is the one place that last principle is deliberately
// inverted: spans are scanned. Backticks are how prose spells a citation — the
// exempt plans write theirs inside them — so exempting spans would leave this arm
// reading only the spelling nobody uses. The cost is that a compiler error quoted
// inline is reported, and the fix for that is to fence it, which is what a
// transcript wants anyway. inline.md in the control holds both halves.
func scanProse(root string, rels []string) proseScan {
	var scan proseScan
	record := func(rel string, line int, text string) {
		scan.hits = append(scan.hits, proseHit{rel: rel, line: line, text: strings.TrimSpace(text)})
	}
	// Reading is collected rather than asserted for the same reason parsing is: a
	// require here would unwind the whole scan through runtime.Goexit, so one
	// unreadable file would hide every violation below it and in every file after
	// it. That is also why this function takes no *testing.T — it reports, and the
	// verdict is the caller's.
	lines := func(rel, path string) ([]string, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			scan.unreadable = append(scan.unreadable, rel)
			return nil, false
		}
		return strings.Split(string(data), "\n"), true
	}

	for _, rel := range rels {
		path := filepath.Join(root, filepath.FromSlash(rel))
		switch filepath.Ext(rel) {
		case ".go":
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if parseErr != nil {
				scan.unparsed = append(scan.unparsed, rel)
				continue
			}
			scan.goFiles++
			for _, group := range f.Comments {
				for _, c := range group.List {
					scan.comments++
					if positionalCitation.MatchString(c.Text) {
						record(rel, fset.Position(c.Pos()).Line, c.Text)
					}
				}
			}
		case ".sh":
			body, ok := lines(rel, path)
			if !ok {
				continue
			}
			scan.shFiles++
			var sh shellScanner
			for i, line := range body {
				hash := sh.comment(line)
				if hash < 0 {
					continue
				}
				scan.shComments++
				if hash > 0 {
					scan.shIndented++
				}
				if positionalCitation.MatchString(line[hash:]) {
					record(rel, i+1, line)
				}
			}
		case ".md":
			body, ok := lines(rel, path)
			if !ok {
				continue
			}
			scan.mdFiles++
			fence, indented, prevBlank := "", false, true
			for i, line := range body {
				blank := strings.TrimSpace(line) == ""
				// An indented code block is a transcript too, and it opens after a
				// blank line rather than with a delimiter. Only a line that is not
				// itself indented ends one, which is why a blank does not clear the
				// state.
				if fence == "" && !blank {
					indented = indentOf(line) >= 4 && (indented || prevBlank)
				}
				prevBlank = blank
				if fence == "" && indented {
					continue
				}
				if marker, bare := fenceMarker(line); marker != "" {
					switch {
					case fence == "":
						fence = marker
					case bare && marker[0] == fence[0] && len(marker) >= len(fence):
						fence = ""
					}
					continue
				}
				if fence != "" {
					continue
				}
				scan.mdLines++
				if positionalCitation.MatchString(line) {
					record(rel, i+1, line)
				}
			}
			if fence != "" {
				scan.unbalanced = append(scan.unbalanced, rel)
			}
		}
	}
	return scan
}

// fenceMarker returns the run of fence characters that begins line, or "", and
// whether nothing follows it. Both spellings count, and the run length is returned
// rather than discarded so a nested block closes at the right depth: a ```` fence
// containing ``` must not be closed by the inner one.
//
// The second return is the info-string rule. CommonMark lets an opening fence
// carry a language and forbids one on a closing fence, which is what stops a
// ```markdown block from being closed by the ```go line it is quoting. Reading
// that line as a close ended the block early, so the quoted example was scanned as
// prose and every citation in it reported — and the fence it re-opened at the next
// delimiter ran to end of file, which the caller reports as an unscanned tail.
// Both failures are loud on a file that is doing nothing wrong.
func fenceMarker(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == ch {
			n++
		}
		if n >= 3 {
			return trimmed[:n], strings.TrimSpace(trimmed[n:]) == ""
		}
	}
	return "", false
}

// eachUnquoted walks line and calls visit for every byte the shell reads as
// syntax rather than data: outside single and double quotes, and not escaped. It
// starts in quote — 0 for a line that inherits nothing — and returns the quote
// still open at the end. visit returns false to stop the walk.
//
// shellScanner and splitCommands both hang off it so the two cannot come to
// disagree about what "quoted" means — which they did, back when the splitter was
// a regexp that had never heard of quoting.
func eachUnquoted(line string, quote byte, visit func(i int, c byte) bool) byte {
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\':
			i++
		default:
			if !visit(i, c) {
				return quote
			}
		}
	}
	return quote
}

// shellScanner reads a script one line at a time, carrying what each line
// inherits from the ones above it. A quote left open and a heredoc awaiting its
// terminator both make the following lines data rather than shell, and their `#`
// lines belong to whatever is embedded there — a Python program, an awk script —
// rather than to this repo. Judging each line alone read those as comments, so a
// path and a line number inside an embedded script would have failed CI for prose
// this repo does not own.
type shellScanner struct {
	quote byte
	// here is the terminator word an open heredoc is waiting for, and strip is
	// the `<<-` form, which lets that word be indented with tabs.
	here  string
	strip bool
}

// comment returns the index of the `#` that opens a comment on line, or -1, and
// advances the scanner past it. A `#` opens a comment only when it is unquoted and
// starts a word, which is what keeps `${#arr}`, `$#`, a URL fragment and a quoted
// "#1" out. Taking the first `#` on the line instead treated everything after a
// quoted one as a comment, so a line whose only path-and-number sat in a quoted
// argument was reported as prose. Those shapes are inputs in
// TestShellCommentStartFindsTheCommentAndNothingElse, and plants in
// TestProseCitationScannerReportsAPlantedCitation, which carries the ones that have
// to stay silent while a citation sits on the same line.
//
// Word-start is approximated as "preceded by whitespace", which under-detects
// rather than over-detects: `foo;#c` is a comment to the shell and prose to this.
// A guard that turns CI red for correct code is worse than one that misses a
// spelling no script here uses. A line opening two heredocs at once errs the same
// way, keeping only the second terminator and so skipping past both bodies.
func (s *shellScanner) comment(line string) int {
	if s.here != "" {
		body := line
		if s.strip {
			body = strings.TrimLeft(body, "\t")
		}
		if body == s.here {
			s.here, s.strip = "", false
		}
		return -1
	}
	hash := -1
	s.quote = eachUnquoted(line, s.quote, func(i int, c byte) bool {
		switch {
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			hash = i
			return false
		// `<<` opens a heredoc. `<<<` is a herestring and opens nothing, and the
		// walk visits each of its three `<`: the middle one is turned away here,
		// and the first by heredocWord, which reads a terminator as a word and a
		// `<` does not begin one. Testing for the third `<` here as well was a
		// second guard on the same case, and a redundant guard is one that can be
		// deleted with the tests still green.
		case c == '<' && i+1 < len(line) && line[i+1] == '<' && (i == 0 || line[i-1] != '<'):
			if word, strip, ok := heredocWord(line[i+2:]); ok {
				s.here, s.strip = word, strip
			}
		}
		return true
	})
	return hash
}

// heredocWord reads the terminator out of what follows a `<<`, and reports
// whether one is there at all. The word may be quoted, which is how a heredoc
// declines to expand its body, and the quotes are not part of it.
func heredocWord(rest string) (string, bool, bool) {
	strip := strings.HasPrefix(rest, "-")
	if strip {
		rest = rest[1:]
	}
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return "", false, false
	}
	if q := rest[0]; q == '\'' || q == '"' {
		end := strings.IndexByte(rest[1:], q)
		if end < 0 {
			return "", false, false
		}
		return rest[1 : 1+end], strip, true
	}
	n := 0
	for n < len(rest) && (rest[n] == '_' ||
		'0' <= rest[n] && rest[n] <= '9' ||
		'a' <= rest[n] && rest[n] <= 'z' ||
		'A' <= rest[n] && rest[n] <= 'Z') {
		n++
	}
	if n == 0 {
		return "", false, false
	}
	return rest[:n], strip, true
}

// shellCommentStart is comment on a line that inherits nothing, for the callers
// that have one line and no file around it — a workflow `run:` value is the whole
// script it is.
func shellCommentStart(line string) int {
	var s shellScanner
	return s.comment(line)
}

// splitCommands cuts line at the unquoted separators that end one command and
// begin another. Every `;`, `&` and `|` cuts, so `&&` and `||` need no case of
// their own: they fall out as two cuts around an empty segment, which matches no
// invocation and costs nothing.
//
// Quoting is the whole point, and a regexp could not express it.
// `-skip 'TestFoo|TestSessionDeathStopsProbing'` is one command; cutting it at the
// alternation left `go test -skip 'TestFoo`, an invocation that reads as carrying
// no -skip at all. skipsRealTmux accepts that spelling and
// TestWorkflowLineClassifiersRejectTheLinesTheyMust asserts it does, so the guard
// would have failed a workflow line its own control calls correct.
func splitCommands(line string) []string {
	var out []string
	start := 0
	eachUnquoted(line, 0, func(i int, c byte) bool {
		if c == ';' || c == '&' || c == '|' {
			out = append(out, line[start:i])
			start = i + 1
		}
		return true
	})
	return append(out, line[start:])
}

// indentOf returns the column line's first non-blank byte sits in, counting a tab
// as advancing to the next multiple of four. Counting bytes read a tab as one
// column, so a tab-indented markdown transcript missed the four-column rule that
// exempts an indented code block and was scanned as prose.
func indentOf(line string) int {
	return columnAfter(line[:len(line)-len(strings.TrimLeft(line, " \t"))])
}

// TestIndentOfCountsColumnsNotBytes pins the tab stop. The markdown rule reads
// four columns, and the only fixture that can distinguish the two rules is one
// where a tab does not start at column 0 — a tab after a space is one byte and
// three columns, and both numbers are wrong for the other rule.
func TestIndentOfCountsColumnsNotBytes(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"no indent", 0},
		{"    four spaces", 4},
		{"\tone tab", 4},
		{" \ta space then a tab", 4},
		{"\t\ttwo tabs", 8},
	} {
		assert.Equalf(t, tc.want, indentOf(tc.line), "indentOf(%q)", tc.line)
	}
}

// columnAfter returns the column a line reaches after s, on the same tab stops.
func columnAfter(s string) int {
	col := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			col += 4 - col%4
			continue
		}
		col++
	}
	return col
}

// trackedFiles lists the files in the index matching patterns, relative to root.
//
// The list comes from git rather than a directory walk because "shipped" is
// exactly what these guards mean and a walk cannot express it — a walk picks up
// whatever is lying in the tree, including a review's own scratch findings file,
// which is by contract a list of paths and line numbers, or a half-written
// repro.sh left in the root mid-debug. TestShellScriptsParse wrote that reason
// down first and now calls this, so the mid-rebase rule and the timeout are one
// decision rather than a copy per guard.
//
// The index is not the working tree, and that costs something: a file written but
// not yet `git add`ed is invisible here, so a guard built on this does not fail
// until the author stages the drift.
func trackedFiles(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := append([]string{"-C", root, "ls-files", "-z", "--"}, patterns...)
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	require.NoError(t, err, "git ls-files failed; this reads the index, not the working tree")

	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		// A file can be in the index and absent from the tree mid-rebase; nothing
		// is proved by failing to read one that is not there.
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr != nil {
			continue
		}
		rels = append(rels, rel)
	}
	return rels
}

// trackedProse lists the durable prose files in the index, minus the historical
// plans and specs.
//
// docs/superpowers is the only exemption, and it is one both callers want: a plan
// describes the tree it was written against, and naming the exact line to edit is
// a plan step's job. web/ is not exempt, though it is a standalone Next.js site
// the Go tooling otherwise ignores, because the rule about how prose cites is
// about the prose and not about which toolchain builds the code beside it. What
// that decision costs is nothing measurable either way: web/ holds one tracked
// prose file, and it carries neither a citation nor a mention of the log the other
// caller looks for.
func trackedProse(t *testing.T, root string) []string {
	t.Helper()
	var rels []string
	for _, rel := range trackedFiles(t, root, "*.go", "*.md", "*.sh") {
		if rel == historicalDocs || strings.HasPrefix(rel, historicalDocs+"/") {
			continue
		}
		rels = append(rels, rel)
	}
	return rels
}

// A line number in prose is a claim with a half-life measured in edits, and it is
// the one claim the author cannot check by reading the sentence: the number is
// true or false somewhere else in the tree.
//
// #675 swept them out of the tree in one comment-only commit. Several were
// provably wrong — pointing at a blank line, a closing brace, an `if err != nil`,
// and in one case a file at a path that did not exist. The rest were merely
// unverifiable, which is the same thing to a reader.
//
// The convention held afterwards by habit alone, and habit lost. #720 shipped a
// fresh pair; the correction round replaced them with a *different* wrong pair,
// because the same commit's other edits had moved the lines again; the claim only
// stopped rotting when the third attempt deleted it. That ratchet is the argument
// for a guard rather than a paragraph — prose asking for symbols is itself prose,
// and it had already been written.
//
// So the rule is that prose cites a symbol, and this enforces it in Go, markdown
// and shell. Cite the function, the const, the field: those move with the code,
// and a rename that strands one is a grep away from being found.
func TestNoProseCitesAPosition(t *testing.T) {
	root := moduleRoot(t)
	rels := trackedProse(t, root)
	scan := scanProse(root, rels)

	for _, hit := range scan.hits {
		t.Errorf("%s:%d cites a position; cite the symbol instead, which survives an edit:\n\t%s",
			hit.rel, hit.line, hit.text)
	}

	// A scan that reached nothing passes exactly as happily as a clean tree, so a
	// broken pathspec or an over-eager exemption would turn this guard off rather
	// than fail it. Assert each surface separately, since a single total would let
	// a whole extension go dark, and assert the unit each one can lose silently
	// rather than the file that holds it: a .go file parses and yields no comments,
	// an unclosed fence blanks a .md file the count still counts, and a scanner
	// that found no `#` anywhere leaves every .sh file scanned and unread.
	require.NotEmpty(t, rels, "git ls-files matched no prose; the pathspec is broken, not the tree")
	require.Positive(t, scan.comments, "parsed no comments; ParseComments is not doing its job")
	require.Positive(t, scan.mdFiles, "scanned no .md files; the exemptions are wrong")
	require.Positive(t, scan.mdLines, "scanned no .md prose lines; every file is fenced shut")
	require.Positive(t, scan.shFiles, "scanned no .sh files; the exemptions are wrong")
	require.Positive(t, scan.shComments, "found no shell comments at all; shellScanner is returning -1 everywhere")
	require.Positive(t, scan.shIndented,
		"found no shell comment past column 0, so every trailing and indented one is going unread; "+
			"the word-start branch is dead and the count above cannot see it, because the comments at "+
			"column 0 are found by the other one")

	// Every listed file must have been read, and every .go file parsed. A file the
	// scanner could not open is one it reported nothing about, which on its own is
	// indistinguishable from a file with nothing to report.
	require.Empty(t, scan.unreadable, "these files could not be read, so they went unscanned")
	require.Empty(t, scan.unparsed, "these .go files did not parse, so they went unscanned")
	require.Empty(t, scan.unbalanced, "these .md files end inside a code fence, so the tail went unscanned")
	wantGo := 0
	for _, rel := range rels {
		if filepath.Ext(rel) == ".go" {
			wantGo++
		}
	}
	require.Equal(t, wantGo, scan.goFiles, "the scan skipped .go files for some reason other than a parse error")
}

// TestProseCitationScannerReportsAPlantedCitation is the control. The tree is
// clean, so TestNoProseCitesAPosition passes whether or not the scanner works —
// disabling either match arm leaves it green, which makes it a test of the
// exemptions and nothing else.
//
// So plant citations and require them reported, and plant the near-misses each
// surface deliberately ignores and require their silence. Both directions matter:
// a scanner that reports nothing and a scanner that reports every string literal
// are both broken, and only one of them is loud.
func TestProseCitationScannerReportsAPlantedCitation(t *testing.T) {
	// The planted citations below are Go string literals, which is why writing
	// them out here is safe: the .go arm reads comments only. If that scoping is
	// ever dropped, this file becomes a violation of its own guard — which is the
	// right way round.
	dir := t.TempDir()
	files := map[string]string{
		// Lines 3, 8 and 10 are claims and must be reported; the extensionless one
		// has no dot to key on, so only a literal name matches it. Line 4 is the
		// same shape in code, line 6 spaces the colon, and line 12 names a path
		// this repo's own output prints — all three stay silent.
		"control.go": "package control\n" +
			"\n" +
			"// A claim about session/instance.go:2228 in a comment.\n" +
			"const parsedFromAgentOutput = \"ui/terminal.go:412\"\n" +
			"\n" +
			"// Prose that names CLAUDE.md: 3 rules is not a citation.\n" +
			"\n" +
			"// A claim about web/src/app/page.tsx:42, the other half of the repo.\n" +
			"\n" +
			"// A claim about justfile:52, which has no extension to key on.\n" +
			"\n" +
			"// A marker row this repo prints, gone.txt:4, is data and not a claim.\n",
		// Lines 2-4 are comments and must be reported. Lines 5-8 each carry the
		// same citation shape in code, and each is silent for a different reason:
		// no `#` at all, a `#` inside double quotes, a `#` opening `${#…}`, and a
		// `#` opening `$#`. Only the first is caught by "the line has no comment";
		// the rest need the word-start rule to be doing its job. Lines 9-18 are
		// three embedded programs and a herestring, whose contents belong to them
		// and not to this repo; the scanner only knows that by carrying state
		// across lines, and the last one closes on a terminator only the `<<-`
		// form's tab-stripping can match. Miss that and the file ends inside a
		// block, taking the comment on line 19 with it. Line 20 is the other
		// direction: a comment is not shell, so the apostrophe in it opens
		// nothing, and line 21 proves the scanner is still reading.
		"control.sh": "#!/usr/bin/env bash\n" +
			"# A whole-line comment naming config/accessors.go:251.\n" +
			"PANE=\"\"  # a trailing comment naming session/tmux/tmux.go:512\n" +
			"\tprintf 'x'  # an indented trailing comment naming ui/list.go:85\n" +
			"grep -oE 'x' \"app/app.go:99\"\n" +
			"echo \"#1\" && grep -n 'x' \"app/app.go:99\"\n" +
			"printf '%s' ${#PANE} \"app/app.go:99\"\n" +
			"[ $# -gt 0 ] && echo \"app/app.go:99\"\n" +
			"cat <<'PY' > /dev/null\n" +
			"# a Python comment naming app/app.go:99\n" +
			"PY\n" +
			"awk '\n" +
			"# an awk program naming app/app.go:99\n" +
			"' /dev/null\n" +
			"grep -q x <<<\"app/app.go:99\"\n" +
			"cat <<-'TXT' > /dev/null\n" +
			"\t# a heredoc body naming app/app.go:99\n" +
			"\tTXT\n" +
			"# A comment naming ui/list.go:85, after every block closed.\n" +
			"# A comment that isn't shell, apostrophe and all.\n" +
			"# A comment naming ui/diff.go:11, still read after it.\n",
		"control.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"```\n" +
			"main.go:12:5: undefined: foo\n" +
			"```\n" +
			"\n" +
			"More prose naming ui/list.go:85, after the fence closed again.\n",
		// A tilde fence and a nested one. Both are silent today in the direction
		// that hides violations, so neither is provable from the tree.
		"tilde.md": "~~~\n" +
			"main.go:12:5: undefined: foo\n" +
			"~~~\n" +
			"Prose naming app/help.go:60.\n",
		"nested.md": "````\n" +
			"```\n" +
			"main.go:12:5: undefined: foo\n" +
			"```\n" +
			"````\n" +
			"Prose naming app/help.go:60.\n",
		// A fenced block quoting another fence of the same length. The inner
		// delimiter carries an info string, so it opens rather than closes and the
		// block runs to the bare one. Reading it as a close scanned the quoted
		// example as prose and left the rest of the file inside a fence that never
		// opened.
		"infostring.md": "Prose naming app/help.go:60.\n" +
			"```markdown\n" +
			"```go\n" +
			"// cites ui/list.go:85\n" +
			"```\n" +
			"Prose naming session/tmux/tmux.go:512.\n",
		// A transcript in an indented block rather than a fenced one. Nothing
		// delimits it, so only the four columns and the blank line above say it is
		// not prose.
		"indented.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"    main.go:12:5: undefined: foo\n" +
			"\n" +
			"More prose naming ui/list.go:85.\n",
		// The same block indented with a tab, which is four columns to a markdown
		// reader and one byte to len(). Counting bytes scanned it as prose.
		"tabbed.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"\tmain.go:12:5: undefined: foo\n" +
			"\n" +
			"More prose naming ui/list.go:85.\n",
		// Both halves of the inline-span decision: the spelling prose actually uses
		// for a citation, and the transcript that spelling cannot be told apart
		// from. Reporting both is the choice; exempting spans would silence the
		// first as well, and that is the one worth catching.
		"inline.md": "A citation written as `app/help.go:60`, which is how prose spells one.\n" +
			"\n" +
			"`main.go:12:5: undefined: foo` quoted inline is reported too; fence it and it is not.\n",
		// Ends inside a fence, so everything after line 3 goes unscanned. The
		// caller has to be told, or the file reads as clean.
		"unbalanced.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"```\n" +
			"main.go:12:5: undefined: foo\n",
		// A file the parser must give up on, so the incompleteness the caller
		// checks for is a state this reaches rather than one it assumes.
		"broken.go": "package control\n\nfunc (\n",
	}
	var rels []string
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		rels = append(rels, name)
	}
	// Listed and never written, so the read arms have something to fail on. Without
	// it the caller's "nothing went unread" assertion is vacuous — it would hold
	// just as well against a scanner that discarded the error.
	rels = append(rels, "missing.md", "missing.sh")

	scan := scanProse(dir, rels)

	byFile := map[string][]int{}
	for _, hit := range scan.hits {
		byFile[hit.rel] = append(byFile[hit.rel], hit.line)
	}
	assert.Equal(t, []int{3, 8, 10}, byFile["control.go"],
		"want the three comments, including the one citing a web/ source and the one naming a file "+
			"with no extension, and none of the string literal, the spaced colon or the printed row")
	assert.Equal(t, []int{2, 3, 4, 19, 21}, byFile["control.sh"],
		"want the whole-line comment and both trailing comments, indented or not, plus the two after "+
			"the embedded programs, and none of the four code lines or the three embedded comments")
	assert.Equal(t, []int{1, 7}, byFile["control.md"],
		"want the prose either side of the fence and not the transcript inside it")
	assert.Equal(t, []int{4}, byFile["tilde.md"], "a ~~~ fence must fence")
	assert.Equal(t, []int{6}, byFile["nested.md"], "the inner ``` must not close the outer ````")
	assert.Equal(t, []int{1, 5}, byFile["indented.md"],
		"want the prose either side and not the indented transcript between them")
	assert.Equal(t, []int{1, 5}, byFile["tabbed.md"],
		"a tab indents by four columns, so the transcript between the prose is a code block")
	assert.Equal(t, []int{1, 3}, byFile["inline.md"],
		"an inline code span is scanned, both when it holds a citation and when it holds a transcript")
	assert.Equal(t, []int{1, 6}, byFile["infostring.md"],
		"want the prose either side of the block and not the fence it quotes; a closing fence carries no info string")
	assert.Equal(t, []int{1}, byFile["unbalanced.md"], "the prose before the unclosed fence still counts")

	require.Equal(t, 1, scan.goFiles, "the .go arm did not run, or counted the file it could not parse")
	require.Equal(t, 1, scan.shFiles, "the .sh arm did not run")
	require.Equal(t, 7, scan.shComments,
		"the shebang, the three comments above and the three below the embedded programs are what the "+
			"scanner must find — and nothing inside a heredoc, a tab-stripped heredoc or a quote "+
			"that spans lines")
	require.Equal(t, 2, scan.shIndented, "the trailing and the indented comment are the two past column 0")
	require.Equal(t, 8, scan.mdFiles, "the .md arm did not run over every markdown fixture")
	require.Positive(t, scan.comments, "the .go arm parsed no comments")
	require.Positive(t, scan.mdLines, "the .md arm scanned no unfenced lines")

	// The unparseable file must be reported and must not have stopped the scan —
	// the assertions above already prove the latter, since every other surface was
	// still reached. The unclosed fence is the same contract one surface over.
	require.Equal(t, []string{"broken.go"}, scan.unparsed)
	require.Equal(t, []string{"unbalanced.md"}, scan.unbalanced)
	require.ElementsMatch(t, []string{"missing.md", "missing.sh"}, scan.unreadable)
}

// TestShellCommentStartFindsTheCommentAndNothingElse is the unit control for the
// single-line half of shellScanner. The multi-line half — a heredoc body, a quote
// left open — is held by control.sh in the planted tree above, because it is a
// property of a file rather than of a line.
func TestShellCommentStartFindsTheCommentAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"# comment", 0},
		{"  \t# indented comment", 3},
		{`PANE=""  # trailing`, 9},
		{`echo "#1" && grep 'x'`, -1},
		{`printf '%s' ${#PANE}`, -1},
		{`[ $# -gt 0 ]`, -1},
		{`curl "http://x/y#frag"`, -1},
		{`grep '#literal'`, -1},
		{`echo \# escaped`, -1},
		// A heredoc opener is still a line of shell, and its own trailing comment
		// is still a comment.
		{`cat <<'EOF'  # opens a block`, 13},
	} {
		assert.Equalf(t, tc.want, shellCommentStart(tc.line), "shellCommentStart(%q)", tc.line)
	}

	// heredocWord decides where a body begins, and getting it wrong in the
	// permissive direction swallows the rest of the file: the scanner would look
	// for a terminator that never arrives and report no comment again.
	for _, tc := range []struct {
		rest  string
		word  string
		strip bool
		ok    bool
	}{
		{"'PY'", "PY", false, true},
		{`"PY"`, "PY", false, true},
		{"-'EOF'", "EOF", true, true},
		{"EOF", "EOF", false, true},
		{"-EOF", "EOF", true, true},
		{" EOF > out", "EOF", false, true},
		{"", "", false, false},
		{"'unterminated", "", false, false},
	} {
		word, strip, ok := heredocWord(tc.rest)
		assert.Equalf(t, tc.ok, ok, "heredocWord(%q) ok", tc.rest)
		assert.Equalf(t, tc.word, word, "heredocWord(%q) word", tc.rest)
		assert.Equalf(t, tc.strip, strip, "heredocWord(%q) strip", tc.rest)
	}

	// A herestring is not a heredoc. Reading one as a heredoc opener leaves the
	// scanner waiting for a terminator spelled like the string's contents, so
	// every line after it goes unread — and both `<` of the pair have to reject
	// it, since the walk visits each one.
	var s shellScanner
	require.Equal(t, -1, s.comment(`grep -q x <<<"$flat"`))
	assert.Empty(t, s.here, "a herestring must not open a heredoc")
	assert.Equal(t, 0, s.comment("# the next line is still read"))
}

// readFile reads a whole file at an absolute path. moduleFile is the form that
// resolves a name against the module root, and is written in terms of this one.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
