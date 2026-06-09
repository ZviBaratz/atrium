package app

// Async branch search and target-path validity checks for new sessions.

import (
	"context"
	"os"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/git"

	tea "github.com/charmbracelet/bubbletea"
)

// branchSearchDebounceMsg fires after the debounce interval to trigger a search.
type branchSearchDebounceMsg struct {
	filter  string
	version uint64
}

// branchSearchResultMsg carries search results back to Update. err marks a failed
// search (e.g. the target is not a git repo) so the picker can clear its loading state
// and show an error hint instead of spinning forever.
type branchSearchResultMsg struct {
	branches []string
	version  uint64
	err      bool
}

const branchSearchDebounce = 150 * time.Millisecond

// scheduleBranchSearch returns a debounced tea.Cmd: sleeps, then triggers a search message.
func (m *home) scheduleBranchSearch(filter string, version uint64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return branchSearchDebounceMsg{filter: filter, version: version}
	}
}

// branchFetchDoneMsg signals that a background `git fetch` for a candidate target repo
// finished (successfully or not), keyed by the path it fetched so a completion for a
// path the user has navigated away from can be dropped.
type branchFetchDoneMsg struct {
	path string
}

// runBranchFetch returns a tea.Cmd that fetches the repo's remote refs in the background
// and reports completion. FetchBranches is best-effort (errors are ignored — offline or
// remoteless repos simply keep their local view), so completion always re-triggers a
// search via the branchFetchDoneMsg handler.
func (m *home) runBranchFetch(path string) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		git.FetchBranches(ctx, path)
		return branchFetchDoneMsg{path: path}
	}
}

// targetValidityDebounceMsg fires after the debounce interval to trigger an async
// state check (targetValidity) of the chosen target path.
type targetValidityDebounceMsg struct {
	path string
}

// targetValidityResultMsg carries the target-state check result back to Update, keyed by
// the path it was computed for so a stale result (the user has since moved on) is dropped.
// headBranch is the resolved name of the branch HEAD points at (only for git targets),
// shown in the branch picker's default base option.
type targetValidityResultMsg struct {
	path          string
	valid, direct bool
	headBranch    string
	// accountName is the Claude account auto-routed for this target (from its origin
	// remote), used to re-point the form's account picker as the project changes.
	// Empty when the feature is dormant. Resolved here, off the keystroke hot path.
	accountName string
}

// scheduleValidityCheck returns a debounced tea.Cmd mirroring scheduleBranchSearch: it
// sleeps, then asks for an async target-state check. Debouncing keeps targetValidity's
// git subprocess off the keystroke hot path while the user types/browses a path.
func (m *home) scheduleValidityCheck(path string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return targetValidityDebounceMsg{path: path}
	}
}

// runValidityCheck returns a tea.Cmd that runs targetValidity in the background and
// reports the result tagged with the path it was computed for.
func (m *home) runValidityCheck(path string) tea.Cmd {
	ctx := m.ctx
	cfg := m.appConfig
	return func() tea.Msg {
		valid, direct, head := targetValidity(ctx, path)
		// Resolve the auto-routed account here too (a git subprocess), so the form's
		// account picker can follow the selected project without re-doing git on the
		// update loop. A direct (non-git) target has no remote -> the inferred default.
		var account string
		if valid {
			remoteURL := ""
			if !direct {
				remoteURL = git.GetRemoteURL(ctx, path)
			}
			account, _, _ = cfg.ResolveClaudeAccount(remoteURL)
		}
		return targetValidityResultMsg{path: path, valid: valid, direct: direct, headBranch: head, accountName: account}
	}
}

// runBranchSearch returns a tea.Cmd that performs the git search in the background.
// It searches the current new-session target repo (m.newSessionPath), captured at call
// time so it reflects the directory chosen in the picker rather than the process cwd.
func (m *home) runBranchSearch(filter string, version uint64) tea.Cmd {
	target := m.newSessionPath
	ctx := m.ctx
	return func() tea.Msg {
		if target == "" {
			var err error
			if target, err = os.Getwd(); err != nil {
				return nil
			}
		}
		branches, err := git.SearchBranches(ctx, target, filter)
		if err != nil {
			log.WarningLog.Printf("branch search failed: %v", err)
			return branchSearchResultMsg{version: version, err: true}
		}
		return branchSearchResultMsg{branches: branches, version: version}
	}
}

// targetValidity reports whether path is a usable new-session target and, if so,
// whether it would be a direct (non-git) session. For a git target it also resolves
// headBranch — the branch HEAD points at — for the branch picker's default base label.
// Both the inline (`n`) and form (`N`) flows use it to drive the picker's inline hint
// and to set the Direct flag.
func targetValidity(ctx context.Context, path string) (valid, direct bool, headBranch string) {
	if !config.DirExists(path) {
		return false, false, ""
	}
	if !git.IsGitRepo(ctx, path) {
		return true, true, ""
	}
	return true, false, git.CurrentBranchName(ctx, path)
}
