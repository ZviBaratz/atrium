//go:build !linux

package doctor

// oomScoreSupported is false off Linux, which has no /proc/<pid>/oom_score; the
// doctor renders the section as unavailable rather than failing.
const oomScoreSupported = false

// readOOMScore is unavailable off Linux.
func readOOMScore(pid int) (score, adj int, ok bool) { return 0, 0, false }
