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

// identityAccessors are the *Instance methods in identity.go that touch ident, DERIVED from
// the file rather than listed here.
//
// Derived because a hardcoded list only covers the accessors that existed when it was
// written, and the whole reason for this change is that wave 1 adds readers of these fields
// — an eleventh accessor added to identity.go with no lock would have passed a fixed list
// silently. It is also the reason expected is asserted below: a derivation that quietly
// found nothing would pass every check in this file.
//
// Writers count as well as readers: a write with no lock races the next reader just as
// surely, and SetBranch is the writer Start calls from its own goroutine.
func identityAccessors(t *testing.T, file *ast.File) map[string]*ast.FuncDecl {
	t.Helper()
	found := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		var touches bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "ident" {
				touches = true
			}
			return !touches
		})
		if touches {
			found[fn.Name.Name] = fn
		}
	}
	// The ten that exist today. Not a whitelist — a floor, so a derivation that silently
	// stopped finding methods fails here instead of passing everything downstream.
	for _, name := range []string{
		"Identity", "Title", "SetTitle", "Branch", "SetBranch",
		"DisplayName", "SetDisplayName", "Note", "SetNote", "AdoptRename",
	} {
		require.Containsf(t, found, name,
			"%s touches ident but was not derived — the derivation is broken, not the code", name)
	}
	return found
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

	for name, fn := range identityAccessors(t, file) {
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

// TestIdentityMuIsALeafLock holds the one invariant that makes a second mutex in Instance
// safe rather than a deadlock waiting to happen: nothing is called while identityMu is held.
//
// A leaf lock cannot be the outer half of a cycle, so the order i.mu → identityMu — which
// this package takes constantly, and several calls deep (recordStatusChange, portOwner,
// probeRunTmux and reservePort all read an identity while an i.mu span is open) — is safe
// without anyone having to know about it.
//
// An earlier draft of this guard asserted the stronger "the two are never held together" and
// tried to enforce it by rejecting an accessor call inside an i.mu span. It was wrong twice
// over: the invariant was already false in this package's own status path, and the check
// could not see a call one hop deeper anyway, so it would have reported a clean tree while
// the property it named was broken. The leaf property is the one that is both true and
// checkable, because every span that could break it is in this one file.
//
// The check: inside any identityMu span in identity.go, the only calls allowed are on
// identityMu itself. That covers the shape that would actually reintroduce the hazard —
// AdoptRename moving its i.mu acquisition inside the identityMu span it currently follows.
func TestIdentityMuIsALeafLock(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "identity.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		span := identityLockSpan(fn)
		if len(span) == 0 {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !span.holdsAt(call.Pos()) {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if mu, ok := sel.X.(*ast.SelectorExpr); ok && mu.Sel.Name == "identityMu" {
					return true
				}
			}
			require.Failf(t, "call inside the identityMu span",
				"identity.go:%d — %s calls out while holding identityMu, which stops it being a "+
					"leaf lock and puts it back in the deadlock class a second mutex invites (#795).",
				fset.Position(call.Pos()).Line, fn.Name.Name)
			return true
		})
	}
}

// lockEvent is one acquisition or release of a mutex inside a function body.
type lockEvent struct {
	pos     token.Pos
	acquire bool
	// deferred marks a release that only runs when the body ends, so it never closes the
	// span for any position inside the body.
	deferred bool
}

type lockSpan []lockEvent

// holdsAt reports whether the lock is held at pos: the depth of acquisitions minus
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

// identityLockSpan collects the function's identityMu events in source order.
//
// Both shapes are in this file and they differ: the accessors defer their release, so the
// span runs to the end of the body, while AdoptRename releases explicitly and then takes
// i.mu — which is sequential, and the whole point.
func identityLockSpan(fn *ast.FuncDecl) lockSpan {
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
		if !ok || mu.Sel.Name != "identityMu" {
			return true
		}
		span = append(span, lockEvent{pos: sel.Pos(), acquire: acquire, deferred: deferred[sel.Pos()]})
		return true
	})
	sort.Slice(span, func(a, b int) bool { return span[a].pos < span[b].pos })
	return span
}
