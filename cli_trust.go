package main

// cli_trust.go — `atrium trust`: the CLI face of the per-repo trust ledger
// (#814, internal/repotrust). `allow` is the headless grant — the TUI's
// create-time prompt cannot reach a spooled `atrium new` or a script — and
// `revoke` is the revocation verb the issue requires; `status` answers "what
// have I trusted, and does it still match" without opening the TUI.
//
// None of these take the TUI lock (TestHeadlessCommandsRunWhileTheTUIHoldsItsLock):
// the ledger is written atomically and read fresh at every enforcement point,
// so a grant or revocation made beside a running TUI takes effect on its next
// resolution sweep — the same contract config.json edits already have.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
)

var (
	trustCmd = &cobra.Command{
		Use:   "trust",
		Short: "Manage which repos may run their own .atrium.json setup",
	}

	trustStatusCmd = &cobra.Command{
		Use:   "status [path]",
		Short: "Show trusted repos, or one repo's trust state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			return runTrustStatus(cmd.Context(), cmd.OutOrStdout(), firstOrCwd(args),
				config.LoadConfig().GetUpdateBaseOnCreate())
		},
	}

	trustAllowCmd = &cobra.Command{
		Use:   "allow [path]",
		Short: "Trust a repo's committed .atrium.json, exactly as it is now",
		Long: "Records that the repo containing path (default: the working directory) may run the\n" +
			"repo_scripts its committed .atrium.json declares — read at the ref a new session\n" +
			"would start from (with update_base_on_create, origin's tip when that is ahead),\n" +
			"which is what a session worktree checks out; uncommitted edits are not what runs\n" +
			"and cannot be trusted. The grant is for the file's exact content: any later change\n" +
			"makes it inert again until re-allowed. The equivalent of answering the TUI's\n" +
			"create-time prompt, for headless use.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			return runTrustAllow(cmd.Context(), cmd.OutOrStdout(), firstOrCwd(args),
				config.LoadConfig().GetUpdateBaseOnCreate())
		},
	}

	trustRevokeAll bool

	trustRevokeCmd = &cobra.Command{
		Use:   "revoke [path]",
		Short: "Withdraw a repo's trust (or every repo's, with --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()
			if trustRevokeAll {
				if len(args) > 0 {
					return errors.New("--all takes no path")
				}
				return runTrustRevokeAll(cmd.OutOrStdout())
			}
			return runTrustRevoke(cmd.Context(), cmd.OutOrStdout(), firstOrCwd(args))
		},
	}
)

func init() {
	trustRevokeCmd.Flags().BoolVar(&trustRevokeAll, "all", false, "revoke every recorded grant")
	trustCmd.AddCommand(trustStatusCmd, trustAllowCmd, trustRevokeCmd)
}

// firstOrCwd is the path argument, defaulting to the working directory — the
// direnv convention: `atrium trust allow` in a repo means this repo.
func firstOrCwd(args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return args[0]
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func runTrustAllow(ctx context.Context, w io.Writer, path string, updateBase bool) error {
	a, err := repotrust.AssessCreateDefault(ctx, path, updateBase)
	if err != nil {
		return err
	}
	if a.FileErr != nil {
		return fmt.Errorf("cannot trust %s: %w", a.Root, a.FileErr)
	}
	if !a.Present {
		return fmt.Errorf("%s has no tracked %s at %s (the ref a new session starts from) — only committed config reaches a session's worktree, so there is nothing to trust (commit the file first)",
			a.Root, repocfg.RepoLocalFileName, a.Ref)
	}
	if len(repocfg.RepoLocalSurfaces(a.Local)) == 0 {
		msg := fmt.Sprintf("%s's %s declares nothing usable, so there is nothing to trust", a.Root, repocfg.RepoLocalFileName)
		for _, p := range a.Local.Problems {
			msg += "\n  " + p.Error()
		}
		return errors.New(msg)
	}
	if a.Granted {
		trustf(w, "%s is already trusted for its current %s (%s)\n", a.Root, repocfg.RepoLocalFileName, shortHash(a.Hash))
		return nil
	}
	replacing := a.HasGrant
	if err := repotrust.Grant(a.Key, a.Hash, a.Remote, time.Now()); err != nil {
		return err
	}
	trustf(w, "trusted %s (%s)\n", a.Root, shortHash(a.Hash))
	trustf(w, "  its %s may now apply: %s\n", repocfg.RepoLocalFileName, declaresSummary(a))
	if replacing {
		trustf(w, "  replaces the grant from %s (%s)\n", a.Record.GrantedAt.Format("2006-01-02"), shortHash(a.Record.Hash))
	}
	trustf(w, "  any change to the file makes it inert again until re-allowed\n")
	return nil
}

func runTrustRevoke(ctx context.Context, w io.Writer, path string) error {
	key := trustKeyFor(ctx, path)
	removed, err := repotrust.Revoke(key)
	if err != nil {
		return err
	}
	if !removed {
		trustf(w, "%s was not trusted; nothing to revoke (see `atrium trust status`)\n", key)
		return nil
	}
	trustf(w, "revoked %s — its %s is inert until allowed again\n", key, repocfg.RepoLocalFileName)
	return nil
}

func runTrustRevokeAll(w io.Writer) error {
	n, err := repotrust.RevokeAll()
	if err != nil {
		return err
	}
	switch n {
	case 0:
		trustf(w, "no repos were trusted; nothing to revoke\n")
	case 1:
		trustf(w, "revoked the 1 trusted repo\n")
	default:
		trustf(w, "revoked all %d trusted repos\n", n)
	}
	return nil
}

// trustKeyFor resolves what the ledger would key path's repo under. Through git
// when it can answer (so a subdirectory revokes its repo), and falling back to
// the canonicalized path itself when it cannot — a revocation must work for a
// repo that was deleted or moved, where no git is left to ask; the recorded key
// was the repo ROOT, so the fallback matches when the user names that root.
func trustKeyFor(ctx context.Context, path string) string {
	if root, err := git.RepoRoot(ctx, path); err == nil {
		path = root
	}
	key, err := repotrust.CanonicalRoot(path)
	if err != nil {
		return path
	}
	return key
}

func runTrustStatus(ctx context.Context, w io.Writer, path string, updateBase bool) error {
	ledger, ledgerErr := repotrust.Load()
	if ledgerErr != nil {
		// Stop here: Load returns an EMPTY ledger on corrupt/future-version, so
		// falling through would add "no repos are trusted; grant one…" — a
		// fresh-install invitation to re-grant, printed exactly when real grants
		// exist but cannot be read.
		trustf(w, "warning: %v — every repo reads as untrusted until this is fixed\n", ledgerErr)
		return nil
	}
	if len(ledger.Repos) == 0 {
		trustf(w, "no repos are trusted; grant one with `atrium trust allow <path>`\n")
		return nil
	}

	keys := make([]string, 0, len(ledger.Repos))
	for k := range ledger.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	trustf(tw, "REPO\tSTATE\tCOVERS\tGRANTED\tHASH\tREMOTE\n")
	for _, key := range keys {
		rec := ledger.Repos[key]
		remote := rec.Remote
		if remote == "" {
			remote = "-"
		}
		// COVERS is what a session created now would take from the file, and it is
		// deliberately empty for any state but "current" — see LiveState. A dash
		// keeps the column readable rather than collapsing two blanks into one.
		state, covers := repotrust.LiveState(ctx, key, rec, updateBase)
		if covers == "" {
			covers = "-"
		}
		trustf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			key, state, covers, rec.GrantedAt.Format("2006-01-02"), shortHash(rec.Hash), remote)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if _, has := ledger.Repos[trustKeyFor(ctx, path)]; !has {
		// The repo the command ran in (or was pointed at) is not in the table the
		// user is looking at; one line saves them diffing paths by eye.
		trustf(w, "\n%s itself is not trusted\n", path)
	}
	return nil
}

// declaresSummary names everything the file would put into a session, off the
// same repocfg.RepoLocalSurfaces list the create-time dialog, `trust status`'s
// COVERS column and doctor all render — the entry half is the entry that runs
// (the one-entry rule), and the seed-list half is counted, so the grant receipt
// cannot describe a different script than enforcement executes or omit the paths
// the same grant seeds.
func declaresSummary(a repotrust.Assessment) string {
	return strings.Join(repocfg.RepoLocalSurfaces(a.Local), " + ")
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// trustf writes one report line, dropping write errors for reapf's stated
// reason: the destination is a terminal or a test buffer, and no verb here
// should abandon a half-done ledger change because stdout went away. (The
// status table's tabwriter rows also ride this; its Flush error IS checked,
// which is where a real write failure surfaces.)
func trustf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
