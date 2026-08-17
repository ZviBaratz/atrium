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

// positionalCitation matches a path bearing one of this repo's source extensions,
// immediately followed by a line number. Keying on the extension is what keeps
// ratios (83:100), clock times (10:00), contrast figures (1.44:1) and ports
// (:3001) out — all of which appear in comments here and none of which is a
// citation.
//
// The colon binds tight on purpose. Allowing space around it also matched
// ordinary prose that names a file and then a number — "CLAUDE.md: 3 rules apply
// here" — and inside a Go block comment, where the whole group arrives as one
// string, the space could span a newline and pair a filename with the next
// sentence's digit.
var positionalCitation = regexp.MustCompile(`[A-Za-z0-9_./-]+\.(?:go|md|yml|yaml|sh|json|toml|tmpl):[0-9]+`)

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
//   - .sh — from the comment `#` to end of line, so a trailing comment counts.
//     Which `#` that is, shellCommentStart decides.
//   - .md — outside fenced code blocks, which are the markdown equivalent of a
//     string literal: a pasted compiler error or `go test` output is a transcript,
//     not a claim.
func scanProse(t *testing.T, root string, rels []string) proseScan {
	t.Helper()
	var scan proseScan
	record := func(rel string, line int, text string) {
		scan.hits = append(scan.hits, proseHit{rel: rel, line: line, text: strings.TrimSpace(text)})
	}
	// Reading is collected rather than asserted for the same reason parsing is:
	// a require here would unwind the whole scan through runtime.Goexit, so one
	// unreadable file would hide every violation below it and in every file after
	// it. Both halves of this loop leave the verdict to the caller.
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
			for i, line := range body {
				hash := shellCommentStart(line)
				if hash < 0 {
					continue
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
			fence := ""
			for i, line := range body {
				if marker := fenceMarker(line); marker != "" {
					switch {
					case fence == "":
						fence = marker
					case marker[0] == fence[0] && len(marker) >= len(fence):
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

// fenceMarker returns the fence delimiter opening or closing line, or "". Both
// spellings count, and the run length is returned rather than discarded so a
// nested block closes at the right depth: a ```` fence containing ``` must not be
// closed by the inner one. Getting either wrong is silent in the direction that
// matters — an unclosed fence blanks every line after it, and the file still
// looks scanned.
func fenceMarker(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, ch := range []byte{'`', '~'} {
		n := 0
		for n < len(trimmed) && trimmed[n] == ch {
			n++
		}
		if n >= 3 {
			return trimmed[:n]
		}
	}
	return ""
}

// shellCommentStart returns the index of the `#` that opens a comment on line, or
// -1. A `#` opens one only when it is unquoted and starts a word, which is what
// keeps `${#arr}`, `$#`, a URL fragment and a quoted "#1" out. Taking the first
// `#` on the line instead treated everything after a quoted one as a comment, so
// a line whose only path-and-number sat in a quoted argument was reported as
// prose. TestProseCitationScannerReportsAPlantedCitation plants those four
// shapes; this rule is what keeps them silent.
//
// Word-start is approximated as "preceded by whitespace", which under-detects
// rather than over-detects: `foo;#c` is a comment to the shell and prose to this.
// A guard that turns CI red for correct code is worse than one that misses a
// spelling no script here uses.
func shellCommentStart(line string) int {
	var quote byte
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
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return i
		}
	}
	return -1
}

// trackedProse lists the durable prose files in the index, minus the historical
// plans and specs. The list comes from git rather than a directory walk because
// "shipped" is exactly what this test means and a walk cannot express it — the
// walk picks up whatever is lying in the tree, including a review's own scratch
// findings file, which is by contract a list of paths and line numbers.
// TestShellScriptsParse reads the index for the same reason.
//
// docs/superpowers is the only exemption, and it is one both callers want: a plan
// describes the tree it was written against, and naming the exact line to edit is
// a plan step's job. web/ was exempt here for one release and is not any more. It
// holds a single tracked prose file, which carries no citation, so the exemption
// bought nothing and cost the log-path guard a README that documents a path.
func trackedProse(t *testing.T, root string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--", "*.go", "*.md", "*.sh").Output()
	require.NoError(t, err, "git ls-files failed; this test reads the index, not the working tree")

	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || rel == historicalDocs || strings.HasPrefix(rel, historicalDocs+"/") {
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
	scan := scanProse(t, root, rels)

	for _, hit := range scan.hits {
		t.Errorf("%s:%d cites a position; cite the symbol instead, which survives an edit:\n\t%s",
			hit.rel, hit.line, hit.text)
	}

	// A scan that reached nothing passes exactly as happily as a clean tree, so a
	// broken pathspec or an over-eager exemption would turn this guard off rather
	// than fail it. Assert each surface separately, since a single total would let
	// a whole extension go dark, and assert the units the scan can lose silently:
	// comments rather than .go files, because a file that parses and yields none
	// is the same silence one level down, and markdown lines rather than .md
	// files, because an unclosed fence blanks a file the count still counts.
	require.NotEmpty(t, rels, "git ls-files matched no prose; the pathspec is broken, not the tree")
	require.Positive(t, scan.comments, "parsed no comments; ParseComments is not doing its job")
	require.Positive(t, scan.mdFiles, "scanned no .md files; the exemptions are wrong")
	require.Positive(t, scan.mdLines, "scanned no .md prose lines; every file is fenced shut")
	require.Positive(t, scan.shFiles, "scanned no .sh files; the exemptions are wrong")

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
		"control.go": "package control\n" +
			"\n" +
			"// A claim about session/instance.go:2228 in a comment.\n" +
			"const parsedFromAgentOutput = \"ui/terminal.go:412\"\n" +
			"\n" +
			"// Prose that names CLAUDE.md: 3 rules is not a citation.\n",
		// Lines 2-4 are comments and must be reported. Lines 5-8 each carry the
		// same citation shape in code, and each is silent for a different reason:
		// no `#` at all, a `#` inside double quotes, a `#` opening `${#…}`, and a
		// `#` opening `$#`. Only the first is caught by "the line has no comment";
		// the rest need shellCommentStart to be doing its job.
		"control.sh": "#!/usr/bin/env bash\n" +
			"# A whole-line comment naming config/accessors.go:251.\n" +
			"PANE=\"\"  # a trailing comment naming session/tmux/tmux.go:512\n" +
			"\tprintf 'x'  # an indented trailing comment naming ui/list.go:85\n" +
			"grep -oE 'x' \"app/app.go:99\"\n" +
			"echo \"#1\" && grep -n 'x' \"app/app.go:99\"\n" +
			"printf '%s' ${#PANE} \"app/app.go:99\"\n" +
			"[ $# -gt 0 ] && echo \"app/app.go:99\"\n",
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

	scan := scanProse(t, dir, rels)

	byFile := map[string][]int{}
	for _, hit := range scan.hits {
		byFile[hit.rel] = append(byFile[hit.rel], hit.line)
	}
	assert.Equal(t, []int{3}, byFile["control.go"],
		"want the comment on line 3, not the string literal on line 4 and not the spaced colon on line 6")
	assert.Equal(t, []int{2, 3, 4}, byFile["control.sh"],
		"want the whole-line comment and both trailing comments, indented or not, and none of the "+
			"four code lines below them")
	assert.Equal(t, []int{1, 7}, byFile["control.md"],
		"want the prose either side of the fence and not the transcript inside it")
	assert.Equal(t, []int{4}, byFile["tilde.md"], "a ~~~ fence must fence")
	assert.Equal(t, []int{6}, byFile["nested.md"], "the inner ``` must not close the outer ````")
	assert.Equal(t, []int{1}, byFile["unbalanced.md"], "the prose before the unclosed fence still counts")

	require.Equal(t, 1, scan.goFiles, "the .go arm did not run, or counted the file it could not parse")
	require.Equal(t, 1, scan.shFiles, "the .sh arm did not run")
	require.Equal(t, 4, scan.mdFiles, "the .md arm did not run over every markdown fixture")
	require.Positive(t, scan.comments, "the .go arm parsed no comments")
	require.Positive(t, scan.mdLines, "the .md arm scanned no unfenced lines")

	// The unparseable file must be reported and must not have stopped the scan —
	// the assertions above already prove the latter, since every other surface was
	// still reached. The unclosed fence is the same contract one surface over.
	require.Equal(t, []string{"broken.go"}, scan.unparsed)
	require.Equal(t, []string{"unbalanced.md"}, scan.unbalanced)
	require.ElementsMatch(t, []string{"missing.md", "missing.sh"}, scan.unreadable)
}

// readFile is moduleFile's absolute-path form, for callers that already hold one.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const (
	// realTmuxSkip names the real-tmux test the workflows decline to run.
	realTmuxSkip = "TestSessionDeathStopsProbing"
	// realTmuxRuns names the real-tmux test they must NOT decline. Its guarantee
	// is an absence, which is the half nothing else would notice going missing.
	realTmuxRuns = "TestCloseSucceedsAfterRebindFromCancelledContext"
	// realTmuxPkg is the tree both names must resolve in.
	realTmuxPkg = "session/tmux"
)

// workflowGlobs are the trees TestEveryWorkflowGoTestSkipsTheRealTmuxTest reads.
// GitHub honours both spellings of the extension, and filepath.Glob does not
// recurse, so a composite action needs its own entry rather than being reached
// through the first. None of the three is hypothetical in the same way: the repo
// has nine .yml workflows, no .yaml one and no composite action today, and the
// two empty globs are here because adding such a file must not silently widen
// what CI runs.
var workflowGlobs = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
	".github/actions/*/action.y*ml",
}

var (
	// suiteInvocation matches a command that runs the Go suite. The `just`
	// recipes are in here because each is a bare `go test ./...` with no -skip —
	// `test` and `cover` directly, `ci` because it runs both — so a workflow step
	// calling one would run the real-tmux test in CI without this guard ever
	// counting the line.
	suiteInvocation = regexp.MustCompile(`\bgo test\b|\bjust (?:test|cover|ci)\b`)
	// skipsRealTmux accepts every spelling of the flag that actually skips it,
	// including `-skip=NAME` and a name inside an alternation. `-run NAME` is the
	// inverse and must not satisfy it.
	skipsRealTmux = regexp.MustCompile(`-skip[= ]\S*` + regexp.QuoteMeta(realTmuxSkip))
	// skipsRealTmuxRuns is the same test applied to the name that must not be
	// skipped. A bare substring check stood here and could not tell skipping the
	// test from running only it, so it failed `-run realTmuxRuns` with a message
	// saying that line skipped it.
	skipsRealTmuxRuns = regexp.MustCompile(`-skip[= ]\S*` + regexp.QuoteMeta(realTmuxRuns))
	// commandSeparator ends one command and begins another.
	commandSeparator = regexp.MustCompile(`&&|\|\||[;|]`)
)

// suiteCommands returns the commands on a workflow line that run the Go suite.
//
// Splitting first is what makes the assertions about a *command* rather than a
// line. Matching the raw line let a -skip anywhere on it vouch for an invocation
// that had none: in a trailing comment, or on a second command after `&&`. The
// whole-line comment case was already handled and the trailing one was not, which
// is the gap the .sh arm was rewritten to close one screen up.
func suiteCommands(line string) []string {
	if i := shellCommentStart(line); i >= 0 {
		line = line[:i]
	}
	var out []string
	for _, seg := range commandSeparator.Split(line, -1) {
		if suiteInvocation.MatchString(seg) {
			out = append(out, seg)
		}
	}
	return out
}

// TestWorkflowLineClassifiersRejectTheLinesTheyMust is the control for the
// classifiers below. All are used through assertions that pass on a match, so on
// a tree where every workflow is already correct they are green whether or not
// they discriminate: widening any of them to match everything is invisible. These
// are the inputs each one has to turn away.
func TestWorkflowLineClassifiersRejectTheLinesTheyMust(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"        run: go test -skip Foo ./...", 1},
		{"        run: just test", 1},
		{"        run: just cover", 1},
		{"        run: just ci", 1},
		{"          run: go build ./... && go vet ./...", 0},
		{"  # we used to run go test here, before the split", 0},
		{"          # A job that runs go test must skip the real-tmux one", 0},
		// A trailing comment is not a command, and a second command is not the
		// first one. Both used to be counted as part of the line that precedes
		// them, which let either vouch for the other.
		{"        run: go test ./...  # -skip lives in the script now", 1},
		{"        run: go test ./... && go test -skip Foo ./session/tmux/...", 2},
	} {
		assert.Lenf(t, suiteCommands(tc.line), tc.want, "suiteCommands(%q)", tc.line)
	}

	for _, tc := range []struct {
		line string
		want bool
	}{
		{"go test -skip " + realTmuxSkip + " ./...", true},
		{"go test -skip=" + realTmuxSkip + " ./...", true},
		{"go test -skip 'TestFoo|" + realTmuxSkip + "' ./...", true},
		{"go test ./...", false},
		{"go test -skip TestSomethingElse ./...", false},
		// -run is the inverse of -skip: this invocation runs ONLY the real-tmux
		// test. Accepting it because the name is present would invert the guard.
		{"go test -run " + realTmuxSkip + " ./...", false},
		{"go test -skip=TestFoo -run " + realTmuxSkip + " ./...", false},
	} {
		assert.Equalf(t, tc.want, skipsRealTmux.MatchString(tc.line), "skipsRealTmux(%q)", tc.line)
	}

	for _, tc := range []struct {
		line string
		want bool
	}{
		{"go test -skip " + realTmuxRuns + " ./...", true},
		{"go test -skip=TestFoo -skip=" + realTmuxRuns + " ./...", true},
		// Runs only that test. Naming it is not skipping it, and the old check
		// could not tell the two apart.
		{"go test -run " + realTmuxRuns + " ./...", false},
		{"go test -skip " + realTmuxSkip + " -run " + realTmuxRuns + " ./session/tmux/...", false},
	} {
		assert.Equalf(t, tc.want, skipsRealTmuxRuns.MatchString(tc.line), "skipsRealTmuxRuns(%q)", tc.line)
	}

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
	} {
		assert.Equalf(t, tc.want, shellCommentStart(tc.line), "shellCommentStart(%q)", tc.line)
	}
}

// TestTheRealTmuxTestNamesResolve pins realTmuxSkip and realTmuxRuns to functions
// that exist. Both are bare strings compared against workflow text, so renaming
// either test leaves the workflows passing a -skip that matches nothing, the
// real-tmux test running in all three CI jobs, and the guard below green — the two
// strings still agree with each other, and agreeing with each other is all it
// checks. TestNoDocClaimsTheLogLivesInTheTempDir asks the log package for its real
// filename against the same failure: a scanner looking for a string nothing
// produces any more passes every time.
func TestTheRealTmuxTestNamesResolve(t *testing.T) {
	root := moduleRoot(t)
	var pkg []string
	for _, rel := range trackedProse(t, root) {
		if filepath.Ext(rel) == ".go" && strings.HasPrefix(rel, realTmuxPkg+"/") {
			pkg = append(pkg, rel)
		}
	}
	require.NotEmpty(t, pkg, "found no .go files under %s", realTmuxPkg)

	for _, name := range []string{realTmuxSkip, realTmuxRuns} {
		found := ""
		for _, rel := range pkg {
			if strings.Contains(readFile(t, filepath.Join(root, filepath.FromSlash(rel))), "func "+name+"(") {
				found = rel
				break
			}
		}
		require.NotEmptyf(t, found,
			"%s declares no func %s. The workflows pass that name to -skip and this package's "+
				"comments describe what it does; a rename that misses either leaves both saying "+
				"something about a test that no longer exists.", realTmuxPkg, name)
	}
}

// TestEveryWorkflowGoTestSkipsTheRealTmuxTest holds the claim session/tmux's
// comments make about CI: TestSessionDeathStopsProbing is skipped by name in every
// workflow command that runs the suite, and TestCloseSucceedsAfterRebindFromCancelledContext
// is not, so the workflows' -skip is not what keeps the second one off a real tmux
// server.
//
// That claim used to be carried by a pair of line numbers into a workflow file.
// One of them was wrong on the day it merged, and the pair also under-counted the
// skip sites it was pointing at. A sentence naming positions in another file is a
// lookup nobody performs twice; the file is right here, so read it.
func TestEveryWorkflowGoTestSkipsTheRealTmuxTest(t *testing.T) {
	cmds := workflowSuiteCommands(t, moduleRoot(t))

	// Without this a renamed job, a moved workflow or a reindented `run:` turns the
	// loop below into a no-op and its assertions never execute.
	require.NotEmpty(t, cmds, "found no suite invocation in %v", workflowGlobs)

	for _, c := range cmds {
		assert.Regexpf(t, skipsRealTmux, c.cmd,
			"%s:%d runs the suite without skipping %s. That test drives a real tmux server "+
				"through a PTY and is local-only; every other command skips it, and "+
				"session/tmux's comments say so.", c.rel, c.line, realTmuxSkip)
		assert.NotRegexpf(t, skipsRealTmuxRuns, c.cmd,
			"%s:%d skips %s. session/tmux documents it as a real-tmux test that DOES run "+
				"in CI under ATRIUM_CI_REQUIRE_TMUX=1; skip it here and the documented "+
				"arrangement is no longer true.", c.rel, c.line, realTmuxRuns)
	}
}

// workflowCommand is one suite invocation, located well enough to fix.
type workflowCommand struct {
	rel  string
	line int
	cmd  string
}

// workflowSuiteCommands returns every command under root that runs the Go suite,
// across all of workflowGlobs. Split out from the assertions so a control can
// point it at a tree that is deliberately wrong; against the real tree the
// assertions are the whole test, and on a repo where every workflow is already
// correct that proves nothing about which files were read.
func workflowSuiteCommands(t *testing.T, root string) []workflowCommand {
	t.Helper()
	var out []workflowCommand
	for _, glob := range workflowGlobs {
		paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		require.NoError(t, err)
		for _, path := range paths {
			rel, relErr := filepath.Rel(root, path)
			require.NoError(t, relErr)
			for i, line := range strings.Split(readFile(t, path), "\n") {
				for _, cmd := range suiteCommands(line) {
					out = append(out, workflowCommand{rel: filepath.ToSlash(rel), line: i + 1, cmd: cmd})
				}
			}
		}
	}
	return out
}

// TestWorkflowScannerReachesEverySpelling is the control for workflowGlobs. The
// repo has nine .yml workflows, no .yaml one and no composite action, so two of
// the three globs match nothing here and the guard above is green whether they
// work or not — which is how a `.yaml` workflow could later add an unskipped
// invocation that nothing reports.
func TestWorkflowScannerReachesEverySpelling(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".github/workflows/a.yml":          "        run: go test -skip " + realTmuxSkip + " ./...\n",
		".github/workflows/b.yaml":         "        run: go test ./...\n",
		".github/actions/setup/action.yml": "        run: just ci\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}

	var seen []string
	unskipped := 0
	for _, c := range workflowSuiteCommands(t, root) {
		seen = append(seen, c.rel)
		if !skipsRealTmux.MatchString(c.cmd) {
			unskipped++
		}
	}
	assert.ElementsMatch(t, []string{
		".github/workflows/a.yml",
		".github/workflows/b.yaml",
		".github/actions/setup/action.yml",
	}, seen, "every spelling GitHub honours must be reached, or one can hide an invocation")
	assert.Equal(t, 2, unskipped, "the .yaml workflow and the composite action must both be judged, not just listed")
}
