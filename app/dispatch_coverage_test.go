package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The keymap's one unguarded drift site. A key needs seven edits to ship — the
// KeyName, the Registry entry, a cheatsheet row, a dispatch case, the golden
// inventory, the README, sometimes the busy allowlist — and six of them have a
// test. The dispatch case did not, so a key could be registered, documented,
// README'd, and completely dead: nothing here fails when `case keys.KeyX` is the
// edit you forgot. The tests below close that, by reading the case labels out of
// dispatchAction's source and holding them against the registry.
//
// This matters more now than it did: the command palette (#374) lists registry
// actions and runs them by name, so a missing case stops being a key nobody
// noticed was dead and becomes a visible row that does nothing.

// dispatchExempt names the registered actions that deliberately have no case in
// dispatchAction, and why. An exemption is a claim that the action is handled
// somewhere else — never that it is unhandled.
var dispatchExempt = map[keys.KeyName]string{
	keys.KeyToggleMark: "consumed only by handleMultiSelectState; space marks a row in " +
		"multi-select mode and does nothing in the default state (keys.go says so too)",
}

// TestEveryRegistryActionHasADispatchCase is direction 1: every dispatched
// Registry entry must have somewhere to land.
func TestEveryRegistryActionHasADispatchCase(t *testing.T) {
	names := keyNameConstants(t)
	cases := dispatchCaseLabels(t)

	var dead []string
	for _, e := range keys.Registry {
		if e.DocOnly {
			// DocOnly entries never enter GlobalKeyStringsMap, so no name ever
			// resolves to them — their keys are handled before dispatch or in the
			// attach layer. TestRegistry_DocumentedOnlyEntries pins which three.
			continue
		}
		name := names[int(e.Name)]
		require.NotEmptyf(t, name, "KeyName %d has no constant name — the const block walk is stale", int(e.Name))
		if cases[name] || dispatchExempt[e.Name] != "" {
			continue
		}
		dead = append(dead, fmt.Sprintf("%s (key %q)", name, strings.Join(e.Binding.Keys(), "/")))
	}
	assert.Empty(t, dead, "registered, documented, and dead: these actions have no case in "+
		"dispatchAction, so pressing their key does nothing. Add the case, or add a "+
		"dispatchExempt entry saying which handler owns the key instead")
}

// TestEveryDispatchExemptionIsRealAndReasoned is direction 2: an exemption may not
// name a key that does not exist, one that already has a case, or offer no
// reason — the three ways this list would rot into a licence to forget.
func TestEveryDispatchExemptionIsRealAndReasoned(t *testing.T) {
	names := keyNameConstants(t)
	cases := dispatchCaseLabels(t)

	registered := map[keys.KeyName]bool{}
	for _, e := range keys.Registry {
		if !e.DocOnly {
			registered[e.Name] = true
		}
	}

	for name, reason := range dispatchExempt {
		label := names[int(name)]
		assert.NotEmptyf(t, reason, "%s is exempted with no reason", label)
		assert.Truef(t, registered[name],
			"%s is exempted from dispatch but is not a dispatched Registry entry — nothing to exempt", label)
		assert.Falsef(t, cases[label],
			"%s has a case in dispatchAction, so the exemption is stale and now hides a real one", label)
	}
}

// TestKeyNames_AllRegisteredOrDeliberatelyAbsent pins the KeyName block against
// the registry by count. KeyReview and KeyPush sat in that block unregistered
// and unreferenced until #374 went looking; the sentinel is what makes the next
// pair fail instead of accumulating.
func TestKeyNames_AllRegisteredOrDeliberatelyAbsent(t *testing.T) {
	assert.Equal(t, len(keys.Registry)+1, int(keys.NumKeyNames),
		"the KeyName constants and the Registry have drifted. Every name needs an Entry, "+
			"and the +1 is KeyScreensaver, whose absence from the Registry is the easter "+
			"egg's exclusion contract (TestRegistry_OmitsScreensaver)")
}

// Quit is the one action whose key never reaches dispatchAction — handleKeyPress
// matches "q" in its prelude so quitting stays live inside every mode. Both arms
// must hold: the key still quits, and the action is reachable by name for the
// callers (the palette) that never press a key.
func TestQuit_ReachableByKeyAndByName(t *testing.T) {
	byKey := newQuitTestHome(t)
	assert.True(t, isQuit(pressQ(t, byKey)), "q must still quit from the list")

	byName := newQuitTestHome(t)
	_, cmd := byName.dispatchAction(keys.KeyQuit)
	assert.True(t, isQuit(cmd), "quit must be dispatchable by name, not only by its key")
}

// keyNameConstants returns the KeyName constants in iota order, so index i is
// the name of KeyName(i). Read from the source rather than a hand-written table:
// a table would be one more copy to forget, which is the bug this file guards.
func keyNameConstants(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../keys/keys.go", nil, 0)
	require.NoError(t, err, "parsing the keys package's const block")

	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		// The KeyName block is the one whose first spec types itself KeyName.
		first, ok := gen.Specs[0].(*ast.ValueSpec)
		if !ok || len(first.Names) == 0 {
			continue
		}
		if id, ok := first.Type.(*ast.Ident); !ok || id.Name != "KeyName" {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				names = append(names, id.Name)
			}
		}
		break
	}

	// Without this the walk could find nothing and every check above would pass
	// vacuously on an empty name table.
	require.NotEmpty(t, names, "the KeyName const block was not found in ../keys/keys.go")
	require.Equal(t, "KeyUp", names[0], "the KeyName block no longer starts at KeyUp — the iota mapping is wrong")
	require.Equal(t, int(keys.NumKeyNames)+1, len(names),
		"the parsed const block disagrees with NumKeyNames — the sentinel must stay last")
	return names
}

// dispatchCaseLabels returns the set of keys.KeyX labels that appear in the case
// clauses of dispatchAction's switch, read from the package's source.
func dispatchCaseLabels(t *testing.T) map[string]bool {
	t.Helper()

	// An explicit walk rather than parser.ParseDir, which staticcheck flags as
	// deprecated (SA1019) since Go 1.25 — the same walk reorder_filter_keys_test.go uses.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()

	var fn *ast.FuncDecl
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if ok && d.Name.Name == "dispatchAction" && d.Body != nil {
				fn = d
			}
		}
	}
	require.NotNil(t, fn, "dispatchAction was not found — this guard reads its case labels")

	// The switch on the resolved action name, specifically: a nested switch on
	// anything else must not contribute labels, or a case could be counted from
	// a branch that never runs.
	var switches []*ast.SwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); ok && id.Name == "name" {
			switches = append(switches, sw)
		}
		return true
	})
	require.Len(t, switches, 1, "dispatchAction must switch on the action name exactly once")

	labels := map[string]bool{}
	for _, stmt := range switches[0].Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			sel, ok := expr.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "keys" {
				labels[sel.Sel.Name] = true
			}
		}
	}
	// Without this the walk could match nothing and direction 1 would report
	// every action dead — loudly — while direction 2 passed on an empty set.
	require.NotEmpty(t, labels, "no keys.KeyX case labels were found in dispatchAction")
	return labels
}
