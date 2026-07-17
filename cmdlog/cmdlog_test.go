package cmdlog

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func rec(session string, err bool) Record {
	return Record{Argv: "git status", Session: session, Start: time.Now(), Err: err}
}

// The ring is bounded: adding more than maxRecords keeps only the most recent
// maxRecords, dropping the oldest. (Mutation guard: an unbounded store would fail
// the length assertion.)
func TestRing_Bounded(t *testing.T) {
	Reset()
	for i := 0; i < maxRecords+250; i++ {
		Add(Record{Argv: "cmd", Session: "s", Start: time.Now()})
	}
	got := Snapshot()
	if len(got) != maxRecords {
		t.Fatalf("Snapshot len = %d, want capped at %d", len(got), maxRecords)
	}
}

// Snapshot returns newest-first.
func TestSnapshot_NewestFirst(t *testing.T) {
	Reset()
	Add(Record{Argv: "first", Start: time.Now()})
	Add(Record{Argv: "second", Start: time.Now()})
	got := Snapshot()
	if len(got) != 2 || got[0].Argv != "second" || got[1].Argv != "first" {
		t.Fatalf("want [second, first], got %+v", got)
	}
}

func TestFailures_And_ForSession(t *testing.T) {
	Reset()
	Add(rec("alpha", false))
	Add(rec("beta", true))
	Add(rec("alpha", true))

	fails := Failures()
	if len(fails) != 2 {
		t.Fatalf("Failures len = %d, want 2", len(fails))
	}
	for _, f := range fails {
		if !f.Err {
			t.Errorf("Failures returned a non-error record: %+v", f)
		}
	}

	alpha := ForSession("alpha")
	if len(alpha) != 2 {
		t.Fatalf("ForSession(alpha) len = %d, want 2", len(alpha))
	}
	for _, r := range alpha {
		if r.Session != "alpha" {
			t.Errorf("ForSession(alpha) returned %q", r.Session)
		}
	}
	if n := len(ForSession("missing")); n != 0 {
		t.Errorf("ForSession(missing) = %d, want 0", n)
	}
}

// RecordCmd reads the exit code and OS-captured stderr from an *exec.ExitError.
func TestRecordCmd_CapturesExitAndStderr(t *testing.T) {
	Reset()
	// A command that writes to stderr and exits non-zero. Output() populates
	// ExitError.Stderr because we don't set cmd.Stderr ourselves.
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	start := time.Now()
	_, err := cmd.Output()
	RecordCmd(cmd.Args, "sess", start, nil, err)

	got := Snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	r := got[0]
	if !r.Err || r.Exit != 3 {
		t.Errorf("Err=%v Exit=%d, want Err=true Exit=3", r.Err, r.Exit)
	}
	if r.Session != "sess" {
		t.Errorf("Session=%q, want sess", r.Session)
	}
	if want := "boom"; !strings.Contains(r.Stderr, want) {
		t.Errorf("Stderr=%q, want it to contain %q", r.Stderr, want)
	}
}

// A successful command records exit 0 and no error/stderr.
func TestRecordCmd_Success(t *testing.T) {
	Reset()
	cmd := exec.CommandContext(context.Background(), "true")
	start := time.Now()
	err := cmd.Run()
	RecordCmd(cmd.Args, "", start, nil, err)
	r := Snapshot()[0]
	if r.Err || r.Exit != 0 || r.Stderr != "" {
		t.Errorf("success record wrong: %+v", r)
	}
}

// Add is safe under concurrent writers and stays bounded.
func TestRing_ConcurrentAdd(t *testing.T) {
	Reset()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				Add(Record{Argv: "x", Start: time.Now()})
			}
		}()
	}
	wg.Wait()
	if n := len(Snapshot()); n != maxRecords {
		t.Fatalf("after 1600 concurrent adds, Snapshot len = %d, want %d", n, maxRecords)
	}
}
