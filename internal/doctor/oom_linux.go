//go:build linux

package doctor

import (
	"os"
	"strconv"
	"strings"
)

// oomScoreSupported reports whether this platform exposes per-process OOM scores.
// Linux does, via /proc/<pid>/oom_score(_adj).
const oomScoreSupported = true

// readOOMScore returns a process's current OOM badness (oom_score) and its
// oom_score_adj knob from /proc. ok is false if either file is unreadable — the
// process exited, or it belongs to another user (its /proc entries are still
// world-readable, but a race can lose them). It never blocks and touches only the
// two small proc files.
func readOOMScore(pid int) (score, adj int, ok bool) {
	score, ok1 := readProcInt(pid, "oom_score")
	adj, ok2 := readProcInt(pid, "oom_score_adj")
	return score, adj, ok1 && ok2
}

func readProcInt(pid int, name string) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/" + name)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return n, true
}
