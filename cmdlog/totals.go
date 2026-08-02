package cmdlog

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Total is the aggregate cost of every recorded invocation of one command verb.
type Total struct {
	Verb  string
	Count int
	Wall  time.Duration
	CPU   time.Duration
}

// Totals aggregates the ring by command verb, heaviest CPU first.
//
// This answers the question a Go profiler cannot: while a child process runs,
// Atrium sits in wait4 and contributes nothing to its own CPU profile, so pprof
// attributes zero to the subprocess share of a busy fleet even though that share
// measured 19.4% of a core against 37.2% in-process at 14 sessions (#546). A
// per-verb split is what turns "subprocesses cost something" into "git diff costs
// this much", which is what a fix has to be aimed at.
//
// The ring is bounded (maxRecords), so this is a rolling window — at the observed
// ~50 spawns/second it covers roughly the last ten seconds, not the whole session.
// Read it as a rate, never as a lifetime total.
func Totals() []Total { return TotalsOf(Snapshot()) }

// TotalsOf aggregates an arbitrary slice of records, so a caller that has already
// filtered (by session, or to failures only) reports the split for what it is
// actually showing rather than for the whole ring.
func TotalsOf(recs []Record) []Total {
	byVerb := map[string]*Total{}
	for _, r := range recs {
		v := r.Verb
		t := byVerb[v]
		if t == nil {
			t = &Total{Verb: v}
			byVerb[v] = t
		}
		t.Count++
		t.Wall += r.Dur
		t.CPU += r.CPU
	}
	out := make([]Total, 0, len(byVerb))
	for _, t := range byVerb {
		out = append(out, *t)
	}
	// CPU first, then count, then name — so the order is total even when several
	// verbs report zero CPU (a platform with no rusage, or a ring of launch
	// failures), which keeps the rendered list stable between frames.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Verb < out[j].Verb
	})
	return out
}

// preludeFlagsWithValues are the flags that may appear BEFORE a subcommand and
// consume the token after them. Atrium's tmux prelude is `-L <socket> -f <conf>`
// and git's is `-C <dir>` / `-c <cfg>`; without consuming the value, the socket
// name reads as the subcommand and every session gets its own verb.
var preludeFlagsWithValues = map[string]bool{"-L": true, "-f": true, "-C": true, "-c": true}

// verbOf reduces an argv to the "binary subcommand" pair that names what ran —
// `git diff`, `tmux capture-pane`, `gh pr`.
//
// It stops at the first subcommand rather than keeping the full argv because the
// point is aggregation: `git diff --numstat <sha>` and `git diff <other-sha>` are
// the same cost centre, and keeping their arguments would scatter one hot verb
// across as many buckets as there are sessions.
//
// It takes the argv as the OS has it, one element per token, because a prelude
// flag's value is arbitrary text. `git -C "/home/u/my repo" status` is four
// elements here and the value is skipped whole; recovered from log text it is
// five words, the skip eats half the path, and the verb becomes "git repo" — one
// bucket per repository, which is the scattering preludeFlagsWithValues exists to
// prevent. See verb for the recovery path and what it cannot do.
//
// The token it returns goes through redactArg, because this reads the raw argv
// rather than Redact's output and so is one of the three paths from an argv to
// displayed text (Redact and cmd.ToString are the others; all scrub). Today no
// secret can reach the returned token — every `-e NAME=<token>` Atrium injects is
// appended after the subcommand this stops at — but that is an ordering accident
// in another file (session/tmux/tmux.go), not a property of this function.
// Scrubbing here keeps "a secret is never recorded verbatim" true by construction,
// the way it was when Redact was the only such path.
func verbOf(argv []string) string {
	if len(argv) == 0 {
		return "?"
	}
	bin := filepath.Base(argv[0])
	for i := 1; i < len(argv); i++ {
		f := argv[i]
		if strings.HasPrefix(f, "-") {
			if preludeFlagsWithValues[f] {
				i++ // skip its value too
			}
			continue
		}
		return bin + " " + redactArg(f)
	}
	return bin
}

// verb recovers a verb from rendered log text, for a Record built from an argv
// string rather than by RecordCmd. It is a fallback: whitespace splitting cannot
// tell one token containing a space from two tokens, so a prelude flag whose
// value contains a space still mis-parses here. Anything capturing a real
// subprocess has the structured argv and must use verbOf.
func verb(argv string) string { return verbOf(strings.Fields(argv)) }
