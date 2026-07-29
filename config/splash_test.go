package config

import (
	"testing"

	"github.com/ZviBaratz/fresco/v2"
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
// Iterated over fresco.Variants() rather than the local SplashVariants() so the
// vocabulary this normalization is checked against is the engine's own: every
// generator the fresco package ships must be a name config accepts and round-
// trips, or a pattern the settings panel offers silently degrades to random. The
// reverse direction (a config name with no generator) is app's vocab test.
func TestGetSplashRoundTripsVariants(t *testing.T) {
	for _, variant := range fresco.Variants() {
		name := variant.String()
		if got := (&Config{Splash: name}).GetSplash(); got != name {
			t.Errorf("GetSplash(%q) = %q, want %q", name, got, name)
		}
	}
}

// TestGetSplashRoundTripsOff guards the one mode that is neither a generator nor
// the random sentinel. It has to survive normalization verbatim: if SplashOff
// ever fell through to random the way an unknown name does, the setting would
// silently re-enable the animation the user turned off.
func TestGetSplashRoundTripsOff(t *testing.T) {
	if got := (&Config{Splash: SplashOff}).GetSplash(); got != SplashOff {
		t.Errorf("GetSplash(%q) = %q, want %q", SplashOff, got, SplashOff)
	}
}

// TestSplashEnabled pins the predicate ui and app gate on. Everything that is
// not an explicit opt-out animates — a nil config, an unset field, a retired
// name and a pinned pattern alike — so a config that cannot be read or parsed
// degrades to the default experience rather than to a dead screen.
func TestSplashEnabled(t *testing.T) {
	var nilCfg *Config
	for name, tc := range map[string]struct {
		cfg  *Config
		want bool
	}{
		"nil config": {nilCfg, true},
		"empty":      {&Config{}, true},
		"random":     {&Config{Splash: SplashRandom}, true},
		"pinned":     {&Config{Splash: SplashVariants()[0]}, true},
		"retired":    {&Config{Splash: "nebula"}, true},
		"off":        {&Config{Splash: SplashOff}, false},
	} {
		if got := tc.cfg.SplashEnabled(); got != tc.want {
			t.Errorf("%s: SplashEnabled() = %v, want %v", name, got, tc.want)
		}
	}
}

// TestSplashOffIsNotAConfiguredVariant guards the sentinel from the config side:
// SplashVariants is the settings panel's pinnable vocabulary, and a generator
// that took the name "off" would be unreachable *and* would disable the splash
// whenever a user picked it. app's TestSplashOffIsNotAVariantName guards the
// engine side of the same collision.
func TestSplashOffIsNotAConfiguredVariant(t *testing.T) {
	for _, name := range SplashVariants() {
		if name == SplashOff {
			t.Errorf("%q is the disable sentinel and cannot also name a pattern", SplashOff)
		}
	}
}
