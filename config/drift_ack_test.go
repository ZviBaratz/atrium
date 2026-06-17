package config

import "testing"

func TestAckedDriftRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: never touch the real data dir

	s := DefaultState()
	if got := s.GetAckedDrift(); len(got) != 0 {
		t.Fatalf("fresh state GetAckedDrift() = %v, want empty", got)
	}
	// A single batched write persists both agents.
	if err := s.SetAckedDrift(map[string]string{"claude": "2.1.179", "gemini": "0.45.1"}); err != nil {
		t.Fatalf("SetAckedDrift: %v", err)
	}
	if got := s.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("GetAckedDrift()[claude] = %q, want 2.1.179", got)
	}

	// A later batch merges (doesn't replace) and persists.
	if err := s.SetAckedDrift(map[string]string{"claude": "2.1.180"}); err != nil {
		t.Fatalf("SetAckedDrift merge: %v", err)
	}

	reloaded := LoadState()
	if got := reloaded.GetAckedDrift()["claude"]; got != "2.1.180" {
		t.Errorf("after reload, GetAckedDrift()[claude] = %q, want 2.1.180", got)
	}
	if got := reloaded.GetAckedDrift()["gemini"]; got != "0.45.1" {
		t.Errorf("after reload, GetAckedDrift()[gemini] = %q, want 0.45.1 (merge dropped it)", got)
	}
}

func TestSetAckedDriftEmptyIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	if err := s.SetAckedDrift(nil); err != nil {
		t.Fatalf("SetAckedDrift(nil): %v", err)
	}
	if err := s.SetAckedDrift(map[string]string{}); err != nil {
		t.Fatalf("SetAckedDrift(empty): %v", err)
	}
	if got := s.GetAckedDrift(); len(got) != 0 {
		t.Errorf("GetAckedDrift() = %v, want empty after no-op writes", got)
	}
}

func TestGetAckedDriftReturnsCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := DefaultState()
	if err := s.SetAckedDrift(map[string]string{"claude": "2.1.179"}); err != nil {
		t.Fatalf("SetAckedDrift: %v", err)
	}
	// Mutating the returned map must not leak into persisted state.
	s.GetAckedDrift()["claude"] = "tampered"
	if got := s.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("GetAckedDrift returned an aliased map: got %q after external mutation", got)
	}
}
