package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modeHandlersWithOwnKeys are the handlers that answer some keys themselves
// before falling through to the dispatch map. A key matched in one of those
// leading cases is invisible to an override inside that mode, so keys.Validate
// warns about it — and this is what stops that warning list from being a guess.
var modeHandlersWithOwnKeys = []string{
	"handleMultiSelectState",
	"handleDiffCommentState",
}

// Every key a mode handler consumes ahead of the dispatch lookup must be known
// to keys' reserved or shadowed table, or a user who rebinds an action onto it
// gets a key that works everywhere except one mode, with nothing telling them so.
//
// Read out of the source rather than listed here, because the tables live in
// another package and describe this one: a case added to a handler would
// otherwise leave the tables quietly incomplete, which is the same shape as the
// bug that let "press k to kill" ship.
func TestShadowTableMatchesTheModeHandlers(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app_keys.go", nil, 0)
	require.NoError(t, err)
	fd, err := parser.ParseFile(fset, "app_diffcomment.go", nil, 0)
	require.NoError(t, err)

	found := map[string][]string{}
	for _, file := range []*ast.File{f, fd} {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if !containsString(modeHandlersWithOwnKeys, name) {
				continue
			}
			// Only the cases BEFORE the dispatch lookup matter: after it, the
			// handler is already routing by KeyName and an override is honored.
			body := fn.Body
			cut := dispatchLookupOffset(body)
			ast.Inspect(body, func(n ast.Node) bool {
				cc, ok := n.(*ast.CaseClause)
				if !ok || (cut > 0 && cc.Pos() > cut) {
					return true
				}
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						found[name] = append(found[name], strings.Trim(lit.Value, `"`))
					}
				}
				return true
			})
		}
	}

	// Without this the walk could match no handler at all and the loop below would
	// assert nothing.
	require.Len(t, found, len(modeHandlersWithOwnKeys),
		"every named mode handler must be found and yield keys — a rename here is silent")

	for handler, ks := range found {
		for _, k := range ks {
			_, known := keys.ConsumedBeforeDispatch(k)
			assert.Truef(t, known,
				"%s answers %q itself, ahead of the dispatch map, but keys' reserved and "+
					"shadowed tables do not know it — an override onto %q would be dead in "+
					"that mode with no warning", handler, k, k)
		}
	}
}

// dispatchLookupOffset is the position of the handler's GlobalKeyStringsMap
// lookup, or 0 when it has none.
func dispatchLookupOffset(body *ast.BlockStmt) token.Pos {
	var at token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "GlobalKeyStringsMap" {
			return true
		}
		if at == 0 || sel.Pos() < at {
			at = sel.Pos()
		}
		return true
	})
	return at
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
