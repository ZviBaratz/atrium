package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVariantTitleIsTheSuffixScheme pins the literal shape against literals: the scheme is
// written out here so a change to VariantTitle has to come through this file. Production
// code cites VariantTitle rather than spelling it; other tests build expected titles by
// hand, which is what makes this the assertion that would catch the change rather than
// merely fail alongside it.
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
