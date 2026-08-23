package tmux

import "time"

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
	PrivateIsPss bool
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
