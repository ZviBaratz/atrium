package keys

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The drift-sites skill states three counts, and until now nothing checked them.
//
// They were wrong twice in the same pull request. The file said 58 registry entries when
// the tree had 60; the correction added 2 to the stale number and landed on 60 again, one
// short of the 61 that adding a binding produces. The skill's own opening line tells the
// reader to "re-count before trusting" and ships a recount recipe — which is an admission
// that the numbers rot, not a defence against it.
//
// So: the recipe is the test. A count nobody can verify at a glance is exactly the
// claim-defect class the skill exists to warn about, and the file is read as authoritative
// precisely when someone is about to touch one of the things it counts.
const skillPath = ".claude/skills/tui-drift-sites/SKILL.md"

// skillCount pulls the first integer preceding label in the skill's prose.
func skillCount(t *testing.T, skill, label string) int {
	t.Helper()
	// The bold markers wrap the whole clause, so only the first number in the sentence
	// is preceded by one — match the number and its label, not the emphasis.
	re := regexp.MustCompile(`(\d+) ` + regexp.QuoteMeta(label))
	m := re.FindStringSubmatch(skill)
	require.Lenf(t, m, 2, "the skill no longer states a bolded count for %q — if the "+
		"sentence was reworded, update this guard rather than deleting the number", label)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func TestSkillCountsMatchTheTree(t *testing.T) {
	skill := moduleFile(t, skillPath)

	// Site 2: one line per Registry entry.
	registry := strings.Count(moduleFile(t, "keys/registry.go"), "\n\t{Name: ")
	// Site 4: dispatch-case LINES, which is below the number of actions because several
	// cases carry two or three names — the skill says so, and that is why it is lines.
	dispatch := strings.Count(moduleFile(t, "app/app_update.go"), "case keys.Key")

	for _, tc := range []struct {
		label string
		got   int
	}{
		{"registry entries", registry},
		{"dispatch-case lines", dispatch},
	} {
		assert.Equalf(t, tc.got, skillCount(t, skill, tc.label),
			"%s says %d %s; the tree has %d. Recount, do not adjust the old number — "+
				"that is how it went wrong last time.", skillPath, skillCount(t, skill, tc.label), tc.label, tc.got)
	}

	// The Config-field count is stated in a different sentence shape.
	fields := configJSONFields(t)
	re := regexp.MustCompile(`(\d+) json-tagged fields on ` + "`Config`")
	m := re.FindStringSubmatch(skill)
	require.Len(t, m, 2, "the skill no longer states a Config field count")
	assert.Equal(t, fmt.Sprint(fields), m[1],
		"%s says %s json-tagged fields on Config; the tree has %d", skillPath, m[1], fields)
}

// configJSONFields counts json-tagged fields on the Config struct itself.
//
// Scoped to the struct on purpose: the file also holds Profile, ClaudeAccount, AgyAccount
// and GHAccount, which carry their own tags — and the skill records that counting the
// whole file gives 59 and answers a different question than the claim above it.
func configJSONFields(t *testing.T) int {
	t.Helper()
	src := moduleFile(t, "config/types.go")
	_, rest, ok := strings.Cut(src, "type Config struct {")
	require.True(t, ok, "config/types.go no longer declares `type Config struct`")
	body, _, ok := strings.Cut(rest, "\n}")
	require.True(t, ok, "could not find the end of the Config struct")
	return strings.Count(body, "`json:")
}
