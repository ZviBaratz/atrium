package doctor

import (
	"fmt"
	"strings"
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

// AgentSkillsResult is what doctor can determine about the injection without launching a
// session: whether it is wanted, whether the installed claude accepts the flag, and where
// the plugin lives. What it cannot determine is the managed-policy refusal above — that one
// is only ever observed as a session dying.
type AgentSkillsResult struct {
	// Enabled is config.GetAgentSkills: whether injection is wanted at all.
	Enabled bool
	// FlagSupported is whether the installed claude advertises the flag.
	FlagSupported bool
	// Invocation is how the shipped skill is typed in a session.
	Invocation string
	// Dir is the plugin directory sessions are pointed at, "" when unresolvable.
	Dir string
}

// Injecting reports whether a claude session launched now would be handed the skills.
func (r AgentSkillsResult) Injecting() bool {
	return r.Enabled && r.FlagSupported && r.Dir != ""
}

// CheckAgentSkills assembles the section from values its caller already holds. A pure
// function of its input, matching CheckImagePreview and CheckKeyboard: the config read and
// the capability probe belong to the caller, so this stays testable without either.
func CheckAgentSkills(enabled, flagSupported bool, invocation, dir string) AgentSkillsResult {
	return AgentSkillsResult{
		Enabled:       enabled,
		FlagSupported: flagSupported,
		Invocation:    invocation,
		Dir:           dir,
	}
}

// RenderAgentSkills formats the section, newline-terminated.
func RenderAgentSkills(r AgentSkillsResult) string {
	var b strings.Builder
	b.WriteString("Agent skills:\n")
	if r.Injecting() {
		fmt.Fprintf(&b, "  %-12s %s\n", "status", "injected into new claude sessions")
		fmt.Fprintf(&b, "  %-12s %s\n", "invoked as", r.Invocation)
		fmt.Fprintf(&b, "  %-12s %s\n", "plugin", r.Dir)
		b.WriteString("    → a session already running keeps what it launched with.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %-12s %s\n", "status", "not injected: "+r.declineReason())
	if r.Enabled {
		// Only worth saying while the feature is wanted. Told to someone who turned it
		// off, "here is how to turn it off" is noise.
		b.WriteString("    → sessions that die at launch naming disableSideloadFlags are being\n")
		b.WriteString("      refused by managed settings; set agent_skills false in config.json.\n")
	}
	return b.String()
}

// declineReason names the first gate that refused, in the order ensureAgentPlugin checks
// them, so the reason printed is the one that actually stopped it rather than the first
// one a reader would guess.
func (r AgentSkillsResult) declineReason() string {
	switch {
	case !r.Enabled:
		return "agent_skills is off in config.json"
	case !r.FlagSupported:
		return "this claude has no --plugin-dir flag"
	default:
		return "the plugin directory could not be resolved"
	}
}
