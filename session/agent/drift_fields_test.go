package agent

import "testing"

func TestAdaptersExposesSeededVersions(t *testing.T) {
	want := map[Key]struct {
		verified string
		gran     Granularity
	}{
		KeyClaude: {"2.1.185", GranularityPatch},
		KeyGemini: {"0.27", GranularityMinor},
		KeyCodex:  {"", GranularityPatch},
		KeyAider:  {"", GranularityPatch},
	}
	got := Adapters()
	if len(got) != len(want) {
		t.Fatalf("Adapters() returned %d adapters, want %d", len(got), len(want))
	}
	for _, a := range got {
		w, ok := want[a.Key]
		if !ok {
			t.Fatalf("unexpected adapter %q", a.Key)
		}
		if a.VerifiedVersion != w.verified {
			t.Errorf("%s VerifiedVersion = %q, want %q", a.Key, a.VerifiedVersion, w.verified)
		}
		if a.DriftGranularity != w.gran {
			t.Errorf("%s DriftGranularity = %d, want %d", a.Key, a.DriftGranularity, w.gran)
		}
	}
}
