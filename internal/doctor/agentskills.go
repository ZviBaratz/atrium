package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// Reporting for the skills Atrium hands its claude sessions (session/tmux/spawnskill.go).
//
// This section exists because the injection is fail-open at every gate. A non-claude
// program, the setting off, a claude with no --plugin-dir flag, an IO failure under the
// data dir — each one silently costs the skill and lets the session launch, which is the
// right trade at launch time and the wrong one to leave undiagnosable.
//
// It also carries the one failure Atrium cannot gate on. `disableSideloadFlags` in an
// organization's managed settings makes claude reject --plugin-dir at startup, and it is
// resolved from whichever managed tier the org uses — server-managed, an MDM plist, a
// Windows registry key, or managed-settings.json. Atrium does not try to predict it, so
// the symptom is a session that dies at launch with claude's own message naming the
// setting. That message is on the dead pane; the remedy is a config key. This is what
// joins them.
//
// Nothing here evaluates a gate. Every answer is tmux.AgentPluginStatus', which is the
// launch path itself, so the report cannot drift from what a launch does — and it is asked
// once per program a session could actually run, because the default program says nothing
// about a profile's.

// AgentSkillsResult is the section's input: the invocation to name, and what a launch of
// each configured program would decide right now.
type AgentSkillsResult struct {
	// Invocation is how the shipped skill is typed in a session.
	Invocation string
	// Programs is one decision per distinct program a session could run, default first.
	// Empty only if nothing is configured.
	Programs []tmux.AgentPluginDecision
}

// CheckAgentSkills assembles the section by asking the launch path about each program.
//
// programs is every program a session could run — the default plus each profile's, in that
// order; duplicates and blanks are dropped, since two profiles naming one binary are one
// question. status is tmux.AgentPluginStatus, injected so the test can drive the states a
// machine running the suite does not have.
func CheckAgentSkills(invocation string, programs []string,
	status func(string) tmux.AgentPluginDecision) AgentSkillsResult {
	r := AgentSkillsResult{Invocation: invocation}
	seen := map[string]bool{}
	for _, p := range programs {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		r.Programs = append(r.Programs, status(p))
	}
	return r
}

// claudePrograms are the decisions for programs that resolve to claude — the only ones the
// skill can reach, and therefore the only ones a status about it can be about.
func (r AgentSkillsResult) claudePrograms() []tmux.AgentPluginDecision {
	var out []tmux.AgentPluginDecision
	for _, d := range r.Programs {
		if d.Claude {
			out = append(out, d)
		}
	}
	return out
}

// Injecting reports whether every claude program a session could run would be handed the
// skill. False when any of them would not, and when none of them is claude: there is no
// state in which "injecting" is true and some claude session gets nothing.
func (r AgentSkillsResult) Injecting() bool {
	claudes := r.claudePrograms()
	if len(claudes) == 0 {
		return false
	}
	for _, d := range claudes {
		if !d.Injecting() {
			return false
		}
	}
	return true
}

// skillsRow is the section's label/value row, at the width the rows around it use.
func skillsRow(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "  %-12s %s\n", label, value)
}

// RenderAgentSkills formats the section, newline-terminated.
func RenderAgentSkills(r AgentSkillsResult) string {
	var b strings.Builder
	b.WriteString("Agent skills:\n")

	claudes := r.claudePrograms()
	switch {
	case len(r.Programs) == 0:
		skillsRow(&b, "status", "not injected: no program is configured")

	case !r.Programs[0].Enabled:
		// Process-wide, so the first decision answers it for all of them.
		skillsRow(&b, "status", "not injected: agent_skills is off in config.json")

	case len(claudes) == 0:
		// The gate a per-program report can state and a single global one could not: this
		// used to be carried by the words "into new claude sessions" on a status line
		// that said "injected", for an install whose sessions are all codex.
		skillsRow(&b, "status", "not injected: no configured program is claude")
		fmt.Fprintf(&b, "    → the skill is a claude plugin; configured: %s\n", r.programList())

	case len(claudes) == 1:
		r.renderOne(&b, claudes[0])

	default:
		// Several distinct claude binaries, which need not agree: a modern claude on PATH
		// and an older one pinned in a profile give opposite answers, and a single status
		// line would be false for one of them whichever way it was written.
		injecting := 0
		for _, d := range claudes {
			if d.Injecting() {
				injecting++
			}
		}
		switch {
		case r.Injecting():
			skillsRow(&b, "status", "injected into new claude sessions")
		case injecting == 0:
			skillsRow(&b, "status", "not injected: no claude program gets the skill")
		default:
			skillsRow(&b, "status", "injected for some claude programs, not others")
		}
		// Gated for the reason the remedy at the foot of this arm is: with every claude
		// program declining, nothing will answer the invocation, so a row naming what to
		// type is an instruction that cannot work — printed directly under a status line
		// saying the skill is not there. renderOne states the same rule by returning
		// before its own invocation row, which is why a one-claude install with this
		// problem already reports it correctly.
		//
		// injecting > 0 rather than r.Injecting(), so the split case keeps the row: there
		// the invocation does work, in the sessions of the programs that got the skill.
		if injecting > 0 {
			skillsRow(&b, "invoked as", r.Invocation)
		}
		for _, d := range claudes {
			if d.Injecting() {
				fmt.Fprintf(&b, "    → %s: injected from %s\n", d.Program, d.Dir)
				continue
			}
			fmt.Fprintf(&b, "    → %s: not injected: %s\n", d.Program, declineReason(d))
			if d.Err != nil {
				fmt.Fprintf(&b, "      %s\n", d.Err.Error())
			}
		}
		// Only where the flag is actually passed to something. With every claude program
		// declining, no session is handed --plugin-dir and claude cannot be refusing it.
		if injecting > 0 {
			b.WriteString(managedPolicyRemedy)
		}
	}
	return b.String()
}

// renderOne is the single-claude-program shape, which is what nearly every install is.
func (r AgentSkillsResult) renderOne(b *strings.Builder, d tmux.AgentPluginDecision) {
	if !d.Injecting() {
		skillsRow(b, "status", "not injected: "+declineReason(d))
		skillsRow(b, "claude", d.Program)
		// The write failure is the one decline whose reason is not self-explanatory:
		// which file refused, and what it said, is what the user acts on. It goes on its
		// own line because the status row is a label/value pair, and a path plus an errno
		// in the value column buries the two words that matter.
		if d.Err != nil {
			fmt.Fprintf(b, "    → %s\n", d.Err.Error())
		}
		return
	}
	skillsRow(b, "status", "injected into new claude sessions")
	skillsRow(b, "invoked as", r.Invocation)
	skillsRow(b, "plugin", d.Dir)
	skillsRow(b, "claude", d.Program)
	b.WriteString("    → a session already running keeps what it launched with.\n")
	b.WriteString(managedPolicyRemedy)
}

// managedPolicyRemedy belongs on the injecting paths and nowhere else. That refusal is
// claude rejecting --plugin-dir, so it requires Atrium to be passing the flag: a section
// reporting that the flag is not passed at all is reporting a state where a session cannot
// be dying of this, and the advice would send someone to disable a feature that was not
// involved. Hedged, because whether the policy is set is the one thing this cannot see.
const managedPolicyRemedy = "    → if a session dies at launch naming disableSideloadFlags, an\n" +
	"      organization's managed settings are refusing sideloaded plugins;\n" +
	"      set agent_skills false in config.json.\n"

// programList names every configured program, for the case where none of them is claude and
// the useful fact is which ones there are.
func (r AgentSkillsResult) programList() string {
	names := make([]string, 0, len(r.Programs))
	for _, d := range r.Programs {
		names = append(names, d.Program)
	}
	return strings.Join(names, ", ")
}

// declineReason names the gate that refused a claude program, from the fields the launch
// itself filled in — nothing here re-derives a gate.
//
// Precondition: the decision's first two gates passed. Both are stated once by the caller
// rather than per program, because both are answered before any program is: the setting is
// process-wide, and a program that is not claude is not in the list this reasons about. So
// the two gates left are the capability probe and the write, and there is no arm here that
// cannot fire — a switch carrying the earlier two would have looked more careful while
// being unreachable, which is the shape that hid a wrong answer in this file before.
//
// The flag reason is hedged on purpose. binHelpContains caches empty output when the probe
// cannot be run, so "no such flag" and "no such binary" arrive here identically; naming
// only the first sends someone to upgrade a claude that was already current.
func declineReason(d tmux.AgentPluginDecision) string {
	if !d.FlagSupported {
		return "this claude's --help does not advertise " + tmux.PluginDirFlag +
			" (an older CLI, or a binary the probe could not run)"
	}
	// Not inferred from an empty directory: Injecting() is Dir non-empty AND Err nil, and
	// the probe above answered yes, so the write is what stopped it.
	return "the plugin directory cannot be written"
}
