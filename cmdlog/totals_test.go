package cmdlog

import (
	"os/exec"
	"testing"
	"time"
)

// verb keeps the binary and its subcommand and nothing else, so one hot command
// aggregates into one bucket.
//
// The tmux rows are the ones that matter: Atrium's prelude is `-L <socket> -f
// <conf>`, and a flag whose value is not consumed makes the socket name read as
// the subcommand — which would scatter every capture across per-socket buckets and
// hide the very verb the aggregate exists to name.
func TestVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv string
		want string
	}{
		{"git subcommand", "git diff --numstat abc123", "git diff"},
		{"git with -C prelude", "git -C /repo status --porcelain", "git status"},
		{"tmux with -L/-f prelude", "tmux -L atrium -f /home/u/.atrium/atrium.conf capture-pane -p -e", "tmux capture-pane"},
		{"tmux has-session", "tmux -L atrium has-session -t=atrium_x", "tmux has-session"},
		{"absolute binary path", "/usr/bin/gh pr view 12", "gh pr"},
		{"binary with no subcommand", "true", "true"},
		{"flags only", "git --version", "git"},
		{"empty", "", "?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verb(tc.argv); got != tc.want {
				t.Errorf("verb(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

// Totals groups by verb and orders heaviest-CPU first, which is what makes the
// aggregate readable as "where is the subprocess time going".
func TestTotals_AggregatesByVerbHeaviestCPUFirst(t *testing.T) {
	Reset()
	add := func(argv string, cpu time.Duration) {
		Add(Record{Argv: argv, Start: time.Now(), Dur: 2 * cpu, CPU: cpu})
	}
	add("git diff --numstat a", 10*time.Millisecond)
	add("git diff --numstat b", 12*time.Millisecond)
	add("tmux -L atrium capture-pane -p", 5*time.Millisecond)
	add("git status --porcelain", 1*time.Millisecond)

	got := Totals()
	if len(got) != 3 {
		t.Fatalf("Totals() has %d verbs, want 3: %+v", len(got), got)
	}
	if got[0].Verb != "git diff" {
		t.Errorf("heaviest verb = %q, want %q", got[0].Verb, "git diff")
	}
	if got[0].Count != 2 {
		t.Errorf("git diff Count = %d, want 2", got[0].Count)
	}
	if want := 22 * time.Millisecond; got[0].CPU != want {
		t.Errorf("git diff CPU = %v, want %v", got[0].CPU, want)
	}
	if want := 44 * time.Millisecond; got[0].Wall != want {
		t.Errorf("git diff Wall = %v, want %v", got[0].Wall, want)
	}
	// The ordering is the assertion, not just the grouping: a stable sort on
	// insertion order would put capture-pane second by luck, so check the tail too.
	if got[1].Verb != "tmux capture-pane" || got[2].Verb != "git status" {
		t.Errorf("order = %q, %q; want tmux capture-pane, git status", got[1].Verb, got[2].Verb)
	}
}

// Verbs that tie on CPU still come back in a total order, so the rendered list
// cannot reshuffle between frames. Every record here reports zero CPU, which is
// what a platform with no rusage — or a ring full of launch failures — produces.
func TestTotals_ZeroCPUIsStillTotallyOrdered(t *testing.T) {
	Reset()
	Add(Record{Argv: "tmux -L atrium has-session -t=x", Start: time.Now()})
	Add(Record{Argv: "git status", Start: time.Now()})
	Add(Record{Argv: "git status", Start: time.Now()})

	got := Totals()
	if len(got) != 2 {
		t.Fatalf("Totals() has %d verbs, want 2", len(got))
	}
	// Count breaks the CPU tie, then name breaks a count tie.
	if got[0].Verb != "git status" || got[1].Verb != "tmux has-session" {
		t.Errorf("order = %q, %q; want git status, tmux has-session", got[0].Verb, got[1].Verb)
	}
}

// A prelude flag's value is arbitrary text, and Atrium passes real paths through
// two of them — `git -C <worktree>` and `tmux -f <conf>`. A repository under a
// directory with a space in its name used to bucket as "git repo", one verb per
// repository, which is precisely the scattering preludeFlagsWithValues exists to
// prevent and which its own comment claims it does.
//
// The fix is that RecordCmd resolves the verb from the argv the OS has, before
// Redact joins it into log text. This drives the real capture path rather than
// calling verbOf directly, because the defect was never in verbOf's logic — it
// was in which of the two argvs reached it.
func TestRecordCmd_ResolvesTheVerbBeforeArgvBecomesText(t *testing.T) {
	Reset()
	cmd := exec.CommandContext(t.Context(), "git", "-C", "/home/u/my repo", "status", "--porcelain")
	RecordCmd(cmd, "", time.Now(), nil, nil)

	recs := Snapshot()
	if len(recs) != 1 {
		t.Fatalf("Snapshot() has %d records, want 1", len(recs))
	}
	if got, want := recs[0].Verb, "git status"; got != want {
		t.Errorf("Verb = %q, want %q", got, want)
	}
	// Argv stays the joined display text the overlay renders; the point is that the
	// verb no longer depends on being able to split it back apart.
	if got, want := recs[0].Argv, "git -C /home/u/my repo status --porcelain"; got != want {
		t.Errorf("Argv = %q, want %q", got, want)
	}
	if got := Totals(); len(got) != 1 || got[0].Verb != "git status" {
		t.Errorf("Totals() = %+v, want a single \"git status\" bucket", got)
	}
}

// A record that arrives with no Verb — built from argv text rather than by
// RecordCmd — is still bucketed, so a hand-built record cannot silently land in
// an empty-named bucket alongside every other one.
func TestAdd_FillsAMissingVerbFromArgvText(t *testing.T) {
	Reset()
	Add(Record{Argv: "tmux -L atrium capture-pane -p", Start: time.Now()})

	recs := Snapshot()
	if len(recs) != 1 {
		t.Fatalf("Snapshot() has %d records, want 1", len(recs))
	}
	if got, want := recs[0].Verb, "tmux capture-pane"; got != want {
		t.Errorf("Verb = %q, want %q", got, want)
	}
}
