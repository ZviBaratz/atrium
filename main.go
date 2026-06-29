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
	"github.com/ZviBaratz/atrium/app"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/daemon"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/signal"
	"path/filepath"
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
	rootCmd         = &cobra.Command{
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
			// Root lifecycle context: cancelled on SIGINT/SIGTERM so in-flight
			// git/gh/tmux subprocesses are killed rather than orphaned on shutdown.
			// (Inside the TUI, Ctrl+C is a key event handled by Bubble Tea, not a
			// signal — this covers SIGTERM and the daemon's signal-driven exit.)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Reset all stored instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			// One-shot CLI command; a plain Background context is enough (the
			// per-operation timeouts still bound every subprocess).
			ctx := context.Background()
			log.Initialize(false)
			defer log.Close()

			state := config.LoadState()
			storage, err := session.NewStorage(state)
			if err != nil {
				return fmt.Errorf("failed to initialize storage: %w", err)
			}

			// Capture the repo paths before deleting instances so CleanupWorktrees
			// can run its git commands in the correct repositories regardless of the
			// current working directory.
			instances, err := storage.LoadInstances(ctx)
			if err != nil {
				return fmt.Errorf("failed to load instances: %w", err)
			}
			repoPaths := make([]string, 0, len(instances))
			for _, inst := range instances {
				repoPaths = append(repoPaths, inst.GetRepoPath())
			}

			if err := storage.DeleteAllInstances(); err != nil {
				return fmt.Errorf("failed to reset storage: %w", err)
			}
			fmt.Println("Storage has been reset successfully")

			if err := tmux.CleanupSessions(ctx, cmd2.MakeExecutor()); err != nil {
				return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
			}
			fmt.Println("Tmux sessions have been cleaned up")

			if err := git.CleanupWorktrees(ctx, repoPaths); err != nil {
				return fmt.Errorf("failed to cleanup worktrees: %w", err)
			}
			fmt.Println("Worktrees have been cleaned up")

			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				// Log (to the file) before returning, matching the root command's
				// handling, so the failure is captured and not just surfaced to stderr.
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
				return fmt.Errorf("failed to stop daemon: %w", err)
			}
			fmt.Println("daemon has been stopped")

			return nil
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
			// Same signal-driven lifecycle as the root command: Ctrl+C aborts a
			// download cleanly instead of leaving the HTTP transfer orphaned.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

	doctorCmd = &cobra.Command{
		Use:   "doctor",
		Short: "Check installed agent CLIs against Atrium's verified heuristic versions",
		Long: "Probes installed agent CLIs (claude, codex, gemini, aider) and reports whether each\n" +
			"one's version has drifted past the version Atrium's pane-classification heuristics were\n" +
			"verified against. Drift means a session's status (busy / needs-input / idle) may be\n" +
			"misread; re-verify the matcher strings in session/agent/registry.go.",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			ctx, cancel := context.WithTimeout(context.Background(), doctor.ProbeTimeout)
			defer cancel()
			fmt.Print(doctor.Render(doctor.CheckInstalled(ctx)))
			return nil
		},
	}
)

// shouldLaunchDaemonOnExit reports whether the autoyes daemon should take over
// when the TUI exits. It re-reads the persisted config rather than reusing the
// value merged at startup, so an auto_yes toggle made in the settings panel
// during the run takes effect; the -y flag still wins for the run it was given.
func shouldLaunchDaemonOnExit(autoYesFlag bool) bool {
	return autoYesFlag || config.LoadConfig().AutoYes
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

	profilesCmd.AddCommand(profilesDetectCmd)
	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(profilesCmd)
	rootCmd.AddCommand(doctorCmd)
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
