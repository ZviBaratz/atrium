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
var positionalCitation = regexp.MustCompile(`[A-Za-z0-9_./-]+\.(?:go|md|yml|yaml|sh|json|toml|tmpl)\s*:\s*[0-9]+`)

// exemptPrefixes names the trees this guard does not read. Two trees, two
// unrelated reasons, so neither generalises to a third: plans and specs describe
// the tree they were written against rather than this one, and naming the exact
// line to edit is a plan step's job; web/ is a standalone Next.js site with its
// own toolchain, excluded from every other Go-side check here for the same reason.
//
// Nothing else is exempt, deliberately. .github/release-notes was considered and
// left in: it carries no positional citation today, and an exemption granted
// against a hypothetical is one nobody can later tell from a live rule.
var exemptPrefixes = []string{
	historicalDocs,
	"web",
}

// proseHit is one positional citation, located well enough to fix.
type proseHit struct {
	rel  string
	line int
	text string
}

// proseScan is what a scan saw, so a caller can prove the scan happened rather
// than infer it from an empty result. A clean tree and a scanner that matched
// nothing produce identical hits; only these counts tell them apart.
type proseScan struct {
	hits []proseHit
	// unparsed collects .go files go/parser could not read, rather than failing
	// on the first one: a scan that stopped early would report a partial result
	// that is indistinguishable from a clean tree.
	unparsed []string
	goFiles  int
	comments int
	mdFiles  int
	shFiles  int
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
//   - .sh — from the first `#` to end of line, so a trailing comment counts. A
//     script's code may name a position because it is parsing one.
//   - .md — outside fenced code blocks, which are the markdown equivalent of a
//     string literal: a pasted compiler error or `go test` output is a transcript,
//     not a claim.
func scanProse(t *testing.T, root string, rels []string) proseScan {
	t.Helper()
	var scan proseScan
	record := func(rel string, line int, text string) {
		scan.hits = append(scan.hits, proseHit{rel: rel, line: line, text: strings.TrimSpace(text)})
	}

	for _, rel := range rels {
		path := filepath.Join(root, filepath.FromSlash(rel))
		switch filepath.Ext(rel) {
		case ".go":
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if parseErr != nil {
				// Collected, not asserted here: a require inside this loop would
				// unwind the whole scan through runtime.Goexit, so one
				// unparseable file would hide every violation below it and in
				// every .md and .sh after it. The caller decides.
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
			scan.shFiles++
			for i, line := range strings.Split(readFile(t, path), "\n") {
				hash := strings.Index(line, "#")
				if hash < 0 {
					continue
				}
				if positionalCitation.MatchString(line[hash:]) {
					record(rel, i+1, line)
				}
			}
		case ".md":
			scan.mdFiles++
			fenced := false
			for i, line := range strings.Split(readFile(t, path), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "```") {
					fenced = !fenced
					continue
				}
				if !fenced && positionalCitation.MatchString(line) {
					record(rel, i+1, line)
				}
			}
		}
	}
	return scan
}

// trackedProse lists the durable prose files in the index, minus the exempt
// trees. The list comes from git rather than a directory walk because "shipped"
// is exactly what this test means and a walk cannot express it — the walk picks
// up whatever is lying in the tree, including a review's own scratch findings
// file, which is by contract a list of paths and line numbers. TestShellScriptsParse
// reads the index for the same reason.
func trackedProse(t *testing.T, root string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--", "*.go", "*.md", "*.sh").Output()
	require.NoError(t, err, "git ls-files failed; this test reads the index, not the working tree")

	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || exempt(rel) {
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

// exempt reports whether rel sits under one of the exempt trees.
func exempt(rel string) bool {
	for _, prefix := range exemptPrefixes {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
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
// So the rule is that prose cites a symbol, and this enforces it. Cite the
// function, the const, the field: those move with the code, and a rename that
// strands one is a grep away from being found.
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
	// a whole extension go dark. Comments get their own check because a .go file
	// that parses and yields none is the same silence one level down.
	require.NotEmpty(t, rels, "git ls-files matched no prose; the pathspec is broken, not the tree")
	require.Positive(t, scan.comments, "parsed no comments; ParseComments is not doing its job")
	require.Positive(t, scan.mdFiles, "scanned no .md files; the exemptions are wrong")
	require.Positive(t, scan.shFiles, "scanned no .sh files; the exemptions are wrong")

	// Every listed .go file must have parsed. A file the scanner could not read
	// is one it reported nothing about, which on its own is indistinguishable
	// from a file with nothing to report.
	require.Empty(t, scan.unparsed, "these .go files did not parse, so they went unscanned")
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
// So plant one citation per surface and require each to be reported, and plant
// the near-misses each surface deliberately ignores and require their silence.
// Both directions matter: a scanner that reports nothing and a scanner that
// reports every string literal are both broken, and only one of them is loud.
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
			"const parsedFromAgentOutput = \"ui/terminal.go:412\"\n",
		"control.sh": "#!/usr/bin/env bash\n" +
			"# A whole-line comment naming config/accessors.go:251.\n" +
			"PANE=\"\"  # a trailing comment naming session/tmux/tmux.go:512\n" +
			"\tprintf 'x'  # an indented trailing comment naming ui/list.go:85\n" +
			"grep -oE 'x' \"app/app.go:99\"\n",
		"control.md": "Prose naming app/help.go:60.\n" +
			"\n" +
			"```\n" +
			"main.go:12:5: undefined: foo\n" +
			"```\n",
		// A file the parser must give up on, so the incompleteness the caller
		// checks for is a state this reaches rather than one it assumes.
		"broken.go": "package control\n\nfunc (\n",
	}
	var rels []string
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		rels = append(rels, name)
	}

	scan := scanProse(t, dir, rels)

	// One hit per surface: the Go comment, the two shell comments, the markdown
	// prose line. Anything else means a scoping rule moved.
	byFile := map[string][]int{}
	for _, hit := range scan.hits {
		byFile[hit.rel] = append(byFile[hit.rel], hit.line)
	}
	assert.Equal(t, []int{3}, byFile["control.go"],
		"want the comment on line 3 and not the string literal on line 4")
	assert.Equal(t, []int{2, 3, 4}, byFile["control.sh"],
		"want the whole-line comment and both trailing comments, indented or not, "+
			"and not the code on line 5")
	assert.Equal(t, []int{1}, byFile["control.md"],
		"want the prose line and not the transcript inside the fence")

	require.Equal(t, 1, scan.goFiles, "the .go arm did not run, or counted the file it could not parse")
	require.Equal(t, 1, scan.shFiles, "the .sh arm did not run")
	require.Equal(t, 1, scan.mdFiles, "the .md arm did not run")
	require.Positive(t, scan.comments, "the .go arm parsed no comments")

	// The unparseable file must be reported and must not have stopped the scan —
	// the assertions above already prove the latter, since every other surface
	// was still reached.
	require.Equal(t, []string{"broken.go"}, scan.unparsed)
}

// readFile is the byte-slurping half of moduleFile, for paths a caller already
// holds absolute.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const (
	// realTmuxSkip names the real-tmux test every workflow declines to run.
	realTmuxSkip = "TestSessionDeathStopsProbing"
	// realTmuxRuns names the real-tmux test they must NOT decline — the
	// non-obvious half, since its guarantee is an absence.
	realTmuxRuns = "TestCloseSucceedsAfterRebindFromCancelledContext"
	// workflowsGlob is the tree this reads, relative to the module root.
	workflowsGlob = ".github/workflows/*.yml"
)

var (
	// suiteInvocation matches a line that runs the Go suite. `just test` is in
	// here because the justfile recipe is a bare `go test ./...` with no -skip:
	// a workflow step that called it would run the real-tmux test in CI without
	// this guard ever counting the line.
	suiteInvocation = regexp.MustCompile(`\b(?:go|just) test\b`)
	// skipsRealTmux accepts every spelling of the flag that actually skips it,
	// including `-skip=NAME` and a name inside an alternation.
	skipsRealTmux = regexp.MustCompile(`-skip[= ]\S*` + regexp.QuoteMeta(realTmuxSkip))
)

// invokesSuite reports whether a workflow line runs the Go suite, as opposed to
// mentioning it. A YAML comment about the suite is prose: asserting against one
// produces a failure message about a sentence, and counting one satisfies the
// vacuity check while nothing real is covered.
func invokesSuite(line string) bool {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return false
	}
	return suiteInvocation.MatchString(line)
}

// TestWorkflowLineClassifiersRejectTheLinesTheyMust is the control for the two
// predicates below. Both are used through assertions that pass on a match, so on
// a tree where every workflow is already correct they are green whether or not
// they discriminate: widening either to match everything is invisible. These are
// the inputs each one has to turn away.
func TestWorkflowLineClassifiersRejectTheLinesTheyMust(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"        run: go test -skip Foo ./...", true},
		{"        run: just test", true},
		// The justfile recipe is a bare `go test ./...`, so a step calling it
		// runs the real-tmux test with no flag this guard would ever see.
		{"          run: go build ./... && go vet ./...", false},
		{"  # we used to run go test here, before the split", false},
		{"          # A job that runs go test must skip the real-tmux one", false},
	} {
		assert.Equalf(t, tc.want, invokesSuite(tc.line), "invokesSuite(%q)", tc.line)
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
}

// TestEveryWorkflowGoTestSkipsTheRealTmuxTest holds the claim session/tmux's
// comments make about CI: TestSessionDeathStopsProbing is skipped by name in
// every workflow invocation of the suite, and TestCloseSucceedsAfterRebindFromCancelledContext
// is not — which is what puts the second one, specifically, on a real tmux server
// in CI.
//
// That claim used to be carried by a pair of line numbers into a workflow file.
// One of them was wrong on the day it merged, and the pair also under-counted the
// skip sites it was pointing at. A sentence naming positions in another file is a
// lookup nobody performs twice; the file is right here, so read it.
func TestEveryWorkflowGoTestSkipsTheRealTmuxTest(t *testing.T) {
	root := moduleRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(workflowsGlob)))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no workflows matched %s", workflowsGlob)

	invocations := 0
	for _, path := range paths {
		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		for i, line := range strings.Split(readFile(t, path), "\n") {
			if !invokesSuite(line) {
				continue
			}
			invocations++
			assert.Regexpf(t, skipsRealTmux, line,
				"%s:%d runs the suite without skipping %s. That test drives a real tmux server "+
					"through a PTY and is local-only; every other invocation skips it, and "+
					"session/tmux's comments say so.", rel, i+1, realTmuxSkip)
			assert.NotContainsf(t, line, realTmuxRuns,
				"%s:%d skips %s. session/tmux documents it as the real-tmux test that DOES run "+
					"in CI, which is what puts that test on a real server under "+
					"ATRIUM_CI_REQUIRE_TMUX=1; skip it here and the documented arrangement is "+
					"no longer true.", rel, i+1, realTmuxRuns)
		}
	}

	// Without this a renamed job, a moved workflow or a reindented `run:` turns
	// the loop into a no-op and the assertions above never execute.
	require.Positive(t, invocations, "found no suite invocation in %s", workflowsGlob)
}
