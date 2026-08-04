package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// OrphanResult is the orphaned-tmux-server snapshot the doctor reports (#547).
//
// Servers are live tmux servers Atrium owns that are not the one it is running on.
// Stale are socket *files* in SocketDir with no server behind them — the cosmetic
// half of the same leak, since tmux never unlinks a socket when its server dies.
// Supported is false off Linux, where a process scan has no /proc to read; the stale
// file list still works there, which is why it is not gated on the same flag.
//
// Now is the instant the snapshot was taken, carried so that rendering an age is a
// pure function of the result rather than of when it happens to be printed.
type OrphanResult struct {
	Supported bool
	Servers   []tmux.OrphanServer
	SocketDir string
	Stale     []tmux.StaleSocket
	Now       time.Time
}

// orphanScan and staleScan are seams so CheckOrphans' assembly is testable without a
// live tmux server, mirroring oom.go's.
var (
	orphanScan = tmux.ScanServers
	staleScan  = tmux.ScanStaleSockets
)

// CheckOrphans gathers the orphan snapshot. Like the other doctor checks it never
// fails and never mutates: it reads /proc and issues read-only `tmux -S …
// display-message` probes, so it is safe beside a live TUI. It deletes nothing and
// signals nothing — every remedy it can offer is printed for the user to run.
func CheckOrphans(ctx context.Context) OrphanResult {
	servers, supported := orphanScan(ctx)
	stale, dir := staleScan(ctx)
	return OrphanResult{
		Supported: supported,
		Servers:   servers,
		SocketDir: dir,
		Stale:     stale,
		Now:       time.Now(),
	}
}

// RenderOrphans formats the orphan snapshot under an "Orphaned tmux servers:"
// header, parallel to RenderOOM.
//
// The header says "tmux servers" rather than a bare "orphans" because doctor already
// uses that word for a Claude login the account list no longer names
// (ui/overlay/accounts.go) — two unrelated things called orphans in one report is one
// too many.
//
// A clean host prints "none", not an empty section: per RenderGates' doc comment,
// "the check ran and found nothing" and "the check silently had nothing to say" must
// not look identical. That matters more here than anywhere else in doctor, because
// the failure this section exists for is invisible by construction.
func RenderOrphans(r OrphanResult) string {
	var b strings.Builder
	b.WriteString("Orphaned tmux servers:\n")

	if !r.Supported {
		// The stale-file list below still works off Linux, so this names what is
		// missing rather than declaring the whole section unavailable.
		b.WriteString("  server scan unavailable — finding a server whose socket no longer resolves\n")
		b.WriteString("  needs a process inventory, which is Linux-only\n")
	} else if len(r.Servers) == 0 && len(r.Stale) == 0 {
		b.WriteString("  none\n")
		return b.String()
	}

	for _, s := range r.Servers {
		renderOrphanServer(&b, s, r.Now)
	}
	renderStaleSockets(&b, r)
	return b.String()
}

// renderOrphanServer writes one server's row plus the remedy that fits its class.
// The remedy is the point of the row: an unreachable server has no tmux command that
// can name it, and saying so is the difference between a report and a dead end.
func renderOrphanServer(b *strings.Builder, s tmux.OrphanServer, now time.Time) {
	switch {
	case !s.ReachableKnown:
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachability unknown  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		b.WriteString("      → tmux could not be run, so nothing here is proven; `atrium reap` lists\n")
		b.WriteString("        these and never kills them\n")
	case s.Reachable:
		fmt.Fprintf(b, "  pid %d  socket %s  up %s  reachable  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		fmt.Fprintf(b, "      → tmux -S %s kill-server\n", s.SocketPath)
	default:
		fmt.Fprintf(b, "  ⚠ pid %d  socket %s  up %s  UNREACHABLE  %s\n",
			s.PID, s.Socket, HumanAge(now.Sub(s.Started)), childSummary(s.Children))
		fmt.Fprintf(b, "      → no tmux command can name this server: %s does not answer for it.\n", s.SocketPath)
		b.WriteString("        `atrium reap --kill` is the only thing that can stop it\n")
	}
	if s.CWDDeleted {
		b.WriteString("        its working directory has been deleted, so its run is long gone\n")
	}
}

// renderStaleSockets writes the class-(a) list: socket files with no server behind
// them.
//
// The remedy names the exact files this run probed, rather than a `find … -name
// 'atrium-*' -delete` glob. A glob re-matches at the moment the user runs it, so it
// can take a socket bound between the report and the command — including the live
// one, which lives in this same directory. #584 shipped a glob delete over this
// directory and it cost thirteen running sessions; naming verified paths cannot do
// that. Atrium prints the command and never runs it.
func renderStaleSockets(b *strings.Builder, r OrphanResult) {
	if len(r.Stale) == 0 {
		if r.SocketDir != "" {
			fmt.Fprintf(b, "  stale socket files: none in %s\n", r.SocketDir)
		}
		return
	}
	fmt.Fprintf(b, "  stale socket files: %d in %s — files only, no server behind them\n",
		len(r.Stale), r.SocketDir)
	paths := make([]string, 0, len(r.Stale))
	for _, s := range r.Stale {
		paths = append(paths, s.Path)
	}
	fmt.Fprintf(b, "      → rm -- %s\n", strings.Join(paths, " "))
}

// childSummary describes what a server is holding, which is what makes killing it a
// decision rather than a formality: these are real agents, and they may hold unpushed
// work (#267). Distinct command names are listed, not pids — `atrium reap` names
// every pid at the point where one is about to die.
func childSummary(kids []tmux.ChildProc) string {
	if len(kids) == 0 {
		return "holds nothing"
	}
	seen := map[string]bool{}
	var comms []string
	for _, k := range kids {
		if k.Comm != "" && !seen[k.Comm] {
			seen[k.Comm] = true
			comms = append(comms, k.Comm)
		}
	}
	sort.Strings(comms)
	unit := "processes"
	if len(kids) == 1 {
		unit = "process"
	}
	if len(comms) == 0 {
		return fmt.Sprintf("holds %d %s", len(kids), unit)
	}
	return fmt.Sprintf("holds %d %s (%s)", len(kids), unit, strings.Join(comms, ", "))
}

// HumanAge renders a duration in the largest two units that stay readable. A
// negative age — clock skew, or a start time from another machine — clamps to zero
// rather than printing a negative uptime.
//
// Exported so `atrium reap`'s confirmation prompt dates a server exactly the way the
// doctor row does: the user reads one and then the other about the same process, and
// two spellings of the same uptime read as two different facts.
func HumanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
