//go:build linux

package tmux

// The procfs cost reader behind the #800 attach-fanout measurement.
//
// Test-only, and deliberately so. Its single consumer is the opt-in harness in
// fanout_measure_linux_test.go; nothing Atrium ships reads a process's CPU ticks,
// Pss or descriptor classes. Shipping it as ordinary package code would put a
// completeness flag (PrivateKnown) into session/tmux under a naming convention that
// internal/doctor's evidence audit reserves for channels a health report may claim
// from — and this is not one. When something in the product does need per-process
// costs (doctor depth, #833), moving it out is the change that earns the audit row.
//
// The guards for everything here live in proccost_guards_linux_test.go.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procCost is what one live process costs, as procfs will admit to it.
//
// It exists for #800: Atrium holds one `tmux attach-session` pty client per started
// session for that session's whole life (Restore), and #548 filed that fanout as
// unmeasured. Pricing it needs a per-process reading rather than a whole-machine
// one, because the question is marginal — what does one *more* session add — and
// because the cost is spread across three processes: Atrium, the client, and the
// tmux server serving it.
//
// CPU and ChildCPU are cumulative since the process started, so a rate is the
// difference between two readings over a known window — never a single reading,
// which describes a lifetime and not a moment.
type procCost struct {
	// CPU is utime+stime: time this process's own threads spent on a CPU.
	CPU time.Duration
	// ChildCPU is cutime+cstime: time its *reaped* children spent. A child that is
	// still running contributes nothing here — it is priced by reading its own pid.
	ChildCPU time.Duration
	// Private is the memory that would actually be returned if the process went
	// away: Pss where the kernel offers smaps_rollup, resident size where it does
	// not. The difference matters here because N tmux clients share one text
	// segment, so RSS counts the same pages N times and answers "how much does one
	// more cost?" with roughly an order of magnitude too much.
	Private int64
	// PrivateIsPss records which of those two Private holds, so a report can say
	// which question it answered rather than letting a reader assume the better one.
	// False also when PrivateKnown is false; check that first.
	PrivateIsPss bool
	// PrivateKnown separates "this process holds no memory" from "neither smaps_rollup
	// nor statm would answer". Private is 0 in both cases and they mean opposite
	// things — the same conflation the CPU fields avoid by failing the whole read.
	// The CPU fields stay valid when this is false, which is why an unreadable memory
	// figure does not fail the sample outright.
	PrivateKnown bool
}

// fdCounts is a process's open file descriptors sorted into the classes that say
// something about per-session fanout.
//
// Ptmx and EventPoll are the two #548 asks about by name: one pty per attach client
// is the fanout itself, and the epoll count was flagged there as "flat but large and
// unexplained" — 125 at 11 sessions — which is only answerable by watching how it
// moves as sessions are added, not by reading one number.
//
// Total counts every descriptor, including ones no class claimed and ones whose link
// vanished mid-walk, so Ptmx+EventPoll+PidFD+Socket+Other can be less than Total.
type fdCounts struct {
	Ptmx      int
	EventPoll int
	PidFD     int
	Socket    int
	Other     int
	Total     int
}

const (
	// The 1-indexed /proc/<pid>/stat fields the cost reader adds to the ones
	// orphan_linux.go already names; parseProcCPU re-bases them past the comm the
	// same way parseStat does.
	//
	// All four, not just the first two. #546's original investigation reported
	// Atrium's cost from utime+stime alone and missed 19.4% of a core — a third of
	// the total — sitting in cutime+cstime, because a process blocked in wait4 pays
	// for its child only once that child is reaped. A reader that stops at field 15
	// gives a plausible-looking wrong answer rather than an obvious failure, which
	// is precisely the shape that survives review.
	statUTimeField  = 14
	statSTimeField  = 15
	statCUTimeField = 16
	statCSTimeField = 17
)

// readProcCost prices one live process. ok is false when the process is gone or
// procfs refuses the read, which for a fleet measurement means "drop this sample",
// never "the cost was zero".
func readProcCost(pid int) (procCost, bool) {
	return readProcCostIn(filepath.Join(procRoot, strconv.Itoa(pid)))
}

// readProcCostIn is readProcCost over an explicit /proc/<pid> directory, so a test can
// build one file by file.
//
// That seam exists for the memory fallback specifically: on any host that offers
// smaps_rollup the statm branch is unreachable, so without a fake procfs the fallback
// ships measured by nothing — and it is the branch that decides whether a reported
// number is a Pss or a resident size several times larger.
func readProcCostIn(dir string) (procCost, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procCost{}, false
	}
	own, children, ok := parseProcCPU(string(raw))
	if !ok {
		return procCost{}, false
	}
	cost := procCost{CPU: own, ChildCPU: children}
	if rollup, err := os.ReadFile(filepath.Join(dir, "smaps_rollup")); err == nil {
		if pss, ok := parsePssBytes(string(rollup)); ok {
			cost.Private, cost.PrivateIsPss, cost.PrivateKnown = pss, true, true
			return cost, true
		}
	}
	// smaps_rollup arrived in 4.14 and can be absent under a hardened or containerised
	// procfs, so resident size is the fallback rather than the sample being dropped —
	// an over-count that is labelled is worth more than a hole. Labelled is the
	// operative word: PrivateIsPss stays false here, and a report that prints this
	// number as a Pss is off by roughly an order of magnitude on shared pages.
	statm, err := os.ReadFile(filepath.Join(dir, "statm"))
	if err != nil {
		return cost, true
	}
	if rss, ok := parseStatmRSSBytes(string(statm)); ok {
		cost.Private, cost.PrivateKnown = rss, true
	}
	return cost, true
}

// parseProcCPU reads the four CPU tick fields out of a raw /proc/<pid>/stat line.
//
// It splits after the LAST ") " for the same reason parseStat does: field 2 is the
// comm in parentheses, the kernel neither escapes nor quotes it, and a tmux server's
// comm is "tmux: server" — the embedded space shifts every later column. Getting
// this wrong on the very processes this file exists to price would report a busy
// client as idle.
func parseProcCPU(raw string) (own, children time.Duration, ok bool) {
	closed := strings.LastIndex(raw, ") ")
	if closed < 0 || strings.IndexByte(raw, '(') < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(raw[closed+len(") "):])
	// Re-based past the comm: field 3 is index 0, so field N is index N-3.
	uIdx := statUTimeField - statStateField
	sIdx := statSTimeField - statStateField
	cuIdx := statCUTimeField - statStateField
	csIdx := statCSTimeField - statStateField
	if len(fields) <= csIdx {
		return 0, 0, false
	}
	var ticks [4]int64
	for i, idx := range [4]int{uIdx, sIdx, cuIdx, csIdx} {
		v, err := strconv.ParseInt(fields[idx], 10, 64)
		if err != nil {
			return 0, 0, false
		}
		ticks[i] = v
	}
	return ticksToDuration(ticks[0]) + ticksToDuration(ticks[1]),
		ticksToDuration(ticks[2]) + ticksToDuration(ticks[3]), true
}

// parsePssBytes reads the Pss line out of a raw /proc/<pid>/smaps_rollup.
//
// Pss and not Rss: the whole question here is what one more attach client adds, and
// the client's text and libc pages are shared with every other one, so Rss answers a
// different question several times over.
func parsePssBytes(raw string) (int64, bool) {
	for _, line := range strings.Split(raw, "\n") {
		rest, found := strings.CutPrefix(line, "Pss:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		// "Pss:  1117 kB" — the value, then its unit. procfs states kB here for every
		// field; a line without both parts is not one this can price.
		if len(fields) < 2 || fields[1] != "kB" {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// parseStatmRSSBytes reads the resident-pages field (the second) out of a raw
// /proc/<pid>/statm and converts it to bytes.
func parseStatmRSSBytes(raw string) (int64, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

// countFDClasses sorts a live process's open descriptors into classes.
func countFDClasses(pid int) (fdCounts, bool) {
	return countFDClassesIn(filepath.Join(procRoot, strconv.Itoa(pid), "fd"))
}

// countFDClassesIn is countFDClasses over an explicit directory, so a test can build
// one out of dangling symlinks instead of needing a process that holds the fds.
//
// A descriptor that cannot be read is counted in Total and left out of every class:
// the walk races the process, and a link that vanishes mid-walk is still evidence the
// process held it a moment ago.
func countFDClassesIn(dir string) (fdCounts, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fdCounts{}, false
	}
	var c fdCounts
	for _, e := range entries {
		c.Total++
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		switch classifyFD(target) {
		case fdPtmx:
			c.Ptmx++
		case fdEventPoll:
			c.EventPoll++
		case fdPidFD:
			c.PidFD++
		case fdSocket:
			c.Socket++
		default:
			c.Other++
		}
	}
	return c, true
}

// fdClass names the kinds of descriptor countFDClassesIn separates.
type fdClass int

const (
	fdOther fdClass = iota
	fdPtmx
	fdEventPoll
	fdPidFD
	fdSocket
)

// classifyFD names what an fd symlink target is.
//
// The pty master is matched on exact targets rather than a "pts" substring so a
// pane's own /dev/pts/N slave — which an attached client legitimately holds — is not
// counted as one more of the masters this measurement is about. Both spellings of
// the master are accepted: a devpts mounted with newinstance (containers, some
// systemd setups) makes /dev/ptmx a symlink and the fd resolves to /dev/pts/ptmx
// instead. Missing that spelling would report a fleet holding zero ptys — the
// "the fanout is free" answer this measurement exists not to give by accident. The
// name still separates it from a slave, which is always /dev/pts/<number>.
func classifyFD(target string) fdClass {
	switch {
	case target == "/dev/ptmx", target == "/dev/pts/ptmx":
		return fdPtmx
	case strings.HasPrefix(target, "anon_inode:[eventpoll]"):
		return fdEventPoll
	case strings.HasPrefix(target, "anon_inode:[pidfd]"):
		return fdPidFD
	case strings.HasPrefix(target, "socket:["):
		return fdSocket
	default:
		return fdOther
	}
}
