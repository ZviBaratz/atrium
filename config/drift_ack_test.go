package config

import "testing"

func TestAckedDriftRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: never touch the real data dir

	s := DefaultState()
	if got := s.GetAckedDrift(); len(got) != 0 {
		t.Fatalf("fresh state GetAckedDrift() = %v, want empty", got)
	}
	if err := s.SetAckedDrift("claude", "2.1.179"); err != nil {
		t.Fatalf("SetAckedDrift: %v", err)
	}
	if got := s.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("GetAckedDrift()[claude] = %q, want 2.1.179", got)
	}

	reloaded := LoadState()
	if got := reloaded.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("after reload, GetAckedDrift()[claude] = %q, want 2.1.179", got)
	}
}
