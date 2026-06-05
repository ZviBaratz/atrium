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
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	// version is overridden at build time via -ldflags "-X main.version=...".
	// GoReleaser injects the tag (e.g. 0.1.0); the justfile injects git describe.
	// Unstamped builds (plain `go build`) report "dev".
	version     = "dev"
	programFlag string
	autoYesFlag bool
	daemonFlag  bool
	binName     string
	rootCmd     = &cobra.Command{
		Use:   "atrium",
		Short: "Atrium - A command center for orchestrating multiple AI coding agents like Claude Code, Aider, Codex, and Amp.",
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
				err := daemon.RunDaemon(ctx, cfg)
				log.ErrorLog.Printf("failed to start daemon %v", err)
				return err
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
			if autoYes {
				defer func() {
					if err := daemon.LaunchDaemon(ctx); err != nil {
						log.ErrorLog.Printf("failed to launch daemon: %v", err)
					}
				}()
			}
			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
			}

			return app.Run(ctx, program, autoYes)
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
				return err
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
			// corresponding release page.
			if version != "dev" && !strings.Contains(version, "-") {
				fmt.Printf("https://github.com/ZviBaratz/atrium/releases/tag/v%s\n", version)
			}
		},
	}
)

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")
	rootCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run a program that loads all sessions"+
		" and runs autoyes mode on them.")

	// Hide the daemonFlag as it's only for internal use
	err := rootCmd.Flags().MarkHidden("daemon")
	if err != nil {
		panic(err)
	}

	profilesCmd.AddCommand(profilesDetectCmd)
	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(profilesCmd)
}

func main() {
	// Extract the binary name from how this was invoked
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
	}
}
