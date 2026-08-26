package config

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/stretchr/testify/assert"
)

// stubDetect makes detectAgentCommand report exactly the given binaries as
// installed, so detection tests never depend on what this machine has.
func stubDetect(t *testing.T, installed map[string]string) {
	t.Helper()
	orig := detectAgentCommand
	detectAgentCommand = func(bin string) (string, error) {
		if p, ok := installed[bin]; ok {
			return p, nil
		}
		return "", fmt.Errorf("%s: not installed", bin)
	}
	t.Cleanup(func() { detectAgentCommand = orig })
}

func TestDetectAgentProfiles(t *testing.T) {
	t.Run("returns installed agents in picker order", func(t *testing.T) {
		stubDetect(t, map[string]string{
			"gemini": "gemini",
			"claude": "/home/u/.claude/local/claude",
		})
		profiles := DetectAgentProfiles()
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "/home/u/.claude/local/claude"},
			{Name: "gemini", Program: "gemini"},
		}, profiles)
	})

	t.Run("no agents installed yields no profiles", func(t *testing.T) {
		stubDetect(t, nil)
		assert.Empty(t, DetectAgentProfiles())
	})
}

func TestMergeDetectedProfiles(t *testing.T) {
	t.Run("appends only new names and reports them", func(t *testing.T) {
		cfg := &Config{
			DefaultProgram: "claude",
			Profiles: []Profile{
				// Hand-edited entry sharing a detected name: must win untouched.
				{Name: "claude", Program: "claude --dangerously-skip-permissions"},
			},
		}
		added := cfg.MergeDetectedProfiles([]Profile{
			{Name: "claude", Program: "/usr/local/bin/claude"},
			{Name: "codex", Program: "codex"},
		})
		assert.Equal(t, []string{"codex"}, added)
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "claude --dangerously-skip-permissions"},
			{Name: "codex", Program: "codex"},
		}, cfg.Profiles)
		assert.Equal(t, "claude", cfg.DefaultProgram, "the default program is never modified")
	})

	t.Run("nothing new is a no-op", func(t *testing.T) {
		cfg := &Config{Profiles: []Profile{{Name: "claude", Program: "claude"}}}
		assert.Empty(t, cfg.MergeDetectedProfiles([]Profile{{Name: "claude", Program: "claude"}}))
		assert.Len(t, cfg.Profiles, 1)
	})
}

func TestSeededDefaultConfig(t *testing.T) {
	t.Run("detected agents become profiles with the first as default", func(t *testing.T) {
		stubDetect(t, map[string]string{"claude": "/usr/local/bin/claude", "aider": "aider"})
		cfg := seededDefaultConfig(DetectAgentProfiles)
		assert.Equal(t, "claude", cfg.DefaultProgram)
		assert.Equal(t, []Profile{
			{Name: "claude", Program: "/usr/local/bin/claude"},
			{Name: "aider", Program: "aider"},
		}, cfg.Profiles)
		assert.Equal(t, "/usr/local/bin/claude", cfg.GetProgram(),
			"the default program resolves through its profile")
	})

	t.Run("no detected agents falls back to the claude literal", func(t *testing.T) {
		stubDetect(t, nil)
		cfg := seededDefaultConfig(DetectAgentProfiles)
		assert.Equal(t, "claude", cfg.DefaultProgram)
		assert.Empty(t, cfg.Profiles)
	})
}

// DefaultConfig must stay pure — the hermeticity contract for the many tests
// (here and in app/) that construct defaults directly: no profiles regardless
// of what the machine has installed, and the bare claude literal as program.
func TestDefaultConfigDoesNotProbe(t *testing.T) {
	stubDetect(t, map[string]string{"claude": "/usr/local/bin/claude", "gemini": "gemini"})
	cfg := DefaultConfig()
	assert.Equal(t, "claude", cfg.DefaultProgram)
	assert.Empty(t, cfg.Profiles, "DefaultConfig must not run agent detection")
}

// agentResolveKey is the registry lookup TestKnownAgentBinsTracksTheRegistry needs, kept in one
// place so the test file does not import session/agent just for a key comparison.
func agentResolveKey(bin string) agent.Key {
	return agent.Resolve(bin).Key
}

// stubIdentity makes agentIdentityOK answer `ok` without exec'ing anything, so a detection test
// that reports a contested binary as installed does not shell out to whatever this machine has
// under that name.
func stubIdentity(t *testing.T, ok bool) {
	t.Helper()
	orig := agentIdentityOK
	agentIdentityOK = func(*agent.Adapter, string) bool { return ok }
	t.Cleanup(func() { agentIdentityOK = orig })
}

// TestDetectSkipsAForeignBinaryUnderAnAgentsName is the AWS Copilot CLI case, which is the one
// contested name in the probed set: `copilot` is also that tool's binary. Detection is an
// exec.LookPath, so without the identity probe Atrium seeds a profile named "copilot" whose
// program prints deployment help and exits — a session that dies the moment it starts — and
// agent.Resolve hands it this adapter's folder-trust gate and busy marker.
//
// Both directions are asserted, because a probe that answered "no" for the real CLI would be the
// same defect wearing the other sign: the agent would silently stop being detected at all.
func TestDetectSkipsAForeignBinaryUnderAnAgentsName(t *testing.T) {
	t.Run("a foreign copilot is not seeded", func(t *testing.T) {
		stubDetect(t, map[string]string{"copilot": "copilot"})
		stubIdentity(t, false)
		assert.Empty(t, DetectAgentProfilesVerified(),
			"the binary is on PATH under an agent's name but is not that agent")
	})

	t.Run("the real copilot still is", func(t *testing.T) {
		stubDetect(t, map[string]string{"copilot": "copilot"})
		stubIdentity(t, true)
		assert.Equal(t, []Profile{{Name: "copilot", Program: "copilot"}},
			DetectAgentProfilesVerified())
	})

	t.Run("an uncontested agent is accepted without running anything", func(t *testing.T) {
		// The REAL probe, against a program that does not exist: an adapter with no
		// VersionMarker must answer true anyway, which is only possible if it never exec'd.
		// Asserted this way round because "did not exec" is not otherwise observable, and the
		// cost of getting it wrong is one process spawned per agent on every first run.
		assert.True(t, agentIdentityOK(agent.Resolve("gemini"), "atrium-no-such-binary-xyz"))
		assert.False(t, agentIdentityOK(agent.Resolve("copilot"), "atrium-no-such-binary-xyz"),
			"and a contested one fails closed when the probe cannot answer")
	})
}

// TestTheConfigLoadPathNeverRunsTheIdentityProbe is the regression guard for a break this
// package cannot feel and CI could not see.
//
// Wiring the identity probe into DetectAgentProfiles put `copilot --version` on the path
// loadStoredConfig (cli_session) re-derives once per poll of `atrium new --wait`. That probe is
// ~2.6s against a cold HOME and creates ~/.cache/copilot/pkg on the way, so four root-package
// tests went from 1.9s to 53s and failed their wait budget. CI stayed green throughout, because
// no CI runner has copilot installed and the probe therefore never ran there — the failure only
// exists on a machine that HAS the contested binary, which is every machine that matters for it.
//
// So the guard is here rather than in the root package: this is where the two detectors are, and
// asserting which one probes is the whole claim. The fatal-on-call stub is deliberate — a
// counter would let a single stray probe pass as "not many".
func TestTheConfigLoadPathNeverRunsTheIdentityProbe(t *testing.T) {
	stubDetect(t, map[string]string{"copilot": "copilot"})
	orig := agentIdentityOK
	agentIdentityOK = func(*agent.Adapter, string) bool {
		t.Error("the config-load path must not run the identity probe: it costs seconds and " +
			"writes to the agent's own cache dir, and it is re-derived per poll by `atrium new --wait`")
		return true
	}
	t.Cleanup(func() { agentIdentityOK = orig })

	assert.NotEmpty(t, DetectAgentProfiles(),
		"the unverified detector still reports what is on PATH")
	assert.NotEmpty(t, SeededDefaultConfig().Profiles,
		"and so does the in-memory fallback the headless commands re-derive")
}

// TestTheVerifiedDetectorDoesRunTheProbe is the other half: a guard that only asserts the
// negative above would also pass if the probe had been deleted outright.
func TestTheVerifiedDetectorDoesRunTheProbe(t *testing.T) {
	stubDetect(t, map[string]string{"copilot": "copilot"})
	probed := 0
	orig := agentIdentityOK
	agentIdentityOK = func(*agent.Adapter, string) bool { probed++; return true }
	t.Cleanup(func() { agentIdentityOK = orig })

	assert.NotEmpty(t, DetectAgentProfilesVerified())
	assert.Positive(t, probed, "the verified detector is the one that checks identity")
}
