//go:build linux

package tmux

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procCostSupported reports whether this platform can price a live process. Linux
// can, via /proc. See proccost_other.go for the rest.
const procCostSupported = true

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

// ticksToDuration converts a USER_HZ tick count to a duration.
//
// The multiply is split rather than done as ticks*time.Second/userHZ, which
// overflows int64 on any host up for a few years.
func ticksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks/userHZ)*time.Second +
		time.Duration(ticks%userHZ)*(time.Second/userHZ)
}

// readProcCost prices one live process. ok is false when the process is gone or
// procfs refuses the read, which for a fleet measurement means "drop this sample",
// never "the cost was zero".
func readProcCost(pid int) (procCost, bool) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
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
			cost.Private, cost.PrivateIsPss = pss, true
			return cost, true
		}
	}
	// smaps_rollup arrived in 4.14 and can be absent under a hardened or containerised
	// procfs, so resident size is the fallback rather than the sample being dropped —
	// an over-count that is labelled is worth more than a hole.
	statm, err := os.ReadFile(filepath.Join(dir, "statm"))
	if err != nil {
		return cost, true
	}
	if rss, ok := parseStatmRSSBytes(string(statm)); ok {
		cost.Private = rss
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
// The pty master is matched on the exact target rather than a "pts" substring so a
// pane's own /dev/pts/N slave — which an attached client legitimately holds — is not
// counted as one more of the masters this measurement is about.
func classifyFD(target string) fdClass {
	switch {
	case target == "/dev/ptmx":
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
