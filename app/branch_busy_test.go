package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/require"
)

// isBranchBusyError recognizes a *git.BranchCheckedOutError directly and through
// wrapping (errors.As), and rejects everything else — the contract the resume path
// and the batch summary both rely on.
func TestIsBranchBusyError(t *testing.T) {
	busy := &git.BranchCheckedOutError{Branch: "session/x", Path: "/repo"}

	got, ok := isBranchBusyError(busy)
	require.True(t, ok)
	require.Equal(t, busy, got)

	// Wrapped still matches, since errors.As unwraps.
	got, ok = isBranchBusyError(fmt.Errorf("resume failed: %w", busy))
	require.True(t, ok)
	require.Equal(t, busy, got)

	// An unrelated error does not match.
	got, ok = isBranchBusyError(errors.New("some other failure"))
	require.False(t, ok)
	require.Nil(t, got)

	// nil is not a branch-busy error.
	got, ok = isBranchBusyError(nil)
	require.False(t, ok)
	require.Nil(t, got)
}
