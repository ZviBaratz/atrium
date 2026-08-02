package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/log"
	"github.com/stretchr/testify/require"
)

// historicalDocs holds records of work already done. They describe the repo as it
// was at the time they were written, so freezing today's log path into them would
// be wrong; everything else is a live claim and must stay true.
const historicalDocs = "docs/superpowers"

// logFileName is the name this guard searches prose for. It is checked against
// the log package's real filename below rather than trusted, because a scanner
// looking for a string nothing produces any more passes every time.
const logFileName = "atrium.log"

// TestNoDocClaimsTheLogLivesInTheTempDir is the fourth README drift guard,
// alongside TestReadmeDocumentsEveryCommand, config.TestReadmeDocumentsEvery-
// ConfigField and keys.TestReadmeDocumentsEveryBinding.
//
// It exists because the log's location was restated in prose in eight places and
// not one of them was guarded — a package doc, an Initialize doc, a Close doc, a
// comment in session/tmux, two in internal/profile, and two README passages —
// one of them a copy-pasteable `grep pprof` recipe aimed at the temp dir, which
// would have silently stopped finding anything when the file moved to the data
// dir (#566). Every prior change in this area was reviewed with doc drift as its
// top finding, so the rule is mechanical rather than a grep somebody promises to
// run.
func TestNoDocClaimsTheLogLivesInTheTempDir(t *testing.T) {
	// A line naming the log file may not also name the temp dir. Precise enough
	// that the pprof section can still document profiles living in $TMPDIR on the
	// lines around it, which they do.
	tempDirTerms := []string{"TMPDIR", "TempDir", "/tmp/"}

	// Ask the log package what it actually calls its file. Without this the guard
	// would keep scanning happily for a name that had been renamed out of
	// existence — the one failure mode a prose scanner cannot see in its own
	// output, since this file's own comments mention the name too.
	t.Cleanup(log.Initialize(t.TempDir(), false))
	livePath, destErr := log.Destination()
	require.NoError(t, destErr)
	require.Equal(t, logFileName, filepath.Base(livePath),
		"the log package writes %q, but this guard searches prose for %q", filepath.Base(livePath), logFileName)

	var scanned int
	root := moduleRoot(t)
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		if d.IsDir() {
			switch {
			case d.Name() == ".git", d.Name() == "node_modules", d.Name() == "bin":
				return filepath.SkipDir
			case rel == filepath.FromSlash(historicalDocs):
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".go" && ext != ".md" {
			return nil
		}
		scanned++

		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, logFileName) {
				continue
			}
			for _, term := range tempDirTerms {
				if strings.Contains(line, term) {
					t.Errorf("%s:%d names atrium.log alongside %q, but the log lives in the data dir (#566):\n\t%s",
						rel, i+1, term, strings.TrimSpace(line))
				}
			}
		}
		return nil
	}))

	// Without this the test passes just as happily on a walk that matched
	// nothing: a broken skip rule would turn the guard off rather than fail it.
	require.NotZero(t, scanned, "walked no .go or .md files; the skip rules are wrong")

	require.Contains(t, moduleFile(t, "README.md"), "~/.atrium/atrium.log",
		"the README must name the log's real location — after the move it is the only prose that does")
}
