package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// minTmuxRe pulls the assignment out of the script. It is anchored to the start of a
// line so the sentence about 3.2 in the comment above it cannot match.
var minTmuxRe = regexp.MustCompile(`(?m)^MIN_TMUX=(\S+)$`)

// TestDriveAgentTmuxFloorMatchesAtrium keeps scripts/drive-agent.sh's tmux floor equal
// to session/tmux.MinVersion.
//
// A shell script cannot read a Go const, so that floor is necessarily a second copy —
// the same shape as the drift sites .claude/skills/tui-drift-sites/SKILL.md enumerates,
// and with the same failure mode: when MinVersion moves, nothing makes the copy move
// with it, and the script goes on admitting a tmux Atrium itself refuses. It is worse
// than a stale doc line, because the script would then start a real server running a
// real agent CLI before anything noticed.
func TestDriveAgentTmuxFloorMatchesAtrium(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	m := minTmuxRe.FindSubmatch(src)
	require.NotNil(t, m, "no MIN_TMUX= assignment in %s — this guard is matching nothing", path)
	require.Equal(t, tmux.MinVersion, string(m[1]),
		"scripts/drive-agent.sh's MIN_TMUX disagrees with session/tmux.MinVersion; update the script")
}

// resumeTableRe pulls the RESUME_TABLE array out of the script, and rows are then read
// only from inside it. Matching rows against the whole file would leave the guard
// anchored to a SHAPE rather than to the array: deleting or renaming RESUME_TABLE while
// leaving four tab-indented `"a|b|c"` lines anywhere in the script would still yield
// rows, still satisfy the both-directions checks, and still pass — which is the mutation
// this guard most needs to fail on, since the table is what the verb reads.
var resumeTableRe = regexp.MustCompile(`(?s)\nRESUME_TABLE=\(\n(.*?)\n\)\n`)

// resumeRowRe pulls one RESUME_TABLE row out of that block:
// `"<agent>|<command>|<verdict>"`, one per line, tab-indented inside the array. It is
// anchored so a row that loses its quoting fails as a missing row rather than matching
// something adjacent.
var resumeRowRe = regexp.MustCompile(`(?m)^\t"([^"|]+)\|([^"|]+)\|([^"|]+)"$`)

// resumeLauncherRe is what holds cmd_resume to the COLUMN it launches. The verb re-parses
// each row itself, so the drift the table guard cannot see is in that parse: swap the read
// order, or write `program="$want"`, and the script drives the literal `alive` as a command
// while every assertion below stays green — under a docstring promising that the table is
// what `resume` launches.
//
// Written as one expression over the three lines rather than three lookups, so a reordered
// read cannot be satisfied by fragments matching in the wrong places.
var resumeLauncherRe = regexp.MustCompile(`(?s)IFS='\|' read -r name cmd want <<<"\$row".*?program="\$cmd".*?expect="\$want"`)

// TestDriveAgentResumeTableMatchesTheRegistry holds scripts/drive-agent.sh's
// RESUME_TABLE to session/agent's adapters, in both directions.
//
// The table is what `drive-agent.sh resume <agent>` launches, so it is a second copy of
// each adapter's Resume rewrite — MIN_TMUX's problem again, with a worse failure: a
// stale command still starts a real CLI and still prints a verdict, so the run reports
// on a launch Atrium would never make and reads exactly like a clean one.
//
// Both directions are asserted because they fail differently. A wrong command drives
// the wrong thing; a MISSING row is an agent whose resume survivability nobody has ever
// driven, which is the gap #712 is about — and the one that appears by itself, the next
// time an adapter grows a Resume.
//
// The verdict column is deliberately unchecked here: no Go test can know whether a
// vendor's CLI survives its own resume flag. Driving it is the only thing that can, and
// that is the point of the verb.
//
// Two mutations this would otherwise miss are pinned first, because both keep every
// assertion below green: rows read from anywhere in the file rather than from the array
// (resumeTableRe), and a launcher that reads the columns in a different order or launches
// a different one (resumeLauncherRe).
func TestDriveAgentResumeTableMatchesTheRegistry(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	require.Regexp(t, resumeLauncherRe, string(src),
		"cmd_resume no longer reads RESUME_TABLE's columns as <agent>|<command>|<verdict> and launches the command; "+
			"the table below would then be held to a script that drives something else")

	rows := resumeTableRows(t, string(src), path)

	driven := map[string]bool{}
	for _, row := range rows {
		name, command, verdict := row[1], row[2], row[3]
		driven[name] = true
		adapter := agent.Resolve(name)
		require.Equal(t, agent.Key(name), adapter.Key,
			"RESUME_TABLE names %q, which resolves to the %q adapter", name, adapter.Key)
		require.NotNil(t, adapter.Resume,
			"RESUME_TABLE has a row for %q, whose adapter has no Resume to drive", name)
		require.Equal(t, adapter.Resume(name), command,
			"RESUME_TABLE's command for %q is not what Atrium relaunches with; re-drive and update the script", name)
		require.Contains(t, []string{"alive", "dead"}, verdict,
			"RESUME_TABLE's verdict for %q must be alive or dead", name)
	}

	for _, adapter := range agent.Adapters() {
		if adapter.Resume == nil {
			continue
		}
		require.True(t, driven[string(adapter.Key)],
			"the %q adapter can resume a conversation and RESUME_TABLE has no row for it, so nothing has ever driven whether its resume flag survives with nothing to resume (#712)",
			adapter.Key)
	}
}

// resumeTableRows returns RESUME_TABLE's rows as {agent, command, verdict}, read from inside
// the array rather than from the file (resumeTableRe's reason).
func resumeTableRows(t *testing.T, src, path string) [][]string {
	t.Helper()
	table := resumeTableRe.FindStringSubmatch(src)
	require.NotNil(t, table, "no RESUME_TABLE=( … ) array in %s — this guard is matching nothing", path)
	rows := resumeRowRe.FindAllStringSubmatch(table[1], -1)
	require.NotEmpty(t, rows, "RESUME_TABLE has no rows this guard recognizes in %s", path)
	return rows
}

// trustWriteDisclosureRe finds each place the script discloses what answering the folder-trust
// gate writes, anchored on gemini's file because that name is the one constant across both
// wordings — the header's and `help`'s.
//
// Anchored on a name rather than matched whole: the two disclosures are prose and are meant to
// be rewritten, so pinning either sentence would make this guard a copy of the text instead of
// a check on it.
var trustWriteDisclosureRe = regexp.MustCompile(`trustedFolders\.json`)

// disclosureSentence returns the one SENTENCE containing lines[at] — the enumeration itself,
// lifted out of the bullet it sits in, with the leading `#` of a comment block ignored so the
// file header and `help` read the same way.
//
// The sentence, because nothing wider works. A line window wide enough to hold a twelve-line
// bullet reaches into its neighbours; the bullet itself is no better, since the same bullet
// naturally mentions an agent again for an unrelated reason. Both were tried, and both passed a
// disclosure whose ENUMERATION had claude removed while some later clause still said the word.
// The enumeration is the thing that reads as exhaustive, so the enumeration is what has to be
// complete.
func disclosureSentence(lines []string, at int) string {
	strip := func(s string) string {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	}
	isBullet := func(i int) bool { return strings.HasPrefix(strip(lines[i]), "- ") }

	lo := at
	for lo > 0 && !isBullet(lo) && strip(lines[lo-1]) != "" {
		lo--
	}
	hi := at + 1
	for hi < len(lines) && strip(lines[hi]) != "" && !isBullet(hi) {
		hi++
	}
	var flat []string
	for _, line := range lines[lo:hi] {
		flat = append(flat, strip(line))
	}
	// ". " and not "." — the enumeration is full of "config.toml," and "~/.claude.json",
	// and splitting on the bare dot would cut it into fragments none of which name
	// everything.
	for _, sentence := range strings.Split(strings.Join(flat, " "), ". ") {
		if strings.Contains(sentence, "trustedFolders.json") {
			return sentence
		}
	}
	return ""
}

// TestDriveAgentDisclosesTheTrustWriteForEveryAgentItDrives holds both disclosures to the set
// of agents `resume` actually drives.
//
// The failure this exists for is not a missing sentence but a COMPLETE-LOOKING one. Both
// disclosures enumerate per-agent config files, which is what makes them read as exhaustive, so
// an agent left out reads as "that one writes nothing" rather than as an omission. #758 shipped
// exactly that: three of the four rows named, and the missing one was claude — whose verdict
// REQUIRES the Enter, because its adapter records the death landing only once the folder is
// trusted. A developer weighing whether to drive without ATR_CAP_ENV would have read the list,
// found claude absent, and written a trust record for a nonce path into their real config.
//
// guard_enter cannot cover this. It greps the flattened pane for "persist", and no trust-gate
// fixture in the tree contains that word — the trust write is invisible to it by construction,
// which is why the disclosure is the whole protection and has to be complete.
//
// Anchored on the RESUME_TABLE rows rather than on a list of names here, so a fifth agent
// arriving with a Resume reddens this the same tick it reddens the registry guard above.
//
// What it proves is that every driven agent is NAMED in the enumerating sentence, not that what
// is said about each one is true. The per-agent artifact — config.toml, trustedFolders.json — is a vendor
// fact with no authority in this tree to check it against, so encoding those four filenames
// here would create a drift site rather than close one. Naming is the half that was actually
// wrong, and it is the half an omission hides behind.
func TestDriveAgentDisclosesTheTrustWriteForEveryAgentItDrives(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(string(src), "\n")
	var sites []int
	for i, line := range lines {
		if trustWriteDisclosureRe.MatchString(line) {
			sites = append(sites, i)
		}
	}
	// Two: the file header's and `help`'s. Pinned, because a guard that only checks the
	// sites it finds passes just as happily when one of them has been deleted.
	require.Len(t, sites, 2,
		"expected two trust-write disclosures in %s (the file header's and `help`'s); "+
			"if one moved or a third arrived, this guard needs to know", path)

	rows := resumeTableRows(t, string(src), path)
	for _, at := range sites {
		sentence := disclosureSentence(lines, at)
		require.NotEmpty(t, sentence,
			"could not lift the enumerating sentence out of the disclosure at %s:%d", path, at+1)
		for _, row := range rows {
			require.Contains(t, sentence, row[1],
				"the trust-write disclosure near %s:%d names some of the agents `resume` drives but not %q — "+
					"an enumeration of per-agent config files reads as exhaustive, so a missing row reads as "+
					"an exemption rather than as an omission",
				path, at+1, row[1])
		}
	}
}

// writeCaptureCallRe matches one write_capture call and captures its whole argument, command
// substitutions and all — the three call sites spell the stem three different ways.
var writeCaptureCallRe = regexp.MustCompile(`(?m)^\t+write_capture (.+)$`)

// capturesWriteRe matches a redirection into the captures directory.
var capturesWriteRe = regexp.MustCompile(`>"\$RUN/captures/`)

// writeCaptureBodyRe isolates write_capture's own body, so the redirections inside it can be
// told from any elsewhere.
var writeCaptureBodyRe = regexp.MustCompile(`(?s)\nwrite_capture\(\) \{\n(.*?)\n\}\n`)

// TestDriveAgentCapturesStayInTheFormEmitCanRead holds every capture the script writes to the
// shape `emit` parses, in the two ways a capture can leave it.
//
// emit globs captures/*.txt and DIES — not skips — on any stem it cannot read as
// <label>-w<width>[-t<frame>]. So one badly named capture does not degrade emit for that
// capture; it aborts emit for the whole run. #758 shipped both halves of that: `resume` wrote
// its pane as a bare `resume-<agent>` and dumped raw scrollback into the same directory as a
// second .txt, which between them made `emit` unusable after any resume — including after the
// mismatch die, which leaves the session up precisely so the capture can be read.
//
// Two assertions because the two failures are independent. A stem with no width is a capture
// emit cannot name; a non-pane .txt in captures/ is a capture emit should never have seen. The
// second is stated as "write_capture is the only writer of that directory", which is the
// invariant emit's own comment already assumes.
func TestDriveAgentCapturesStayInTheFormEmitCanRead(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "scripts", "drive-agent.sh")
	src, err := os.ReadFile(path)
	require.NoError(t, err)

	calls := writeCaptureCallRe.FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, calls, "no write_capture calls in %s — this guard is matching nothing", path)
	for _, call := range calls {
		require.Contains(t, call[1], "-w",
			"write_capture %s builds a stem with no -w<width>, which is the one shape emit refuses — "+
				"and it refuses it by dying on the whole run, not by skipping that capture", call[1])
	}

	body := writeCaptureBodyRe.FindStringSubmatch(string(src))
	require.NotNil(t, body, "no write_capture() { … } definition in %s — this guard is matching nothing", path)
	require.Equal(t,
		len(capturesWriteRe.FindAllString(string(src), -1)),
		len(capturesWriteRe.FindAllString(body[1], -1)),
		"something other than write_capture writes into $RUN/captures/ — emit reads every .txt in "+
			"there as a fixture-form pane and dies on anything else, so a diagnostic artifact belongs "+
			"beside that directory rather than in it")
}
