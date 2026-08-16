package main

import (
	"errors"
	"fmt"
	"io"
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
			"stays queued and is created the next time one starts, provided that is within 24\n" +
			"hours — a request older than that names a branch point the tree has moved on\n" +
			"from, so it is discarded with a receipt rather than built. An Atrium that is\n" +
			"running but attached to a session is a third case: its poll loop is parked, so\n" +
			"the request waits for the detach rather than for a relaunch. Use --wait to block\n" +
			"until the session actually exists and be told the branch it was given (`atrium\n" +
			"ls` reports the same branch once it does).\n\n" +
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
// A struct rather than eight parameters because most of them are strings that would
// be interchangeable at a call site, and that call site is a test as often as it is
// RunE.
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
	// Refused rather than stripped, for the reason the title is not slugged either: the
	// branch and tmux names derive from it, so rewriting one is choosing a name the
	// caller did not ask for. TrimSpace above does not cover this — it takes the ends
	// and leaves an interior newline, which is the one that breaks the row. See
	// outbox.FirstControlRune for what a control character does to the list.
	if bad, ok := outbox.FirstControlRune(title); ok {
		return fmt.Errorf("the title contains %q; a session title is rendered as one row, so it "+
			"cannot contain control characters (a title captured from an issue or a commit "+
			"message often has a trailing newline — trim it, or quote only the first line)", bad)
	}

	if r.wait < 0 {
		// Cobra parses "--wait -5s" happily. Left to the r.wait > 0 test below it would
		// silently mean "do not wait", so a caller that fat-fingered a sign would be
		// told the request was queued and never learn what became of it.
		return fmt.Errorf("--wait %s is negative; pass a positive duration", r.wait)
	}

	// One read for both the profile table and the branch prefix, and a read is all it
	// is: loadStoredConfig, not config.LoadConfig, for the reasons that function
	// documents — the loader sweeps in-flight temp files and seeds a config.json.
	cfg := loadStoredConfig()
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
		// Tail only, for runSend's reason: trailing newlines are an artifact of how the
		// text arrived (a heredoc, a pipe), while leading whitespace could be meaningful.
		Prompt: strings.TrimRight(r.prompt, "\r\n"),
		Force:  r.force,
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
//
// It applies to an explicit --path too, not only to the cwd default. An agent
// scripting this passes --path "$PWD" as readily as it relies on the default, and the
// two should not mean different things; the note on stderr is what keeps the override
// visible either way. Pass the repo itself to opt out — it is not a worktree, so
// there is nothing to redirect.
//
// The redirect target is re-validated because it comes off disk rather than off the
// command line: a repo that has since been moved or deleted would otherwise be spooled
// as a path that no longer exists, and the caller would be told "queued" for a request
// the drain can only refuse minutes later.
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
		if !config.DirExists(repo) {
			return "", fmt.Errorf("%s is session %q's worktree, but its repo %s is gone (pass --path)",
				abs, owner, repo)
		}
		_, _ = fmt.Fprintf(errOut, "note: %s is session %q's worktree — creating in its repo %s\n",
			abs, owner, repo)
		return repo, nil
	}
	return abs, nil
}

// worktreeOwner reports the session whose managed worktree contains path, and the
// repo that worktree was cut from. Containment rather than equality, because an
// agent is usually somewhere below the worktree root rather than at it.
//
// Both sides are resolved through symlinks first. Without that the check misses on
// macOS as a matter of routine, where a data dir under /tmp is recorded as
// /private/tmp — and a missed redirect is silent, producing a worktree of a worktree
// rather than an error.
func worktreeOwner(path string, instances []session.InstanceData) (title, repo string, ok bool) {
	path = resolvePath(path)
	for _, d := range instances {
		wt := d.Worktree.WorktreePath
		if wt == "" || d.Worktree.RepoPath == "" {
			continue
		}
		if within(path, resolvePath(wt)) {
			return d.Title, d.Worktree.RepoPath, true
		}
	}
	return "", "", false
}

// resolvePath returns path with symlinks resolved, or path cleaned when it cannot be
// resolved — a path that does not exist is not a reason to fail a containment test.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
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
// The protocol is awaitSpool's, shared with `send --wait`. What differs is what the
// record settling means here: the drain holds the request until Start has finished
// *and the row has been persisted*, so it going away says the worktree, the branch and
// the agent all exist and are recorded — not merely that some Atrium consumed the
// request.
//
// "Settling" is two files, which is why the claim path is passed in. The drain renames
// an accepted request to outbox.ClaimPath for the whole of the build (#716), so the
// record alone going away means only "some atrium has taken this" — the state the wait
// is precisely meant to sit through. It is also what makes an interrupted build
// legible: a claim outlives the process that made it, so a caller still blocked here
// keeps waiting rather than being told a half-built session was created, and the next
// atrium's reconcile settles the claim one way or the other.
//
// That second half is what makes the next line safe rather than a race. The branch is
// read back out of state.json rather than derived from the
// title. They would usually agree, but "usually" is not something to print: the
// slug rules have a hash fallback for titles that sanitize to nothing, and a
// non-git target has no branch at all.
func waitForCreate(out io.Writer, path, title, repo string, timeout time.Duration) error {
	if err := awaitSpool(path, outbox.ClaimPath(path), timeout, spoolWaitCopy{
		refused: "atrium did not create the session",
		// Neither half of the obvious wording is knowable from here. "No TUI created it"
		// is wrong while one is mid-create — the record is deliberately held for the whole
		// of Start, so its presence means "queued or being built", and this side cannot
		// tell those apart. "Picked up the next time one runs" is wrong when a TUI is
		// running but attached: its poll loop is parked, and the request drains on detach
		// rather than on a relaunch. Say only what is true of the file.
		timedOut: fmt.Sprintf("waited %s without a session appearing; the request is still in the "+
			"outbox — either queued, or being built right now, since a create is held there until "+
			"its worktree, branch and agent exist. A running atrium drains it on its next tick, or "+
			"on detach if it is attached to a session; otherwise the next one to start does", timeout),
	}); err != nil {
		return err
	}
	// The record's absence is necessary evidence, not sufficient. Reject writes the
	// receipt first but unlinks the record even when that write fails (outbox.Reject),
	// so "gone, no receipt" is reachable without a session ever existing — on a full or
	// read-only data dir, which is exactly when the receipt cannot be written. Read the
	// row back and require it: the drain holds the record until persistInstances has
	// returned, so a session that really was created is in state.json by now, and the
	// alternative is printing `created` and exiting 0 for one that is not.
	d, err := storedSession(title, repo)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "created %q%s\n", title, createdBranchClause(d))
	return nil
}

// storedSession reads back the row the TUI recorded for this create. A miss is an
// error rather than a quieter success: it is the one thing that separates a direct
// (non-git) session, which legitimately has no branch to print, from a create whose
// outcome was lost.
func storedSession(title, repo string) (session.InstanceData, error) {
	instances, err := loadStoredInstances()
	if err != nil {
		return session.InstanceData{}, fmt.Errorf(
			"atrium took the request but this process could not read back the session it made: %w", err)
	}
	want := filepath.Clean(repo)
	for _, d := range instances {
		if d.Title == title && filepath.Clean(d.Path) == want {
			return d, nil
		}
	}
	return session.InstanceData{}, fmt.Errorf(
		"atrium took the request but recorded no session %q in %s; the outcome was lost rather than "+
			"reported, so check %s and the log before retrying", title, repo, config.StateFileName)
}

// createdBranchClause names the branch and worktree the TUI recorded for d, or "" for
// a direct (non-git) session, which has neither. Reached only with a row in hand, so ""
// here means "no branch" and nothing else.
func createdBranchClause(d session.InstanceData) string {
	switch {
	case d.Branch == "":
		return ""
	case d.Worktree.WorktreePath == "":
		return fmt.Sprintf(" on %s", d.Branch)
	default:
		return fmt.Sprintf(" on %s (%s)", d.Branch, d.Worktree.WorktreePath)
	}
}
