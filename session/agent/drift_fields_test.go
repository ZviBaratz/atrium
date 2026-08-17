package agent

import "testing"

func TestAdaptersExposesSeededVersions(t *testing.T) {
	want := map[Key]struct {
		verified string
		gran     Granularity
		gates    map[string]bool
	}{
		// Claude is the only adapter whose UI is chosen by a remote gate rather than
		// by its version; the pin records that every capture in registry.go came from
		// the ungated (hint-list) footer branch. #337.
		KeyClaude: {"2.1.210", GranularityMinor, map[string]bool{"tengu_copper_thistle": false}},
		// Deliberately NOT moved by #713, which re-drove the folder-trust gate and nothing
		// else: the busy and confirmation literals are bundle-grep presence only and
		// generateNameGemini's contract was last checked here, so 0.27 is still the last
		// version at which the WHOLE adapter was verified. registry.go's gemini header
		// carries the argument, including why the drifted warning that results is the
		// cheaper error. A future bump belongs to a drive that covers all four surfaces,
		// or to the per-surface split in #721.
		KeyGemini: {"0.27", GranularityMinor, nil},
		KeyCodex:  {"0.147.0", GranularityMinor, nil},
		KeyAider:  {"0.86.2", GranularityMinor, nil},
		KeyAgy:    {"1.1.11", GranularityMinor, nil},
	}
	got := Adapters()
	if len(got) != 5 {
		t.Fatalf("Adapters() returned %d adapters, want 5", len(got))
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
		if len(a.VerifiedGates) != len(w.gates) {
			t.Errorf("%s pins %d gates, want %d", a.Key, len(a.VerifiedGates), len(w.gates))
			continue
		}
		for _, g := range a.VerifiedGates {
			want, ok := w.gates[g.Name]
			if !ok {
				t.Errorf("%s pins unexpected gate %q", a.Key, g.Name)
				continue
			}
			if g.Value != want {
				t.Errorf("%s gate %s pinned %t, want %t", a.Key, g.Name, g.Value, want)
			}
		}
	}
}
