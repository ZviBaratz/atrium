package session

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

const (
	testCutID     = "aaaa1111-1111-4111-8111-111111111111"
	testDroppedID = "bbbb2222-2222-4222-8222-222222222222"
	testForkID    = "cccc3333-3333-4333-8333-333333333333"
)

func testSeed() ForkSeed {
	return ForkSeed{
		SourceTranscript: "/home/zvi/.claude/projects/-home-zvi-src/source.jsonl",
		CutEntryID:       testCutID,
		DroppedMessageID: testDroppedID,
		NewSessionID:     testForkID,
	}
}

// stubForkEntries stands a transcript in front of verifyFork: present says which
// ids the forked file holds. Restored when the test ends.
func stubForkEntries(t *testing.T, present map[string]bool, err error) {
	t.Helper()
	original := ContainsForkEntries
	ContainsForkEntries = func(_ context.Context, _ string, ids ...string) (map[string]bool, error) {
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(ids))
		for _, id := range ids {
			out[id] = present[id]
		}
		return out, nil
	}
	t.Cleanup(func() { ContainsForkEntries = original })
}

// TestVerifyFork_RefusesAnUntruncatedFork is the assertion the whole feature
// rests on, at the layer that decides whether a session starts.
//
// Outside print mode claude takes --resume-session-at and ignores it: the run
// exits 0, the envelope reports no error, and the forked transcript is the entire
// conversation. Every signal except the transcript's contents says success. So the
// untruncated arm here is not an edge case — it is the exact shape of the defect
// #644 exists to prevent, and a verifyFork that passed it would ship a
// "fork from checkpoint" that silently forks from HEAD.
func TestVerifyFork_RefusesAnUntruncatedFork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present map[string]bool
		wantErr string
	}{
		{
			name:    "truncated fork",
			present: map[string]bool{testCutID: true, testDroppedID: false},
		},
		{
			// The control. Same run, same exit code, same JSON envelope — the flag was
			// simply not honoured. Only the dropped turn's presence separates it.
			name:    "untruncated: the dropped turn survived",
			present: map[string]bool{testCutID: true, testDroppedID: true},
			wantErr: "kept the turn it was meant to drop",
		},
		{
			// A fork that lost the prefix is a different failure and must not be
			// reported as the one above.
			name:    "the kept entry is missing",
			present: map[string]bool{testCutID: false, testDroppedID: false},
			wantErr: "missing the checkpoint it was cut at",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubForkEntries(t, tc.present, nil)
			err := verifyFork(context.Background(), "/home/zvi/work", "/cfg", testSeed())
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("verifyFork rejected a correctly truncated fork: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("verifyFork accepted a fork it should have refused (%s)", tc.name)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("verifyFork error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The refusal has to say what went wrong in claude's terms, because the user sees
// it as a failed session start with no pane to inspect: nothing else in Atrium
// will ever mention --resume-session-at, and "the fork failed" would leave a
// reader hunting a bug in Atrium's own argv.
func TestVerifyFork_UntruncatedRefusalNamesTheMechanism(t *testing.T) {
	stubForkEntries(t, map[string]bool{testCutID: true, testDroppedID: true}, nil)
	err := verifyFork(context.Background(), "/home/zvi/work", "/cfg", testSeed())
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"--resume-session-at", testDroppedID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// A fork with no dropped turn to look for cannot be verified, so the check must
// not quietly pass. Every real seed carries one — Checkpoint.MessageID is always
// set — which is why the guard is here rather than assumed.
func TestVerifyFork_NoDroppedMarkerStillChecksTheKeptEntry(t *testing.T) {
	seed := testSeed()
	seed.DroppedMessageID = ""
	stubForkEntries(t, map[string]bool{testCutID: false}, nil)
	if err := verifyFork(context.Background(), "/home/zvi/work", "/cfg", seed); err == nil {
		t.Error("verifyFork accepted a fork missing its kept entry")
	}
}

// TestForkArgv covers the flags claude refuses to work without, and the one it
// accepts and ignores.
//
// It is deliberately a weak test and says so: passing proves the argv carries
// --resume-session-at, not that claude honoured it. That is unprovable from here
// — it is what verifyFork reads the transcript for — so this exists to catch a
// flag dropped or misspelled, nothing more.
func TestForkArgv(t *testing.T) {
	argv := forkArgv(testSeed(), "carry on from here")
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"-p carry on from here",
		"--session-id " + testForkID,
		"--fork-session",
		"--resume " + testSeed().SourceTranscript,
		"--resume-session-at " + testCutID,
		"--output-format json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, argv)
		}
	}
	// --session-id is legal beside --resume only with --fork-session ("Error:
	// --session-id can only be used with --continue or --resume if --fork-session is
	// also specified"), and --resume-session-at is refused without --resume. Both
	// pairings are claude's, not Atrium's, so a reordering that drops one is a
	// startup failure rather than a test failure.
	for _, pair := range [][2]string{{"--session-id", "--fork-session"}, {"--resume-session-at", "--resume"}} {
		if strings.Contains(joined, pair[0]) && !strings.Contains(joined, pair[1]) {
			t.Errorf("%s is present without %s, which claude refuses", pair[0], pair[1])
		}
	}
	// The source is passed as a path, not an id: the fork runs in the NEW session's
	// worktree, and claude resolves a bare id inside the current directory's project
	// — which is not where the forking session's transcript lives.
	if !strings.Contains(joined, "/source.jsonl") {
		t.Errorf("--resume was not given the transcript path: %v", argv)
	}
}

// An incomplete seed must be refused before a subprocess is started: claude would
// answer an empty --resume-session-at by forking the whole conversation, which is
// the silent failure again.
func TestMaterializeFork_RefusesAnIncompleteSeed(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*ForkSeed)
	}{
		{"no transcript", func(s *ForkSeed) { s.SourceTranscript = "" }},
		{"no cut entry", func(s *ForkSeed) { s.CutEntryID = "" }},
		{"no session id", func(s *ForkSeed) { s.NewSessionID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := testSeed()
			tc.mut(&seed)
			// A nil executor is the assertion: reaching one would panic, so a passing
			// test is proof no subprocess was attempted.
			if err := MaterializeFork(context.Background(), nil, "claude", "/work", "", seed, "go"); err == nil {
				t.Error("MaterializeFork accepted an incomplete seed")
			}
		})
	}
}

// An empty prompt is refused before the subprocess, because claude's own refusal
// is unhelpful here: driven on 2.1.226, `-p ""` exits 1 with "No deferred tool
// marker found in the resumed session ... Provide a prompt to continue the
// conversation" — true, and about a mechanism a fork has nothing to do with.
func TestMaterializeFork_RefusesAnEmptyPrompt(t *testing.T) {
	for _, prompt := range []string{"", "   ", "\n"} {
		// Nil executor again: reaching it would panic.
		err := MaterializeFork(context.Background(), nil, "claude", "/work", "", testSeed(), prompt)
		if err == nil {
			t.Errorf("MaterializeFork accepted the prompt %q", prompt)
			continue
		}
		if !strings.Contains(err.Error(), "needs a prompt") {
			t.Errorf("prompt %q: error = %v, want it to name the requirement", prompt, err)
		}
	}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// claude validates the id itself ("Error: Invalid session ID. Must be a valid
// UUID") and refuses one already in use, so both the shape and the uniqueness are
// preconditions of a fork starting at all.
func TestNewSessionID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("NewSessionID = %q, which is not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewSessionID repeated %q", id)
		}
		seen[id] = true
	}
}

// forkEnv must point claude at the session's config dir and must NOT relocate
// $HOME. The fork's whole product is a file under that config dir; namingEnv's
// throwaway home would file the conversation where nothing looks for it, and —
// since credentials live in the config dir — strip the auth the call needs.
func TestForkEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/old/config")
	t.Setenv("HOME", "/home/zvi")

	env := forkEnv("/accounts/work")

	var configs, homes []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			configs = append(configs, kv)
		}
		if strings.HasPrefix(kv, "HOME=") {
			homes = append(homes, kv)
		}
	}
	if len(configs) != 1 || configs[0] != "CLAUDE_CONFIG_DIR=/accounts/work" {
		t.Errorf("CLAUDE_CONFIG_DIR entries = %v, want exactly the session's", configs)
	}
	if len(homes) != 1 || homes[0] != "HOME=/home/zvi" {
		t.Errorf("HOME entries = %v, want the real one left alone", homes)
	}

	// With no account pinned the ambient environment is inherited untouched.
	if got := forkEnv(""); len(got) != len(env) {
		t.Errorf("forkEnv(\"\") returned %d entries, want the same %d as the parent", len(got), len(env))
	}
}
