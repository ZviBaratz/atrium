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
// Linux; ServerFound is false when no server runs on the socket.
type OOMResult struct {
	Supported   bool
	Margin      int
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
func CheckOOM(ctx context.Context) OOMResult {
	r := OOMResult{Supported: oomScoreSupported, Margin: configuredOOMMargin()}
	if !r.Supported {
		return r
	}
	serverPID, panes, ok := oomDiscover(ctx)
	if !ok {
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
// session and are safe beside a live TUI. ok is false when tmux is absent or no
// server is running on Atrium's socket.
func discoverTmuxOOM(ctx context.Context) (serverPID int, panes []paneRef, ok bool) {
	socket := config.RuntimeName()
	out, err := exec.CommandContext(ctx, "tmux", "-L", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return 0, nil, false
	}
	serverPID, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, nil, false
	}
	paneOut, err := exec.CommandContext(ctx, "tmux", "-L", socket, "list-panes", "-a", "-F", "#{pane_pid} #{session_name}").Output()
	if err != nil {
		return serverPID, nil, true // server found; panes just unreadable
	}
	for _, line := range strings.Split(strings.TrimSpace(string(paneOut)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		session := ""
		if len(fields) > 1 {
			session = fields[1]
		}
		panes = append(panes, paneRef{PID: pid, Session: session})
	}
	return serverPID, panes, true
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
			// The margin is baked in at session start, so an at/below agent is either
			// unprotected (margin off) or predates the setting (restart applies it).
			if r.Margin > 0 {
				b.WriteString("           restart those sessions to apply the margin\n")
			} else {
				b.WriteString("           enable agent_oom_margin in Settings and restart sessions\n")
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
