package tmux

import "os/exec"

// tmuxCommand builds a tmux *exec.Cmd with cs's isolation flags prepended ahead of
// the subcommand: `tmux -L <socket> [-f <conf>] <args...>`. -L and -f are global
// (server) options and must precede the subcommand, so they are always prepended,
// never appended. The returned command works for both execution paths in this
// package: ptyFactory.Start(cmd) and cmdExec.Run/Output(cmd).
func tmuxCommand(args ...string) *exec.Cmd {
	full := []string{"-L", socketName()}
	if conf := tmuxConfigPath(); conf != "" {
		full = append(full, "-f", conf)
	}
	return exec.Command("tmux", append(full, args...)...)
}
