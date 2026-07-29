package theme

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoStringConversionsOfPaletteColors is the guard Hex's doc comment promises.
//
// On v1 a palette token IS a string, so string(pal.Accent) compiles and works, and
// nothing stops a new one appearing. On v2 it stops compiling — but by then it is
// inside the migration commit, showing up as unrelated breakage in the diff that is
// already the hardest to review. This keeps the tree clean in the window between.
//
// The field names come from reflecting over Palette rather than from a list here,
// so adding a token cannot silently fall outside the guard.
func TestNoStringConversionsOfPaletteColors(t *testing.T) {
	var fields []string
	pt := reflect.TypeOf(Palette{})
	for i := 0; i < pt.NumField(); i++ {
		fields = append(fields, pt.Field(i).Name)
	}
	require.NotEmpty(t, fields, "reflection found no Palette fields — the guard would pass vacuously")

	// string(<expr>.<Token>) — the shape every converted site had.
	re := regexp.MustCompile(`string\([\w.]*\.(` + strings.Join(fields, "|") + `)\)`)

	root := moduleRoot(t)
	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "web", "docs", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllString(string(src), -1) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+": "+m)
		}
		return nil
	}))

	require.Emptyf(t, offenders,
		"palette colours must reach a string through theme.Hex, not a string() conversion —\n"+
			"that conversion stops compiling under Lip Gloss v2 (#393):\n  %s",
		strings.Join(offenders, "\n  "))
}

// moduleRoot walks up from the package directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked past the filesystem root without finding go.mod")
		dir = parent
	}
}
