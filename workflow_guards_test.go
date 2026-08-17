package main

import (
	"go/ast"
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

// The guards in this file hold what CI runs to what the code says CI runs. They
// share the shell lexer in prose_citations_test.go — shellCommentStart and
// splitCommands — because a workflow `run:` value is shell and has to be read as
// shell; nothing else is common between them. They landed there first because the
// claim they replace used to be a pair of line numbers into a workflow file, which
// is an origin story rather than a subject.

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

// suiteCommands returns the commands in a piece of shell that run the Go suite.
//
// Splitting first is what makes the assertions about a *command* rather than a
// line. Matching the raw text let a -skip anywhere on it vouch for an invocation
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
		{"go test -skip Foo ./...", 1},
		{"just test", 1},
		{"just test-race", 1},
		{"just cover", 1},
		{"just ci", 1},
		{"go build ./... && go vet ./...", 0},
		{"# we used to run go test here, before the split", 0},
		// A trailing comment is not a command, and a second command is not the
		// first one. Both used to be counted as part of the line that precedes
		// them, which let either vouch for the other.
		{"go test ./...  # -skip lives in the script now", 1},
		{"go test ./... && go test -skip Foo ./session/tmux/...", 2},
		// A quoted separator separates nothing. The alternation is the spelling
		// skipsRealTmux exists to accept, and splitting it left half a flag.
		{"go test -skip 'TestFoo|" + realTmuxSkip + "' ./...", 1},
		{"go test ./... | tee out.log", 1},
	} {
		assert.Lenf(t, suiteCommands(tc.line), tc.want, "suiteCommands(%q)", tc.line)
	}

	// The predicates are only ever applied to what suiteCommands returns, so an
	// input the table above blesses is not yet proof: assert the composition. The
	// alternation passed skipsRealTmux as a raw line and failed it as a command,
	// and nothing here could see the difference until the two were run together.
	alternation := "go test -skip 'TestFoo|" + realTmuxSkip + "' ./..."
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

// runKey matches the `run:` mapping key, capturing everything before it and its
// value. What bounds a block scalar is the column the key itself sits in, not the
// indent of the line: in a sequence item spelled `- run: |` the key starts past
// the dash, and measuring from the dash instead made every sibling key of that
// step — its `name:`, the entries under its `env:` — deeper than the boundary and
// so part of the script.
var runKey = regexp.MustCompile(`^(\s*(?:-\s+)?)run:\s*(.*)$`)

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
			blockIndent = columnAfter(m[1])
			continue
		}
		out = append(out, scriptLine{line: i + 1, text: m[2]})
	}
	end()
	return out
}

// joinContinuations folds a line ending in a backslash into the one after it,
// keeping the first line's number so a failure points at the head of the command.
//
// Only an odd number of trailing backslashes continues a line: an even run is
// escaped backslashes, and the command ends there. Folding on any run at all
// joined a command to the one after it and attributed the second one's flags — or
// its missing -skip — to the first.
func joinContinuations(lines []scriptLine) []scriptLine {
	var out []scriptLine
	for _, l := range lines {
		if k := len(out) - 1; k >= 0 {
			head := strings.TrimRight(out[k].text, " \t")
			if run := len(head) - len(strings.TrimRight(head, `\`)); run%2 == 1 {
				out[k].text = head[:len(head)-1] + " " + strings.TrimSpace(l.text)
				continue
			}
		}
		out = append(out, l)
	}
	return out
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
	// justRecipeCall matches one recipe calling another from its body. A name is
	// required, so `just --list` adds no edge.
	justRecipeCall = regexp.MustCompile(`\bjust\s+([A-Za-z][A-Za-z0-9_-]*)`)
)

// justSuiteRecipes returns the names of the recipes in a justfile that run the Go
// suite, whether in their own body or through another recipe that does. A recipe
// reaches another two ways — the dependency list on its header, which is how `ci`
// runs no `go test` itself and reaches two recipes that do, and a `just <name>`
// call in its body, which the header does not mention at all.
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
			if r := byName[current]; r != nil {
				if justGoTest.MatchString(line) && !justRunsNoTests.MatchString(line) {
					r.suite = true
				}
				if m := justRecipeCall.FindStringSubmatch(line); m != nil {
					r.deps = append(r.deps, m[1])
				}
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
	assert.Equal(t, []string{"test", "cover", "verify", "ci"}, justSuiteRecipes(strings.Join([]string{
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
		"# Reaches the suite by calling a recipe, which its header does not mention.",
		"verify:",
		"    just test",
		"",
		"# Benchmarks run no tests, so they cannot reach the real-tmux one.",
		"bench pattern='.' *pkgs='./...':",
		"    {{go}} test -run '^$' -bench '{{pattern}}' {{pkgs}}",
		"",
		"# A body-level `just` with no recipe name is not an edge to anywhere.",
		"default:",
		"    @just --list",
		"",
		"lint:",
		"    golangci-lint run",
		"",
		"ci: build vet lint test cover",
	}, "\n")), "want the recipes that run tests, the one that calls another, and the aggregate that "+
		"depends on them — and neither bench, whose -run '^$' runs none, nor default, whose `just` "+
		"names no recipe")
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
		// The same block scalar, opened on the sequence dash. Its siblings below
		// are indented past the dash but not past the key, and reading the block's
		// boundary off the dash swept every one of them into the script.
		"      - run: |",
		"          ./ci.sh",
		"        name: A step whose name says go test",
		"        env:",
		"          CMD: go test ./...",
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
	require.Len(t, got, 2, "want the two run: invocations and none of the names, the if: or the env:, got %+v", got)
	assert.Equal(t, 6, got[0].line, "the single-line run: step")
	assert.Regexp(t, skipsRealTmux, got[0].text)
	assert.Equal(t, 9, got[1].line, "a wrapped command is reported at its head, not its tail")
	assert.Regexpf(t, skipsRealTmux, got[1].text,
		"the backslash must join the flag to the invocation, or the head reads as unskipped")

	// An even run of backslashes is escaped backslashes and ends the command, so
	// folding on it would attribute the next command's flags to this one — or, as
	// here, hide that the next command has none.
	unfolded := runScript([]string{
		"        run: |",
		`          echo 'x' \\`,
		"          go test ./...",
	})
	require.Len(t, unfolded, 2, "an escaped backslash is not a line continuation, got %+v", unfolded)
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
