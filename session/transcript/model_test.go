package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// modelRoot builds a fake claude config root whose only transcript for cwd has
// the given content and mtime, returning the root and the transcript path.
func modelRoot(t *testing.T, cwd, content string, mtime time.Time) (root, path string) {
	t.Helper()
	root = t.TempDir()
	path = filepath.Join(root, "projects", sanitizeCWD(cwd), "session.jsonl")
	writeFileWithMtime(t, path, content, mtime)
	return root, path
}

// TestLatestModel_NewestAssistantWins locks in the selection rule on the shared
// fixture: the last non-sidechain, non-synthetic assistant entry's model wins —
// here claude-opus-4-7, even though a sidechain entry and a "<synthetic>"
// placeholder follow it.
func TestLatestModel_NewestAssistantWins(t *testing.T) {
	const cwd = "/home/zvi/work"
	data, err := os.ReadFile(filepath.Join("testdata", "model.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	root, path := modelRoot(t, cwd, string(data), time.Now())

	model, stamp, err := LatestModel(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestModel: %v", err)
	}
	if model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", model)
	}
	if stamp.Path != path || stamp.Size == 0 {
		t.Errorf("stamp not populated: %+v", stamp)
	}
}

// TestLatestModel_BareAlias verifies an alias-style model value (observed in
// real transcripts, e.g. from `claude --model fable`) is extracted verbatim.
func TestLatestModel_BareAlias(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd,
		`{"type":"assistant","isSidechain":false,"message":{"model":"fable","content":[{"type":"text","text":"hi"}]}}`+"\n",
		time.Now())

	model, _, err := LatestModel(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil {
		t.Fatalf("LatestModel: %v", err)
	}
	if model != "fable" {
		t.Errorf("model = %q, want fable", model)
	}
}

// TestLatestModel_StampShortCircuit pins the change-detection contract: an
// unchanged transcript returns ("", prev, nil) without re-reading; an appended
// entry (newer mtime) yields the new model under a fresh stamp.
func TestLatestModel_StampShortCircuit(t *testing.T) {
	const cwd = "/home/zvi/work"
	base := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}]}}` + "\n"
	mtime := time.Now().Add(-time.Minute)
	root, path := modelRoot(t, cwd, base, mtime)

	model, stamp, err := LatestModel(context.Background(), "claude", cwd, Stamp{}, Options{Root: root})
	if err != nil || model != "claude-sonnet-4-6" {
		t.Fatalf("first call: model=%q err=%v", model, err)
	}

	model, again, err := LatestModel(context.Background(), "claude", cwd, stamp, Options{Root: root})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if model != "" || !again.Equal(stamp) {
		t.Errorf("unchanged transcript: model=%q stamp=%+v, want \"\" and unchanged stamp", model, again)
	}

	appended := base + `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"more"}]}}` + "\n"
	writeFileWithMtime(t, path, appended, mtime.Add(30*time.Second))

	model, fresh, err := LatestModel(context.Background(), "claude", cwd, stamp, Options{Root: root})
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if model != "claude-opus-4-7" {
		t.Errorf("after append: model = %q, want claude-opus-4-7", model)
	}
	if fresh.Equal(stamp) {
		t.Error("after append: stamp did not advance")
	}
}

// TestLatestModel_NoAssistantInWindow pins the degradation contract: a tail
// window holding no assistant entry returns "" with an *advanced* stamp, so the
// caller keeps its previous value and never re-parses the same bytes.
func TestLatestModel_NoAssistantInWindow(t *testing.T) {
	const cwd = "/home/zvi/work"
	// One assistant entry, then a giant user tool-result line. A MaxBytes window
	// smaller than the giant line starts mid-line; the partial line is discarded
	// and nothing assistant-shaped remains in the window.
	content := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}]}}` + "\n" +
		`{"type":"user","isSidechain":false,"message":{"content":[{"type":"tool_result","content":"` + strings.Repeat("x", 8*1024) + `"}]}}` + "\n"
	root, _ := modelRoot(t, cwd, content, time.Now())

	model, stamp, err := LatestModel(context.Background(), "claude", cwd, Stamp{}, Options{Root: root, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("LatestModel: %v", err)
	}
	if model != "" {
		t.Errorf("model = %q, want \"\" (no assistant entry in window)", model)
	}
	if stamp.Equal(Stamp{}) {
		t.Error("stamp did not advance past prev")
	}
}

// TestLatestModel_Unsupported: non-claude programs return ErrUnsupported, the
// same contract as Render.
func TestLatestModel_Unsupported(t *testing.T) {
	_, _, err := LatestModel(context.Background(), "codex", "/anywhere", Stamp{}, Options{Root: t.TempDir()})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// TestLatestModel_EnvRootFallback: with no explicit Root, the transcript root
// resolves through $CLAUDE_CONFIG_DIR — the same rule as Render (and the reason
// these tests always pin Root or the env: never the developer's real ~/.claude).
func TestLatestModel_EnvRootFallback(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}]}}`+"\n",
		time.Now())
	t.Setenv("CLAUDE_CONFIG_DIR", root)

	model, _, err := LatestModel(context.Background(), "claude", cwd, Stamp{}, Options{})
	if err != nil {
		t.Fatalf("LatestModel: %v", err)
	}
	if model != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", model)
	}
}

// TestLatestModelCanceledContextReturnsPromptly pins the cancellation contract:
// an already-cancelled context short-circuits before any filesystem I/O, returning
// the caller's prev stamp and ctx.Err(). This is the poll path's shutdown behavior —
// a cancelled app context unwinds an in-flight transcript read instead of parsing it.
func TestLatestModelCanceledContextReturnsPromptly(t *testing.T) {
	const cwd = "/home/zvi/work"
	root, _ := modelRoot(t, cwd,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}]}}`+"\n",
		time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	prev := Stamp{Path: "/sentinel"}
	model, stamp, err := LatestModel(ctx, "claude", cwd, prev, Options{Root: root})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if model != "" {
		t.Errorf("model = %q, want \"\" on cancellation", model)
	}
	if !stamp.Equal(prev) {
		t.Errorf("stamp = %+v, want prev %+v returned unchanged", stamp, prev)
	}
}

// TestStampEqual exercises the field-wise comparison, including the time.Time
// pitfall Equal exists to avoid (wall-equal instants must compare equal).
func TestStampEqual(t *testing.T) {
	now := time.Now()
	s := Stamp{Path: "/a", ModTime: now, Size: 1}
	if !s.Equal(Stamp{Path: "/a", ModTime: now.Round(0), Size: 1}) {
		t.Error("wall-equal instants must compare equal")
	}
	for _, o := range []Stamp{
		{Path: "/b", ModTime: now, Size: 1},
		{Path: "/a", ModTime: now.Add(time.Second), Size: 1},
		{Path: "/a", ModTime: now, Size: 2},
	} {
		if s.Equal(o) {
			t.Errorf("Stamp %+v should differ from %+v", o, s)
		}
	}
}
