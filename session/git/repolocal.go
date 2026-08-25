package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RepoRoot resolves the toplevel of the repository containing path (git
// rev-parse --show-toplevel). It is the identity the repo-trust ledger keys on
// — note that for a path inside a linked git worktree this is that worktree's
// own toplevel, not the shared common dir, so two linked checkouts of one
// repository are two identities. RepoGroupKey wraps the same resolver but
// keeps only the basename; trust needs the whole path.
func RepoRoot(ctx context.Context, path string) (string, error) {
	return findGitRepoRoot(ctx, path)
}

// FileAtRef reads one file out of a repository at ref, as it would be CHECKED
// OUT: `git cat-file --filters`, so eol/smudge attributes apply and the bytes
// match what a fresh worktree of that ref will hold on disk. That is the
// property the repo-trust ledger needs — a grant hashes these bytes, and
// enforcement hashes the worktree's file, so any systematic difference between
// the two readers (a trimmed newline, an unapplied filter) would make every
// grant unmatchable. (localGit's TrimSpace is exactly such a difference, which
// is why this rides the bytes-returning runners.) ref is what
// StartPointPreview answers: the ref the session's worktree will materialize,
// which is HEAD only when no base freshening or explicit base applies.
//
// present is false when ref carries no such file — including an unborn HEAD (a
// fresh init with no commits), where nothing is checked out at all. err is for
// git being unable to answer (off PATH, killed by timeout), telling the caller
// "could not determine" apart from "determined absent"; the discriminator is
// ProbeGitRepo's: a git that exited with a real status answered.
//
// maxBytes bounds the read twice: the blob's stored size is checked before the
// content is asked for (a repo can commit a file of any size, and this runs on
// session-creation paths), and the filtered stream is read through a hard cap
// that kills the filter on overflow — a filter can expand (an LFS pointer is
// tiny, its content is not; the smudge command comes from the user's global
// gitconfig but the repo's .gitattributes selects it), and buffering first and
// checking after would let the repo choose the allocation. Oversized returns
// present=true with an error, never a truncation: truncated bytes would parse
// and hash as something the repo never said.
func FileAtRef(ctx context.Context, root, ref, name string, maxBytes int64) (data []byte, present bool, err error) {
	listed, err := localGit(ctx, root, "ls-tree", "--name-only", ref, "--", name)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			// git ran and said no: an unborn HEAD, a ref that does not exist, or no
			// repository here any more. Either way the ref checks out no such file.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("list %s in %s of %s: %w", name, ref, root, err)
	}
	if strings.TrimSpace(listed) == "" {
		return nil, false, nil
	}

	spec := ref + ":" + name
	sizeText, err := localGit(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return nil, true, fmt.Errorf("size %s in %s: %w", spec, root, err)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return nil, true, fmt.Errorf("size of %s in %s is unreadable (%q): %w", spec, root, sizeText, err)
	}
	if size > maxBytes {
		return nil, true, fmt.Errorf("%s in %s is %d bytes, over the %d-byte cap", name, root, size, maxBytes)
	}

	data, err = localGitBytesCapped(ctx, root, maxBytes, "cat-file", "--filters", spec)
	if err != nil {
		if errors.Is(err, errOutputOverCap) {
			return nil, true, fmt.Errorf("%s in %s filters to more than the %d-byte cap", name, root, maxBytes)
		}
		return nil, true, fmt.Errorf("read %s in %s: %w", spec, root, err)
	}
	return data, true, nil
}
