//go:build !windows

package update

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Two appliers must never run concurrently: the swap renames through fixed
// .old/.new names, so the second acquire has to fail fast, and releasing must
// reopen the door.
func TestAcquireUpdateLock_Exclusive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	unlock, err := acquireUpdateLock()
	require.NoError(t, err)

	_, err = acquireUpdateLock()
	require.Error(t, err, "a held lock must refuse a second applier")

	unlock()
	unlock2, err := acquireUpdateLock()
	require.NoError(t, err, "releasing the lock must allow the next update")
	unlock2()
}
