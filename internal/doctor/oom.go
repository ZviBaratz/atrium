package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// OOMAgent is one agent pane's OOM standing: its session, pid, current badness
// (Score) and oom_score_adj (Adj). Known is false when its /proc entries could not
// be read (the pane died between listing and reading).
type OOMAgent struct {
	Session string
	PID     int
	Score   int
	Adj     int
	Known   bool
}

// OOMResult is the OOM-ranking snapshot the doctor reports: the configured agent
// margin, and — on Linux with a live server — the shared tmux server's badness
// versus each agent pane's, so the user can see whether an OOM kill would shed one
// recoverable session or the whole server (every session). Supported is false off
// Linux.
//
// ServerFound and LiveServerUnknown are separate, and reading ServerFound alone is the
// bug this pair exists to prevent: it is false both when tmux reported that no server is
// on Atrium's socket and — before LiveServerUnknown existed — when the probe never
// answered, whether because tmux could not be run at all or, since #730, because it ran
// and could not open the socket. Only LiveServerUnknown == false makes !ServerFound
// evidence of an empty fleet.
type OOMResult struct {
	Supported bool
	Margin    int
	// LiveServerUnknown reports that the probe for which server is on Atrium's socket
	// was never answered: tmux absent, the probe's budget spent, or (since #730) tmux run
	// and unable to open the socket. It is not "no server is running", which is a
	// determined answer and safe.
	//
	// Named for the same fact as tmux.ScanGaps.LiveServerUnknown on purpose — one
	// spelling for one question, so a reader who met it in the orphan section does not
	// have to work out whether this is the same thing. It is a different question from
	// ServerKnown below, which is about the /proc read of a server already located.
	LiveServerUnknown bool
	// ServerFound reports that a server is on Atrium's socket. Meaningful only when
	// LiveServerUnknown is false.
	ServerFound bool
	ServerPID   int
	ServerScore int
	ServerAdj   int
	ServerKnown bool
	Agents      []OOMAgent
}

// paneRef is a live agent pane: its process id and tmux session name.
type paneRef struct {
	PID     int
	Session string
}

// oomDiscover and oomRead are seams so CheckOOM's assembly is testable without a
// live tmux server or real /proc: production wires the read-only tmux queries and
// the platform /proc reader.
var (
	oomDiscover = discoverTmuxOOM
	oomRead     = readOOMScore
)

// CheckOOM gathers the OOM-ranking snapshot. Like the other doctor checks it never
// fails: off Linux it reports "unsupported"; with no live server it reports the
// configured margin only. It reads config.json read-only and issues only read-only
// tmux queries (bounded by ctx), so it is safe to run beside a live TUI.
//
// The platform gate lives here and every reading lives in gatherOOM, mirroring
// CheckPressure/gatherPressure. The split is what keeps the assembly reachable off Linux:
// this function returns before consulting a single seam, so a test driving it on an
// unsupported platform gets the "unavailable" render however the seams are set.
func CheckOOM(ctx context.Context) OOMResult {
	if !oomScoreSupported {
		return OOMResult{Supported: false, Margin: configuredOOMMargin()}
	}
	return gatherOOM(ctx)
}

// gatherOOM takes every reading and assembles the snapshot.
//
// Supported is true here because it describes the readings this function took, not the
// platform it ran on — CheckOOM owns that question. A test may therefore call it directly
// anywhere to exercise the assembly against stubbed seams.
func gatherOOM(ctx context.Context) OOMResult {
	r := OOMResult{Supported: true, Margin: configuredOOMMargin()}
	serverPID, panes, found, known := oomDiscover(ctx)
	r.LiveServerUnknown = !known
	if !found {
		return r
	}
	r.ServerFound = true
	r.ServerPID = serverPID
	r.ServerScore, r.ServerAdj, r.ServerKnown = oomRead(serverPID)
	for _, p := range panes {
		a := OOMAgent{Session: p.Session, PID: p.PID}
		a.Score, a.Adj, a.Known = oomRead(p.PID)
		r.Agents = append(r.Agents, a)
	}
	return r
}

// discoverTmuxOOM locates the live Atrium tmux server and its agent panes with
// read-only tmux queries (display-message / list-panes), which never mutate a
// session and are safe beside a live TUI.
//
// found reports that a server is there; known reports that the question was answered at
// all, and found is meaningful only when it is true. The pid probe goes through
// tmux.AmbientServerPID rather than a copy of it built here, which is what supplies that
// second bool: this function used to run its own `display-message -p '#{pid}'` and treat
// every failure as "no server", so tmux off PATH — or a fork that failed under the very
// memory pressure this section exists to diagnose — rendered as an empty fleet on a host
// running eighteen agents. That is the conflation #599 fixed one package over, reached
// from here instead.
//
// list-panes keeps its own command: it is not a pid probe, it has no evidence rule to
// share, and a failure of it is already distinguishable — a live server always hosts at
// least one pane, so an empty list against found == true is the unreadable case.
func discoverTmuxOOM(ctx context.Context) (serverPID int, panes []paneRef, found, known bool) {
	serverPID, known = tmux.AmbientServerPID(ctx)
	if !known {
		return 0, nil, false, false
	}
	if serverPID == 0 {
		// tmux ran and determined there is no server on Atrium's socket. An empty fleet
		// is a real answer, and a safe one.
		return 0, nil, false, true
	}
	paneOut, err := exec.CommandContext(ctx, "tmux", "-L", config.RuntimeName(), "list-panes", "-a", "-F", "#{pane_id} #{pane_pid} #{session_name}").Output()
	if err != nil {
		return serverPID, nil, true, true // server found; panes just unreadable
	}
	return serverPID, agentPanes(paneOut), true, true
}

// agentPanes reduces `list-panes -a` output to one pane per session: the agent's.
// An Atrium session can hold more than one pane — a user can split the window while
// attached, since the attach proxy forwards the tmux prefix — and only the
// numerically smallest pane id is the pane new-session created for the agent; the
// rest are the user's own splits and must not be scored as agents (session/tmux's
// smallestPaneID applies the same rule for capture and keystrokes). Otherwise a
// stray split's oom_score would inflate the session count and could flip the
// verdict. Input lines are "%<pane-id> <pid> <session>"; first-seen session order
// is preserved for stable output.
func agentPanes(out []byte) []paneRef {
	type winner struct {
		ref    paneRef
		paneID int
	}
	best := map[string]winner{}
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) < 3 {
			continue
		}
		paneID, err := paneNum(fields[0])
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		session := fields[2]
		if cur, seen := best[session]; !seen {
			order = append(order, session)
			best[session] = winner{ref: paneRef{PID: pid, Session: session}, paneID: paneID}
		} else if paneID < cur.paneID {
			best[session] = winner{ref: paneRef{PID: pid, Session: session}, paneID: paneID}
		}
	}
	panes := make([]paneRef, 0, len(order))
	for _, session := range order {
		panes = append(panes, best[session].ref)
	}
	return panes
}

// paneNum parses a tmux pane id ("%3") to its numeric part (3).
func paneNum(s string) (int, error) {
	if !strings.HasPrefix(s, "%") {
		return 0, fmt.Errorf("not a pane id: %q", s)
	}
	return strconv.Atoi(s[1:])
}

// configuredOOMMargin reads the effective agent_oom_margin from config.json
// read-only — never config.LoadConfig, which mutates the data dir and would race a
// live TUI. A missing or unreadable file resolves to the on-by-default margin, the
// same value a fresh install would apply.
func configuredOOMMargin() int {
	dir, err := config.GetConfigDir()
	if err != nil {
		return config.DefaultOOMMargin()
	}
	b, err := os.ReadFile(filepath.Join(dir, config.ConfigFileName))
	if err != nil {
		return config.DefaultOOMMargin()
	}
	var cfg config.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config.DefaultOOMMargin()
	}
	return cfg.GetAgentOOMMargin()
}

// RenderOOM formats the OOM-ranking snapshot under an "OOM ranking:" header,
// parallel to RenderCapacity. It shows the configured margin, the server-vs-agents
// standing, and a verdict: whether an OOM kill would shed one recoverable session
// or the shared server (every session).
func RenderOOM(r OOMResult) string {
	var b strings.Builder
	b.WriteString("OOM ranking:\n")
	if !r.Supported {
		b.WriteString("  unavailable — oom_score_adj is Linux-only\n")
		return b.String()
	}

	if r.Margin > 0 {
		fmt.Fprintf(&b, "  %-18s +%d (agents raised above the tmux server)\n", "agent margin", r.Margin)
	} else {
		fmt.Fprintf(&b, "  %-18s off (agents share the server's oom_score)\n", "agent margin")
	}

	// Before the emptiness test, never instead of it — the same order RenderOrphans uses
	// for its scan gaps. "no live atrium tmux server" is not merely a wrong label here:
	// it is an instruction, and it tells a user whose fleet is running to start a
	// session. The margin above still prints, because it is read from config and was
	// established.
	if r.LiveServerUnknown {
		b.WriteString("  live server not established — tmux could not be asked which server is on\n")
		b.WriteString("  Atrium's socket, so this is not an empty fleet, only an unanswered question\n")
		// The re-run leads and the checks come second, as in renderStaleGaps: the probe
		// fails when tmux is off PATH, when its budget was spent, and — since #730 — when
		// Atrium's own socket exists and cannot be opened. Naming only PATH sends a user
		// whose PATH is fine to check it, and the third cause answers identically on every
		// re-run, so it needs the check named or this line is a loop.
		b.WriteString("         → re-run to get the live ranking; if it persists, check that tmux is\n")
		b.WriteString("           on PATH and that Atrium's own socket can be opened (ls -l)\n")
		return b.String()
	}
	if !r.ServerFound {
		b.WriteString("  no live atrium tmux server — start a session to see the live ranking\n")
		return b.String()
	}

	fmt.Fprintf(&b, "  %-18s %s  pid %d\n", "tmux server", scoreWithAdj(r.ServerScore, r.ServerAdj, r.ServerKnown), r.ServerPID)

	known, belowOrEqual, minScore, minSession, maxScore := summarizeAgents(r)
	switch {
	case len(r.Agents) == 0:
		// A live server always hosts at least one pane, so an empty list means the
		// read-only list-panes query failed — say so rather than claim none exist.
		b.WriteString("  agent panes unreadable\n")
	case known == 0:
		fmt.Fprintf(&b, "  %-18s %d sessions (oom_score unreadable)\n", "agents", len(r.Agents))
	default:
		fmt.Fprintf(&b, "  %-18s %d sessions, oom_score %d–%d (lowest %s @ %d)\n", "agents", known, minScore, maxScore, minSession, minScore)
	}

	// Verdict from the live scores: the danger is the lowest-ranked agent — if even
	// it outranks the server, every session is safer than the server.
	if r.ServerKnown && known > 0 {
		if belowOrEqual == 0 {
			b.WriteString("         → agents outrank the tmux server; an OOM kill sheds one recoverable session, not all\n")
		} else {
			fmt.Fprintf(&b, "         → ⚠ %d of %d agents rank at or below the server (oom_score %d); an OOM kill could take every session\n", belowOrEqual, known, r.ServerScore)
			// Each launch applies the current margin, so an at/below agent is either
			// unprotected (margin off) or predates the setting; relaunching it via
			// pause → resume re-applies the current margin.
			if r.Margin > 0 {
				b.WriteString("           pause and resume those sessions to apply the margin\n")
			} else {
				b.WriteString("           enable agent_oom_margin in Settings, then pause and resume those sessions\n")
			}
		}
	}
	return b.String()
}

// summarizeAgents reduces the agent rows to the numbers the verdict needs: how many
// had readable scores, how many rank at or below the server, and the lowest/highest
// readable scores with the lowest-scoring session's name.
func summarizeAgents(r OOMResult) (known, belowOrEqual, minScore int, minSession string, maxScore int) {
	first := true
	for _, a := range r.Agents {
		if !a.Known {
			continue
		}
		known++
		if r.ServerKnown && a.Score <= r.ServerScore {
			belowOrEqual++
		}
		if first || a.Score < minScore {
			minScore, minSession = a.Score, a.Session
		}
		if first || a.Score > maxScore {
			maxScore = a.Score
		}
		first = false
	}
	return known, belowOrEqual, minScore, minSession, maxScore
}

// scoreWithAdj renders "oom_score N (adj M)", or "oom_score unknown" when the /proc
// read failed.
func scoreWithAdj(score, adj int, known bool) string {
	if !known {
		return "oom_score unknown"
	}
	return fmt.Sprintf("oom_score %d  (adj %d)", score, adj)
}
