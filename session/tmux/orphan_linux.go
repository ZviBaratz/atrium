//go:build linux

package tmux

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// orphanScanSupported reports whether this platform can inventory processes. Linux
// can, via /proc.
const orphanScanSupported = true

const (
	// procRoot is the procfs mount every reader below goes through.
	procRoot = "/proc"

	// tmuxCommMarker is the cheap prefilter for "might be a tmux server".
	//
	// It is a substring test, not an equality test, and that is deliberate: tmux's
	// Linux setproctitle shim builds "<progname>: server (<socket>)", truncates it to
	// the 16-byte prctl name and trims at the last space, so "tmux: server" falls out
	// only because getprogname() happens to be 4 characters. The stable part is the
	// program name. This filter is about cost, not about identity — ownership is
	// decided by the socket name, further down.
	tmuxCommMarker = "tmux"

	// deletedSuffix is what the kernel appends to a /proc/<pid>/cwd link target when
	// the directory it names has been removed — the signature of a run whose temp
	// root was cleaned up around a server that outlived it.
	deletedSuffix = " (deleted)"

	// listeningFlags is the /proc/net/unix Flags column of a socket that has been
	// listen()ed on — SO_ACCEPTCON. It is the filter that separates a server's bound
	// socket from a client endpoint connected to it: both rows can carry the same
	// path, so without it a client's pid resolves to the server's socket and is
	// reported as owning it.
	listeningFlags = "00010000"

	// userHZ is the tick rate /proc/<pid>/stat's starttime is expressed in. The
	// kernel has exported proc times in a fixed USER_HZ of 100 since 2.6,
	// independently of CONFIG_HZ, so this is a constant of the /proc interface
	// rather than of the running kernel.
	userHZ = 100

	// The 1-indexed /proc/<pid>/stat fields this package reads, counted from the
	// start of the line; parseStat re-bases them past the comm.
	statStateField     = 3
	statPPidField      = 4
	statStartTimeField = 22
)

// procStat is what one /proc/<pid>/stat line is read for: the process name, its
// parent, and when it started.
//
// StartTicks is signed rather than the kernel's unsigned long long so the conversion
// to a time.Duration is exact and total: a value that would not fit fails the parse
// instead of wrapping, and no real uptime comes close.
type procStat struct {
	Comm       string
	PPid       int
	StartTicks int64
}

// bootTime returns the wall-clock instant the machine booted, from /proc/stat's
// btime line. It is a constant for the life of the process, so it is read once.
var bootTime = sync.OnceValues(readBootTime)

// readBootTime parses btime (seconds since the epoch) out of /proc/stat.
func readBootTime() (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, found := strings.CutPrefix(line, "btime ")
		if !found {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

// procStartTime returns when the process started, from /proc/<pid>/stat's starttime
// plus the boot time. ok is false when the process is gone or either read fails —
// never a zero time passed off as an answer, because callers turn this into a
// PID-reuse decision.
func procStartTime(pid int) (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, false
	}
	st, ok := parseStat(string(raw))
	if !ok {
		return time.Time{}, false
	}
	return startTimeOf(st.StartTicks)
}

// startTimeOf converts a starttime in USER_HZ ticks to a wall-clock instant.
func startTimeOf(ticks int64) (time.Time, bool) {
	boot, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	// Split the conversion rather than multiplying ticks by time.Second, which
	// overflows int64 on a host up for a few years.
	return boot.Add(time.Duration(ticks/userHZ)*time.Second +
		time.Duration(ticks%userHZ)*(time.Second/userHZ)), true
}

// parseStat reads the comm, parent pid and starttime out of a raw /proc/<pid>/stat
// line.
//
// It splits after the LAST ") " rather than splitting the whole line, because field
// 2 is the comm in parentheses and the kernel neither escapes nor quotes it. A tmux
// server's comm is "tmux: server"; the embedded space shifts every later column, so
// a whole-line split reads the wrong one — measured on a live server, the naive
// index yields 0 for starttime, which then formats as the machine's boot time. That
// is a plausible-looking wrong answer, not a visible failure, and it would silently
// defeat the PID-reuse guard that compares two readings of this value.
func parseStat(raw string) (procStat, bool) {
	// A comm may itself contain ") " (it is an arbitrary 15-byte string), so the
	// last occurrence is the one that closes it: everything after is fixed-width.
	open := strings.IndexByte(raw, '(')
	closed := strings.LastIndex(raw, ") ")
	if open < 0 || closed < open {
		return procStat{}, false
	}
	fields := strings.Fields(raw[closed+len(") "):])
	ppidIdx := statPPidField - statStateField
	startIdx := statStartTimeField - statStateField
	if len(fields) <= startIdx {
		return procStat{}, false
	}
	ppid, err := strconv.Atoi(fields[ppidIdx])
	if err != nil {
		return procStat{}, false
	}
	ticks, err := strconv.ParseInt(fields[startIdx], 10, 64)
	if err != nil {
		return procStat{}, false
	}
	return procStat{Comm: raw[open+1 : closed], PPid: ppid, StartTicks: ticks}, true
}

// listeningSockets maps inode to filesystem path for every listening unix socket on
// the host, from a single read of /proc/net/unix.
//
// The kernel keeps the path it was bound to even after that file is unlinked, which
// is the entire reason an orphaned tmux server whose TMUX_TMPDIR root was deleted
// can still be named (#547). Abstract sockets (paths starting with "@") are skipped:
// they have no file, so nothing downstream — the socket-name predicate, `tmux -S` —
// can use one.
func listeningSockets() map[uint64]string {
	raw, err := os.ReadFile(filepath.Join(procRoot, "net", "unix"))
	if err != nil {
		return nil
	}
	socks := map[uint64]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		// Columns: Num RefCount Protocol Flags Type St Inode Path. The header line
		// and any path-less row fall out of the field count or the parses below.
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[3] != listeningFlags {
			continue
		}
		inode, err := strconv.ParseUint(fields[6], 10, 64)
		if err != nil {
			continue
		}
		// Take the path from the raw line rather than fields[7]: a path may contain
		// spaces, and the kernel prints it unquoted.
		path := fieldsTail(line, 7)
		if path == "" || strings.HasPrefix(path, "@") {
			continue
		}
		socks[inode] = path
	}
	return socks
}

// fieldsTail returns what remains of line after n whitespace-separated fields, with
// leading whitespace trimmed. "" when the line has fewer than n fields.
func fieldsTail(line string, n int) string {
	s := line
	for range n {
		s = strings.TrimLeft(s, " \t")
		j := strings.IndexAny(s, " \t")
		if j < 0 {
			return ""
		}
		s = s[j:]
	}
	return strings.TrimSpace(s)
}

// socketPathFor returns the path of the listening unix socket pid has bound, or ""
// when it has none. listening is the map from listeningSockets, read once by the
// caller so a scan over many pids does not re-read /proc/net/unix per candidate.
func socketPathFor(pid int, listening map[uint64]string) string {
	if len(listening) == 0 {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(procRoot, strconv.Itoa(pid), "fd"))
	if err != nil {
		// Not our uid, or the process exited between listing and reading.
		return ""
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "fd", e.Name()))
		if err != nil {
			continue
		}
		inode, ok := socketInode(target)
		if !ok {
			continue
		}
		if path, ok := listening[inode]; ok {
			return path
		}
	}
	return ""
}

// procEntry is one live process as the single /proc pass recorded it.
type procEntry struct {
	stat procStat
	uid  uint32
}

// inventoryCandidates returns the processes worth classifying as Atrium tmux
// servers: owned by this uid, plausibly tmux by comm, with their bound socket path,
// start time, cwd-deleted flag and children attached.
//
// It never decides ownership — that is assembleServers' job, from the socket name.
// The comm test here is a cost filter that fails open (see tmuxCommMarker), and the
// uid test is a hard one: another user's server is unkillable anyway, and listing it
// is the privacy concern #445 raised.
func inventoryCandidates(ctx context.Context) []candidate {
	procs := readProcTable(ctx)
	if len(procs) == 0 {
		return nil
	}
	uid := uint32(os.Getuid()) //nolint:gosec // a uid is never negative and always fits.

	// Which pids are candidates, decided before the children pass so that pass can
	// attribute a child to its parent in one sweep.
	isCandidate := map[int]bool{}
	for pid, e := range procs {
		if e.uid == uid && strings.Contains(e.stat.Comm, tmuxCommMarker) {
			isCandidate[pid] = true
		}
	}
	if len(isCandidate) == 0 {
		return nil
	}

	kids := map[int][]ChildProc{}
	for pid, e := range procs {
		if !isCandidate[e.stat.PPid] {
			continue
		}
		started, ok := startTimeOf(e.stat.StartTicks)
		if !ok {
			continue
		}
		kids[e.stat.PPid] = append(kids[e.stat.PPid], ChildProc{PID: pid, Comm: e.stat.Comm, Started: started})
	}
	for ppid := range kids {
		sort.Slice(kids[ppid], func(i, j int) bool { return kids[ppid][i].PID < kids[ppid][j].PID })
	}

	// One read of /proc/net/unix serves every candidate's socket lookup.
	listening := listeningSockets()

	cands := make([]candidate, 0, len(isCandidate))
	for pid := range isCandidate {
		started, ok := startTimeOf(procs[pid].stat.StartTicks)
		if !ok {
			continue
		}
		cands = append(cands, candidate{
			PID:        pid,
			SocketPath: socketPathFor(pid, listening),
			Started:    started,
			CWDDeleted: cwdDeleted(pid),
			Children:   kids[pid],
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].PID < cands[j].PID })
	return cands
}

// readProcTable walks /proc once, recording each live process's stat line and owning
// uid. Processes that exit mid-walk simply drop out: every read that fails is
// skipped rather than reported, because a scan of a moving target has no consistent
// snapshot to promise.
func readProcTable(ctx context.Context) map[int]procEntry {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	procs := make(map[int]procEntry, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return procs
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory
		}
		// The /proc/<pid> directory is owned by the process's real uid, so one stat
		// answers ownership without parsing /proc/<pid>/status.
		info, err := os.Stat(filepath.Join(procRoot, e.Name()))
		if err != nil {
			continue
		}
		sys, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		st, ok := parseStat(string(raw))
		if !ok {
			continue
		}
		procs[pid] = procEntry{stat: st, uid: sys.Uid}
	}
	return procs
}

// cwdDeleted reports whether the process's working directory has been removed. The
// kernel marks such a link target with a " (deleted)" suffix.
func cwdDeleted(pid int) bool {
	target, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "cwd"))
	if err != nil {
		return false
	}
	return strings.HasSuffix(target, deletedSuffix)
}

// socketInode parses the inode out of an fd symlink target of the form
// "socket:[12345]". ok is false for any other kind of fd.
func socketInode(target string) (uint64, bool) {
	rest, found := strings.CutPrefix(target, "socket:[")
	if !found {
		return 0, false
	}
	rest, found = strings.CutSuffix(rest, "]")
	if !found {
		return 0, false
	}
	inode, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return inode, true
}
