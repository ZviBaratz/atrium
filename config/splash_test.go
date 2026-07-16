package config

import (
	"testing"

	"github.com/ZviBaratz/atrium/splash"
)

// TestGetSplashDefaultsToRandom pins the normalization: nil receiver, empty
// field, and unknown (hand-edited) values all resolve to random mode.
func TestGetSplashDefaultsToRandom(t *testing.T) {
	var nilCfg *Config
	for name, got := range map[string]string{
		"nil config": nilCfg.GetSplash(),
		"empty":      (&Config{}).GetSplash(),
		"unknown":    (&Config{Splash: "sparkles"}).GetSplash(),
		// A name that WAS a variant until V5 retired it. Same path as any other
		// unknown value, and that is the whole decision: a stale pin degrades to
		// random silently, with no migration and no notice.
		"retired": (&Config{Splash: "nebula"}).GetSplash(),
	} {
		if got != SplashRandom {
			t.Errorf("%s: GetSplash() = %q, want %q", name, got, SplashRandom)
		}
	}
}

// TestGetSplashRoundTripsVariants guards every settings-panel option: a pinned
// pattern name must come back verbatim, never fall through to random.
//
// Iterated over splash.Variants() rather than the local SplashVariants() so the
// vocabulary this normalization is checked against is the engine's own: every
// generator the splash package ships must be a name config accepts and round-
// trips, or a pattern the settings panel offers silently degrades to random. The
// reverse direction (a config name with no generator) is app's vocab test.
func TestGetSplashRoundTripsVariants(t *testing.T) {
	for _, variant := range splash.Variants() {
		name := variant.String()
		if got := (&Config{Splash: name}).GetSplash(); got != name {
			t.Errorf("GetSplash(%q) = %q, want %q", name, got, name)
		}
	}
}
