// Atrium is a terminal command center for orchestrating multiple AI coding
// agents, each running in its own tmux session inside an isolated git worktree.
// This package is the Cobra CLI entrypoint: the bare `atrium` invocation loads
// config, initializes tmux, and starts the Bubble Tea TUI (app.Run); the hidden
// --daemon flag reuses the binary as the autoyes background process.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ZviBaratz/atrium/app"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/daemon"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/internal/profile"
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	// version is overridden at build time via -ldflags "-X main.version=...".
	// GoReleaser injects the tag (e.g. 0.1.0); the justfile injects git describe.
	// Unstamped builds (plain `go build`) report "dev".
	version         = "dev"
	programFlag     string
	autoYesFlag     bool
	daemonFlag      bool
	updateCheckOnly bool
	verboseFlag     bool
	// binName is overwritten from os.Args[0] in main(); the default keeps help
	// and error text sensible for any code path that reads it before then.
	binName = "atrium"
	// quitSignals is the set that drives a graceful shutdown. Registering SIGHUP
	// is load-bearing: it overrides Go's default "terminate without running
	// defers" disposition, so closing the terminal / losing SSH cancels the
	// lifecycle context and lets the deferred autoyes-daemon handoff run instead
	// of hard-killing the process. Extracted as a package var so a test can assert
	// SIGHUP stays in the set (see TestQuitSignals).
	quitSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	rootCmd     = &cobra.Command{
		Use:   "atrium",
		Short: "Atrium - A command center for orchestrating multiple AI coding agents like Claude Code, Aider, Codex, and Amp.",
		// A runtime failure is not a usage error: let main() be the single
		// error printer (exit 1, message to stderr) rather than Cobra also
		// printing its own "Error: ..." line. SilenceUsage drops the usage
		// block on failures; both flags propagate to every subcommand.
		SilenceErrors: true,
		SilenceUsage:  true,
		// Apply --verbose before any command runs (and so before the deferred
		// log.Close), for the root command and every subcommand. None of them
		// define their own PersistentPreRun, so this one covers all.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			log.SetVerbose(verboseFlag)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Root lifecycle context: cancelled on SIGINT/SIGTERM/SIGHUP so in-flight
			// git/gh/tmux subprocesses are killed rather than orphaned on shutdown,
			// and — crucially for SIGHUP (terminal close / SSH disconnect) — so the
			// deferred autoyes-daemon handoff below runs instead of the process being
			// hard-killed with its defers skipped. (Inside the TUI, Ctrl+C is a key
			// event handled by Bubble Tea, not a signal.)
			ctx, stop := signal.NotifyContext(context.Background(), quitSignals...)
			defer stop()
			log.Initialize(logDir(), daemonFlag)
			defer log.Close()

			// Arm the profiling trigger for both the TUI and the daemon, after the log
			// exists (it is where the written path is reported) and before either takes
			// over the terminal. It costs one parked goroutine; see internal/profile for
			// why the trigger is a signal rather than a flag.
			defer profile.Install(ctx)()

			// Resolve NO_COLOR here, above BOTH tmux.Init calls below, because the
			// managed tmux config is rendered by those and tmux — not Bubble Tea —
			// draws the in-session status band. Deciding this any later (inside
			// app.Run, say) writes the band's colours into the config first and
			// leaves every session started this run tinted, on the one surface the
			// renderer's colour profile cannot reach.
			//
			// Atrium tests the variable itself rather than relying on
			// colorprofile.Detect, which parses it through strconv.ParseBool and so
			// ignores NO_COLOR=yes, =x, =0 and =2 — all four of which the spec says
			// mean off. The restore function is discarded: this is process startup,
			// the same way theme.Set's is in app.Run.
			theme.SetMono(theme.NoColorRequested(os.Environ()))

			if daemonFlag {
				cfg := config.LoadConfig()
				if err := tmux.Init(cfg.TmuxConfigOverride, cfg.GetSessionContextBar()); err != nil {
					log.WarningLog.Printf("failed to initialize tmux config: %v", err)
				}
				if err := daemon.RunDaemon(ctx, cfg); err != nil {
					log.ErrorLog.Printf("failed to start daemon: %v", err)
					return err
				}
				return nil
			}

			// cs no longer requires being launched from within a git repository. A new
			// session's target repo defaults to the highlighted session's repo (or the
			// cwd when it is a repo), and the N overlay's directory picker lets you choose
			// any project. When there is no repo context, session creation guides you to
			// pick one rather than failing at startup.
			cfg := config.LoadConfig()

			// Enforce one interactive atrium per data dir (issue #230). A second TUI
			// sharing this state.json would let this run's exit-time autoyes daemon
			// snapshot clobber the other's instances and non-instance state; refuse to
			// start instead. The kernel frees an flock on process death, so a crashed
			// TUI never wedges the next one. The defer registers BEFORE the exit-time
			// LaunchDaemon defer below, so (LIFO) the lock is released only AFTER that
			// daemon is launched — otherwise a second TUI could grab the lock and run
			// concurrently with the daemon we just launched, the exact hazard above.
			releaseTUILock, err := acquireTUILockOrWarn("running", "close the other instance before starting a new one")
			if err != nil {
				return err
			}
			defer releaseTUILock()

			if err := tmux.Init(cfg.TmuxConfigOverride, cfg.GetSessionContextBar()); err != nil {
				log.WarningLog.Printf("failed to initialize tmux config: %v", err)
			}

			// Program flag overrides config
			program := cfg.GetProgram()
			if programFlag != "" {
				program = programFlag
			}
			// AutoYes flag overrides config
			autoYes := cfg.AutoYes
			if autoYesFlag {
				autoYes = true
			}
			// The daemon takes over auto-accepting only while the TUI is closed.
			// Whether to launch it is decided at exit time from the *persisted*
			// config — not the autoYes value merged above — so an auto_yes toggle
			// made in the settings panel during this run takes effect.
			defer func() {
				if !shouldLaunchDaemonOnExit(autoYesFlag) {
					return
				}
				if err := daemon.LaunchDaemon(ctx); err != nil {
					log.ErrorLog.Printf("failed to launch daemon: %v", err)
				}
			}()
			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
			}

			return app.Run(ctx, program, autoYes, version, binName)
		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config and log paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJSON, _ := json.MarshalIndent(cfg, "", "  ")

			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Config: %s\n", filepath.Join(configDir, config.ConfigFileName))
			// The log path is the answer to "where do I look?", and since it moved
			// out of the temp dir this is the only place that reports it on demand.
			// Both paths go above the config JSON so neither is buried under it.
			logPath, logErr := log.Destination()
			if logErr != nil {
				_, _ = fmt.Fprintf(out, "Log: %s (unavailable: %v)\n", logPath, logErr)
			} else {
				_, _ = fmt.Fprintf(out, "Log: %s\n", logPath)
			}
			_, _ = fmt.Fprintf(out, "%s\n", configJSON)

			return nil
		},
	}

	profilesCmd = &cobra.Command{
		Use:   "profiles",
		Short: "Manage agent profiles",
	}

	profilesDetectCmd = &cobra.Command{
		Use:   "detect",
		Short: "Probe for installed agent CLIs and add missing profiles",
		Long: "Probes the machine for known agent CLIs (claude, codex, gemini, aider) and appends a\n" +
			"profile for each newly found one. Existing profiles and the default program are never\n" +
			"modified, so hand-edited entries always survive a re-detect.",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			cfg := config.LoadConfig()
			added := cfg.MergeDetectedProfiles(config.DetectAgentProfiles())
			if len(added) == 0 {
				fmt.Println("no new agents detected; profiles unchanged")
				return nil
			}
			if err := config.SaveConfig(cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			fmt.Printf("added profiles: %s\n", strings.Join(added, ", "))
			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
			// Only link to a release for a clean release version. Dev builds report
			// "dev" or a `git describe` string (e.g. 0.1.0-5-gabc-dirty) that has no
			// corresponding release page. Same predicate as the updater, so the two
			// commands can never disagree on what counts as a release build.
			if update.IsUpdatableVersion(version) {
				fmt.Printf("https://github.com/ZviBaratz/atrium/releases/tag/v%s\n", version)
			}
		},
	}

	updateCmd = &cobra.Command{
		Use:   "update",
		Short: "Update atrium to the latest release",
		Long: "Checks GitHub releases for a newer version, downloads the matching archive,\n" +
			"verifies its checksum, and atomically replaces the current binary. Running\n" +
			"sessions are not disturbed; the new version takes effect on the next launch.",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			if !update.IsUpdatableVersion(version) {
				return fmt.Errorf("this is a dev build (version %q); self-update only works on release builds — see install.sh", version)
			}
			// Same signal-driven lifecycle as the root command: Ctrl+C (or a
			// terminal close, via SIGHUP) aborts a download cleanly instead of
			// leaving the HTTP transfer orphaned.
			ctx, stop := signal.NotifyContext(context.Background(), quitSignals...)
			defer stop()

			// Bound the metadata query so a blackholed connection (captive portal,
			// dropped packets) fails fast instead of hanging the command. The
			// download below stays on the signal context: large archives on slow
			// links shouldn't be killed by an arbitrary deadline, and Ctrl+C works.
			checkCtx, cancelCheck := context.WithTimeout(ctx, 30*time.Second)
			rel, err := update.Check(checkCtx, version)
			cancelCheck()
			if err != nil {
				return fmt.Errorf("update check failed: %w", err)
			}
			if rel == nil {
				fmt.Printf("%s v%s is the latest version\n", binName, version)
				return nil
			}
			if updateCheckOnly {
				fmt.Printf("v%s is available (current: v%s) — run `%s update` to install\n", rel.Version, version, binName)
				return nil
			}
			// Verify writability before printing the "updating..." line so a
			// permission failure never reads as "updating ... / update failed:".
			if err := rel.Preflight(); err != nil {
				return fmt.Errorf("cannot apply update: %w", err)
			}
			fmt.Printf("updating v%s → v%s ...\n", version, rel.Version)
			if err := rel.Apply(ctx); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}
			fmt.Printf("✓ updated to v%s — restart %s to apply\n", rel.Version, binName)
			return nil
		},
	}

	hookEventArg     string
	hookStateFileArg string
	// The SessionStart brief's facts, baked into that hook's command line at launch (#485).
	// They are flags rather than a state.json read because config.LoadState mutates the data
	// dir, which a subprocess running beside a live TUI must not do — see session/tmux/brief.go.
	hookBrief    tmux.SessionBrief
	hookEventCmd = &cobra.Command{
		Use:    tmux.HookSubcommand,
		Short:  "Internal: record a Claude Code hook event into a session's status file",
		Hidden: true,
		// Invoked by the injected Claude Code settings.json hooks (see
		// session/tmux/hooks.go), once per hook event, to maintain the structured
		// status record — the working/ready latch plus the set of in-flight sub-agent
		// ids that distinguishes a finished turn from one still waiting on a background
		// sub-agent (#290), plus the turn's permission mode (#324). It runs the locked
		// read-modify-write that shell can't do portably, then exits. Best-effort by contract:
		// a hook must never fail or stall the agent, so this always exits 0, reads stdin only
		// for the events that need a payload field (tmux.HookEventReadsStdin), and bounds that
		// read in both size and time.
		//
		// One event runs the other way: session-start prints a situational brief on stdout for
		// Claude to read back into the agent's context (#485). It touches no state file, so it
		// is routed before the state-record path rather than through it.
		RunE: func(cmd *cobra.Command, args []string) error {
			if hookEventArg == tmux.HookEventSessionStart {
				runSessionStartHook(os.Stdout, hookBrief)
				return nil
			}
			runHookEvent(hookStateFileArg, hookEventArg, os.Stdin)
			return nil
		},
	}

	doctorCmd = &cobra.Command{
		Use:   "doctor",
		Short: "Check Atrium's core dependencies (tmux, git, gh) and agent CLI heuristic versions",
		Long: "Core dependencies probes tmux, git, and gh: tmux and git are\n" +
			"required, and one that is missing — or present but older than the version Atrium\n" +
			"needs — exits nonzero so scripts/CI can gate; gh is optional, needed\n" +
			"only for push/PR flows, and its authentication is reported but never fatal. Agent\n" +
			"heuristics probes installed agent CLIs (claude, codex, gemini, aider) and reports whether\n" +
			"each one's version has drifted past the version Atrium's pane-classification heuristics\n" +
			"were verified against; drift means a session's status may be misread (re-verify the\n" +
			"matcher strings in session/agent/registry.go). Feature gates covers what a version\n" +
			"cannot: Claude renders parts of its UI from server-resolved feature gates, so the same\n" +
			"version can render differently — per account — with no version change at all. It reports\n" +
			"the value Claude last resolved in each config dir against the value those heuristics were\n" +
			"verified under. On Linux, OOM ranking compares the shared tmux server's oom_score against\n" +
			"each agent pane's, and warns if an out-of-memory kill would take the server (and with it\n" +
			"every session) instead of a single recoverable agent. Claude account identities reports\n" +
			"which login each configured account's config_dir is actually signed in as, checks it\n" +
			"against that account's expect_account, and flags separate accounts that turn out to share\n" +
			"one login — the state in which sessions silently bill an account nobody routed them to.\n" +
			"Terminal background detection reports which rung of the light/dark ladder can answer here,\n" +
			"for anyone whose theme: auto did not adapt: it reads COLORFGBG, and names OSC 11 as the\n" +
			"rung that outranks it but cannot be probed from a one-shot command — that query needs the\n" +
			"running TUI, which sends it at startup, on refocus and after a detach. On Linux, Orphaned\n" +
			"tmux servers finds tmux servers Atrium started that are still running but are not the live\n" +
			"one — including the case no socket lookup can reach, where the socket file was deleted along\n" +
			"with the temp directory holding it, so neither `tmux ls` nor `atrium reset` can name the\n" +
			"server and only a process scan finds it. It also lists socket files left behind by servers\n" +
			"that already died. That whole section is read-only: it prints the command for each case and\n" +
			"runs none of them, because these servers hold live agents with unpushed work (see\n" +
			"`atrium reap`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			// Give each section its own probe budget off a fresh context: the core-dep
			// probes include a networked `gh auth status` that can be slow, and sharing
			// one deadline would let it eat into the agent probes' budget and spuriously
			// time them out.
			depsCtx, cancelDeps := context.WithTimeout(context.Background(), doctor.ProbeTimeout)
			defer cancelDeps()
			deps := doctor.CheckDeps(depsCtx, runtime.GOOS, ghAuthChecker)
			fmt.Print(doctor.RenderDeps(deps))
			fmt.Println()

			agentCtx, cancelAgents := context.WithTimeout(context.Background(), doctor.ProbeTimeout)
			defer cancelAgents()
			fmt.Print(doctor.Render(doctor.CheckInstalled(agentCtx)))

			// No probe budget: this section only reads a local JSON file per config
			// dir, so there is no subprocess to wedge and nothing to bound.
			if gates := doctor.RenderGates(doctor.CheckGatesInstalled()); gates != "" {
				fmt.Println()
				fmt.Print(gates)
			}

			// Host capacity: pure introspection (thread count + RAM), no subprocess and
			// no config/tmux access, so it is safe beside a live TUI and needs no budget.
			fmt.Println()
			fmt.Print(doctor.RenderCapacity(doctor.CheckCapacity()))

			// Terminal background detection: a pure read of the environment, so it needs
			// no budget either. It reports the rungs it can reach and names the one it
			// cannot — OSC 11 needs the running TUI to send the query and wait.
			fmt.Println()
			fmt.Print(doctor.RenderScheme(doctor.CheckScheme(os.Environ())))

			// OOM ranking: on Linux, compares the shared tmux server's oom_score against
			// each agent pane's so a single kill is shown to shed one recoverable session
			// rather than the server (every session). Read-only tmux queries + /proc, and
			// a read-only config read — safe beside a live TUI; unavailable off Linux. The
			// tmux queries get their own probe budget so a wedged server can't hang doctor.
			oomCtx, cancelOOM := context.WithTimeout(context.Background(), doctor.ProbeTimeout)
			defer cancelOOM()
			fmt.Println()
			fmt.Print(doctor.RenderOOM(doctor.CheckOOM(oomCtx)))

			// Orphaned tmux servers: servers Atrium started that outlived their run,
			// including the ones no socket lookup can reach because the socket file went
			// with a deleted temp root (#547). Read-only — a /proc inventory plus
			// `tmux -S … display-message` probes — and it kills and deletes nothing: the
			// processes here hold live agents with unpushed work, so every remedy is
			// printed for the user to run. Its own probe budget, for the same reason the
			// OOM section has one.
			orphanCtx, cancelOrphans := context.WithTimeout(context.Background(), doctor.ProbeTimeout)
			defer cancelOrphans()
			fmt.Println()
			fmt.Print(doctor.RenderOrphans(doctor.CheckOrphans(orphanCtx)))

			// Claude account identities: reads the login recorded in each account's
			// config dir and compares it against that account's expect_account. No
			// probe budget for the same reason as the gates section — one local JSON
			// read per dir, no subprocess. This is the only check that can see an
			// account whose directory was re-logged-in to a different login: routing
			// verifies a path, and every other section takes the name beside it on
			// trust.
			if ids := doctor.RenderAccountIdentity(doctor.CheckAccountIdentityInstalled()); ids != "" {
				fmt.Println()
				fmt.Print(ids)
			}

			// Account pools: same config.LoadConfig() read the gates section uses
			// (installedGateDirs above) — flags pool members that share a
			// config_dir, since that's the same Claude login and rotation across
			// them is a silent no-op. Distinct from the identity section above, which
			// catches the inverse: different dirs that turn out to be one login.
			if pools := doctor.RenderPools(doctor.CheckPools(config.LoadConfig())); pools != "" {
				fmt.Println()
				fmt.Print(pools)
			}

			// Account state keys: state.json indexes the cluster order, the rate-limit
			// flags and the rotation cursors by account/pool NAME, so a rename can leave
			// entries naming something config no longer has. Harmless (unknown names are
			// kept on purpose, so a slot survives its sessions), which is why they are
			// only reported. state.json is read directly, never via config.LoadState —
			// doctor must not mutate a data dir a live TUI owns.
			if keys := doctor.RenderAccountKeys(
				doctor.CheckAccountKeys(config.LoadConfig(), loadStoredAccountKeys())); keys != "" {
				fmt.Println()
				fmt.Print(keys)
			}

			if doctor.RequiredUnmet(deps) {
				// Nonzero exit for CI/scripts. The root command already sets
				// SilenceErrors/SilenceUsage, so main() prints just this message to
				// stderr (no "Error:"/usage noise over the report rendered above).
				// Deliberately covers both causes without naming which: the table
				// rendered directly above already says, and repeating it here would be a
				// second copy to keep in sync.
				return fmt.Errorf("a required dependency is missing or too old (see the hints above; run `atrium doctor` again after fixing it)")
			}
			return nil
		},
	}
)

// ghAuthChecker reports whether gh is authenticated, for the doctor core-deps
// probe. It runs `gh auth status` under the same short probe budget; any nonzero
// exit (not logged in, misconfigured) counts as unauthenticated. gh is optional,
// so this never fails the command — it only downgrades gh's reported state.
func ghAuthChecker(ctx context.Context) error {
	return exec.CommandContext(ctx, "gh", "auth", "status").Run()
}

// shouldLaunchDaemonOnExit reports whether the autoyes daemon should take over
// when the TUI exits. It re-reads the persisted config rather than reusing the
// value merged at startup, so an auto_yes toggle made in the settings panel
// during the run takes effect; the -y flag still wins for the run it was given.
func shouldLaunchDaemonOnExit(autoYesFlag bool) bool {
	return autoYesFlag || config.LoadConfig().AutoYes
}

// runHookEvent applies one Claude Code hook event to a session's status file. It is the
// body of the hidden `hook` subcommand. Best-effort: a missing arg is a silent no-op, and
// an update error is surfaced to stderr (which Claude captures for its own hook logs) but
// never propagated — the caller always exits 0 so a hook can't disturb the agent.
func runHookEvent(stateFile, event string, stdin io.Reader) {
	if stateFile == "" || event == "" {
		return
	}
	var payload tmux.HookPayload
	if tmux.HookEventReadsStdin(event) {
		payload = parseHookPayload(stdin)
	}
	// Claude exports the turn's resolved effort level to every hook subprocess. Reading the
	// env var rather than the stdin payload is what lets the N-per-turn per-tool latches carry
	// effort at all: HookEventReadsStdin keeps those deliberately payload-free, so their
	// subprocess never touches stdin. The once-per-turn edges do read it, for permission_mode
	// (#324) — safe because parseHookPayload is bounded in both size and time, so the "a hook
	// can never block on stdin" property survives the change. Empty for a model without effort
	// support (and stale on UserPromptSubmit) — UpdateHookState's write rule sorts that out.
	if err := tmux.UpdateHookState(stateFile, event, payload, os.Getenv("CLAUDE_EFFORT")); err != nil {
		fmt.Fprintf(os.Stderr, "atrium hook: %v\n", err)
	}
}

// runSessionStartHook prints the SessionStart context brief for Claude to fold into the
// agent's context (#485). It is the only hook that writes TO the agent, and the only one whose
// stdout is part of the contract.
//
// Best-effort in the same way its sibling is, and for a sharper reason: Claude discards a
// malformed or empty hook payload silently, so every failure here is already indistinguishable
// from "no brief". An incomplete set of facts prints nothing rather than a sentence with a hole
// in it, a marshal error goes to stderr (which Claude captures for its own hook logs), and the
// caller always exits 0 so a brief can never disturb the agent.
func runSessionStartHook(w io.Writer, b tmux.SessionBrief) {
	out, err := tmux.SessionStartHookOutput(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "atrium hook: session brief: %v\n", err)
		return
	}
	if out == nil {
		return
	}
	if _, err := fmt.Fprintln(w, string(out)); err != nil {
		fmt.Fprintf(os.Stderr, "atrium hook: session brief: %v\n", err)
	}
}

// hookPayloadLimit bounds how much of a hook's stdin payload we will buffer. json.Decoder
// buffers a whole top-level value before unmarshalling, so an unbounded read is the only real
// memory exposure here. 4 MiB sits far above any payload these events carry and far below
// anything that could hurt; on overflow the decode fails and we degrade to the zero payload.
//
// That degradation is not free, so the cap is set where it can only ever fire on a broken
// contract: an overflowing SubagentStart/Stop loses its agent_id, stranding a member in the
// in-flight set until the wall-clock watchdog clears it (#290) — a bounded, self-healing
// cost, but a real one, and one the previous unbounded Decode would not have paid.
const hookPayloadLimit = 4 << 20

// hookPayloadTimeout caps the wait for stdin. A var so tests can shorten it.
var hookPayloadTimeout = 2 * time.Second

// parseHookPayload reads the fields the state record consumes out of a hook's stdin payload.
// Best-effort three ways, and every degradation is safe:
//
//   - BOUNDED IN SIZE (io.LimitReader): a truncated read fails the decode → zero payload.
//   - BOUNDED IN TIME (goroutine + select): claude always writes the payload and closes the
//     pipe, but were stdin ever a non-pipe or a never-closed fd, an unbounded decode would
//     stall the hook — and claude WAITS for its hooks. This makes hookstate.go's "a hook can
//     never block on stdin" property true by CONSTRUCTION rather than by abstention, which is
//     what lets the once-per-turn edges read stdin at all; it also hardens the sub-agent path,
//     which previously did an unbounded Decode. The blocked goroutine is bounded by process
//     lifetime — the hook exits immediately after. The cap is orders of magnitude above a real
//     pipe read, so it can only fire on a broken contract, never on a slow-but-real pipe
//     (which would cost a lost agent_id and regress #290).
//   - TOLERANT OF ABSENCE: an empty, absent, or unparseable payload yields the zero value.
//
// Crucially, every degradation costs ONLY the payload-derived fields. The working/ready latch
// and the #311 heartbeat ride the --event command line, not stdin, so a failed read can never
// disturb #290/#311 — and an empty permission_mode is a no-op in applyHookEvent, not a
// blanked chip.
func parseHookPayload(stdin io.Reader) tmux.HookPayload {
	ch := make(chan tmux.HookPayload, 1) // buffered: a late goroutine never blocks on send
	go func() {
		var p tmux.HookPayload
		_ = json.NewDecoder(io.LimitReader(stdin, hookPayloadLimit)).Decode(&p)
		ch <- p
	}()
	select {
	case p := <-ch:
		return p
	case <-time.After(hookPayloadTimeout):
		return tmux.HookPayload{}
	}
}

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")
	rootCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run a program that loads all sessions"+
		" and runs autoyes mode on them.")
	// Persistent so every subcommand (each defers log.Close) honors it.
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false,
		"Print the log file path on exit")

	// Hide the daemonFlag as it's only for internal use
	err := rootCmd.Flags().MarkHidden("daemon")
	if err != nil {
		panic(err)
	}

	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false,
		"Only check whether a newer release exists; do not install it")
	rootCmd.AddCommand(updateCmd)

	hookEventCmd.Flags().StringVar(&hookEventArg, "event", "", "hook event name (internal)")
	hookEventCmd.Flags().StringVar(&hookStateFileArg, "state-file", "", "session status file path (internal)")
	// SessionStart brief facts (internal, #485).
	hookEventCmd.Flags().StringVar(&hookBrief.Name, "session", "", "atrium session name (internal)")
	hookEventCmd.Flags().StringVar(&hookBrief.Origin, "origin", "", "origin repo path (internal)")
	hookEventCmd.Flags().StringVar(&hookBrief.Branch, "branch", "", "session branch (internal)")
	hookEventCmd.Flags().StringVar(&hookBrief.WorktreesRoot, "worktrees-root", "", "worktrees root (internal)")

	lsCmd.Flags().BoolVar(&lsJSONFlag, "json", false, "Emit machine-readable JSON instead of a table")

	peekCmd.Flags().IntVar(&peekLinesFlag, "lines", 0,
		"Number of lines to print, reaching into scrollback (default: the visible pane)")
	peekCmd.Flags().BoolVar(&peekColorFlag, "color", false, "Keep ANSI color escapes in the output")
	peekCmd.Flags().StringVar(&peekPathFlag, "path", "", "Repo path, to disambiguate a title used in more than one repo")

	sendCmd.Flags().DurationVar(&sendWaitFlag, "wait", 0,
		"Block until a running Atrium has queued the prompt, e.g. --wait 10s")
	sendCmd.Flags().StringVar(&sendPathFlag, "path", "", "Repo path, to disambiguate a title used in more than one repo")

	profilesCmd.AddCommand(profilesDetectCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(peekCmd)
	rootCmd.AddCommand(sendCmd)
	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(profilesCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(hookEventCmd)
}

func main() {
	// Extract the binary name from how this was invoked
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
