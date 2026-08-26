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
// session: whether it is wanted, whether the installed claude accepts the flag, and whether
// the plugin can be put where sessions are pointed. What it cannot determine is the
// managed-policy refusal above — that one is only ever observed as a session dying.
type AgentSkillsResult struct {
	// Enabled is config.GetAgentSkills: whether injection is wanted at all.
	Enabled bool
	// FlagSupported is whether the claude that sessions run advertises the flag.
	FlagSupported bool
	// Invocation is how the shipped skill is typed in a session.
	Invocation string
	// Dir is the plugin directory sessions are pointed at, "" when unresolvable.
	Dir string
	// PluginErr is why that directory cannot hold the plugin, nil when it can. Its own
	// gate rather than a "" Dir because the two decline for different reasons and the
	// remedies are nothing alike: no home directory versus one that cannot be written.
	PluginErr error
}

// Injecting reports whether a claude session launched now would be handed the skills.
func (r AgentSkillsResult) Injecting() bool {
	return r.Enabled && r.FlagSupported && r.Dir != "" && r.PluginErr == nil
}

// CheckAgentSkills assembles the section. The config read and the capability probe belong
// to the caller, matching CheckImagePreview and CheckKeyboard; the plugin directory arrives
// as a function because resolving it is the one gate with a side effect, and it must be
// reached in the launch path's order — ensureAgentPlugin never attempts the write when an
// earlier gate has already refused, so neither may this. That ordering is what makes
// declineReason's answer the gate that actually stopped it.
func CheckAgentSkills(enabled, flagSupported bool, invocation string,
	plugin func() (string, error)) AgentSkillsResult {
	r := AgentSkillsResult{
		Enabled:       enabled,
		FlagSupported: flagSupported,
		Invocation:    invocation,
	}
	if !r.Enabled || !r.FlagSupported {
		return r
	}
	r.Dir, r.PluginErr = plugin()
	return r
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
		// The managed-policy remedy belongs HERE, on the working path, and nowhere else.
		// That refusal is claude rejecting --plugin-dir, so it requires Atrium to be
		// passing the flag: a section reporting "not injected" is reporting that the flag
		// is not passed at all, where a session cannot be dying of this and the advice
		// sends someone to disable a feature that was not involved. Hedged, because
		// whether the policy is set is the one thing this cannot see.
		b.WriteString("    → if a session dies at launch naming disableSideloadFlags, an\n")
		b.WriteString("      organization's managed settings are refusing sideloaded plugins;\n")
		b.WriteString("      set agent_skills false in config.json.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  %-12s %s\n", "status", "not injected: "+r.declineReason())
	if detail := r.declineDetail(); detail != "" {
		fmt.Fprintf(&b, "    → %s\n", detail)
	}
	return b.String()
}

// declineReason names the first gate that refused, in the order CheckAgentSkills evaluates
// them — which is ensureAgentPlugin's, minus one. The gate this cannot report is the first
// one a launch checks: whether the session's program is claude at all. That is per-session,
// and a default of codex says nothing about a claude profile, so it is carried by the
// status line's wording ("into new claude sessions") rather than reported as a refusal.
//
// The flag reason is hedged on purpose. binHelpContains caches empty output when the probe
// cannot be run, so "no such flag" and "no such binary" arrive here identically; naming
// only the first sends someone to upgrade a claude that was already current.
func (r AgentSkillsResult) declineReason() string {
	switch {
	case !r.Enabled:
		return "agent_skills is off in config.json"
	case !r.FlagSupported:
		return "this claude's --help does not advertise --plugin-dir (an older CLI, or a " +
			"binary the probe could not run)"
	case r.PluginErr != nil:
		return "the plugin directory cannot be written"
	default:
		return "the plugin directory could not be resolved"
	}
}

// declineDetail is the second line a decline may need, "" when the reason says everything.
// Only the write failure has one: which directory refused, and why, is what the user acts on,
// and the status line is a label/value row — a path plus an errno in its value column buries
// the two words that matter behind sixty characters of path.
func (r AgentSkillsResult) declineDetail() string {
	if r.Enabled && r.FlagSupported && r.PluginErr != nil {
		return r.PluginErr.Error()
	}
	return ""
}
