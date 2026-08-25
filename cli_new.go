package main

import (
	"context"
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
	"github.com/ZviBaratz/atrium/session/agent"

	"github.com/spf13/cobra"
)

var (
	newPathFlag     string
	newProgramFlag  string
	newProfileFlag  string
	newVariantsFlag string
	newBranchFlag   string
	newModelFlag    string
	newModeFlag     string
	newEffortFlag   string
	newAccountFlag  string
	newForceFlag    bool
	newWaitFlag     time.Duration

	newCmd = &cobra.Command{
		Use:   "new <title> [prompt]",
		Short: "Create one or more sessions without a TUI",
		Long: "Requests a new session — a git worktree, a branch and an agent — the same way\n" +
			"pressing the new-session key does, but from a script, a CI job or an agent\n" +
			"working through a queue of issues.\n\n" +
			"Creation is asynchronous. The request is spooled to the data directory and the\n" +
			"running Atrium picks it up within about a second; with no Atrium running it\n" +
			"stays queued and is created the next time one starts, provided that is within 24\n" +
			"hours — a request older than that names a branch point the tree has moved on\n" +
			"from, so it is discarded with a receipt rather than built. An Atrium that is\n" +
			"running but has handed its terminal to a session is a third case: its poll loop\n" +
			"is parked, so the request waits for the detach rather than for a relaunch. That\n" +
			"case is detected rather than left to be guessed at — it warns on stderr when it\n" +
			"spools, and --wait names it at the deadline instead of listing every possibility.\n" +
			"Use --wait to block until the session actually exists and be told the branch it\n" +
			"was given (`atrium ls` reports the same branch once it does).\n\n" +
			"The title is the session's name, and because the branch and tmux names derive\n" +
			"from it, choosing a title is choosing a branch. A title whose derived names are\n" +
			"already taken is refused rather than silently suffixed.\n\n" +
			"--variants is the one exception, and it is an exception because it is asked\n" +
			"for. It fans one request out across several sessions — --variants\n" +
			"claude:2,codex:1 creates three — sharing this prompt, this base branch and\n" +
			"this repo. N sessions cannot share one branch, so the title becomes a stem:\n" +
			"the variants are named <title>-1, <title>-2 and so on, skipping any name a\n" +
			"session or a local branch in the target repo already owns. Each derived title\n" +
			"is printed as it is queued, and --wait names the branch each one was given.\n" +
			"Asking which names are taken means asking a repository, so a fan-out of two\n" +
			"or more needs a git target; a fan-out of one keeps the bare title, derives\n" +
			"nothing, and is exactly --profile claude. The derived names meet the same\n" +
			"length limit the title does, so a long title can be refused for a suffix a\n" +
			"plain one would never need. --variants names profiles and chooses what to\n" +
			"run, so it cannot be combined with --program or --profile.\n\n" +
			"The session cap is charged to the whole batch: it fits, or it is refused\n" +
			"whole, each member answered with its own receipt, rather than creating\n" +
			"variants until the cap closes. The charge is live, so room taken after part\n" +
			"of a batch is already built leaves the rest refused together, and each\n" +
			"receipt counts what was still queued rather than what was asked for. One\n" +
			"member is left out of both the count and the refusal: a variant atrium is\n" +
			"recovering from an interrupted build, which is answered at its own gate\n" +
			"instead. --force answers the host-capacity question for the batch exactly\n" +
			"as it does for one session. A batch is built one session at a time, so\n" +
			"--wait over a fan-out has to be sized for all of its builds in series; and\n" +
			"with no --branch each variant starts from the target's HEAD at its own\n" +
			"creation time, so pass --branch when the comparison must share a start\n" +
			"point.\n\n" +
			"--model, --effort and --permission-mode pin the claude flags of the same\n" +
			"names on what the new session runs — the create form's model, effort and\n" +
			"mode fields, as flags. A pin replaces the same flag in a profile's program,\n" +
			"and it needs a program that runs claude: a session that would run anything\n" +
			"else is refused rather than quietly unpinned. A fan-out is the one softer\n" +
			"case, matching the form: the pins ride the claude variants, leave the other\n" +
			"members untouched, and only a batch with no claude member is refused. With\n" +
			"no --program or --profile a pin resolves the configured default program now,\n" +
			"from the stored config, where an unpinned request leaves that choice to the\n" +
			"draining TUI.\n\n" +
			"--account pins which claude_accounts entry the session runs on, by the name it\n" +
			"is configured under — what a pick on the create form's Account picker does for\n" +
			"an entry row. A pool name is not an entry name and is refused, saying so: the\n" +
			"picker's pool rows leave the member to rotation, which is what an unpinned\n" +
			"request already gets for the pool the repository routes to. Without the flag\n" +
			"the account is routed, and for two accounts sharing a pool and a rule that is\n" +
			"a choice the caller does not control. The name is resolved by the atrium that\n" +
			"creates the session, and its CLAUDE_CONFIG_DIR and the account name recorded\n" +
			"for the session come off that one entry, so the label the session carries and\n" +
			"the login it runs under cannot disagree. Like a pick at the keyboard it\n" +
			"overrides pool rotation and creates even on a rate-limited account.\n\n" +
			"Setting CLAUDE_CONFIG_DIR inside the program is the older way to force an\n" +
			"account and it still works, but it is invisible to Atrium: the env the program\n" +
			"sets is closer to the process than the one Atrium injects, so the session runs\n" +
			"on that directory while the account recorded for it is the routed one. It warns\n" +
			"on stderr — including when the program carrying it is the one in your\n" +
			"config.json, which is where this is usually written — and combining it with\n" +
			"--account is refused rather than resolved in the pin's favour, because the\n" +
			"program string is the half that would win.\n\n" +
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
				title:       args[0],
				path:        newPathFlag,
				program:     newProgramFlag,
				profile:     newProfileFlag,
				variants:    newVariantsFlag,
				variantsSet: cmd.Flags().Changed("variants"),
				branch:      newBranchFlag,
				model:       newModelFlag,
				mode:        newModeFlag,
				effort:      newEffortFlag,
				account:     newAccountFlag,
				prompt:      prompt,
				force:       newForceFlag,
				wait:        newWaitFlag,
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
	// variants is the raw --variants spec, unparsed, and variantsSet is whether the flag
	// was given at all. Both, because "" is a value a caller can pass: `--variants
	// "$VARIANTS"` with the variable unset reaches here as the empty string, and reading
	// that as "no fan-out" hands a script one session where it asked for N, with no error
	// and no warning. Unset is the single-session form, which is every path this command
	// had before #761.
	variants    string
	variantsSet bool
	branch      string
	// model, mode and effort are the claude pins --model, --permission-mode and
	// --effort asked for ("" = not asked). They are folded into each resolved
	// program at spool time by agent.ComposeProgramFlags — the create form's own
	// composition — rather than carried as fields of the spooled record: the
	// program string is something every draining TUI already honors verbatim,
	// where a new record field would be silently dropped by one older than it.
	model  string
	mode   string
	effort string
	// account is the claude_accounts name --account pinned ("" = not asked, let routing
	// choose). Unlike the three pins above it does NOT ride the program string: an
	// account is injected as a tmux session env rather than composed into a command, and
	// writing it into the program as `env CLAUDE_CONFIG_DIR=...` is the workaround #854
	// is about — it beats Atrium's own injection, so the session runs on one account
	// while state.json records another. So it travels as outbox.Request.Account, a NAME
	// the draining atrium resolves once for both halves.
	account string
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

	// The flags that name what to run are settled before anything is resolved, so a
	// command line that contradicts itself is answered as such rather than through
	// whichever fact about the world happens to be wrong too.
	if err := checkProgramFlags(r); err != nil {
		return err
	}

	// One read for both the profile table and the branch prefix, and a read is all it
	// is: loadStoredConfig, not config.LoadConfig, for the reasons that function
	// documents — the loader sweeps in-flight temp files and seeds a config.json.
	cfg := loadStoredConfig()
	// What to run is settled ahead of the target, as --profile's answer was before
	// --variants existed, so a profile typo is reported before a bad --path rather than
	// behind it. Resolving the whole spec here rather than inside the plan is what keeps
	// that true for a fan-out: the profile table is the same table either way, and a name
	// that is not in it is a mistake about the command line.
	programs, err := resolveCreatePrograms(cfg, r)
	if err != nil {
		return err
	}
	// The account belongs to the same round as the programs: it is part of what the
	// session will run, so a name that is not in claude_accounts is answered before a
	// bad --path, and before anything is spooled. The name is spooled verbatim, not
	// normalised: matching is exact, so there is nothing to canonicalise, and the drain
	// resolves the same string again against its own config.
	account, err := resolveCreateAccount(cfg, r, programs)
	if err != nil {
		return err
	}
	warnProgramSetsAccount(errOut, cfg, r, programs)

	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	path, err := resolveNewTarget(errOut, r.path, instances)
	if err != nil {
		return err
	}

	reqs, err := planCreateRequests(context.Background(), cfg, r, programs, account, title, path, instances)
	if err != nil {
		return err
	}
	members, err := spoolBatch(reqs)
	if err != nil {
		return err
	}

	// Printed only once the whole batch is committed, so a rollback leaves no line
	// claiming a variant was queued that has just been withdrawn.
	for _, member := range members {
		_, _ = fmt.Fprintf(out, "queued: create %q in %s\n", member.title, path)
	}

	// Once for the command, not once per member: the warning is about what is draining
	// the spool, which is one answer however many records went into it.
	warnSpoolWaiting(errOut, "creating", r.wait > 0)
	if r.wait > 0 {
		return waitForCreates(out, members, path, r.wait)
	}
	return nil
}

// resolveCreatePrograms settles what each requested session will run, and settles it
// against nothing but the config: one program for the single-session form, one per
// variant in spec order for a fan-out, each with the claude pins folded in. Length is
// what the plan below reads it by, so "how many sessions" and "what each runs" are
// one answer rather than two.
func resolveCreatePrograms(cfg *config.Config, r newRequest) ([]string, error) {
	programs, err := resolveBasePrograms(cfg, r)
	if err != nil {
		return nil, err
	}
	return applyProgramPins(cfg, r, programs)
}

// resolveBasePrograms is resolveCreatePrograms before the pins: what --program,
// --profile or --variants named, with "" standing for the draining TUI's default.
func resolveBasePrograms(cfg *config.Config, r newRequest) ([]string, error) {
	if !r.fansOut() {
		program, err := resolveNewProgram(cfg, r.program, r.profile)
		if err != nil {
			return nil, err
		}
		return []string{program}, nil
	}
	specs, err := parseVariantSpec(r.variants)
	if err != nil {
		return nil, err
	}
	return resolveVariantPrograms(cfg, specs)
}

// applyProgramPins folds --model, --permission-mode and --effort into each resolved
// program, exactly as the create form's submit folds its model, mode and effort
// fields: agent.ComposeProgramFlags applies a pin only to a program that resolves to
// claude, so a mixed fan-out carries the pins on its claude members and leaves the
// rest untouched. What the CLI adds is the refusal — the form cannot take a value
// for an agent it renders no field for, and the flag-shaped equivalent of "no field"
// is an error rather than a silent drop, so a request none of whose members runs
// claude is refused whole, before anything is spooled.
//
// A pinned request with no --program or --profile resolves the configured default
// here rather than spooling the "" an unpinned one carries: "" defers the choice of
// program to the draining TUI, and a pin cannot be folded into a choice not yet
// made. The value read is the stored config's, on runNew's standing terms — a TUI
// launched with a one-off root --program override would have chosen differently,
// and the drain's own gates still hold either way.
func applyProgramPins(cfg *config.Config, r newRequest, programs []string) ([]string, error) {
	if r.model == "" && r.mode == "" && r.effort == "" {
		return programs, nil
	}
	claude := 0
	for i, program := range programs {
		if program == "" {
			program = cfg.GetProgram()
		}
		if agent.Resolve(program).Key == agent.KeyClaude {
			claude++
		}
		composed, err := agent.ComposeProgramFlags(program, r.model, r.mode, r.effort)
		if err != nil {
			return nil, err
		}
		programs[i] = composed
	}
	if claude == 0 {
		if len(programs) == 1 {
			return nil, fmt.Errorf("%s pins claude's flags, but the session would run %q, "+
				"which is not claude; drop the pin or pick a claude profile",
				pinFlagNames(r), programs[0])
		}
		return nil, fmt.Errorf("%s pins claude's flags, but none of the %d requested variants "+
			"runs claude; drop the pin or add a claude entry to --variants",
			pinFlagNames(r), len(programs))
	}
	return programs, nil
}

// resolveCreateAccount settles which claude_accounts entry --account named, returning
// the name to spool ("" when the flag was not passed, which leaves the choice to routing
// exactly as it was before the flag existed).
//
// A courtesy check, like every other check this command makes: the drain resolves the
// name again against its own config, which is the copy that will be honoured. What it
// buys is the error a caller can act on — a typo answered at the terminal that made it,
// naming the accounts that exist, rather than minutes later in a rejection receipt.
//
// The contradiction is not a courtesy. A program that sets CLAUDE_CONFIG_DIR itself wins
// over the dir Atrium injects (agent.ProgramSetsClaudeConfigDir says why), so a pin
// combined with one would stamp the pinned name onto a session running somewhere else,
// which is the exact divergence #854 is about. Refused rather than resolved in either
// direction: honouring the pin means silently rewriting the caller's own program string,
// and honouring the program means accepting the flag and ignoring it. The drain refuses
// the same combination independently, because a program string it substitutes itself is
// one this command never sees.
func resolveCreateAccount(cfg *config.Config, r newRequest, programs []string) (string, error) {
	if r.account == "" {
		return "", nil
	}
	if overriding := programsSettingConfigDir(cfg, programs); len(overriding) > 0 {
		return "", fmt.Errorf("--account %q cannot hold: %s sets CLAUDE_CONFIG_DIR itself, "+
			"which the agent reads in preference to the directory Atrium injects; drop "+
			"--account, or run a program that leaves the account to Atrium",
			r.account, describeConfigDirProgram(r, programs, overriding))
	}
	if problem := cfg.ClaudeAccountPinProblem(r.account); problem != "" {
		return "", fmt.Errorf("--account %q %s", r.account, problem)
	}
	return r.account, nil
}

// warnProgramSetsAccount says on stderr that a program string is choosing the Claude
// account behind Atrium's back, and what that costs. It is a warning and not a refusal
// because that spelling is the only way to pin an account on an atrium without
// --account, so it is in active use; what it is not is silent.
//
// Never reached alongside a pin — resolveCreateAccount refuses that combination first —
// so this speaks only to a caller who has not been told about the flag yet, which is why
// it names it.
//
// Silent when claude_accounts is empty, which is not a courtesy either: with no accounts
// configured Config.ResolveClaudeAccount stamps nothing, so there is no recorded account
// for the program's directory to disagree with, and the sentence below would describe a
// divergence that cannot happen while recommending a flag that would be refused.
func warnProgramSetsAccount(errOut io.Writer, cfg *config.Config, r newRequest, programs []string) {
	if r.account != "" || len(cfg.ClaudeAccounts) == 0 {
		return
	}
	overriding := programsSettingConfigDir(cfg, programs)
	if len(overriding) == 0 {
		return
	}
	_, _ = fmt.Fprintf(errOut, "warning: %s sets CLAUDE_CONFIG_DIR, so that is the directory the "+
		"agent reads while Atrium records the account its routing chose — the two can name "+
		"different logins. Pass --account <name> to pin one Atrium also records.\n",
		describeConfigDirProgram(r, programs, overriding))
}

// programsSettingConfigDir returns the programs that set CLAUDE_CONFIG_DIR in their own
// command line, in the order they were resolved. What counts as setting it is
// agent.ProgramSetsClaudeConfigDir, shared with the drain's own guard so the two cannot
// disagree about the shape.
//
// The "" a program list carries when no --program, --profile or --variants was passed is
// resolved to the configured default first, because that is the string the draining TUI
// will run. An atrium whose config.json sets the account this way sets it for every
// session, which is where the workaround #854 describes actually lives — so a check that
// read only what the flags named would pass exactly the command line this flag was added
// for. Resolved for the check alone: the "" is still what gets spooled, so the drain keeps
// choosing its own default, as it does for a request from any other atrium.
func programsSettingConfigDir(cfg *config.Config, programs []string) []string {
	var hits []string
	for _, program := range programs {
		if program == "" {
			program = cfg.GetProgram()
		}
		if agent.ProgramSetsClaudeConfigDir(program) {
			hits = append(hits, program)
		}
	}
	return hits
}

// describeConfigDirProgram is the subject of the two messages above: which program is
// setting the variable, in a spelling that does not tell the caller to look at a flag
// they may not have passed.
//
// Three shapes, because the sentence has to be true for a caller who wrote the program
// string, one who named a profile or a variant spec, and one who passed neither and is
// about to be shown a string out of their config.json for the first time. A fan-out is
// counted rather than quoted — three command lines in one sentence is not readable — and
// counted against the whole batch, so "1 of 2" says the other member is fine. The count
// is phrased as the program OF n sessions rather than as n programs so that the verb
// following it is singular whatever the count is.
func describeConfigDirProgram(r newRequest, programs, overriding []string) string {
	if len(programs) != 1 {
		return fmt.Sprintf("the program of %d of the %d requested sessions",
			len(overriding), len(programs))
	}
	if r.program == "" && r.profile == "" && !r.variantsSet {
		return fmt.Sprintf("the configured default program %q", overriding[0])
	}
	return fmt.Sprintf("the program %q", overriding[0])
}

// pinFlagNames names the pin flags this command line actually passed, so a refusal
// tells the caller which of their own flags to drop rather than listing all three.
func pinFlagNames(r newRequest) string {
	var names []string
	if r.model != "" {
		names = append(names, "--model")
	}
	if r.mode != "" {
		names = append(names, "--permission-mode")
	}
	if r.effort != "" {
		names = append(names, "--effort")
	}
	return strings.Join(names, "/")
}

// planCreateRequests turns the command line into the records to spool: one for an
// ordinary create, N for a fan-out.
//
// The single-session branch is what this command has always done, unchanged and
// deliberately including what it does NOT do — it runs no git, so an ordinary
// `atrium new` still spools without forking a subprocess. A --variants total of one
// takes that same branch with the resolved program, so `--variants claude:1` is a true
// synonym for `--profile claude` down to the bare title, matching the create form's own
// contract that a batch of one is not a batch.
func planCreateRequests(
	ctx context.Context, cfg *config.Config, r newRequest,
	programs []string, account, title, path string, instances []session.InstanceData,
) ([]outbox.Request, error) {
	// Tail only, for runSend's reason: trailing newlines are an artifact of how the text
	// arrived (a heredoc, a pipe), while leading whitespace could be meaningful. Trimmed
	// once here and shared by every member — one prompt is what a bake-off is.
	base := outbox.Request{
		Path:   path,
		Branch: r.branch,
		Prompt: strings.TrimRight(r.prompt, "\r\n"),
		Force:  r.force,
		// One account for every member of a fan-out, and deliberately so: a bake-off
		// compares programs, and spreading its members across two logins would put the
		// thing being measured and the thing paying for it on different accounts. An
		// unpinned fan-out still rotates, which is the spread a caller who wants one asks
		// for by not passing this.
		Account: account,
	}

	if len(programs) == 1 {
		if err := checkTitleFree(cfg.BranchPrefix, title, path, instances); err != nil {
			return nil, err
		}
		base.Title, base.Program = title, programs[0]
		return []outbox.Request{base}, nil
	}

	titles, err := planVariantTitles(ctx, cfg.BranchPrefix, title, len(programs), path, instances)
	if err != nil {
		return nil, err
	}
	batch, err := outbox.NewBatchID()
	if err != nil {
		return nil, err
	}
	reqs := make([]outbox.Request, 0, len(programs))
	for i, program := range programs {
		member := base
		member.Title, member.Program, member.Batch = titles[i], program, batch
		// Declared per member rather than inferred by the drain, which cannot infer it:
		// a batch becomes visible one atomic rename at a time, so what a drain tick can
		// COUNT is not what this command committed to. See outbox.Request.BatchSize.
		member.BatchSize, member.BatchIndex = len(programs), i+1
		reqs = append(reqs, member)
	}
	return reqs, nil
}

// warnSpoolWaiting prints the "nothing is going to pick this up soon" warning shared by
// `new` and `send`, or nothing when there is nothing to say. See spoolWarningFor
// (drainstate.go) for the two cases and who each is addressed to.
//
// waiting says the caller is about to block on --wait, which suppresses the no-TUI
// warning: that one predicts an outcome the wait is about to report for real, and
// printing a prediction before the wait even begins is what TestNewWaitSkipsTheNoTUIWarning
// exists to stop.
//
// The parked warning is deliberately NOT suppressed, and the asymmetry is the point. It
// is not addressed to the caller at all — it is addressed to the person at the keyboard,
// the only party who can unblock it, and it is actionable the moment it is printed
// rather than at the deadline. Under `--wait 60s` the suppressed version would leave
// them a minute of silence in front of a session that is not going to appear.
func warnSpoolWaiting(errOut io.Writer, gerund string, waiting bool) {
	verdict, payload := drainState()
	if waiting && verdict != drainParked {
		return
	}
	if msg := spoolWarningFor(verdict, payload, gerund); msg != "" {
		_, _ = fmt.Fprint(errOut, msg)
	}
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

// fansOut reports whether this command line asked for a fan-out. The flag being GIVEN
// decides, not what it was given, so an explicitly empty --variants reaches
// parseVariantSpec and is refused there rather than quietly meaning "one session". A
// non-empty spec answers yes on its own, for a runNew caller that did not come through
// cobra and has no Changed to read.
func (r newRequest) fansOut() bool { return r.variantsSet || r.variants != "" }

// checkProgramFlags refuses a command line whose flags disagree about what to run.
//
// --variants names several programs, so it excludes the two singular flags. Written by
// hand rather than through cobra's mutually-exclusive groups, following resolveNewProgram
// — which owns the --program/--profile half — for that function's reasons: the message
// names what to drop, and a cobra group is invisible to a test that calls runNew
// directly, which most of this command's tests do.
//
// It runs before anything is loaded or resolved, and that ordering is the point: a caller
// who passed contradictory flags AND a bad path should hear about the flags, which is the
// mistake they can see in their own command line.
func checkProgramFlags(r newRequest) error {
	if !r.fansOut() {
		return nil
	}
	if r.program != "" {
		return errors.New("--variants chooses the programs itself; drop --program")
	}
	if r.profile != "" {
		return errors.New("--variants chooses the profiles itself; drop --profile")
	}
	return nil
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
// The deadline message names who is not draining rather than listing everyone who might
// not be — see joinTimedOut and drainerClause. That was not possible when this was
// written: tui.lock is held whether the loop is running or parked, so a live-but-attached
// Atrium and a live-and-polling one were the same observation from here.
//
// That second half is what makes the next line safe rather than a race. The branch is
// read back out of state.json rather than derived from the
// title. They would usually agree, but "usually" is not something to print: the
// slug rules have a hash fallback for titles that sanitize to nothing, and a
// non-git target has no branch at all.
func waitForCreate(out io.Writer, path, title, repo string, timeout time.Duration) error {
	if err := awaitSpool(path, outbox.ClaimPath(path), timeout, spoolWaitCopy{
		refused: "atrium did not create the session",
		// The file half of this stays deliberately vague, because it has to: the record is
		// held for the whole of Start, so its presence means "queued or being built" and
		// this side cannot tell those apart.
		//
		// The drainer half no longer has to be. It used to enumerate all three cases — a
		// running TUI, an attached one, and none at all — because nothing here could tell
		// which held; handover.lock is what changed that, so drainerClause names the one
		// that is true and this enumeration is reached only when neither lock could answer.
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s without a session appearing; the request is "+
				"still in the outbox — either queued, or being built right now, since a create is "+
				"held there until its worktree, branch and agent exist", timeout),
				"A running atrium drains it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does")
		},
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
