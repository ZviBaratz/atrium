package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sentence that names a key is a copy of the keymap, and the least guarded
// one: it lives in a string literal, no map knows about it, and it stays
// grammatical forever after the key it names has moved. Two shipped saying
// "press k to kill", where k moves the selection and the kill key is ctrl-x.
//
// So the rule is that prose reads the key from the registry
// (keys.LabelOf / keys.PrimaryKey), and this walks the chrome packages' source
// to enforce it. It replaces ui/key_prose_test.go, which pinned four known
// literals to their KeyName: that caught a rebind of those four, and nothing
// else — a fifth literal was invisible to it, which is how the "press k" pair
// survived. Scanning for the shape instead covers the ones nobody listed.
//
// The allowlist is not a licence to skip the lookup. Every entry names a key
// that belongs to an overlay's own handler rather than to the keymap: no
// Registry entry owns it, no config override can move it, and there is nothing
// to look up. A literal for a registry action does not belong here.
func TestNoProseNamesAKeyLiterally(t *testing.T) {
	// press X to …, Press 'X' to …, press X for … — the shape a "how do I do
	// this" clause takes. The token is short by construction: a key label is one
	// or two cells, and %s (the generated form) is what should be there instead.
	proseKey := regexp.MustCompile(`[Pp]ress '?([^ '"]{1,3})'? (?:to|for)\b`)

	// Tokens that are not keys: the generated form, and the two phrasings that
	// name no particular key.
	notAKey := map[string]bool{"%s": true, "any": true, "a": true, "the": true}

	// The chrome packages: everything that renders text at the user. keys/ is
	// excluded because its compound labels are the registry, not a copy of it.
	dirs := []string{".", "../ui", "../ui/overlay"}

	var offenders []string
	scanned, literals := 0, 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		require.NoErrorf(t, err, "reading %s", dir)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			require.NoErrorf(t, err, "parsing %s", path)
			scanned++

			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				literals++
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, m := range proseKey.FindAllStringSubmatch(text, -1) {
					if notAKey[m[1]] {
						continue
					}
					if reason, ok := proseKeyAllowlist[path]; ok {
						t.Logf("%s: allowed (%s)", path, reason)
						continue
					}
					offenders = append(offenders, fmt.Sprintf("%s: %q names key %q in prose",
						fset.Position(lit.Pos()), m[0], m[1]))
				}
				return true
			})
		}
	}

	// Without these the walk could find nothing and every check above would pass
	// vacuously — the failure mode a source-scanning guard is most prone to.
	require.Positive(t, scanned, "the walk must find source files")
	require.Positive(t, literals, "the walk must find string literals to inspect")
	assert.Empty(t, offenders,
		"prose must name a key through keys.LabelOf (or keys.PrimaryKey) so it cannot "+
			"contradict a rebind. If the key is an overlay's own and no Registry entry "+
			"owns it, add the file to the allowlist with the reason")
}

// proseKeyAllowlist names the files whose key-naming prose is legitimately a
// literal, and why. Every entry is an overlay-local key: the overlay's own
// handler owns it, no Registry entry does, and no config override can move it,
// so there is nothing to look up. One copy, read by both tests below, because a
// second copy of a list of exceptions is the same defect this file is about.
var proseKeyAllowlist = map[string]string{
	"../ui/overlay/accounts.go":          "n opens the account form from inside the accounts overlay",
	"../ui/overlay/settings_nav.go":      "↵ opens the accounts overlay from the settings rail",
	"../ui/overlay/settings_profiles.go": "n and D are the profiles panel's own row keys",
}

// A stale allowlist entry is a hole, not a harmless leftover: it would silently
// permit a real literal in a file that no longer has an excusable one. So an
// entry has to still be earning its place — the file must exist and must still
// contain prose naming a key.
func TestProseKeyAllowlistIsNotStale(t *testing.T) {
	proseKey := regexp.MustCompile(`[Pp]ress '?([^ '"]{1,3})'? (?:to|for)\b`)
	for path, reason := range proseKeyAllowlist {
		src, err := os.ReadFile(path)
		if !assert.NoErrorf(t, err, "allowlisted file %s no longer exists", path) {
			continue
		}
		assert.Truef(t, proseKey.Match(src),
			"%s is allowlisted (%s) but no longer names a key in prose — drop the entry",
			path, reason)
	}
}
