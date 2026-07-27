package overlay

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notesFixture builds 30 rule-ful Claude accounts — rule-ful so there is no
// catch-all, which keeps the pre-existing "unmatched repos" hint on screen, since
// that one is charged unconditionally inside the 12 and must not be confused with
// the two conditional notes under test. split adds a non-contiguous pool; collide
// points two accounts at one login.
func notesFixture(t *testing.T, split, collide bool) *AccountsOverlay {
	t.Helper()
	var accts []config.ClaudeAccount
	logins := map[string]string{}
	for i := 0; i < 30; i++ {
		dir := fmt.Sprintf("/h/d%02d", i)
		accts = append(accts, config.ClaudeAccount{
			Name: fmt.Sprintf("acct%02d", i), ConfigDir: dir, RemoteMatches: []string{dir},
		})
		logins[dir] = dir + "@corp.com"
	}
	if split {
		accts[0].Pool, accts[2].Pool = "work", "work" // non-adjacent → splitPools flags it
	}
	if collide {
		logins["/h/d01"] = logins["/h/d00"]
	}
	return identityOverlay(t, accts, logins)
}

// countNoteLines is how many lines renderList actually spends on the notes.
func countNoteLines(out string, notes listNotes) int {
	printed := 0
	for _, line := range strings.Split(out, "\n") {
		if notes.splitPool != "" && strings.Contains(line, notes.splitPool) {
			printed++
		}
		if notes.identity != "" && strings.Contains(line, notes.identity) {
			printed++
		}
	}
	return printed
}

// TestAccountsOverlay_RowWindowChargesEveryNoteItPrints is #479's point stated as a
// property rather than a refactor: the notes are computed once and both the budget
// and the printing read that one value, so they cannot disagree. The pairing matters
// more than the arithmetic — a note printed but uncounted makes the box one line
// taller than the terminal it was measured against (9b25662, #499).
//
// The both-notes case is the one nothing in the repo could see before: the two
// pre-existing chrome tests each use exactly ONE note (budget 12 vs 11), so a
// lines() that saturated at 1 passed them both.
func TestAccountsOverlay_RowWindowChargesEveryNoteItPrints(t *testing.T) {
	for _, tc := range []struct {
		name         string
		split, coll  bool
		wantNotes    int
		wantRowsAt24 int
	}{
		{"no notes", false, false, 0, 12},
		{"split pool only", true, false, 1, 11},
		{"identity only", false, true, 1, 11},
		{"both", true, true, 2, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := notesFixture(t, tc.split, tc.coll)
			o.SetSize(80, 24)

			notes := o.listNotes()
			require.Equal(t, tc.wantNotes, notes.lines(), "notes this config can render")

			start, end := o.rowWindow(o.activeLen(), notes)
			assert.Equal(t, tc.wantRowsAt24, end-start, "each rendered note costs exactly one row of the budget")
			assert.Equal(t, tc.wantNotes, countNoteLines(o.renderList(), notes),
				"renderList prints exactly the notes the budget was charged for")
			// Counted on the dir column, not the name: the identity note names the
			// colliding accounts, so counting "acct" would count the note too.
			assert.Equal(t, tc.wantRowsAt24, strings.Count(o.renderList(), "/h/d"),
				"and prints exactly the rows the window returned")
		})
	}
}

// The notes belong to the Claude tab — both features are Claude-only — and the gate
// now lives in ONE place instead of the three it used to. Nothing in the repo
// asserted rowWindow's budget off that tab, so a lost gate would have shown up only
// as a GH list mysteriously one row shorter.
func TestAccountsOverlay_NotesAreClaudeTabOnly(t *testing.T) {
	o := notesFixture(t, true, true)
	for i := 0; i < 30; i++ {
		o.cfg.GHAccounts = append(o.cfg.GHAccounts, config.GHAccount{
			Name: fmt.Sprintf("gh%02d", i), ConfigDir: "~/.config/gh", RemoteMatches: []string{"acme/"},
		})
	}
	o.SetSize(80, 24)
	require.Equal(t, 2, o.listNotes().lines(), "the Claude tab has both notes to show")

	o.selectTab(tabGH)
	notes := o.listNotes()
	assert.Equal(t, 0, notes.lines(), "neither note belongs to the GitHub tab")
	start, end := o.rowWindow(o.activeLen(), notes)
	assert.Equal(t, 12, end-start, "so the GitHub list keeps the full row budget")

	out := o.renderList()
	assert.NotContains(t, out, "is split", "and prints neither note")
	assert.NotContains(t, out, "same login")
}

// The third tab, and the empty case: a config whose Claude tab has both notes must
// still report none from a tab with no accounts at all. rowWindow is never reached
// on an empty tab (renderList takes the other branch), so what this actually pins is
// that the single remaining tab gate covers Antigravity as well as GitHub.
func TestAccountsOverlay_EmptyTabIsChargedNoNotes(t *testing.T) {
	o := notesFixture(t, true, true)
	o.SetSize(80, 24)
	o.selectTab(tabAgy) // no Antigravity accounts in this fixture

	assert.Equal(t, 0, o.listNotes().lines(), "an empty tab has no notes to charge")
	out := o.renderList()
	assert.Contains(t, out, "No Antigravity accounts")
	assert.NotContains(t, out, "is split")
}
