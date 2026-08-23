package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVariantTitleIsTheSuffixScheme pins the literal shape against literals. It is the
// one place the <stem>-N spelling is written twice on purpose: everywhere else cites
// VariantTitle, so a change to the scheme has to come through here.
func TestVariantTitleIsTheSuffixScheme(t *testing.T) {
	require.Equal(t, "fix-auth-1", VariantTitle("fix-auth", 1))
	require.Equal(t, "fix-auth-2", VariantTitle("fix-auth", 2))
	require.Equal(t, "fix-auth-10", VariantTitle("fix-auth", 10))
}

// TestVariantTitleCanOutgrowMaxTitleLen pins the hazard main.planVariantTitles' length
// check exists for: a stem at the cap is not a stem a batch can be derived from, because
// the suffix grows a rune at every power of ten. Asserted rather than left to a comment
// so a later change to either constant cannot quietly make that check dead code — and
// asserted HERE because the other derivation, app.planVariantTitles, does not check at
// all (atrium#784), so a claim about "every caller" would be false.
func TestVariantTitleCanOutgrowMaxTitleLen(t *testing.T) {
	stem := strings.Repeat("a", MaxTitleLen)
	require.Len(t, []rune(stem), MaxTitleLen)

	require.Greater(t, len([]rune(VariantTitle(stem, 1))), MaxTitleLen,
		"a stem AT the cap overflows at the very first variant, so the check cannot be "+
			"deferred to the tail of a batch")
	require.Greater(t, len([]rune(VariantTitle(stem, 10))), len([]rune(VariantTitle(stem, 9))),
		"the suffix widens at ten, which is why a scan that fits at 9 can stop fitting later")
}
