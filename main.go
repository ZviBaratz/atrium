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
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"
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
	binName         string
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
			log.Initialize(daemonFlag)
			defer log.Close()

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
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJSON, _ := json.MarshalIndent(cfg, "", "  ")

			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJSON)

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
			log.Initialize(false)
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
			log.Initialize(false)
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
	hookEventCmd     = &cobra.Command{
		Use:    tmux.HookSubcommand,
		Short:  "Internal: record a Claude Code hook event into a session's status file",
		Hidden: true,
		// Invoked by the injected Claude Code settings.json hooks (see
		// session/tmux/hooks.go), once per hook event, to maintain the structured
		// status record — the working/ready latch plus the set of in-flight sub-agent
		// ids that distinguishes a finished turn from one still waiting on a background
		// sub-agent (#290). It runs the locked read-modify-write that shell can't do
		// portably, then exits. Best-effort by contract: a hook must never fail or stall
		// the agent, so this always exits 0 and reads stdin only for the sub-agent events
		// that carry an agent_id.
		RunE: func(cmd *cobra.Command, args []string) error {
			runHookEvent(hookStateFileArg, hookEventArg, os.Stdin)
			return nil
		},
	}

	doctorCmd = &cobra.Command{
		Use:   "doctor",
		Short: "Check Atrium's core dependencies (tmux, git, gh) and agent CLI heuristic versions",
		Long: "Reports two sections. Core dependencies probes tmux, git, and gh: tmux and git are\n" +
			"required (a missing one exits nonzero so scripts/CI can gate); gh is optional, needed\n" +
			"only for push/PR flows, and its authentication is reported but never fatal. Agent\n" +
			"heuristics probes installed agent CLIs (claude, codex, gemini, aider) and reports whether\n" +
			"each one's version has drifted past the version Atrium's pane-classification heuristics\n" +
			"were verified against; drift means a session's status may be misread (re-verify the\n" +
			"matcher strings in session/agent/registry.go).",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
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
			if doctor.MissingRequired(deps) {
				// Nonzero exit for CI/scripts. The root command already sets
				// SilenceErrors/SilenceUsage, so main() prints just this message to
				// stderr (no "Error:"/usage noise over the report rendered above).
				return fmt.Errorf("missing required dependency (see the hints above; run `atrium doctor` after installing)")
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
	var agentID string
	if tmux.HookEventReadsAgentID(event) {
		agentID = parseSubagentID(stdin)
	}
	// Claude exports the turn's resolved effort level to every hook subprocess. Reading the
	// env var rather than the stdin payload is what lets the high-frequency working/ready
	// latches carry effort at all: HookEventReadsAgentID keeps them deliberately payload-free
	// so their subprocess can never block on stdin. Empty for a model without effort support
	// (and stale on UserPromptSubmit) — UpdateHookState's write rule sorts that out.
	if err := tmux.UpdateHookState(stateFile, event, agentID, os.Getenv("CLAUDE_EFFORT")); err != nil {
		fmt.Fprintf(os.Stderr, "atrium hook: %v\n", err)
	}
}

// parseSubagentID pulls the agent_id out of a SubagentStart/Stop hook's stdin payload.
// Best-effort: an absent, empty, or unparseable payload yields "", which applyHookEvent
// treats as "can't track this one" (skipped) rather than corrupting the in-flight set.
func parseSubagentID(stdin io.Reader) string {
	var payload struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(stdin).Decode(&payload); err != nil {
		return ""
	}
	return payload.AgentID
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

	profilesCmd.AddCommand(profilesDetectCmd)
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
