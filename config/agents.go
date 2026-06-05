package config

import "os/exec"

// Agent auto-detection: probe the machine for known agent CLIs so profiles
// exist without hand-editing config.json. Two triggers: DefaultConfig seeds
// profiles on first run, and `atrium profiles detect` re-probes and merges new
// agents into an existing config without touching user-edited entries.

// knownAgentBins are the agent CLIs probed when seeding or refreshing
// profiles, in picker order. Each name doubles as the generated profile's
// Name and matches the binary's adapter in session/agent by basename.
var knownAgentBins = []string{"claude", "codex", "gemini", "aider"}

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

// DetectAgentProfiles probes for the known agent CLIs and returns one profile
// per installed binary, in picker order. Missing binaries are skipped silently.
func DetectAgentProfiles() []Profile {
	var profiles []Profile
	for _, bin := range knownAgentBins {
		program, err := detectAgentCommand(bin)
		if err != nil {
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
