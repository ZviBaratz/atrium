package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	"github.com/spf13/cobra"
)

var (
	lsJSONFlag bool

	lsCmd = &cobra.Command{
		Use:   "ls",
		Short: "List sessions (add --json for a machine-readable snapshot)",
		Long: "Lists every stored session without attaching to tmux or starting the TUI.\n\n" +
			"Status and diff figures are last-known values recorded by the running TUI, not\n" +
			"live probes — use the updated_at field to judge how fresh they are. A running\n" +
			"TUI records every status change as it happens, so status is current to within\n" +
			"seconds; diff figures refresh on a slower sweep, and nothing refreshes at all\n" +
			"while no TUI is running.\n\n" +
			"For how long a session has held its status, subtract status_changed_at — not\n" +
			"updated_at, which is one shared instant dating the snapshot, nor created_at,\n" +
			"which is the age of the worktree.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()
			return runLs(cmd.OutOrStdout(), lsJSONFlag)
		},
	}
)

// sessionJSON is the published shape of `atrium ls --json`.
//
// It is a separate type from session.InstanceData on purpose, not a re-marshal
// of it. The stored struct serializes Status as a bare integer (it has no
// MarshalJSON), carries decode-only legacy fields, internal pane geometry, and
// per-session Claude/gh config directories. Publishing it directly would make
// every one of those an accidental part of the contract. Fields here may be
// added, but never removed or repurposed.
type sessionJSON struct {
	Title          string `json:"title"`
	DisplayName    string `json:"display_name"`
	Note           string `json:"note,omitempty"`
	Path           string `json:"path"`
	Worktree       string `json:"worktree"`
	Branch         string `json:"branch"`
	Status         string `json:"status"`
	Program        string `json:"program"`
	TmuxName       string `json:"tmux_name"`
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// Account and Pool are the labels last written to state.json. They track the
	// config: a TUI launch re-derives them from the session's CLAUDE_CONFIG_DIR, so
	// after an account is renamed these report the new name from the first launch
	// onward — not the name in force when the session was created (#470).
	Account       string     `json:"account,omitempty"`
	Pool          string     `json:"pool,omitempty"`
	AutoYes       bool       `json:"auto_yes"`
	Direct        bool       `json:"direct"`
	Unread        bool       `json:"unread"`
	Muted         bool       `json:"muted"`
	QueuedPrompts int        `json:"queued_prompts"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
	// StatusChangedAt is when Status last actually changed — the field to
	// subtract from now() for "how long has it been like this". Distinct from
	// UpdatedAt in the way that matters to a consumer: UpdatedAt is one instant
	// shared by every row of a listing (it dates the snapshot), whereas this one
	// is per-session and dates the session. Null for a session stored by a build
	// predating the field and not yet observed since.
	StatusChangedAt *time.Time `json:"status_changed_at"`
	Diff            diffJSON   `json:"diff"`
}

type diffJSON struct {
	Added        int `json:"added"`
	Removed      int `json:"removed"`
	FilesChanged int `json:"files_changed"`
	Commits      int `json:"commits"`
	Behind       int `json:"behind"`
	// Unpushed is a pointer so "not computed yet" stays distinguishable from
	// "nothing to push": collapsing the two is what made the kill dialog claim
	// pushed work would be discarded (#322).
	Unpushed *int `json:"unpushed"`
	Dirty    bool `json:"dirty"`
}

// runLs writes the session list to w, as JSON when jsonOut is set and as a
// human-readable table otherwise.
func runLs(w io.Writer, jsonOut bool) error {
	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	if jsonOut {
		return writeSessionsJSON(w, instances)
	}
	return writeSessionsTable(w, instances)
}

func writeSessionsJSON(w io.Writer, instances []session.InstanceData) error {
	// Non-nil so an empty fleet marshals to "[]" rather than "null"; a consumer
	// iterating the result should not have to special-case having no sessions.
	out := make([]sessionJSON, 0, len(instances))
	for _, d := range instances {
		out = append(out, toSessionJSON(d))
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("failed to encode sessions: %w", err)
	}
	return nil
}

func toSessionJSON(d session.InstanceData) sessionJSON {
	displayName := d.DisplayName
	if displayName == "" {
		displayName = d.Title // mirrors Instance.DisplayName's fallback
	}
	return sessionJSON{
		Title:           d.Title,
		DisplayName:     displayName,
		Note:            d.Note,
		Path:            d.Path,
		Worktree:        d.Worktree.WorktreePath,
		Branch:          d.Branch,
		Status:          d.Status.String(),
		Program:         d.Program,
		TmuxName:        d.TmuxName,
		Model:           d.Model,
		PermissionMode:  d.PermissionMode,
		Effort:          d.Effort,
		Account:         d.ClaudeAccount,
		Pool:            d.ClaudeAccountPool,
		AutoYes:         d.AutoYes,
		Direct:          d.Direct,
		Unread:          d.Unread,
		Muted:           d.Muted,
		QueuedPrompts:   len(d.PromptQueue),
		CreatedAt:       nilIfZero(d.CreatedAt),
		UpdatedAt:       nilIfZero(d.UpdatedAt),
		StatusChangedAt: nilIfZero(d.StatusChangedAt),
		Diff: diffJSON{
			Added:        d.DiffStats.Added,
			Removed:      d.DiffStats.Removed,
			FilesChanged: d.DiffStats.FilesChanged,
			Commits:      d.DiffStats.Commits,
			Behind:       d.DiffStats.Behind,
			Unpushed:     d.DiffStats.Unpushed,
			Dirty:        d.DiffStats.Dirty,
		},
	}
}

// nilIfZero maps an unset timestamp to JSON null. A zero time.Time would
// otherwise be published as "0001-01-01T00:00:00Z", which reads like real data.
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func writeSessionsTable(w io.Writer, instances []session.InstanceData) error {
	if len(instances) == 0 {
		_, err := fmt.Fprintf(w, "No sessions. Run %s to create one.\n", binName)
		return err
	}

	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	// Writes go into the tabwriter's buffer; the underlying write error, if any,
	// surfaces from Flush below, which is the one return value worth checking.
	_, _ = fmt.Fprintln(tw, "TITLE\tREPO\tSTATUS\tBRANCH\tDIFF\tQUEUE\tUPDATED")
	for _, d := range instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			orDash(d.Title),
			orDash(filepath.Base(d.Path)),
			d.Status.String(),
			orDash(d.Branch),
			diffCell(d.DiffStats),
			queueCell(len(d.PromptQueue)),
			shortAgo(d.UpdatedAt, now),
		)
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func diffCell(s session.DiffStatsData) string {
	if s.Added == 0 && s.Removed == 0 {
		return "-"
	}
	return fmt.Sprintf("+%d/-%d", s.Added, s.Removed)
}

func queueCell(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

// shortAgo renders how long ago at was, in the largest unit that keeps the cell
// narrow. An unset timestamp renders "-", and a timestamp in the future (a clock
// skew, or state written by another machine) clamps to "0s" rather than showing
// a negative age.
func shortAgo(at, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	d := now.Sub(at)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
