package app

// The splash pattern vocabulary is defined twice — config.SplashVariants() for
// the settings panel, ui.splashVariantNames for the generators — in packages
// that cannot import each other. app imports both, so this is the only place
// the two can be held to each other.

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/require"
)

// TestSplashVocabularyAgrees pins the two hand-maintained lists to each other.
//
// Both failure directions are silent, which is why this is worth a test rather
// than a comment. A name in config but not ui falls through SetSplashVariant's
// lookup into the random branch: the settings panel offers a pattern, the user
// pins it, and they get a different one every launch with no error anywhere. A
// name in ui but not config is a generator no user can reach.
//
// Order is deliberately not compared: config's list is settings-panel display
// order, and ui's is a map. Only membership is a shared contract.
func TestSplashVocabularyAgrees(t *testing.T) {
	require.ElementsMatch(t, config.SplashVariants(), ui.SplashVariantNames(),
		"config.SplashVariants() and ui's pinnable names must list the same patterns; "+
			"a name in only one silently resolves to random")
}

// TestSplashRandomIsNotAVariantName guards the sentinel: config.GetSplash()
// returns SplashRandom for anything it does not recognize, so a variant that
// took that name would be unpinnable in a way no other test would notice —
// SetSplashVariant would pin it, and GetSplash would keep reporting random.
func TestSplashRandomIsNotAVariantName(t *testing.T) {
	require.NotContains(t, ui.SplashVariantNames(), config.SplashRandom,
		"%q is the random sentinel and cannot also name a pattern", config.SplashRandom)
}
