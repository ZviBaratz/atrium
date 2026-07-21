package doctor

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// On Linux, readOOMScore reads this process's own /proc entries and returns a
// plausible score. Off Linux it is unsupported.
func TestReadOOMScore_Self(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("oom_score_adj is Linux-only; GOOS=%s", runtime.GOOS)
	}
	score, adj, ok := readOOMScore(os.Getpid())
	if !ok {
		t.Fatalf("readOOMScore(self) ok=false, want a readable score on linux")
	}
	// oom_score is a 0..1000 badness; oom_score_adj is a -1000..1000 knob.
	if score < 0 || score > 2000 {
		t.Errorf("self oom_score = %d, want a plausible 0..1000 badness", score)
	}
	if adj < -1000 || adj > 1000 {
		t.Errorf("self oom_score_adj = %d, want within [-1000,1000]", adj)
	}
}

func TestRenderOOM_UnsupportedSaysLinuxOnly(t *testing.T) {
	out := RenderOOM(OOMResult{Supported: false})
	if !strings.Contains(out, "unavailable") || !strings.Contains(out, "Linux") {
		t.Errorf("unsupported render = %q, want an unavailable/Linux-only note", out)
	}
}

func TestRenderOOM_NoServerStillShowsMargin(t *testing.T) {
	out := RenderOOM(OOMResult{Supported: true, Margin: 300, ServerFound: false})
	if !strings.Contains(out, "+300") {
		t.Errorf("render = %q, want the configured margin shown", out)
	}
	if !strings.Contains(out, "no live") {
		t.Errorf("render = %q, want a 'no live server' note", out)
	}
}

func TestRenderOOM_MarginOffIsLabelled(t *testing.T) {
	out := RenderOOM(OOMResult{Supported: true, Margin: 0, ServerFound: false})
	if !strings.Contains(out, "off") {
		t.Errorf("render = %q, want the margin shown as off", out)
	}
}

// When every agent outranks the server, the verdict is positive and carries no
// warning glyph.
func TestRenderOOM_ProtectedVerdict(t *testing.T) {
	r := OOMResult{
		Supported: true, Margin: 300,
		ServerFound: true, ServerPID: 12589, ServerScore: 800, ServerAdj: 200, ServerKnown: true,
		Agents: []OOMAgent{
			{Session: "repo_A", PID: 111, Score: 1090, Adj: 500, Known: true},
			{Session: "repo_B", PID: 222, Score: 1050, Adj: 500, Known: true},
		},
	}
	out := RenderOOM(r)
	if !strings.Contains(out, "800") {
		t.Errorf("render = %q, want the server score shown", out)
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("protected render must carry no warning glyph: %q", out)
	}
	if !strings.Contains(out, "outrank") {
		t.Errorf("render = %q, want a positive 'agents outrank the server' verdict", out)
	}
}

// When any agent ranks at or below the server, the verdict warns: the server (and
// therefore every session) could be killed first.
func TestRenderOOM_UnprotectedVerdictWarns(t *testing.T) {
	r := OOMResult{
		Supported: true, Margin: 0,
		ServerFound: true, ServerPID: 12589, ServerScore: 800, ServerAdj: 200, ServerKnown: true,
		Agents: []OOMAgent{
			{Session: "repo_A", PID: 111, Score: 809, Adj: 200, Known: true},
			{Session: "repo_B", PID: 222, Score: 790, Adj: 200, Known: true}, // below the server
		},
	}
	out := RenderOOM(r)
	if !strings.Contains(out, "⚠") {
		t.Errorf("unprotected render must warn: %q", out)
	}
	if !strings.Contains(out, "below") {
		t.Errorf("render = %q, want it to name the at/below-server risk", out)
	}
}

// A live server whose panes couldn't be listed/read must say so, not claim there
// are none (a running server always has at least one pane).
func TestRenderOOM_ServerFoundButNoReadableAgents(t *testing.T) {
	r := OOMResult{
		Supported: true, Margin: 300,
		ServerFound: true, ServerPID: 12589, ServerScore: 800, ServerAdj: 200, ServerKnown: true,
		Agents: nil,
	}
	out := RenderOOM(r)
	if !strings.Contains(out, "unreadable") {
		t.Errorf("render = %q, want it to report the panes as unreadable, not absent", out)
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("no readable agents means no verdict/warning: %q", out)
	}
}

// CheckOOM assembles its result from the injected discovery and reader seams, so
// the tmux/​proc plumbing is exercisable without a live server.
func TestCheckOOM_AssemblesFromSeams(t *testing.T) {
	if !oomScoreSupported {
		t.Skipf("oom scoring unsupported on %s", runtime.GOOS)
	}
	prevDiscover, prevRead := oomDiscover, oomRead
	t.Cleanup(func() { oomDiscover, oomRead = prevDiscover, prevRead })

	oomDiscover = func(context.Context) (int, []paneRef, bool) {
		return 12589, []paneRef{{PID: 111, Session: "repo_A"}, {PID: 222, Session: "repo_B"}}, true
	}
	scores := map[int][2]int{12589: {800, 200}, 111: {1090, 500}, 222: {1050, 500}}
	oomRead = func(pid int) (int, int, bool) {
		v, ok := scores[pid]
		return v[0], v[1], ok
	}

	r := CheckOOM(context.Background())
	if !r.ServerFound || r.ServerPID != 12589 || r.ServerScore != 800 {
		t.Fatalf("server not assembled: %+v", r)
	}
	if len(r.Agents) != 2 || r.Agents[0].Score != 1090 || !r.Agents[1].Known {
		t.Fatalf("agents not assembled: %+v", r.Agents)
	}
}
