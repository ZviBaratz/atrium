package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A line number in prose is a claim with a half-life measured in edits, and it is
// the one claim the author cannot check by reading the sentence: the number is
// true or false somewhere else in the tree.
//
// #675 swept fourteen of them out in one comment-only commit. Six were provably
// wrong — pointing at a blank line, a closing brace, an `if err != nil`, and in
// one case a path that did not exist. The rest were merely unverifiable, which is
// the same thing to a reader.
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
	// A path bearing one of this repo's source extensions, immediately followed
	// by a line number. Keying on the extension is what keeps ratios (83:100),
	// clock times (10:00), contrast figures (1.44:1) and ports (:3001) out — all
	// of which appear in comments here and none of which is a citation.
	positional := regexp.MustCompile(`[A-Za-z0-9_./-]+\.(?:go|md|yml|yaml|sh|json|toml|tmpl)\s*:\s*[0-9]+`)

	// A predicate that has stopped matching passes every file it is handed. Prove
	// it still fires, and prove the exclusion above is the reason the tree is
	// clean rather than a regex that matches nothing.
	require.Regexp(t, positional, "see session/instance.go:2228 for the write",
		"the predicate no longer recognises a positional citation")
	for _, notACitation := range []string{
		"an edge:core ratio of 83:100, i.e. no vignette",
		"a 2:1 WIDE picture, twice as tall as wide",
		"OpenableURL(\"http://localhost:8080\")",
		"contrast is 12.91:1, so the band asks for 7.10",
	} {
		assert.NotRegexp(t, positional, notACitation,
			"the predicate reads a non-citation as one; it would fail on prose that is fine")
	}

	root := moduleRoot(t)
	goFiles, comments, mdFiles, shFiles := 0, 0, 0, 0

	// report is the single failure path, so a hit in a .go comment, a .sh comment
	// and a .md line all read the same way and all name the remedy.
	report := func(rel string, line int, text string) {
		t.Errorf("%s:%d cites a position; cite the symbol instead, which survives an edit:\n\t%s",
			rel, line, strings.TrimSpace(text))
	}

	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		if d.IsDir() {
			switch {
			case d.Name() == ".git", d.Name() == "node_modules", d.Name() == "bin":
				return filepath.SkipDir
			// web/ is a standalone Next.js site with its own toolchain, excluded
			// from every other Go-side check here for the same reason.
			case rel == "web":
				return filepath.SkipDir
			// Plans and specs record work as it was scoped, and naming the exact
			// line to edit is a plan step's job. Freezing today's positions into
			// them would be wrong; they describe the tree they were written
			// against, not the one under the test.
			case rel == filepath.FromSlash(historicalDocs):
				return filepath.SkipDir
			}
			return nil
		}

		switch filepath.Ext(path) {
		case ".go":
			// Comments only. This repo parses positional references as a feature —
			// hints/scan.go reads them out of agent error output, ui/diff_anchor.go
			// writes them into review anchors — so the same regex over whole .go
			// files matches those packages' fixtures, which are string literals
			// doing their job rather than claims about the tree. Scoping to
			// comments is what makes the rule mean anything.
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			require.NoErrorf(t, parseErr, "parsing %s", rel)
			goFiles++
			for _, group := range f.Comments {
				for _, c := range group.List {
					comments++
					if positional.MatchString(c.Text) {
						report(rel, fset.Position(c.Pos()).Line, c.Text)
					}
				}
			}
		case ".sh":
			shFiles++
			for i, line := range strings.Split(readFile(t, path), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") && positional.MatchString(line) {
					report(rel, i+1, line)
				}
			}
		case ".md":
			mdFiles++
			for i, line := range strings.Split(readFile(t, path), "\n") {
				if positional.MatchString(line) {
					report(rel, i+1, line)
				}
			}
		}
		return nil
	}))

	// A walk that matched nothing passes exactly as happily as a clean tree, so a
	// broken skip rule would turn this guard off rather than fail it. Each counter
	// covers one of the three prose surfaces above; asserting only the total would
	// let a whole surface go dark.
	require.Positive(t, goFiles, "walked no .go files; the skip rules are wrong")
	require.Positive(t, comments, "parsed no comments; ParseComments is not doing its job")
	require.Positive(t, mdFiles, "walked no .md files; the skip rules are wrong")
	require.Positive(t, shFiles, "walked no .sh files; the skip rules are wrong")
}

// readFile is the byte-slurping half of moduleFile, for paths the walk already
// holds absolute.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// realTmuxSkip names the test CI declines to run, and the flag it declines with.
const (
	realTmuxSkip  = "TestSessionDeathStopsProbing"
	realTmuxRuns  = "TestCloseSucceedsAfterRebindFromCancelledContext"
	workflowsGlob = ".github/workflows/*.yml"
)

// TestEveryWorkflowGoTestSkipsTheRealTmuxTest holds the claim that
// session/tmux's own comments make about CI: of the two tests that drive a real
// tmux server, one is skipped by name everywhere and the other is not, which is
// why RequireTmux's hard failure applies to the second.
//
// That claim used to be carried by a pair of line numbers into build.yml. One of
// them was wrong on the day it merged, and the pair also under-counted the skip
// sites it was pointing at. A sentence naming N positions in another file is N
// lookups nobody performs twice; the file is right here, so read it.
func TestEveryWorkflowGoTestSkipsTheRealTmuxTest(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(moduleRoot(t), filepath.FromSlash(workflowsGlob)))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no workflows matched %s", workflowsGlob)

	invocations := 0
	for _, path := range paths {
		rel, relErr := filepath.Rel(moduleRoot(t), path)
		require.NoError(t, relErr)
		for i, line := range strings.Split(readFile(t, path), "\n") {
			if !strings.Contains(line, "go test") {
				continue
			}
			invocations++
			assert.Containsf(t, line, "-skip "+realTmuxSkip,
				"%s:%d runs go test without -skip %s. That test drives a real tmux server "+
					"through a PTY and is local-only; every other invocation skips it, and "+
					"session/tmux's comments say so.", rel, i+1, realTmuxSkip)
			assert.NotContainsf(t, line, realTmuxRuns,
				"%s:%d skips %s, but session/tmux documents it as the real-tmux test that "+
					"DOES run in CI — which is what makes RequireTmux's hard failure reachable "+
					"under ATRIUM_CI_REQUIRE_TMUX=1. Skip it and that guard goes quiet.",
				rel, i+1, realTmuxRuns)
		}
	}

	// Without this a renamed job, a moved workflow or a reindented `run:` turns
	// the loop into a no-op and the assertions above never execute.
	require.Positive(t, invocations, "found no `go test` invocation in %s", workflowsGlob)
}
