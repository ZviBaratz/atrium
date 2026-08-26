package config

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/agent"
)

// Agent auto-detection: probe the machine for known agent CLIs so profiles
// exist without hand-editing config.json. Two triggers: DefaultConfig seeds
// profiles on first run, and `atrium profiles detect` re-probes and merges new
// agents into an existing config without touching user-edited entries.

// KnownAgentBins are the agent CLIs probed when seeding or refreshing profiles, in picker
// order. Each name doubles as the generated profile's Name and matches the binary's adapter in
// session/agent by basename.
//
// DERIVED from the adapter registry rather than listed, which is the whole point. It was a
// literal, and nothing tied the two together: an adapter added to the registry and forgotten
// here passed the glyph guard, the version pin and paneCoverage, then was never probed for —
// missing from BOTH sides of every comparison, so no table test could see the gap. The keys
// ARE the binary names by construction (agent.Resolve matches an adapter's aliases against the
// program's basename, and each of these adapters is keyed on its own binary), and picker order
// is registry order, which is what claude leading depends on.
func KnownAgentBins() []string {
	adapters := agent.Adapters()
	bins := make([]string, 0, len(adapters))
	for _, a := range adapters {
		bins = append(bins, string(a.Key))
	}
	return bins
}

// identityProbeTimeout bounds the `--version` probe below. Short, and shorter than doctor's:
// this one runs while a first-run config is being seeded, where a wedged binary would stall
// startup rather than a diagnostic command the user chose to run.
const identityProbeTimeout = 5 * time.Second

// agentIdentityOK reports whether the installed binary really is the agent whose name it
// carries, by checking the adapter's VersionMarker against `<program> --version`. An adapter
// with no marker is not contested and is accepted without exec'ing anything, which is every
// adapter but copilot.
//
// FAIL-CLOSED on a probe that does not answer: a binary that cannot be run, times out, or
// prints something without the vendor string is not this agent. The cost of that direction is a
// missing profile the user can add by hand; the cost of the other is a profile whose sessions
// die on launch, and an `atrium doctor` line about the wrong CLI's version.
//
// A var so tests can say what is installed without depending on the machine.
var agentIdentityOK = func(a *agent.Adapter, program string) bool {
	if a.VersionMarker == "" {
		return true
	}
	cmd := ProgramCommand(program)
	if cmd == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, cmd, "--version").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), a.VersionMarker)
}

// detectAgentCommand resolves an agent binary name to a runnable program
// string, or an error when it is not installed. claude keeps the
// shell-profile-aware probe (its installer commonly defines an alias rather
// than a PATH entry, so the resolved path is required); every other agent is a
// plain PATH lookup, cheap enough to run for the whole known list at startup.
// A var so tests can stub what is "installed" without depending on the machine.
var detectAgentCommand = func(bin string) (string, error) {
	if bin == defaultProgram {
		return GetClaudeCommand()
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", err
	}
	// The bare name is preferred over the resolved path when PATH finds it:
	// tmux launches programs through the shell, so the name keeps working
	// across version-manager upgrades that move the underlying binary.
	return bin, nil
}

// DetectAgentProfiles probes for the known agent CLIs and returns one profile per installed
// binary, in picker order. Missing binaries are skipped silently, and so is a binary that is
// installed under an agent's name but is not that agent — see agentIdentityOK.
func DetectAgentProfiles() []Profile {
	var profiles []Profile
	for _, bin := range KnownAgentBins() {
		program, err := detectAgentCommand(bin)
		if err != nil {
			continue
		}
		if !agentIdentityOK(agent.Resolve(bin), program) {
			continue
		}
		profiles = append(profiles, Profile{Name: bin, Program: program})
	}
	return profiles
}

// MergeDetectedProfiles appends detected profiles whose Name is not already
// taken, returning the added names. Existing entries and DefaultProgram are
// never modified — a user's hand-edited profile always wins over detection.
func (c *Config) MergeDetectedProfiles(detected []Profile) (added []string) {
	for _, d := range detected {
		exists := false
		for _, p := range c.Profiles {
			if p.Name == d.Name {
				exists = true
				break
			}
		}
		if !exists {
			c.Profiles = append(c.Profiles, d)
			added = append(added, d.Name)
		}
	}
	return added
}

// ProgramCommand returns program's command — its first whitespace-separated
// token (the binary name of a string like "aider --model x") — or "" when the
// string is empty or blank. It is the one place command-name extraction lives,
// shared by ProgramInstalled and the app's missing-program warning.
func ProgramCommand(program string) string {
	if fields := strings.Fields(program); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// ProgramInstalled reports whether program's command — its first
// whitespace-separated token — resolves to something runnable. It reuses
// detectAgentCommand so the resolution matches agent detection exactly: the
// "claude" token goes through the shell-profile-aware probe (an aliased or
// shell-function claude is not falsely reported missing), every other token is
// a plain PATH lookup. An empty program (no token) is never installed.
func ProgramInstalled(program string) bool {
	cmd := ProgramCommand(program)
	if cmd == "" {
		return false
	}
	_, err := detectAgentCommand(cmd)
	return err == nil
}
