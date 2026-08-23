package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// identityAccessors are the methods that may touch ident, and every one of them must take
// identityMu. Writers as well as readers: a write with no lock races the next reader just
// as surely, and SetBranch is the writer Start calls from its own goroutine.
var identityAccessors = []string{
	"Identity",
	"Title", "SetTitle",
	"Branch", "SetBranch",
	"DisplayName", "SetDisplayName",
	"Note", "SetNote",
	"AdoptRename",
}

// TestEveryIdentityAccessorTakesTheLock is the half of #795's guard the normal gate can
// fail on.
//
// The -race test beside it cannot: a data race is invisible to `go build`, `go vet`,
// golangci-lint and an untagged `go test`, so on every gate except `just test-race` those
// cases report success whether or not the locking is present (#718 hit the same wall). This
// one asserts the shape of the fix instead of its effect.
//
// It checks that identityMu is named in the body, not that it is held correctly — a lock
// taken and released around the wrong statement would pass here. That is what the -race
// cases are for; this one catches the mutation that matters in practice, which is a lock
// that is simply not there.
func TestEveryIdentityAccessorTakesTheLock(t *testing.T) {
	file := parseIdentityFile(t)
	bodies := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		bodies[fn.Name.Name] = fn
	}

	for _, name := range identityAccessors {
		fn, ok := bodies[name]
		require.Truef(t, ok, "%s must live in identity.go beside the field it guards", name)

		var takesLock bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "identityMu" {
				takesLock = true
			}
			return !takesLock
		})
		require.Truef(t, takesLock,
			"%s reads or writes the identity fields without taking identityMu — "+
				"an unguarded access races AdoptRename and Start's SetBranch (#795)", name)
	}
}

// TestIdentityFieldsAreTouchedOnlyByTheirAccessors holds the whole family to one access
// site set. That is the property that makes "guarded" a fact about the field rather than an
// argument about the goroutine each reader happens to be on — the argument #719 had to make
// reader by reader, and that rotted every time a reader was added.
//
// It looks for `x.ident` anywhere in the package's non-test sources outside identity.go.
// What it deliberately does NOT see is a composite literal — `&Instance{ident: …}` in
// NewInstance and FromInstanceData — because a `ident:` key is a field name, not a selector.
// Those two are safe for a reason no lock is needed for: they build the value before it is
// published, so no other goroutine can hold a pointer to it yet. Anything that touches ident
// on an instance that already exists is a selector, and is what this catches.
//
// Out-of-package readers need no guard here at all, and deliberately have none: the fields
// are unexported, so the compiler is the enforcement, and re-adding the convenience the tree
// had before does not even compile — an exported Title field cannot coexist with the Title
// method. A test asserting that was written for this file and deleted: its mutation could
// not be made to build, which means it could never have failed for the reason it claimed.
func TestIdentityFieldsAreTouchedOnlyByTheirAccessors(t *testing.T) {
	fset := token.NewFileSet()
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, path := range sources {
		name := filepath.Base(path)
		if name == "identity.go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ident" {
				return true
			}
			pos := fset.Position(sel.Pos())
			require.Failf(t, "identity field touched outside its accessors",
				"%s:%d reaches ident directly; go through the accessors in identity.go, "+
					"which are what take identityMu (#795)", name, pos.Line)
			return true
		})
	}
}

func parseIdentityFile(t *testing.T) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "identity.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	return file
}

// TestIdentityIsNeverReadInsideTheLiveStateLock holds the one invariant that makes a second
// mutex safe rather than a deadlock waiting to happen: identityMu and i.mu are never held
// together.
//
// A second lock in a struct that already has one is a lock-ordering hazard by default. This
// package keeps it out of that class by never nesting the two — AdoptRename writes ident and
// tmuxName under sequential acquisitions, and SetupFailureReport takes its title before
// entering i.mu's span rather than inside it, which is the site that made this test worth
// writing.
//
// The check is positional, and it has to track the whole span rather than just the
// acquisition. Both shapes are in this package and they differ: SetupFailureReport defers
// its Unlock, so its span runs to the end of the body and a call after the Lock is nested;
// Start unlocks explicitly and then calls SetBranch, which is sequential and fine. The first
// draft of this test assumed every span was deferred and failed on Start within a minute of
// being written. So a call counts as nested only while the running Lock/Unlock depth is
// positive, with a deferred Unlock holding the span open to the end of the body.
//
// What it does not see: an accessor reached one call deeper, from a helper invoked inside the
// span. That is the same limit ui's capture-path guard has, and the same answer — the nesting
// it does catch is the shape this package actually writes.
func TestIdentityIsNeverReadInsideTheLiveStateLock(t *testing.T) {
	accessors := map[string]bool{}
	for _, name := range identityAccessors {
		accessors[name] = true
	}

	fset := token.NewFileSet()
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			span := liveStateLockSpan(fn)
			if len(span) == 0 {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !accessors[sel.Sel.Name] || !isReceiver(sel.X) || !span.holdsAt(call.Pos()) {
					return true
				}
				require.Failf(t, "identity accessor called inside the i.mu span",
					"%s:%d — %s.%s() runs while %s holds i.mu, which nests identityMu inside it. "+
						"Take the value before the lock; the two must never be held together (#795).",
					filepath.Base(path), fset.Position(call.Pos()).Line,
					"i", sel.Sel.Name, fn.Name.Name)
				return true
			})
		}
	}
}

// lockEvent is one acquisition or release of i.mu inside a function body. A deferred
// release is recorded with releases=false at the end of the body's reach: it closes nothing
// before then.
type lockEvent struct {
	pos     token.Pos
	acquire bool
	// deferred marks a release that only runs when the body ends, so it never closes the
	// span for any position inside the body.
	deferred bool
}

type lockSpan []lockEvent

// holdsAt reports whether i.mu is held at pos: the depth of acquisitions minus
// non-deferred releases that precede it.
func (s lockSpan) holdsAt(pos token.Pos) bool {
	depth := 0
	for _, e := range s {
		if e.pos >= pos {
			break
		}
		switch {
		case e.acquire:
			depth++
		case !e.deferred:
			depth--
		}
	}
	return depth > 0
}

// liveStateLockSpan collects the function's i.mu events in source order.
func liveStateLockSpan(fn *ast.FuncDecl) lockSpan {
	deferred := map[token.Pos]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if d, ok := n.(*ast.DeferStmt); ok {
			deferred[d.Call.Fun.Pos()] = true
		}
		return true
	})

	var span lockSpan
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		acquire := sel.Sel.Name == "Lock" || sel.Sel.Name == "RLock"
		release := sel.Sel.Name == "Unlock" || sel.Sel.Name == "RUnlock"
		if !acquire && !release {
			return true
		}
		mu, ok := sel.X.(*ast.SelectorExpr)
		if !ok || mu.Sel.Name != "mu" || !isReceiver(mu.X) {
			return true
		}
		span = append(span, lockEvent{pos: sel.Pos(), acquire: acquire, deferred: deferred[sel.Pos()]})
		return true
	})
	sort.Slice(span, func(a, b int) bool { return span[a].pos < span[b].pos })
	return span
}

// isReceiver reports whether the expression is the bare identifier `i`, which is what every
// *Instance method in this package names its receiver.
func isReceiver(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "i"
}
