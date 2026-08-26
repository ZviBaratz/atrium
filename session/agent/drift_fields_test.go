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
		// Moved by #736, which is the drive this comment used to ask for by name: "a drive
		// that covers all four surfaces". #736 drove three of them — the confirmation dialog
		// and the busy marker on a width ladder, and generateNameGemini's `gemini -p`
		// contract, in the same authenticated sessions — joining the two gates #713 and #717
		// had already driven at 0.55.1. How many surfaces that is in total, and which one is
		// still unprobed, is registry.go's gemini header to state and not this comment's to
		// re-derive: three files used to count it independently and two of them were wrong.
		// That header lists each surface with its capture, including the two things the
		// drive found that a bundle grep could not: both literals are PRESENT and neither is
		// reachable at every width. Moving the pin also forced #744: doctor flags drift only
		// when installed > verified, so passing 0.55.1 would have silenced the only amber a
		// 0.55.x user saw while headless naming was still broken for them. Fixed in the same
		// change rather than filed, in runGeminiHeadless.
		//
		// The other route this comment offered is still open and still better — a per-surface
		// pin (#721), which would have let #713 move the one surface it drove instead of
		// waiting for the expensive three.
		KeyGemini: {"0.55.1", GranularityMinor, nil},
		KeyCodex:  {"0.147.0", GranularityMinor, nil},
		KeyAider:  {"0.86.2", GranularityMinor, nil},
		KeyAgy:    {"1.1.11", GranularityMinor, nil},
		// Driven at 1.0.80 across three surfaces, each with its own verbatim width ladder:
		// the folder-trust gate, the approval prompt and the busy marker. registry.go's
		// copilot header enumerates them; this row only pins the version they were driven at.
		KeyCopilot: {"1.0.80", GranularityMinor, nil},
	}
	got := Adapters()
	// len(want), not a literal: the count and the table are the same fact, and a hardcoded
	// number is one more place to forget when an adapter lands.
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
