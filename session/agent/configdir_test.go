package agent

import "testing"

// ProgramSetsClaudeConfigDir decides two refusals and a warning, so what it has to get
// right is the boundary between an ASSIGNMENT — which beats the directory Atrium injects,
// and is the #854 divergence — and a mention, which sets nothing. Both directions cost
// something: a miss is a session labelled with an account it is not running, and a false
// hit is a legitimate command line that cannot pass --account at all, since the refusal
// offers no override.
func TestProgramSetsClaudeConfigDir(t *testing.T) {
	sets := []string{
		"env CLAUDE_CONFIG_DIR=/home/zvi/.claude-work claude",
		"CLAUDE_CONFIG_DIR=/home/zvi/.claude-work claude --continue",
		`env CLAUDE_CONFIG_DIR="/home/zvi/my claude" claude`,
		"env GH_CONFIG_DIR=/x CLAUDE_CONFIG_DIR=/y claude",
	}
	for _, program := range sets {
		if !ProgramSetsClaudeConfigDir(program) {
			t.Errorf("ProgramSetsClaudeConfigDir(%q) = false, want true", program)
		}
	}

	doesNot := []string{
		"claude",
		"",
		`claude --append-system-prompt "never read CLAUDE_CONFIG_DIR"`,
		"claude --add-dir /home/zvi/CLAUDE_CONFIG_DIR_notes",
		// Lowercase is a different variable name, and env names are case-sensitive.
		"env claude_config_dir=/y claude",
	}
	for _, program := range doesNot {
		if ProgramSetsClaudeConfigDir(program) {
			t.Errorf("ProgramSetsClaudeConfigDir(%q) = true, want false", program)
		}
	}
}
