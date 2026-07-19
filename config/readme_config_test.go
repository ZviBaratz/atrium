package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// moduleFile walks up from the test's working directory to the module root and
// reads the named file (see the identical helper in package keys).
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			b, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			return string(b)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "reached filesystem root without finding go.mod (looking for %s)", name)
		dir = parent
	}
}

// TestReadmeDocumentsEveryConfigField is the anti-rot guard for #382's config
// reference: every json-tagged field of Config must have a backtick-wrapped row
// in the README "Configuration reference" table, so adding a config key without
// documenting it fails the build.
func TestReadmeDocumentsEveryConfigField(t *testing.T) {
	readme := moduleFile(t, "README.md")
	start := strings.Index(readme, "#### Configuration reference")
	require.GreaterOrEqual(t, start, 0, "README must have a #### Configuration reference section")
	section := readme[start:]
	if end := strings.Index(section, "### FAQs"); end > 0 {
		section = section[:end]
	}

	tp := reflect.TypeOf(Config{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		require.Containsf(t, section, "`"+name+"`",
			"config field %s (json:%q) is missing a row in the README configuration reference table",
			tp.Field(i).Name, name)
	}
}
