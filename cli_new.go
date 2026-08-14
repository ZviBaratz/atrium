package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	"github.com/spf13/cobra"
)

var (
	newPathFlag    string
	newProgramFlag string
	newProfileFlag string
	newBranchFlag  string
	newForceFlag   bool
	newWaitFlag    time.Duration

	newCmd = &cobra.Command{
		Use:   "new <title> [prompt]",
		Short: "Create a session without a TUI",
		Long: "Requests a new session — a git worktree, a branch and an agent — the same way\n" +
			"pressing the new-session key does, but from a script, a CI job or an agent\n" +
			"working through a queue of issues.\n\n" +
			"Creation is asynchronous. The request is spooled to the data directory and the\n" +
			"running Atrium picks it up within about a second; with no Atrium running it\n" +
			"stays queued and is created the next time one starts. Use --wait to block until\n" +
			"the session actually exists, which is also the only way to be told its branch.\n\n" +
			"The title is the session's name, and because the branch and tmux names derive\n" +
			"from it, choosing a title is choosing a branch. A title whose derived names are\n" +
			"already taken is refused rather than silently suffixed.\n\n" +
			"The first prompt is optional, as it is in the create form. Pass \"-\" to read it\n" +
			"from stdin, which is what makes a multi-line prompt practical to pipe in; omit\n" +
			"the argument entirely and the session starts with no prompt at all.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			prompt, err := firstPrompt(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			return runNew(cmd.OutOrStdout(), cmd.ErrOrStderr(), newRequest{
				title:   args[0],
				path:    newPathFlag,
				program: newProgramFlag,
				profile: newProfileFlag,
				branch:  newBranchFlag,
				prompt:  prompt,
				force:   newForceFlag,
				wait:    newWaitFlag,
			})
		},
	}
)

// newRequest is what the command line asked for, before any of it is resolved.
// A struct rather than eight parameters because every field is a string and the
// call site is a test as often as it is RunE.
type newRequest struct {
	title   string
	path    string
	program string
	profile string
	branch  string
	prompt  string
	force   bool
	wait    time.Duration
}

// firstPrompt returns the session's first prompt: the second argument, or stdin
// when that argument is "-".
//
// Unlike `send`, an absent argument means *no prompt* rather than stdin. The
// create form's prompt field is explicitly optional, and a bare
// `atrium new fix-auth` in a terminal would otherwise block on a tty forever.
func firstPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) < 2 {
		return "", nil
	}
	if args[1] != "-" {
		return args[1], nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read the prompt from stdin: %w", err)
	}
	return string(data), nil
}

// runNew spools a create-session request.
//
// Like runSend it never writes state.json, for the same reason: that file has
// exactly one writer at any instant — the TUI holding tui.lock, or the autoyes
// daemon in the window where no TUI is alive — and both rewrite it whole from
// their own view of the instance list, so an outside append would be clobbered
// rather than merged. The daemon is not an alternative creator either: it
// snapshots the instance list once for its lifetime precisely because the TUI is
// the only thing that creates sessions.
//
// So this stays a pure producer. Everything it checks below is a courtesy — a
// fast, accurate error for the mistakes a caller actually makes — and none of it
// is authoritative. The drain re-runs every gate against the live instance list at
// the moment it creates, which is the only moment the answer is true.
func runNew(out, errOut io.Writer, r newRequest) error {
	title := strings.TrimSpace(r.title)
	if title == "" {
		return errors.New("refusing to create a session with no title")
	}
	if n := len([]rune(title)); n > session.MaxTitleLen {
		return fmt.Errorf("the title is %d characters; the limit is %d, the same one the new-session field enforces",
			n, session.MaxTitleLen)
	}

	// One load for both the profile table and the branch prefix. LoadConfig is the
	// read doctor and debug already use; the never-call-the-loader rule is about
	// state.json's instance list, which loadStoredInstances handles.
	cfg := config.LoadConfig()
	program, err := resolveNewProgram(cfg, r.program, r.profile)
	if err != nil {
		return err
	}

	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	path, err := resolveNewTarget(errOut, r.path, instances)
	if err != nil {
		return err
	}
	if err := checkTitleFree(cfg.BranchPrefix, title, path, instances); err != nil {
		return err
	}

	spooled, err := outbox.WriteCreate(outbox.Request{
		Title:   title,
		Path:    path,
		Program: program,
		Branch:  r.branch,
		Prompt:  strings.TrimRight(r.prompt, "\r\n"),
		Force:   r.force,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "queued: create %q in %s\n", title, path)

	if r.wait > 0 {
		return waitForCreate(out, spooled, title, path, r.wait)
	}
	if running, known := tuiRunning(); known && !running {
		_, _ = fmt.Fprintf(errOut,
			"warning: no atrium TUI is running, so nothing is creating this yet; "+
				"it stays queued and is picked up the next time one starts\n")
	}
	return nil
}

// resolveNewProgram turns --program/--profile into the program string the request
// carries, or "" for "whatever the draining TUI is configured with".
//
// The profile is resolved here rather than in the drain so a typo fails
// immediately, before anything is spooled — the same reason `send` resolves its
// target up front.
func resolveNewProgram(cfg *config.Config, program, profile string) (string, error) {
	if program != "" && profile != "" {
		return "", errors.New("--program and --profile both name the command to run; pass one")
	}
	if profile == "" {
		return program, nil
	}
	profiles := cfg.GetProfiles()
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		if p.Name == profile {
			return p.Program, nil
		}
		names = append(names, p.Name)
	}
	return "", fmt.Errorf("no profile %q (configured: %s)", profile, strings.Join(names, ", "))
}

// resolveNewTarget picks the repository the session is created in: --path, or the
// current directory.
//
// The redirection is what makes the motivating case work. An agent running inside
// an Atrium session is standing in a *worktree*, and `git rev-parse
// --show-toplevel` there answers with the worktree, so a create defaulting to the
// current directory would build a worktree of a worktree and group the session
// under a name like "issue-703_18cbc8d8a4652705". state.json records which repo
// each worktree belongs to, so the repo is a lookup of a value Atrium itself
// wrote, not an inference — and it is said out loud, because acting on a path the
// caller did not type should never be silent.
func resolveNewTarget(errOut io.Writer, flag string, instances []session.InstanceData) (string, error) {
	target := flag
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to resolve the current directory (pass --path): %w", err)
		}
		target = cwd
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("%q is not a usable path: %w", target, err)
	}
	if !config.DirExists(abs) {
		return "", fmt.Errorf("%q is not a directory", abs)
	}

	if owner, repo, ok := worktreeOwner(abs, instances); ok {
		_, _ = fmt.Fprintf(errOut, "note: %s is session %q's worktree — creating in its repo %s\n",
			abs, owner, repo)
		return repo, nil
	}
	return abs, nil
}

// worktreeOwner reports the session whose managed worktree contains path, and the
// repo that worktree was cut from. Containment rather than equality, because an
// agent is usually somewhere below the worktree root rather than at it.
func worktreeOwner(path string, instances []session.InstanceData) (title, repo string, ok bool) {
	for _, d := range instances {
		wt := d.Worktree.WorktreePath
		if wt == "" || d.Worktree.RepoPath == "" {
			continue
		}
		if within(path, wt) {
			return d.Title, d.Worktree.RepoPath, true
		}
	}
	return "", "", false
}

// within reports whether path is dir or sits underneath it. It compares cleaned
// paths with a separator appended, so "/repo/web-old" is not read as being inside
// "/repo/web".
func within(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// checkTitleFree refuses a title whose derived names a stored session in the same
// repo already owns.
//
// It is scoped by path rather than by repo group because deriving the group means
// running git, and this is a pre-check: the drain runs the real one
// (variantTitleConflictIn) against the live list, including the orphan-branch half
// this cannot see at all. What it buys is the common mistake — re-using a title
// that is plainly taken — reported before anything is spooled, in the same
// resolve-then-spool order `send` uses.
func checkTitleFree(prefix, title, path string, instances []session.InstanceData) error {
	want := filepath.Clean(path)
	for _, d := range instances {
		if filepath.Clean(d.Path) != want {
			continue
		}
		if session.DerivedNamesCollide(prefix, d.Title, title) {
			return fmt.Errorf("session %q in %s already uses that name (or one that derives the same branch)",
				d.Title, path)
		}
	}
	return nil
}

// waitForCreate blocks until the spooled request has been accounted for, then
// reports the session that came of it.
//
// The protocol is `send --wait`'s: a rejection receipt first — it is written
// before the request is unlinked, so a refusal can never be observed as a
// success — then the file's disappearance, then the deadline. What differs is
// what the disappearance means. The drain holds the request until Start has
// finished, so the file going away says the worktree, the branch and the agent
// all exist, not merely that some Atrium consumed the request.
//
// The branch is then read back out of state.json rather than derived from the
// title. They would usually agree, but "usually" is not something to print: the
// slug rules have a hash fallback for titles that sanitize to nothing, and a
// non-git target has no branch at all.
func waitForCreate(out io.Writer, path, title, repo string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if reason, ok := outbox.Rejection(path); ok {
			if err := outbox.ClearRejection(path); err != nil {
				log.ErrorLog.Printf("failed to clear an outbox rejection receipt: %v", err)
			}
			return fmt.Errorf("atrium did not create the session: %s", reason)
		}
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(out, "created %q%s\n", title, createdBranchClause(title, repo))
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"waited %s and no atrium TUI created the session; the request is still queued "+
					"and is picked up the next time one runs", timeout)
		}
		time.Sleep(drainPollInterval)
	}
}

// createdBranchClause names the branch and worktree the TUI recorded, or "" when
// it recorded neither — a direct (non-git) session has no branch, and a session
// this process cannot find is one it should not describe.
func createdBranchClause(title, repo string) string {
	instances, err := loadStoredInstances()
	if err != nil {
		log.ErrorLog.Printf("created the session but could not read back its branch: %v", err)
		return ""
	}
	want := filepath.Clean(repo)
	for _, d := range instances {
		if d.Title != title || filepath.Clean(d.Path) != want {
			continue
		}
		if d.Branch == "" {
			return "" // a direct session: no worktree, no branch
		}
		if d.Worktree.WorktreePath == "" {
			return fmt.Sprintf(" on %s", d.Branch)
		}
		return fmt.Sprintf(" on %s (%s)", d.Branch, d.Worktree.WorktreePath)
	}
	return ""
}
