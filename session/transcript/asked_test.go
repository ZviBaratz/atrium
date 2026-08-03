package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// askedRoot builds a fake claude config root whose only transcript for cwd holds
// content, returning the root.
func askedRoot(t *testing.T, cwd, content string) string {
	t.Helper()
	root := t.TempDir()
	writeFileWithMtime(t, filepath.Join(root, "projects", sanitizeCWD(cwd), "session.jsonl"),
		content, time.Now())
	return root
}

// assistantLine wraps prose in the assistant-entry envelope captured from a real
// transcript (see testdata/asked.jsonl) so a test case exercises the same decode path
// production does, not a hand-simplified shape.
func assistantLine(text string) string {
	return `{"isSidechain":false,"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-7",` +
		`"content":[{"type":"text","text":` + quote(text) + `}]}}` + "\n"
}

// quote JSON-escapes s. Hand-rolled rather than json.Marshal so a fixture line stays
// readable in a failure message.
func quote(s string) string {
	out := []rune{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, r)
		}
	}
	return string(append(out, '"'))
}

// TestEndedAsking_Fixture drives the captured fixture end to end. Its final main-session
// turn asks a question with a trailing parenthetical — the exact shape the measurement
// found the narrower "last line ends with '?'" rule missing — and a SIDECHAIN entry
// asking its own question precedes it, so this also proves a sub-agent's question cannot
// carry the main turn.
func TestEndedAsking_Fixture(t *testing.T) {
	const cwd = "/home/zvi/work"
	data, err := os.ReadFile(filepath.Join("testdata", "asked.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	root := askedRoot(t, cwd, string(data))

	asked, stamp, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if !asked {
		t.Error("asked = false, want true (the turn ends with a question plus a parenthetical)")
	}
	if stamp.Path == "" || stamp.Size == 0 {
		t.Errorf("stamp not populated: %+v", stamp)
	}
}

// TestEndedAsking_SidechainQuestionAlone pins the sidechain filter on its own: a
// transcript whose ONLY question is a sub-agent's must not read as an ask.
//
// The sidechain entry is deliberately LAST. Ordering it before the main turn's reply —
// the arrangement the fixture uses, and the obvious way to write this — proves nothing:
// "last qualifying entry wins" would reach the main-session statement either way, so the
// test passes with the isSidechain check deleted. Verified by deleting it.
func TestEndedAsking_SidechainQuestionAlone(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := askedRoot(t, cwd,
		assistantLine("Done. Dispatching the sweep now.")+
			`{"isSidechain":true,"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"Should I keep going?"}]}}`+"\n")

	asked, _, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if asked {
		t.Error("asked = true, want false — a sidechain question must not carry the main turn")
	}
}

// TestEndedAsking_Rule covers the measured rule's decision boundary. Every "want true"
// case here is a real shape lifted from the 2026-08-02 corpus sweep; every "want false"
// case is one the sweep showed a naive rule getting wrong.
func TestEndedAsking_Rule(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain question", "Want me to implement the hold?", true},
		{"question then parenthetical",
			"Want me to run it for you, or will you do it? (I held off since it ends your agent.)", true},
		{"question then explanatory sentence",
			"Want me to dispatch the automation? It modifies `main`, so I'll only trigger it on your go-ahead.", true},
		{"question with a quoted phrase after it",
			`Should I use the "atrium" name here?`, true},

		{"plain statement", "The PR is green across all 13 checks and ready to merge.", false},
		{"quoted question inside a statement",
			`The fix was asking the reviewer "which of my reasons is wrong?" rather than guessing.`, false},
		// The typographic pair is the one claude's own prose actually reaches for, so
		// masking only the ASCII quote would leave the commonest shape uncovered.
		{"typographic-quoted question inside a statement",
			"The fix was asking the reviewer “which of my reasons is wrong?” rather than guessing.", false},
		{"question mark inside inline code",
			"The fleet view already supports `?format=json`, so no new endpoint is needed.", false},
		// An UNTERMINATED fence is what actually exercises the stripping. A closed one
		// cannot: its own "```" is then the last non-empty line and carries no '?', so
		// the case passes with fence handling deleted. Verified by deleting it.
		{"unterminated fence swallows the rest",
			"Run this to confirm:\n\n```sh\ncurl 'http://x/y?a=1'", false},
		{"closed fenced '?' with a trailing statement",
			"Ran the check:\n\n```sh\ncurl 'http://x/y?a=1'\n```\n\nAll six archives are present.", false},
		{"a real question after a fenced block still counts",
			"Ran this:\n\n```sh\ngo test ./...\n```\n\nWant me to open the PR?", true},
		{"question earlier, statement last",
			"Want me to merge it?\n\nEither way the branch is pushed and CI is green.", false},
		{"imperative ask (the known structural miss)",
			"Say the word and I'll post it and resolve the thread.", false},
	}

	const cwd = "/home/zvi/work"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := askedRoot(t, cwd, assistantLine(tc.text))
			asked, _, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
			if err != nil {
				t.Fatalf("EndedAsking: %v", err)
			}
			if asked != tc.want {
				t.Errorf("asked = %v, want %v for %q", asked, tc.want, tc.text)
			}
		})
	}
}

// TestEndedAsking_NoTextBlockFailsSafe pins the fail-safe branch: a turn whose last
// assistant entry is a tool call carries no prose to answer, so the rule must return
// false rather than reaching back to an earlier entry's question — which would hold a
// queued prompt on a question the agent already moved past.
func TestEndedAsking_NoTextBlockFailsSafe(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := askedRoot(t, cwd,
		assistantLine("Should I check the other file too?")+
			`{"isSidechain":false,"type":"assistant","message":{"model":"claude-opus-4-7","content":[{"type":"tool_use","name":"Read","input":{}}]}}`+"\n")

	asked, _, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if asked {
		t.Error("asked = true, want false — a trailing tool_use entry has no prose to answer")
	}
}

// TestEndedAsking_LastTextBlockWins pins which block is read when one entry carries
// several: the LAST one. Claude interleaves prose and tool calls within a turn, so an
// earlier block's question is not what the turn ended on.
func TestEndedAsking_LastTextBlockWins(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := askedRoot(t, cwd,
		`{"isSidechain":false,"type":"assistant","message":{"model":"claude-opus-4-7","content":[`+
			`{"type":"text","text":"Should I start with the poll path?"},`+
			`{"type":"tool_use","name":"Read","input":{}},`+
			`{"type":"text","text":"Started with the poll path. It is done."}]}}`+"\n")

	asked, _, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if asked {
		t.Error("asked = true, want false — the LAST text block is a statement")
	}
}

// TestEndedAsking_StampShortCircuit pins the change-detection contract shared with
// LatestModel: an unchanged transcript returns (false, prev, nil) WITHOUT reading the
// file, so the caller must distinguish that false from a real verdict by comparing
// stamps rather than trusting the bool.
func TestEndedAsking_StampShortCircuit(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := askedRoot(t, cwd, assistantLine("Want me to go ahead?"))

	asked, stamp, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if !asked {
		t.Fatal("first read: asked = false, want true")
	}

	asked2, stamp2, err := EndedAsking(context.Background(), "claude", cwd, stamp, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking (unchanged): %v", err)
	}
	if asked2 {
		t.Error("unchanged read returned asked = true; it must short-circuit to false")
	}
	if !stamp2.Equal(stamp) {
		t.Errorf("unchanged read advanced the stamp: %+v -> %+v", stamp, stamp2)
	}
}

// TestEndedAsking_Unsupported pins that non-claude agents get no verdict at all, so
// codex/gemini/aider sessions keep their existing delivery behaviour untouched.
func TestEndedAsking_Unsupported(t *testing.T) {
	_, _, err := EndedAsking(context.Background(), "codex", "/home/zvi/work", Stamp{}, Options{Root: t.TempDir()})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestEndedAsking_ContextCancelled pins that an already-cancelled context is reported
// before any filesystem I/O, so app shutdown unwinds the read.
func TestEndedAsking_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := EndedAsking(ctx, "claude", "/home/zvi/work", Stamp{}, Options{Root: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestEndedAsking_TrailingToolUseKeepsTheProse pins the shape blocksEndAsking's doc used
// to describe backwards: within ONE entry, blocks after the last text block are ignored,
// so [text(question), tool_use] reads as true. TestEndedAsking_NoTextBlockFailsSafe covers
// a separate trailing entry with no prose at all, and TestEndedAsking_LastTextBlockWins
// ends on text — neither reaches this case, which is why the doc drifted unnoticed.
//
// True is the wanted answer, not merely the observed one: on an idle pane a trailing
// tool_use means the call never produced a result, and holding a queued prompt is the
// safe direction.
func TestEndedAsking_TrailingToolUseKeepsTheProse(t *testing.T) {
	const cwd = "/home/zvi/work"
	root := askedRoot(t, cwd,
		`{"isSidechain":false,"type":"assistant","message":{"model":"claude-opus-4-7","content":[`+
			`{"type":"text","text":"Should I remove the file now?"},`+
			`{"type":"tool_use","name":"Bash","input":{}}]}}`+"\n")

	asked, _, err := EndedAsking(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("EndedAsking: %v", err)
	}
	if !asked {
		t.Error("asked = false, want true — the last TEXT block still carries the question")
	}
}
