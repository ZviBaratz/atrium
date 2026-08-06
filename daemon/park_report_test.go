package daemon

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/parkreport"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubStorage is a config.InstanceStorage whose save can be made to fail, which is the
// only way to drive saveAndReport's ordering invariant. It stores nothing: these tests are
// about which of the two writes happens, not about what state.json ends up containing.
type stubStorage struct {
	saveErr error
	saved   int
}

func (s *stubStorage) SaveInstances(json.RawMessage) error {
	s.saved++
	return s.saveErr
}
func (s *stubStorage) GetInstances() json.RawMessage { return json.RawMessage("[]") }
func (s *stubStorage) DeleteAllInstances() error     { return nil }

func newStubStorage(t *testing.T, saveErr error) (*session.Storage, *stubStorage) {
	t.Helper()
	stub := &stubStorage{saveErr: saveErr}
	storage, err := session.NewStorage(stub)
	require.NoError(t, err)
	return storage, stub
}

// sandbox points HOME at a fresh temp dir so each test spools into its own data dir. The
// package TestMain already pins HOME away from the real one; this keeps tests from seeing
// each other's reports.
func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func report(titles ...string) session.DeferredRecovery {
	d := session.DeferredRecovery{Limit: 2}
	for _, title := range titles {
		d.Sessions = append(d.Sessions, session.ParkedSession{Title: title, Path: "/repo/web"})
	}
	return d
}

// TestSaveAndReportSpoolsThePark is #622's fix at the daemon end: the report the daemon
// has nowhere to toast reaches the next TUI through the spool instead of being discarded.
func TestSaveAndReportSpoolsThePark(t *testing.T) {
	sandbox(t)
	storage, stub := newStubStorage(t, nil)

	saveAndReport(storage, nil, report("alpha", "bravo"))

	require.Equal(t, 1, stub.saved, "the instances are still persisted")
	spooled, ok := parkreport.Read(time.Now())
	require.True(t, ok, "the park must not be silent")
	assert.Equal(t, []parkreport.Session{
		{Title: "alpha", Path: "/repo/web"},
		{Title: "bravo", Path: "/repo/web"},
	}, spooled.Sessions, "identified as the pair, so a reader can reconcile it")
	assert.Equal(t, 2, spooled.Limit, "in the number the loader actually applied")
}

// TestSaveAndReportSpoolsNothingWhenTheSaveFailed is the ordering invariant, and the
// reason the spool write lives after the save rather than at the load site where the
// report is produced.
//
// A park is durable only as the Paused row SaveInstances writes. If that write failed
// those sessions are still recorded Running, so the next TUI recovers them — and a report
// claiming capacity parked them would name sessions running in front of the user, which
// is the same class of false claim this whole change exists to remove.
func TestSaveAndReportSpoolsNothingWhenTheSaveFailed(t *testing.T) {
	sandbox(t)
	storage, stub := newStubStorage(t, errors.New("disk full"))

	saveAndReport(storage, nil, report("alpha"))

	require.Equal(t, 1, stub.saved)
	_, ok := parkreport.Read(time.Now())
	assert.False(t, ok, "no park is durable, so there is nothing to explain")
}

// The overwhelmingly common shutdown parks nothing. It must leave no file behind: a
// report naming no session would be read, found empty and discarded on the next launch,
// having cost a write for nothing.
func TestSaveAndReportSpoolsNothingWhenNothingWasParked(t *testing.T) {
	sandbox(t)
	storage, stub := newStubStorage(t, nil)

	saveAndReport(storage, nil, session.DeferredRecovery{})

	require.Equal(t, 1, stub.saved, "the save still happens; it is the report that is skipped")
	_, ok := parkreport.Read(time.Now())
	assert.False(t, ok)
}
