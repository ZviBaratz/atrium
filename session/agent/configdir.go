package agent

import "strings"

// ProgramSetsClaudeConfigDir reports whether a program command line chooses the
// Claude account itself, by assigning CLAUDE_CONFIG_DIR on the line that launches
// the agent: `env CLAUDE_CONFIG_DIR=<dir> claude …`, or the bare
// `CLAUDE_CONFIG_DIR=<dir> claude …` a shell accepts.
//
// It matters because that assignment beats the one Atrium injects. Atrium sets the
// dir as a tmux session environment variable (tmux.Session's SetClaudeConfigDir,
// passed as `new-session -e`), which the shell tmux starts inherits — and the
// program's own assignment is applied inside that shell, after it. So a session
// whose program sets the variable runs on the program's directory whatever Atrium
// routed or pinned, while state.json records what Atrium chose: the divergence
// #854 is about.
//
// A substring test on the assignment, which is all the shape reduces to. The `=` is
// required, so a program that merely mentions the name in prose — a system-prompt
// string, a --add-dir argument — is not reported. Case-sensitive, because
// environment variable names are. It still over-reports an assignment that is not
// really one (quoted text, an `unset`), and that is the direction to be wrong in:
// what a false positive costs is a message about an ambiguity that reads oddly,
// while a false negative is the silent mislabel this exists to catch.
//
// One home for the rule because both `atrium new` (which warns, and refuses to
// combine it with --account) and the create drain (which refuses the same
// combination for a program the CLI never saw) have to agree about what the shape
// is.
func ProgramSetsClaudeConfigDir(program string) bool {
	return strings.Contains(program, "CLAUDE_CONFIG_DIR=")
}
