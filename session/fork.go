package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/session/transcript"
)

// ForkSeed is everything needed to branch a Claude conversation at a checkpoint
// into a new session, and to prove afterwards that it happened.
//
// The proof is not optional here. Claude honours --resume-session-at only in
// print mode; an interactive resume takes the flag and *ignores* it, forking the
// whole conversation with no error, no warning and a session that starts and
// answers normally. So a fork is materialized by a print run and then read back
// off disk — DroppedMessageID exists for no other reason.
type ForkSeed struct {
	// SourceTranscript is the forking session's transcript path. A path rather
	// than its session id because the fork runs in the *new* session's working
	// directory, and claude resolves a bare id within the current directory's
	// project — where the source's transcript is not. Claude's own background
	// launcher passes `transcriptPath ?? sessionId` for the same reason.
	SourceTranscript string
	// CutEntryID is the chain entry to keep through: Checkpoint.ForkAtID, the last
	// entry before the checkpoint's prompt. Claude keeps up to and *including* the
	// entry named, so this is the answer to "fork before that prompt".
	CutEntryID string
	// DroppedMessageID is the checkpoint's own prompt — the first thing the cut
	// discards, and therefore the marker whose absence proves the cut happened.
	DroppedMessageID string
	// NewSessionID is the forked conversation's id. Claude refuses a --session-id
	// that is not a valid UUID, or one already in use.
	NewSessionID string
}

// NewSessionID returns a random RFC 4122 version 4 UUID, the only form claude
// accepts for --session-id ("Error: Invalid session ID. Must be a valid UUID").
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate a session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// forkArgv is the command line that materializes a truncated fork.
//
// Split out from MaterializeFork so the argv is assertable without running
// claude — but note what such a test does and does not prove. It proves the flag
// is *passed*; only reading the forked transcript back proves it was *honoured*,
// which is the whole point of verifyFork below. The order follows claude's own
// background launcher, which builds --session-id/--fork-session ahead of --resume.
func forkArgv(seed ForkSeed, prompt string) []string {
	return []string{
		"-p", prompt,
		"--output-format", "json",
		"--session-id", seed.NewSessionID,
		"--fork-session",
		"--resume", seed.SourceTranscript,
		"--resume-session-at", seed.CutEntryID,
	}
}

// MaterializeFork runs the print-mode fork that writes the truncated conversation
// to disk under seed.NewSessionID, then verifies it. It returns nil only when the
// forked transcript exists, still holds the entry the cut kept, and no longer
// holds the turn the cut dropped.
//
// It runs in workDir — the new session's worktree — because that is what decides
// which project directory the fork is filed in, and the interactive session that
// resumes it will be looking there.
//
// Deliberately not runClaudeHeadless, which is built for the opposite job: that
// one puts claude under a throwaway $HOME, drops CLAUDE_CONFIG_DIR and passes
// --no-session-persistence, so it leaves nothing behind. Leaving something behind
// is the entire purpose of this call.
func MaterializeFork(ctx context.Context, executor cmd.Executor, claudePath, workDir, claudeConfigDir string, seed ForkSeed, prompt string) error {
	if seed.SourceTranscript == "" || seed.CutEntryID == "" || seed.NewSessionID == "" {
		return fmt.Errorf("incomplete fork seed")
	}

	c := exec.CommandContext(ctx, claudePath, forkArgv(seed, prompt)...)
	c.Dir = workDir
	c.Env = forkEnv(claudeConfigDir)

	out, err := executor.Output(c)
	if err != nil {
		return fmt.Errorf("could not fork the conversation: %w: %s", err, forkStderr(err))
	}
	// claude exits 0 and reports failures as result text, so the envelope's flag is
	// the only reliable signal — the same reason runClaudeHeadless reads it.
	var res claudeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return fmt.Errorf("could not parse the fork's output: %w", err)
	}
	if res.IsError {
		return fmt.Errorf("claude refused the fork: %s", strings.TrimSpace(res.Result))
	}

	return verifyFork(ctx, workDir, claudeConfigDir, seed)
}

// verifyFork reads the forked transcript back and refuses anything that is not a
// truncated fork.
//
// This is the guard the whole feature turns on. Outside print mode
// --resume-session-at is ignored rather than refused, so "the process ran and
// exited 0" is compatible with a fork of the entire conversation — the failure
// looks exactly like success from every angle except the transcript itself. The
// dropped-turn check is therefore not belt-and-braces beside the kept-entry one:
// it is the only assertion of the two that a silently-untruncated fork fails.
func verifyFork(ctx context.Context, workDir, claudeConfigDir string, seed ForkSeed) error {
	opts := transcript.Options{Root: claudeConfigDir}
	path := transcript.ForkPath("claude", workDir, seed.NewSessionID, opts)
	if path == "" {
		return fmt.Errorf("could not resolve where the fork should have been written")
	}
	present, err := ContainsForkEntries(ctx, path, seed.CutEntryID, seed.DroppedMessageID)
	if err != nil {
		return fmt.Errorf("could not read the forked conversation at %s: %w", path, err)
	}
	if !present[seed.CutEntryID] {
		return fmt.Errorf("the forked conversation is missing the checkpoint it was cut at (%s)", seed.CutEntryID)
	}
	if seed.DroppedMessageID != "" && present[seed.DroppedMessageID] {
		return fmt.Errorf(
			"the fork kept the turn it was meant to drop (%s): claude ignored --resume-session-at, "+
				"so this session would have been seeded from the end of the conversation instead of the checkpoint",
			seed.DroppedMessageID)
	}
	return nil
}

// ContainsForkEntries is transcript.ContainsEntries, indirected so tests can stand
// a transcript in front of verifyFork without a claude run.
var ContainsForkEntries = transcript.ContainsEntries

// ForkConversation resolves claude and materializes the fork, the shape the rest
// of the package calls — mirroring GenerateName, which likewise finds the binary
// and supplies the real executor so its caller needs neither.
func ForkConversation(ctx context.Context, workDir, claudeConfigDir string, seed ForkSeed, prompt string) error {
	claudePath, err := resolveClaudeBinary()
	if err != nil {
		return fmt.Errorf("cannot fork the conversation without claude: %w", err)
	}
	return MaterializeFork(ctx, cmd.MakeExecutor(), claudePath, workDir, claudeConfigDir, seed, prompt)
}

// forkConversation is the seam Instance.Start goes through, so a test can start a
// session carrying a seed without a claude on PATH and without a network call.
var forkConversation = ForkConversation

// forkEnv is the environment for the fork run: the parent's, with
// CLAUDE_CONFIG_DIR pointed at the session's account when it has one.
//
// Unlike namingEnv this must NOT relocate $HOME. The fork's whole product is a
// file under the config dir the session will read from, so a throwaway home would
// write the conversation somewhere nothing ever looks — and, since claude keeps
// credentials in the config dir, would also strip the auth the call needs.
func forkEnv(claudeConfigDir string) []string {
	base := os.Environ()
	if claudeConfigDir == "" {
		return base
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+claudeConfigDir)
}

// forkStderr surfaces what claude wrote to stderr when it exits non-zero, which is
// where its own refusals land ("No message found with message.uuid of ...",
// "Session ID ... is already in use"). Without it the caller reports only "exit
// status 1" for a failure claude explained precisely.
func forkStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return ""
}

// SetForkSeed arms the instance to seed its conversation from a checkpoint of
// another one. prompt is the turn the fork's print run takes, which becomes the
// session's first turn — the print run is what writes the truncated conversation
// to disk, so it has to ask something, and asking the user's own first prompt is
// what keeps that from being a wasted turn.
//
// Must be called before Start; a later call has no effect, and there is nothing to
// re-fork afterwards.
func (i *Instance) SetForkSeed(seed *ForkSeed, prompt string) {
	i.forkSeed = seed
	i.forkPrompt = prompt
}
