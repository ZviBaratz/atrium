package session

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// identityRaceTurns is how many accessor calls each case below drives.
//
// A loop rather than a single call because the race detector reports only the
// interleavings it actually observes: it holds a bounded shadow history per memory word,
// so one unlucky pair can complete with the two accesses too far apart to be compared.
// Both sides here are nanoseconds — unlike #718's, whose reader was a millisecond-scale
// EnsureSession — so the count has to buy the overlap that a slow side gives for free. At
// 5000 an unlocked mutant was caught roughly one run in three: the reader finished before
// the writer goroutine had been scheduled at all. This count plus driveIdentityRace's
// start-barrier is what makes every mutation in the matrix fail on every run.
const identityRaceTurns = 200_000

// raceInstance is an unstarted instance carrying only an identity. Nothing below reaches
// tmux or git, so it needs neither testutil.RequireTmux nor a teardown.
func raceInstance() *Instance {
	return &Instance{ident: identity{title: "before", branch: "zvi/before"}}
}

// identitySink is where every read below lands, and it is load-bearing rather than
// decorative: `_ = i.DisplayName()` lets the compiler delete the load, because an accessor
// whose result is unused and whose body is inlinable has nothing left to do — and a load
// that was deleted is a load the detector cannot instrument. That is not hypothetical. The
// DisplayName case SURVIVED its mutation (lock removed, `just test-race` still green) until
// the read had somewhere to go; the other accessors happened to escape the same fate only
// because a `defer` blocks inlining, which the mutation also removes.
var identitySink string

// driveIdentityRace runs read against write concurrently: write spins until read has taken
// its turns, so the fast side never finishes first and leaves the detector nothing to pair.
func driveIdentityRace(read func() string, write func(turn int)) {
	stop := make(chan struct{})
	// running closes once the writer has taken a turn, so the reader cannot run its whole
	// loop before the writer goroutine is scheduled — which is how an unguarded mutant
	// escaped detection on two runs in three.
	running := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for turn := 0; ; turn++ {
			select {
			case <-stop:
				return
			default:
			}
			write(turn)
			if turn == 0 {
				close(running)
			}
		}
	}()
	<-running
	for range identityRaceTurns {
		identitySink = read()
	}
	close(stop)
	wg.Wait()
}

// TestIdentityReadsDoNotRaceItsWrites is #795's guard: each identity field read against the
// write that lands on it, one case per field, so removing any single accessor's lock turns
// exactly one case red and names the field it belongs to.
//
// THE DETECTOR IS THE ASSERTION, AND ONLY UNDER -race. This test passes vacuously under
// `just test`, `just ci`, and both non-race CI test jobs; the only gate that can fail it is
// `just test-race` and CI's "Race detector" job. Its sibling
// TestEveryIdentityAccessorTakesTheLock is what the normal gate can see.
//
// To watch a case fail, delete the RLock/RUnlock pair from the accessor it names in
// identity.go (shrink the mutation until it compiles: the deferred RUnlock goes with the
// RLock) and run `just test-race`. Each yields "WARNING: DATA RACE" pairing the write in
// AdoptRename, SetBranch, SetDisplayName or SetNote against the read in the accessor.
//
// The writers are fabricated rather than driven through Rename: the I/O half deliberately
// adopts nothing, so it is AdoptRename and the setters that are under test. Each writer
// alternates between two values, so every turn is a real write rather than a store of the
// value already there.
//
// One writer has no case here and cannot get one: SetTitle refuses a started instance, so
// there is no reachable state in which it runs beside a reader on another goroutine. Its
// lock is held to the tree by TestEveryIdentityAccessorTakesTheLock alone — measured, not
// assumed: dropping the lock from SetTitle leaves every case below green.
func TestIdentityReadsDoNotRaceItsWrites(t *testing.T) {
	t.Run("Title", func(t *testing.T) {
		i := raceInstance()
		driveIdentityRace(func() string { return i.Title() }, func(turn int) {
			i.AdoptRename(RenamedIdentity{Title: renameTurnValue(turn, "after", "after-again")})
		})
	})

	t.Run("Branch", func(t *testing.T) {
		i := raceInstance()
		driveIdentityRace(func() string { return i.Branch() }, func(turn int) {
			i.AdoptRename(RenamedIdentity{
				Title:  "after",
				Branch: renameTurnValue(turn, "zvi/after", "zvi/after-again"),
			})
		})
	})

	// Start publishes the branch from its own goroutine, so AdoptRename is not the only
	// writer this field has to be safe against — and it is the writer the update-thread-only
	// argument the old code relied on never covered.
	t.Run("Branch/SetBranch", func(t *testing.T) {
		i := raceInstance()
		driveIdentityRace(func() string { return i.Branch() }, func(turn int) {
			i.SetBranch(renameTurnValue(turn, "zvi/started", "zvi/restarted"))
		})
	})

	// A set label: the accessor returns displayName and never reaches title, so this case
	// is about the displayName field alone.
	t.Run("DisplayName/set", func(t *testing.T) {
		i := raceInstance()
		i.SetDisplayName("label")
		driveIdentityRace(func() string { return i.DisplayName() }, func(turn int) {
			i.SetDisplayName(renameTurnValue(turn, "label", "other label"))
		})
	})

	// The fallback: with no label, DisplayName reads title, so it is a second route onto
	// the field AdoptRename writes — the reason #718's guard set names DisplayName at all.
	// Its writer is AdoptRename, not SetDisplayName, since the label must stay empty.
	t.Run("DisplayName/fallback", func(t *testing.T) {
		i := raceInstance()
		require.Empty(t, i.Identity().DisplayName,
			"precondition: only an unset label makes DisplayName fall back to title")
		driveIdentityRace(func() string { return i.DisplayName() }, func(turn int) {
			i.AdoptRename(RenamedIdentity{Title: renameTurnValue(turn, "after", "after-again")})
		})
	})

	t.Run("Note", func(t *testing.T) {
		i := raceInstance()
		driveIdentityRace(func() string { return i.Note() }, func(turn int) {
			i.SetNote(renameTurnValue(turn, "blocked on review", "ready to merge"))
		})
	})

	// Identity() is the whole-family snapshot, so it must be safe against every writer at
	// once — the case that fails if a future field joins the struct without joining the lock.
	t.Run("Identity", func(t *testing.T) {
		i := raceInstance()
		driveIdentityRace(func() string { id := i.Identity(); return id.Title + id.Branch + id.DisplayName + id.Note }, func(turn int) {
			i.AdoptRename(RenamedIdentity{
				Title:  renameTurnValue(turn, "after", "after-again"),
				Branch: renameTurnValue(turn, "zvi/after", "zvi/after-again"),
			})
			i.SetDisplayName(renameTurnValue(turn, "label", "other label"))
			i.SetNote(renameTurnValue(turn, "blocked", "ready"))
		})
	})
}

func renameTurnValue(turn int, even, odd string) string {
	if turn%2 == 0 {
		return even
	}
	return odd
}

// TestAdoptRenameSwapsTitleAndBranchTogether pins the consistency half of the fix, which
// the detector cannot see: a reader must never observe the new title beside the old branch.
// AdoptRename writes both under one acquisition, so Identity() — which reads both under one
// acquisition — can only return a pair from the same side of the rename.
func TestAdoptRenameSwapsTitleAndBranchTogether(t *testing.T) {
	i := raceInstance()
	before := i.Identity()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			i.AdoptRename(RenamedIdentity{Title: "after", Branch: "zvi/after"})
		}
	}()

	for range identityRaceTurns {
		got := i.Identity()
		require.Contains(t,
			[]Identity{before, {Title: "after", Branch: "zvi/after"}}, got,
			"a snapshot must come from one side of the rename or the other, never half of each")
	}
	close(stop)
	wg.Wait()
}
