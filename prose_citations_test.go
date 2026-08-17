package main

import (
	"context"
	"go/ast"
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
//
// The set spans both halves of the repo. web/ is a Next.js site with its own
// toolchain, but a line number pointed into one of its sources rots exactly as a
// line number pointed into a Go file does — and since web/ stopped being exempt
// from the scan, leaving its extensions out would have covered the prose in that
// directory without covering references into it. The planted comment in
// TestProseCitationScannerReportsAPlantedCitation is what holds that.
var positionalCitation = regexp.MustCompile(
	`[A-Za-z0-9_./-]+\.(?:go|md|yml|yaml|sh|json|toml|tmpl|ts|tsx|css|mjs):[0-9]+`)

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
//     not a claim. An indented block is one too, so four spaces after a blank line
//     opens the same exemption. That rule is coarser than CommonMark, which reads
//     the indent relative to an enclosing list item: a paragraph indented under a
//     bullet goes unscanned here. Missing a line is the direction to err in — a
//     guard that turns CI red for a correctly written file costs more than one
//     that stays quiet about a citation nobody has written yet.
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
				scan.shComments++
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
// syntax rather than data: outside single and double quotes, and not escaped.
// visit returns false to stop the walk.
//
// shellCommentStart and splitCommands both hang off it so the two cannot come to
// disagree about what "quoted" means — which they did, back when the splitter was
// a regexp that had never heard of quoting.
func eachUnquoted(line string, visit func(i int, c byte) bool) {
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
		default:
			if !visit(i, c) {
				return
			}
		}
	}
}

// shellCommentStart returns the index of the `#` that opens a comment on line, or
// -1. A `#` opens one only when it is unquoted and starts a word, which is what
// keeps `${#arr}`, `$#`, a URL fragment and a quoted "#1" out. Taking the first
// `#` on the line instead treated everything after a quoted one as a comment, so
// a line whose only path-and-number sat in a quoted argument was reported as
// prose. The shapes are inputs in TestWorkflowLineClassifiersRejectTheLinesTheyMust
// and plants in TestProseCitationScannerReportsAPlantedCitation, which carries the
// ones that have to stay silent while a citation sits on the same line.
//
// Word-start is approximated as "preceded by whitespace", which under-detects
// rather than over-detects: `foo;#c` is a comment to the shell and prose to this.
// A guard that turns CI red for correct code is worse than one that misses a
// spelling no script here uses.
func shellCommentStart(line string) int {
	start := -1
	eachUnquoted(line, func(i int, c byte) bool {
		if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			start = i
			return false
		}
		return true
	})
	return start
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
	eachUnquoted(line, func(i int, c byte) bool {
		if c == ';' || c == '&' || c == '|' {
			out = append(out, line[start:i])
			start = i + 1
		}
		return true
	})
	return append(out, line[start:])
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
// the Go tooling otherwise ignores. It holds a single tracked prose file, which
// carries no citation, so exempting it buys nothing here and costs the log-path
// guard a README that documents a path — this list is shared.
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
	scan := scanProse(t, root, rels)

	for _, hit := range scan.hits {
		t.Errorf("%s:%d cites a position; cite the symbol instead, which survives an edit:\n\t%s",
			hit.rel, hit.line, hit.text)
	}

	// A scan that reached nothing passes exactly as happily as a clean tree, so a
	// broken pathspec or an over-eager exemption would turn this guard off rather
	// than fail it. Assert each surface separately, since a single total would let
	// a whole extension go dark, and assert the unit each one can lose silently
	// rather than the file that holds it: a .go file parses and yields no comments,
	// an unclosed fence blanks a .md file the count still counts, and a
	// shellCommentStart that found nothing anywhere leaves every .sh file scanned
	// and unread.
	require.NotEmpty(t, rels, "git ls-files matched no prose; the pathspec is broken, not the tree")
	require.Positive(t, scan.comments, "parsed no comments; ParseComments is not doing its job")
	require.Positive(t, scan.mdFiles, "scanned no .md files; the exemptions are wrong")
	require.Positive(t, scan.mdLines, "scanned no .md prose lines; every file is fenced shut")
	require.Positive(t, scan.shFiles, "scanned no .sh files; the exemptions are wrong")
	require.Positive(t, scan.shComments, "found no shell comments; shellCommentStart is returning -1 everywhere")

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
			"// Prose that names CLAUDE.md: 3 rules is not a citation.\n" +
			"\n" +
			"// A claim about web/src/app/page.tsx:42, the other half of the repo.\n",
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
		// delimits it, so only the four spaces and the blank line above say it is
		// not prose.
		"indented.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"    main.go:12:5: undefined: foo\n" +
			"\n" +
			"More prose naming ui/list.go:85.\n",
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
	assert.Equal(t, []int{3, 8}, byFile["control.go"],
		"want both comments, including the one citing a web/ source, and neither the string "+
			"literal below the first nor the spaced colon between them")
	assert.Equal(t, []int{2, 3, 4}, byFile["control.sh"],
		"want the whole-line comment and both trailing comments, indented or not, and none of the "+
			"four code lines below them")
	assert.Equal(t, []int{1, 7}, byFile["control.md"],
		"want the prose either side of the fence and not the transcript inside it")
	assert.Equal(t, []int{4}, byFile["tilde.md"], "a ~~~ fence must fence")
	assert.Equal(t, []int{6}, byFile["nested.md"], "the inner ``` must not close the outer ````")
	assert.Equal(t, []int{1, 5}, byFile["indented.md"],
		"want the prose either side and not the indented transcript between them")
	assert.Equal(t, []int{1, 6}, byFile["infostring.md"],
		"want the prose either side of the block and not the fence it quotes; a closing fence carries no info string")
	assert.Equal(t, []int{1}, byFile["unbalanced.md"], "the prose before the unclosed fence still counts")

	require.Equal(t, 1, scan.goFiles, "the .go arm did not run, or counted the file it could not parse")
	require.Equal(t, 1, scan.shFiles, "the .sh arm did not run")
	require.Equal(t, 4, scan.shComments, "the shebang and the three comments above are what shellCommentStart must find")
	require.Equal(t, 6, scan.mdFiles, "the .md arm did not run over every markdown fixture")
	require.Positive(t, scan.comments, "the .go arm parsed no comments")
	require.Positive(t, scan.mdLines, "the .md arm scanned no unfenced lines")

	// The unparseable file must be reported and must not have stopped the scan —
	// the assertions above already prove the latter, since every other surface was
	// still reached. The unclosed fence is the same contract one surface over.
	require.Equal(t, []string{"broken.go"}, scan.unparsed)
	require.Equal(t, []string{"unbalanced.md"}, scan.unbalanced)
	require.ElementsMatch(t, []string{"missing.md", "missing.sh"}, scan.unreadable)
}

// readFile reads a whole file at an absolute path. moduleFile is the form that
// resolves a name against the module root, and is written in terms of this one.
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
// through the first. Only the .yml glob matches anything here today; the other two
// are for the day someone adds a file they cover, which must not silently widen
// what CI runs. TestWorkflowScannerReachesEverySpelling is what keeps them from
// being decoration until then.
//
// The composite-action entry is one directory deep under .github/actions, which is
// convention rather than a rule: `uses:` resolves a local action against any path
// in the repo, so an action elsewhere in the tree is out of scope here and its
// `run:` steps go unread.
var workflowGlobs = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
	".github/actions/*/action.y*ml",
}

var (
	// suiteInvocation matches a command that runs the Go suite. The `just` recipes
	// are in here because each runs `go test` with no -skip, so a workflow step
	// calling one would run the real-tmux test in CI without this guard ever
	// counting the line. Which recipes those are is not this comment's to say:
	// TestSuiteInvocationCoversEveryJustRecipe reads the justfile and holds this
	// pattern to it, so adding a recipe that runs the suite fails there rather than
	// opening a hole here.
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
)

// suiteCommands returns the commands on a workflow line that run the Go suite.
//
// Splitting first is what makes the assertions about a *command* rather than a
// line. Matching the raw line let a -skip anywhere on it vouch for an invocation
// that had none: in a trailing comment, or on a second command after `&&`. The
// whole-line comment case was already handled and the trailing one was not, which
// is the gap shellCommentStart closes.
func suiteCommands(line string) []string {
	if i := shellCommentStart(line); i >= 0 {
		line = line[:i]
	}
	var out []string
	for _, seg := range splitCommands(line) {
		if suiteInvocation.MatchString(seg) {
			out = append(out, seg)
		}
	}
	return out
}

// TestWorkflowLineClassifiersRejectTheLinesTheyMust is the control for the
// classifiers below. On a tree where every workflow is already correct each one is
// green whether or not it discriminates, so this supplies the inputs each has to
// turn away. Which direction goes unnoticed depends on how a classifier is used,
// and they are not all used the same way: skipsRealTmux is asserted to match, so
// widening it to everything is the invisible break, while skipsRealTmuxRuns is
// asserted NOT to match, so its invisible break is narrowing to nothing. Both
// directions are covered below for both.
func TestWorkflowLineClassifiersRejectTheLinesTheyMust(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"        run: go test -skip Foo ./...", 1},
		{"        run: just test", 1},
		{"        run: just test-race", 1},
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
		// A quoted separator separates nothing. The alternation is the spelling
		// skipsRealTmux exists to accept, and splitting it left half a flag.
		{"        run: go test -skip 'TestFoo|" + realTmuxSkip + "' ./...", 1},
		{"        run: go test ./... | tee out.log", 1},
	} {
		assert.Lenf(t, suiteCommands(tc.line), tc.want, "suiteCommands(%q)", tc.line)
	}

	// The predicates are only ever applied to what suiteCommands returns, so an
	// input the table above blesses is not yet proof: assert the composition. The
	// alternation passed skipsRealTmux as a raw line and failed it as a command,
	// and nothing here could see the difference until the two were run together.
	alternation := "        run: go test -skip 'TestFoo|" + realTmuxSkip + "' ./..."
	cmds := suiteCommands(alternation)
	require.Len(t, cmds, 1, "suiteCommands(%q)", alternation)
	assert.Regexp(t, skipsRealTmux, cmds[0],
		"the alternation must survive the split, or the guard fails a workflow that does skip the test")

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
// that exist. Both are bare strings compared against workflow text, and the guard
// below stays green after either is renamed — the two strings still agree with
// each other, and agreeing with each other is all it checks. What the rename costs
// differs: realTmuxSkip leaves the workflows passing a -skip that matches nothing,
// so the real-tmux test runs in CI wherever the suite does, while realTmuxRuns
// leaves the assertion that nothing skips it looking for a name nothing could
// name. TestNoDocClaimsTheLogLivesInTheTempDir asks the log package for its real
// filename against the same failure: a scanner looking for a string nothing
// produces any more passes every time.
func TestTheRealTmuxTestNamesResolve(t *testing.T) {
	root := moduleRoot(t)
	pkg := trackedFiles(t, root, realTmuxPkg+"/*.go")
	require.NotEmpty(t, pkg, "found no .go files under %s", realTmuxPkg)

	// Parsed rather than grepped for "func Name(". A commented-out declaration left
	// behind by a refactor satisfies a substring search, which would be this test
	// failing in exactly the way it exists to prevent.
	declared := map[string]bool{}
	fset := token.NewFileSet()
	for _, rel := range pkg {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		require.NoErrorf(t, err, "%s does not parse", rel)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				declared[fn.Name.Name] = true
			}
		}
	}

	for _, name := range []string{realTmuxSkip, realTmuxRuns} {
		require.Truef(t, declared[name],
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
				"through a PTY and is local-only, and session/tmux's comments say every "+
				"workflow command that runs the suite skips it.", c.rel, c.line, realTmuxSkip)
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

// scriptLine is one line of shell from a `run:` step, and the workflow line it
// came from.
type scriptLine struct {
	line int
	text string
}

// runKey matches the `run:` mapping key, capturing its indent and its value. The
// indent is what bounds a block scalar: everything indented past the key belongs
// to the script, and the first line that is not ends it.
var runKey = regexp.MustCompile(`^(\s*)(?:-\s+)?run:\s*(.*)$`)

// runScript returns the shell of every `run:` step in a workflow file. Only a
// `run:` value is shell; the rest of the document is YAML that merely quotes it.
// Judging whole lines meant a step named `Run go test with coverage`, an
// `if: contains(…, 'go test')` or an action's `description:` was read as an
// unskipped suite invocation, and the only way to satisfy the assertion would have
// been to write -skip into a step name.
//
// A `run:` value that opens a block scalar takes the indented lines below it, and
// a trailing backslash joins a line to the next — a wrapped invocation is one
// command, not a head with no -skip followed by a tail that matches nothing.
func runScript(body []string) []scriptLine {
	var out, current []scriptLine
	blockIndent := -1
	end := func() {
		out = append(out, joinContinuations(current)...)
		current, blockIndent = nil, -1
	}
	for i, line := range body {
		if blockIndent >= 0 {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if indentOf(line) > blockIndent {
				current = append(current, scriptLine{line: i + 1, text: line})
				continue
			}
			end()
		}
		m := runKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// `|`, `>` and their chomping variants all open a block; so does an empty
		// value, which is the same step spelled over two lines.
		if v := strings.TrimSpace(m[2]); v == "" || v[0] == '|' || v[0] == '>' {
			blockIndent = len(m[1])
			continue
		}
		out = append(out, scriptLine{line: i + 1, text: m[2]})
	}
	end()
	return out
}

// joinContinuations folds a line ending in a backslash into the one after it,
// keeping the first line's number so a failure points at the head of the command.
func joinContinuations(lines []scriptLine) []scriptLine {
	var out []scriptLine
	for _, l := range lines {
		if k := len(out) - 1; k >= 0 {
			if head := strings.TrimRight(out[k].text, " \t"); strings.HasSuffix(head, `\`) {
				out[k].text = strings.TrimSuffix(head, `\`) + " " + strings.TrimSpace(l.text)
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// indentOf returns the width of line's leading whitespace in bytes.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
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
			for _, sl := range runScript(strings.Split(readFile(t, path), "\n")) {
				for _, cmd := range suiteCommands(sl.text) {
					out = append(out, workflowCommand{rel: filepath.ToSlash(rel), line: sl.line, cmd: cmd})
				}
			}
		}
	}
	return out
}

var (
	// justGoTest matches a recipe body line that runs `go test`, through the
	// justfile's `go` variable or directly.
	justGoTest = regexp.MustCompile(`(?:\{\{go\}\}|\bgo\b) test\b`)
	// justRunsNoTests matches the `-run '^$'` idiom, which is how `bench` runs
	// benchmarks without running a single test. A recipe spelled that way cannot
	// reach the real-tmux test, so it is not a suite recipe.
	justRunsNoTests = regexp.MustCompile(`-run[= ]'?\^\$'?`)
)

// justSuiteRecipes returns the names of the recipes in a justfile that run the Go
// suite, whether in their own body or through a dependency — `ci` runs no `go
// test` itself and reaches two recipes that do.
func justSuiteRecipes(justfile string) []string {
	type recipe struct {
		deps  []string
		suite bool
	}
	byName := map[string]*recipe{}
	var order []string
	current := ""
	for _, line := range strings.Split(justfile, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indentOf(line) > 0 {
			if r := byName[current]; r != nil && justGoTest.MatchString(line) && !justRunsNoTests.MatchString(line) {
				r.suite = true
			}
			continue
		}
		// Anything else at column 0 ends the previous recipe, whether or not it
		// starts a new one: a comment, an attribute, an assignment.
		current = ""
		colon := strings.Index(line, ":")
		if strings.HasPrefix(line, "#") || colon < 0 {
			continue
		}
		// `name := value` is an assignment, not a recipe. Nothing in the control
		// dies when this check is removed, and that is a property of justfiles
		// rather than a gap: a misread assignment has no indented body, so it can
		// never be marked a suite recipe. It stays because a name that collided
		// with a real recipe would overwrite that recipe's dependencies.
		if colon+1 < len(line) && line[colon+1] == '=' {
			continue
		}
		head := strings.Fields(line[:colon])
		if len(head) == 0 {
			continue
		}
		current = head[0]
		if byName[current] == nil {
			byName[current] = &recipe{}
			order = append(order, current)
		}
		byName[current].deps = strings.Fields(line[colon+1:])
	}

	for changed := true; changed; {
		changed = false
		for _, r := range byName {
			if r.suite {
				continue
			}
			for _, dep := range r.deps {
				if d := byName[dep]; d != nil && d.suite {
					r.suite, changed = true, true
					break
				}
			}
		}
	}

	var out []string
	for _, name := range order {
		if byName[name].suite {
			out = append(out, name)
		}
	}
	return out
}

// TestSuiteInvocationCoversEveryJustRecipe holds suiteInvocation to the justfile.
//
// The `just` half of that pattern is a hardcoded list of recipe names, and nothing
// in the workflows exercises it: every CI step calls `go test` directly, so the
// alternatives match nothing in this repo and a wrong list is invisible. Add a
// recipe that runs the suite, or rename one that does, and a workflow step calling
// it runs the real-tmux PTY test in CI while this guard counts no line at all —
// the precise hole the list exists to close.
func TestSuiteInvocationCoversEveryJustRecipe(t *testing.T) {
	recipes := justSuiteRecipes(readFile(t, filepath.Join(moduleRoot(t), "justfile")))
	require.NotEmpty(t, recipes, "parsed no suite recipes out of the justfile; the parser is broken, not the file")

	for _, name := range recipes {
		assert.Regexpf(t, suiteInvocation, "just "+name,
			"`just %s` runs the Go suite, but suiteInvocation does not match it. A workflow step "+
				"calling it would run %s in CI and TestEveryWorkflowGoTestSkipsTheRealTmuxTest "+
				"would never see the line.", name, realTmuxSkip)
	}
}

// TestJustfileParserFindsRecipesThroughDependencies is the control for
// justSuiteRecipes. Against the real justfile it returns a set that happens to be
// right, and a parser that found recipes by any other rule — or found none at all
// past the first assignment — would return the same set or an empty one, which the
// caller's assertions cannot tell apart from correct.
func TestJustfileParserFindsRecipesThroughDependencies(t *testing.T) {
	assert.Equal(t, []string{"test", "cover", "ci"}, justSuiteRecipes(strings.Join([]string{
		`go := env_var_or_default("GO", "go")`,
		`set shell := ["bash", "-uc"]`,
		"",
		"# Run the suite.",
		"test:",
		"    {{go}} test ./...",
		"",
		"cover:",
		"    {{go}} test -coverprofile=coverage.out ./...",
		"    {{go}} tool cover -func=coverage.out",
		"",
		"# Benchmarks run no tests, so they cannot reach the real-tmux one.",
		"bench pattern='.' *pkgs='./...':",
		"    {{go}} test -run '^$' -bench '{{pattern}}' {{pkgs}}",
		"",
		"lint:",
		"    golangci-lint run",
		"",
		"ci: build vet lint test cover",
	}, "\n")), "want the two recipes that run tests and the aggregate that depends on them, "+
		"and not bench, whose -run '^$' runs none")
}

// TestWorkflowScannerReadsOnlyRunSteps is the control for runScript. Every real
// workflow here puts its suite invocation on a single `run:` line with the -skip
// already there, so the scanner is green whether or not it can tell shell from
// YAML — and the failure it hides is a false red on a correct file, which is worse
// than a miss because the only way to clear it is to write a test flag into a step
// name.
func TestWorkflowScannerReadsOnlyRunSteps(t *testing.T) {
	body := strings.Split(strings.Join([]string{
		"jobs:",
		"  test:",
		"    steps:",
		// Prose about the suite, in three places YAML allows it. None is shell.
		"      - name: Run go test with coverage",
		"        if: contains(github.event.head_commit.message, 'go test')",
		"        run: go test -skip " + realTmuxSkip + " ./...",
		"      - name: Wrapped",
		"        run: |",
		"          go test \\",
		"            -skip " + realTmuxSkip + " ./...",
		"          echo done",
		// Back out to step level: the block is over, and this key is not shell.
		"      - name: After the block, still go test in the name",
		"        uses: ./.github/actions/setup",
	}, "\n"), "\n")

	var got []scriptLine
	for _, sl := range runScript(body) {
		if len(suiteCommands(sl.text)) > 0 {
			got = append(got, sl)
		}
	}
	require.Len(t, got, 2, "want the two run: invocations and neither the names nor the if:, got %+v", got)
	assert.Equal(t, 6, got[0].line, "the single-line run: step")
	assert.Regexp(t, skipsRealTmux, got[0].text)
	assert.Equal(t, 9, got[1].line, "a wrapped command is reported at its head, not its tail")
	assert.Regexpf(t, skipsRealTmux, got[1].text,
		"the backslash must join the flag to the invocation, or the head reads as unskipped")
}

// TestWorkflowScannerReachesEverySpelling is the control for workflowGlobs. Two of
// the three match nothing in this repo, so the guard above is green whether they
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
	}, seen, "both extensions must be reached, or one of them can hide an invocation")
	assert.Equal(t, 2, unskipped, "the .yaml workflow and the composite action must both be judged, not just listed")
}
