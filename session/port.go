// Managed dev-server ports (#389): one TCP port per session, drawn from the range its
// repo declares, so two sessions of one repo can run the same server at once.
//
// The port is exported as $ATRIUM_PORT and {{.Session.Port}} through the same channel
// session_env rides, and is held for exactly as long as the session's WORKTREE is:
// allocated where the worktree is materialized (create, and again on resume), released
// on pause and on kill. That pairing is deliberate — a paused session has no directory
// to run a server in, and holding its port would starve a range in the one situation
// (many parked sessions, few ports) where a user is likeliest to hit the bottom of it.

package session

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
)

// portRegistry is the set of ports Atrium's own sessions hold.
//
// It exists because a bind probe cannot answer the question on its own: the port a
// session was handed is free until its dev server actually starts, and may never be
// bound at all — so two sessions created in the same second would both probe the same
// port free and both take it, which is precisely what AC 3 forbids.
//
// Process-wide state, which is sound here for the reason the daemon snapshot is: a
// per-data-dir flock (tui.lock, held in main.go) means one TUI owns a data dir, and the
// TUI is the only thing that creates or resumes sessions. Ports held by a session of
// ANOTHER data dir, or by any other program, are caught by the probe instead.
type portRegistry struct {
	mu sync.Mutex
	// held maps a port to the session title holding it. The owner is not decoration:
	// release is by owner, so a teardown that fires late cannot free a port a different
	// session has since been handed.
	held map[int]string
}

func newPortRegistry() *portRegistry { return &portRegistry{held: map[int]string{}} }

// livePorts is the registry every Instance allocates from. One per process, seeded at
// startup by the restored sessions themselves (FromInstanceData re-reserves the port a
// session was running on), so a fresh session cannot be handed a port an already
// running dev server is using.
var livePorts = newPortRegistry()

// portOwner is this session's key in the registry: its title AND its workspace path.
//
// Not the title alone. The list scopes titles per repo group, so two repositories can
// each hold a session called "web" — and reserve() treats a re-reservation by the same
// owner as success (the restore path re-running), so one key for two sessions would
// hand them both the same port and call it idempotent. Not the tmux name either, which
// is empty until the session's first Start and the port is allocated before that.
func (i *Instance) portOwner() string { return i.Title + "\x00" + i.Path }

// reserve claims a specific port for owner, reporting false when someone else holds it.
// Reserving a port the same owner already holds succeeds — it is the restore path
// re-running, not a collision.
func (r *portRegistry) reserve(port int, owner string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if holder, taken := r.held[port]; taken && holder != owner {
		return false
	}
	r.held[port] = owner
	return true
}

// release gives a port back, if owner is the one holding it.
func (r *portRegistry) release(port int, owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held[port] == owner {
		delete(r.held, port)
	}
}

// allocate hands owner the lowest port in rng that no session holds and nothing is
// listening on, reporting false when the range has none left.
//
// The bind probe races by construction: a port free at probe time can be taken before
// the session's server binds it. Holding the listener open until then is not an option
// — the whole point is to hand the port to another process — so the probe is a filter
// against what is ALREADY running, and the registry is what makes Atrium's own sessions
// exact.
func (r *portRegistry) allocate(rng repocfg.PortRange, owner string) (int, bool) {
	for port := rng.Lo; port <= rng.Hi; port++ {
		if !r.reserve(port, owner) {
			continue
		}
		if probePort(port) {
			return port, true
		}
		// Something outside Atrium holds it. Give the reservation back rather than
		// keeping the port off the range for the life of the process: whatever is
		// listening may well exit, and the next session should get it.
		r.release(port, owner)
	}
	return 0, false
}

// probePort is the bind probe, a package var so a test about the RESERVATION rules can
// run against a synthetic range instead of borrowing real ports from the machine it is
// on. The probe itself keeps its own test, with a real listener.
var probePort = portIsFree

// portIsFree reports whether a listener can take the port right now.
//
// BOTH the wildcard address and the loopback, because neither alone is conclusive on
// every platform Atrium runs on. Go sets SO_REUSEADDR on its listeners, and BSD reads
// that as permission to bind the same port on a DIFFERENT local address — so on macOS a
// wildcard probe succeeds against a server already serving 127.0.0.1:3000, and a
// loopback probe succeeds against one on 0.0.0.0:3000. Linux refuses both overlaps, so
// there one probe would have done; the pair is what makes the answer mean the same
// thing on both.
//
// This is the only socket Atrium binds, and it is opt-in: nothing here runs for a repo
// with no port_range. On macOS the wildcard half can raise the application firewall's
// "accept incoming connections?" prompt the first time — the price of detecting a
// server on 0.0.0.0, which the loopback probe cannot see there.
//
// Either bind failing is enough to call the port taken. That errs toward skipping a
// port, which costs one number out of a range; the other direction hands two servers
// the same port and shows up as a dev server that will not start.
func portIsFree(port int) bool {
	// context.Background(), and a ListenConfig only because the linter asks for one: a
	// bind is a single syscall with nothing to resolve and nothing to wait on, so there
	// is no operation here for a deadline to bound.
	for _, host := range []string{"", "127.0.0.1"} {
		var lc net.ListenConfig
		l, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = l.Close()
	}
	return true
}

// Port returns the session's managed port, or 0 when it holds none.
func (i *Instance) Port() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.port
}

// PortText is the port as the template context and the environment carry it: decimal
// text, empty when there is none. Empty rather than "0" is what lets the custom-command
// emptiness gate refuse `--port {{.Session.Port}}` on a session with no managed port.
func (i *Instance) PortText() string {
	if p := i.Port(); p != 0 {
		return fmt.Sprint(p)
	}
	return ""
}

// setPort records a port the registry has already granted this session.
func (i *Instance) setPort(port int) {
	i.mu.Lock()
	i.port = port
	i.mu.Unlock()
}

// reservePort brings the session's port into line with what its repo declares: it keeps
// the port it already holds, allocates one when it holds none, and releases one the
// config no longer asks for.
//
// Keeping an already-held port matters on the restart path: a running session's dev
// server is bound to the port persisted in state.json, and re-allocating would hand the
// session a different number while the server stayed where it was.
func (i *Instance) reservePort(s repocfg.Script) {
	rng, ok := s.Ports()
	if !ok {
		i.releasePort()
		return
	}
	if held := i.Port(); held != 0 {
		return
	}
	port, ok := livePorts.allocate(rng, i.portOwner())
	if !ok {
		// Not fatal: the session starts without a port, exactly as a repo with no
		// port_range does. Said out loud because the symptom otherwise is a dev server
		// command that silently runs with an empty --port.
		i.setPortProblem(fmt.Sprintf(
			"No port was free in %s for %q.\n\nThe session is running without $ATRIUM_PORT. "+
				"Free one by killing or pausing another session in this repo, or widen port_range in config.json.",
			rng, i.Title))
		log.WarningLog.Printf("port_range %s is exhausted; %q starts without a port", rng, i.Title)
		return
	}
	i.setPort(port)
}

// releasePort gives up the session's port, and does nothing when it holds none. Called
// wherever the worktree goes away: pause, and kill.
func (i *Instance) releasePort() {
	i.mu.Lock()
	port := i.port
	i.port = 0
	i.mu.Unlock()
	if port != 0 {
		livePorts.release(port, i.portOwner())
	}
}

// claimPersistedPort re-reserves the port a restored session was running on, so a
// session created later in the same run cannot be handed it.
//
// A port it cannot claim is dropped rather than kept: two sessions carrying one port is
// a state.json that was written by an older Atrium or edited by hand, and the one that
// keeps it is arbitrary — but a session that quietly runs on a port another session's
// server owns is a collision the user then debugs by hand.
func (i *Instance) claimPersistedPort() {
	port := i.Port()
	if port == 0 {
		return
	}
	if !livePorts.reserve(port, i.portOwner()) {
		log.WarningLog.Printf("session %q claimed port %d, which is already held; dropping it", i.Title, port)
		i.setPort(0)
	}
}

// setPortProblem records the one-line report for a session whose range had nothing free.
func (i *Instance) setPortProblem(msg string) {
	i.mu.Lock()
	i.portProblem = msg
	i.mu.Unlock()
}

// PortProblem returns the report for a session whose repo declares a port range with
// nothing free in it, or "" when there is nothing to say.
func (i *Instance) PortProblem() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.portProblem
}

// ClearPortProblem drops the report once it has been shown, so the preview tick cannot
// reopen it forever.
func (i *Instance) ClearPortProblem() {
	i.mu.Lock()
	i.portProblem = ""
	i.mu.Unlock()
}
