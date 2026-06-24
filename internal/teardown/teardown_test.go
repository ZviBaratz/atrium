package teardown

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/log"
)

// captureErrorLog redirects log.ErrorLog to a buffer for the duration of fn and
// returns what was written, so a test can assert whether a method logged.
func captureErrorLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.ErrorLog.SetOutput(&buf)
	defer log.ErrorLog.SetOutput(io.Discard)
	fn()
	return buf.String()
}

func TestRecordNilIsNoop(t *testing.T) {
	var tc Errors
	if tc.Record("do thing", nil) {
		t.Fatal("Record(nil) should return false")
	}
	if err := tc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after only a nil Record", err)
	}
}

func TestAddNilIsNoop(t *testing.T) {
	var tc Errors
	tc.Add(nil)
	if err := tc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after only a nil Add", err)
	}
}

func TestRecordWrapsAndReturnsTrue(t *testing.T) {
	var tc Errors
	sentinel := errors.New("boom")
	if !tc.Record("commit changes", sentinel) {
		t.Fatal("Record(non-nil) should return true")
	}
	err := tc.Err()
	if err == nil {
		t.Fatal("Err() should be non-nil after a recorded error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Err() does not unwrap to the original error: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "failed to commit changes") {
		t.Errorf("Err() = %q, want it to contain the op prefix", got)
	}
}

func TestRecordLogs(t *testing.T) {
	var tc Errors
	out := captureErrorLog(t, func() {
		tc.Record("commit changes", errors.New("boom"))
	})
	if !strings.Contains(out, "failed to commit changes") {
		t.Errorf("Record should log the wrapped error, logged %q", out)
	}
}

func TestWrapRetainsButDoesNotLog(t *testing.T) {
	var tc Errors
	sentinel := errors.New("boom")
	var recorded bool
	out := captureErrorLog(t, func() {
		recorded = tc.Wrap("cleanup git worktree", sentinel)
	})
	if !recorded {
		t.Fatal("Wrap(non-nil) should return true")
	}
	if out != "" {
		t.Errorf("Wrap should not log, logged %q", out)
	}
	err := tc.Err()
	if !errors.Is(err, sentinel) {
		t.Errorf("Wrap should retain the error for Err(): %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "failed to cleanup git worktree") {
		t.Errorf("Wrap should wrap with the op prefix, got %q", got)
	}
}

func TestWrapNilIsNoop(t *testing.T) {
	var tc Errors
	if tc.Wrap("do thing", nil) {
		t.Fatal("Wrap(nil) should return false")
	}
	if err := tc.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after only a nil Wrap", err)
	}
}

func TestErrEmptyIsNil(t *testing.T) {
	var tc Errors
	if err := tc.Err(); err != nil {
		t.Fatalf("zero-value Err() = %v, want nil", err)
	}
}

func TestErrSingleErrorStringIsUnwrapped(t *testing.T) {
	var tc Errors
	sentinel := errors.New("only one")
	tc.Add(sentinel)
	err := tc.Err()
	if !errors.Is(err, sentinel) {
		t.Errorf("Err() with one error should unwrap to it: %v", err)
	}
	if got := err.Error(); got != sentinel.Error() {
		t.Errorf("Err() = %q, want the single error's own message %q", got, sentinel.Error())
	}
}

func TestErrJoinsMultiple(t *testing.T) {
	var tc Errors
	e1 := errors.New("first")
	e2 := errors.New("second")
	tc.Add(e1)
	tc.Record("second step", e2)
	err := tc.Err()
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Errorf("Err() should unwrap to both errors: %v", err)
	}
}
