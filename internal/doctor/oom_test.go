package doctor

import (
	"context"
	"os"
	"os/exec"
	"reflect"
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

// The determined empty fleet: tmux answered, and answered that nothing is on Atrium's
// socket. LiveServerUnknown false is what makes "no live" a finding here rather than a
// fabrication — see TestRenderOOM_UnaskedLiveServerIsNotAnEmptyFleet for the other half.
func TestRenderOOM_NoServerStillShowsMargin(t *testing.T) {
	out := RenderOOM(OOMResult{Supported: true, Margin: 300, ServerFound: false})
	if !strings.Contains(out, "+300") {
		t.Errorf("render = %q, want the configured margin shown", out)
	}
	if !strings.Contains(out, "no live") {
		t.Errorf("render = %q, want a 'no live server' note", out)
	}
}

// TestRenderOOM_UnaskedLiveServerIsNotAnEmptyFleet: the two states above and below this
// line rendered identically until LiveServerUnknown existed, and the shared sentence was
// the wrong one of the two — "no live atrium tmux server — start a session to see the
// live ranking", printed on a host with eighteen running agents.
//
// The margin still prints: it comes from config.json, which was read.
func TestRenderOOM_UnaskedLiveServerIsNotAnEmptyFleet(t *testing.T) {
	out := RenderOOM(OOMResult{Supported: true, Margin: 300, LiveServerUnknown: true})
	if strings.Contains(out, "no live atrium tmux server") || strings.Contains(out, "start a session") {
		t.Errorf("render = %q, want no claim of an empty fleet from an unanswered probe", out)
	}
	if !strings.Contains(out, "not established") {
		t.Errorf("render = %q, want the gap named", out)
	}
	if !strings.Contains(out, "+300") {
		t.Errorf("render = %q, want the configured margin still shown", out)
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

// An agent whose score exactly equals the server's is at risk: a tie is not safe
// (the kernel could pick either), so the boundary is <=, not <.
func TestRenderOOM_TieWithServerCountsAsAtRisk(t *testing.T) {
	r := OOMResult{
		Supported: true, Margin: 300,
		ServerFound: true, ServerPID: 12589, ServerScore: 800, ServerAdj: 200, ServerKnown: true,
		Agents: []OOMAgent{
			{Session: "repo_A", PID: 111, Score: 800, Adj: 200, Known: true}, // exactly ties the server
		},
	}
	out := RenderOOM(r)
	if !strings.Contains(out, "⚠") {
		t.Errorf("an agent tying the server must warn: %q", out)
	}
	if !strings.Contains(out, "1 of 1") {
		t.Errorf("render = %q, want the tying agent counted at/below the server", out)
	}
}

// list-panes -a returns every pane, including any split the user opened while
// attached. Only the smallest pane id per session is the agent; splits must not be
// scored as extra agents.
func TestAgentPanes_OneAgentPerSessionSmallestPaneID(t *testing.T) {
	// repo_A has a user split (%3) whose row precedes the agent's (%0); repo_B has a
	// single pane. The split must be dropped and first-seen session order kept.
	out := []byte("%3 999 repo_A\n%0 111 repo_A\n%1 222 repo_B\n")
	got := agentPanes(out)
	want := []paneRef{{PID: 111, Session: "repo_A"}, {PID: 222, Session: "repo_B"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agentPanes = %+v, want %+v (one agent pane per session, smallest id)", got, want)
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

	oomDiscover = func(context.Context) (int, []paneRef, bool, bool) {
		return 12589, []paneRef{{PID: 111, Session: "repo_A"}, {PID: 222, Session: "repo_B"}}, true, true
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

// TestDiscoverTmuxOOM_UnrunnableTmuxIsNotAnEmptyFleet drives the real discovery function
// — not the seam — with tmux unreachable, and is the test whose absence let this ship.
//
// Every other test here stubs oomDiscover, so the classification inside it was never
// exercised: its `ok` collapsed a failed exec into "no server on Atrium's socket" and no
// assertion could tell the two apart, because the signature had no way to express the
// difference. That is the shape of all seven instances of this class — the gap was in the
// source, and the suite only ever saw the seam.
//
// PATH is emptied rather than TMUX_TMPDIR repointed, because those exercise the two
// different branches: an empty PATH makes exec.LookPath fail, which is the "could not be
// asked" case, while a sandbox socket root with no server is the determined empty fleet
// asserted below it.
func TestDiscoverTmuxOOM_UnrunnableTmuxIsNotAnEmptyFleet(t *testing.T) {
	t.Setenv("PATH", "")

	pid, panes, found, known := discoverTmuxOOM(context.Background())
	if known {
		t.Errorf("known = true with tmux off PATH; a question that could not be asked was not answered")
	}
	if found {
		t.Errorf("found = true with tmux off PATH, want no claim about a server that was never probed")
	}
	if pid != 0 || panes != nil {
		t.Errorf("discoverTmuxOOM = (%d, %v), want no data from a probe that never ran", pid, panes)
	}
}

// TestDiscoverTmuxOOM_EmptySocketIsADeterminedAnswer is the other half: tmux runs, finds
// nothing on Atrium's socket, and that is evidence. testutil.SandboxHomeMain points
// TMUX_TMPDIR at a private root, so the socket this probes is the sandbox's and never the
// developer's live fleet.
func TestDiscoverTmuxOOM_EmptySocketIsADeterminedAnswer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	pid, panes, found, known := discoverTmuxOOM(context.Background())
	if !known {
		t.Errorf("known = false with tmux runnable and a sandbox socket root, want a determined answer")
	}
	if found {
		t.Errorf("found = true on an empty sandbox socket, want no server: pid %d panes %v", pid, panes)
	}
}
