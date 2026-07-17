package git

import (
	"os/exec"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
)

// recordCmd logs a finished git/gh subprocess into the command log (#372). The
// session/git layer runs its subprocesses directly (never through cmd.Executor),
// so every chokepoint here records explicitly. session labels the owning session
// ("" for the package-level helpers — SearchBranches, checkGHCLI, localGit — that
// hold only a repo path). out is any captured output used as the failure tail;
// recording never alters the caller's return values.
func recordCmd(cmd *exec.Cmd, session string, start time.Time, out []byte, err error) {
	cmdlog.RecordCmd(cmd.Args, session, start, out, err)
}
