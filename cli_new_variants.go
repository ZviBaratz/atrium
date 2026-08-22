package main

// cli_new_variants.go — `atrium new --variants`, the headless half of the N-variant
// fan-out (#761).
//
// #387 gave the create form a per-profile count stepper, so one submit races three
// claudes, or claude against codex, and the diff chips make the comparison. It landed in
// the TUI only, which left the bake-off keyboard-only — and a script, a CI job or an
// agent working a queue is exactly the caller that wants it.
//
// One invocation, N spooled records sharing a batch id. Each member is an ordinary
// outbox.Request carrying its own final title and program, so every gate, claim,
// receipt, disclosure and recovery path below the session cap treats it as the single
// create it is; the id exists so the drain can charge the cap to the batch rather than
// to each member and refuse one that does not fit whole rather than creating it from the
// tail. That is also why the titles are derived HERE rather than in the drain: a record
// that names the session it will become is one an atrium too old to have heard of a
// batch still creates correctly, and it is one whose branch this command can print
// before it exits.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
)

// variantSpec is one entry of --variants: a configured profile and how many sessions of
// it to create.
type variantSpec struct {
	profile string
	count   int
}

// parseVariantSpec turns the --variants value into its entries, in the order the caller
// wrote them — which becomes the order the variants are titled and drained in.
//
// The grammar is comma-separated `profile[:count]`, mirroring uzi's `--agents`, and the
// count splits on the LAST colon rather than the first. That is not pedantry: with no
// `profiles` block in config.json, config.GetProfiles synthesizes one profile whose Name
// is the whole default_program, so a colon in a profile name is reachable on a default
// install and a first-colon split would make the only configured profile unnameable.
//
// A profile name containing a comma cannot be expressed at all, and is refused saying so
// rather than mis-split: the separator has to be something, and a name with a comma in it
// is still reachable one session at a time through --profile.
//
// Every refusal names the entry it is about. Nothing here consults the config, so a
// mistyped grammar is reported before an unknown-profile error can mask it.
func parseVariantSpec(raw string) ([]variantSpec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("--variants was given no profiles; pass e.g. --variants claude:2,codex:1")
	}
	var specs []variantSpec
	total := 0
	for _, token := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(token)
		if entry == "" {
			return nil, fmt.Errorf("--variants %q has an empty entry; check for a doubled or trailing comma", raw)
		}
		name, count := entry, 1
		if i := strings.LastIndex(entry, ":"); i >= 0 {
			name = strings.TrimSpace(entry[:i])
			n, err := strconv.Atoi(strings.TrimSpace(entry[i+1:]))
			if err != nil {
				return nil, fmt.Errorf("--variants entry %q has a count that is not a number; "+
					"the form is profile or profile:count", entry)
			}
			count = n
		}
		if name == "" {
			return nil, fmt.Errorf("--variants entry %q names no profile", entry)
		}
		if count < 1 {
			return nil, fmt.Errorf("--variants entry %q asks for %d sessions; a count is at least 1", entry, count)
		}
		for _, seen := range specs {
			if seen.profile == name {
				// Refused rather than summed, so the total the caller reads off their own
				// command line is the total they get.
				return nil, fmt.Errorf("--variants names profile %q twice; give it one count", name)
			}
		}
		specs = append(specs, variantSpec{profile: name, count: count})
		total += count
	}
	if total > session.MaxVariantBatch {
		return nil, fmt.Errorf("--variants asks for %d sessions; one atrium new fans out to at most %d",
			total, session.MaxVariantBatch)
	}
	return specs, nil
}

// resolveVariantPrograms expands specs into one program string per session, in spec
// order, so index i of the result is the program of variant i.
//
// Each entry goes through resolveNewProgram, the same function --profile uses, so an
// unknown profile is refused in the same words with the same list of what is configured.
// One spelling of that message, and a fan-out gets the profile table's behaviour for
// free rather than a second opinion about it.
func resolveVariantPrograms(cfg *config.Config, specs []variantSpec) ([]string, error) {
	var programs []string
	for _, spec := range specs {
		program, err := resolveNewProgram(cfg, "", spec.profile)
		if err != nil {
			return nil, err
		}
		for i := 0; i < spec.count; i++ {
			programs = append(programs, program)
		}
	}
	return programs, nil
}

// planVariantTitles allocates total collision-free titles from stem, one per variant.
//
// It mirrors app.planVariantTitles — same <stem>-N scheme through session.VariantTitle,
// same ascending scan, same skip-what-is-taken — and it is a mirror on purpose rather
// than a share: that one asks the LIVE instance list through the create form's own
// verdicts, and this side has neither. What it does have is the stored list and the
// repo's branches, which is a strictly weaker predicate over the same candidates.
//
// Weaker, and therefore not authoritative — which is runNew's standing terms for every
// check it makes. This one cannot see a session filed under a repo GROUP that differs
// from its path, a tmux name a legacy session shadows, an owned `_term`/`_run` sibling a
// rename left behind, or a create that another process spools a moment later. The drain
// re-runs variantTitleConflictIn against the live list at the only moment the answer is
// true, and a variant that loses there is refused with a receipt its caller reads. What
// deriving here buys instead is worth that: the record names the session it will become,
// so an atrium too old to know about batches still creates it correctly, and the caller
// is told its branch names when it spools rather than only if it waits.
func planVariantTitles(
	ctx context.Context, prefix, stem string, total int, path string, instances []session.InstanceData,
) ([]string, error) {
	titles := make([]string, 0, total)
	for n := 1; len(titles) < total && n <= total+session.VariantTitleScan; n++ {
		cand := session.VariantTitle(stem, n)
		// Terminates the scan rather than skipping the candidate: suffixes only grow, so
		// once one is over the cap every later one is too, and reporting this as "not
		// enough free names" would send the caller looking for a collision that is not
		// there.
		if got := len([]rune(cand)); got > session.MaxTitleLen {
			return nil, fmt.Errorf("variant %q is %d characters; the limit is %d, so a fan-out of "+
				"%d needs a title at least %d shorter",
				cand, got, session.MaxTitleLen, total, got-session.MaxTitleLen)
		}
		if checkTitleFree(prefix, cand, path, instances) != nil {
			continue
		}
		taken, err := variantBranchTaken(ctx, prefix, cand, path)
		if err != nil {
			return nil, err
		}
		if taken {
			continue
		}
		titles = append(titles, cand)
	}
	if len(titles) < total {
		return nil, fmt.Errorf("could not find %d free names from %q in %s; "+
			"the numbered variants of it are taken as far as this looked", total, stem, path)
	}
	return titles, nil
}

// variantBranchTaken reports whether candidate's session branch already exists in the
// target repo, and errors when git could not be asked.
//
// git.LookupLocalBranch rather than git.LocalBranchExists, and the difference is the
// whole reason this function exists: LocalBranchExists is `err == nil`, so it answers
// "free" for a repo it cannot read — and this caller acts on the negative by CHOOSING
// the name, which would hand every variant a name the drain then refuses. A repo git
// cannot read is also the answer to a target that is not a repository, and a fan-out
// needs one: every variant wants its own worktree, which is the same refusal the create
// form makes for a direct target.
//
// Reached only for candidates the in-memory check already cleared, so the common case
// costs one git invocation per variant and nothing for a run without --variants.
func variantBranchTaken(ctx context.Context, prefix, candidate, path string) (bool, error) {
	exists, err := git.LookupLocalBranch(ctx, path, git.BranchNameForSession(prefix, candidate))
	if err != nil {
		return false, fmt.Errorf("could not read the branches of %s to pick free variant names, so "+
			"--variants has nothing to derive them against (a fan-out needs a git repository, one "+
			"worktree per variant): %w", path, err)
	}
	return exists, nil
}

// writeCreateRecord is the seam spoolBatch writes through.
//
// It exists for lookupBranchTip's reason: nothing outside this process can make the k-th
// write of a batch fail, and the withdrawal below is the half that only runs when one
// does.
var writeCreateRecord = outbox.WriteCreate

// spoolBatch commits every member of a batch, or none of them, and returns the record
// paths in variant order.
//
// A batch half in the spool is a batch created half, which is the outcome the whole-batch
// cap gate exists to prevent — so a failed write withdraws what has already landed
// before it returns. The withdrawal's own failures are joined into the error rather than
// logged and dropped: those members may still be created, minutes after their caller was
// told the command failed, and that is precisely the thing a caller has to be able to
// find out about.
func spoolBatch(reqs []outbox.Request) ([]string, error) {
	records := make([]string, 0, len(reqs))
	for _, r := range reqs {
		record, err := writeCreateRecord(r)
		if err != nil {
			errs := []error{fmt.Errorf("failed to queue variant %q: %w", r.Title, err)}
			return nil, errors.Join(append(errs, withdrawSpooled(records)...)...)
		}
		records = append(records, record)
	}
	return records, nil
}

// withdrawSpooled removes records this command wrote and has decided not to leave
// behind. outbox.Remove rather than DiscardCreate: nothing has claimed them — the drain
// claims only what it is about to build — and a record that is already gone is not an
// error.
func withdrawSpooled(records []string) []error {
	var errs []error
	for _, record := range records {
		if err := outbox.Remove(record); err != nil {
			errs = append(errs, fmt.Errorf("a queued variant could not be withdrawn and may still "+
				"be created: %w", err))
		}
	}
	return errs
}

// spooledVariant pairs a member's title with the record `--wait` watches for it. The
// title is carried rather than re-read from the record because the record is what goes
// away when the session is made.
type spooledVariant struct {
	title  string
	record string
}

// waitForCreates blocks until every member of a spooled batch has been accounted for,
// reporting each session as it appears.
//
// One member delegates to waitForCreate, so an ordinary `atrium new --wait` keeps every
// word it had. A batch is the same protocol N times over, with three properties the
// singular version does not need:
//
//   - One deadline for the whole command, not one each. The drain starts one spooled
//     session at a time (createStartBudget), so a batch's builds run in series and a
//     per-member timeout would multiply what the caller asked for.
//   - Members are awaited in variant order, which is drain order: ListCreates is oldest
//     first and the members were written in order.
//   - It never returns early. A refusal partway through still leaves the members before
//     it created and the ones after it to be answered, and a caller that ran a bake-off
//     is owed the sessions it got as much as the reason for the one it did not.
//
// A remaining duration that has gone negative is passed through deliberately rather than
// short-circuited: awaitSpool takes a full sample before it tests its deadline, so a
// member that is already refused or already settled is reported as such instead of as a
// timeout it beat.
func waitForCreates(out io.Writer, members []spooledVariant, repo string, timeout time.Duration) error {
	if len(members) == 1 {
		return waitForCreate(out, members[0].record, members[0].title, repo, timeout)
	}

	deadline := time.Now().Add(timeout)
	created := 0
	var failures []error
	for _, member := range members {
		err := awaitSpool(member.record, outbox.ClaimPath(member.record), time.Until(deadline),
			batchWaitCopy(member.title, timeout, len(members), &created))
		if err == nil {
			d, storeErr := storedSession(member.title, repo)
			if storeErr != nil {
				failures = append(failures, storeErr)
				continue
			}
			created++
			_, _ = fmt.Fprintf(out, "created %q%s\n", member.title, createdBranchClause(d))
			continue
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d requested sessions were not created: %w",
		len(failures), len(members), errors.Join(failures...))
}

// batchWaitCopy is one member's wording. created is read at the deadline rather than
// captured, so the tally names what had landed by then — the number a caller needs to
// tell "the batch was refused" from "the batch is still going up".
func batchWaitCopy(title string, timeout time.Duration, total int, created *int) spoolWaitCopy {
	return spoolWaitCopy{
		refused: fmt.Sprintf("atrium did not create %q", title),
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s without session %q appearing; %d of %d were "+
				"created, and the rest are still in the outbox — a batch is built one session at a "+
				"time, so it needs a --wait sized for all of them", timeout, title, *created, total),
				"A running atrium drains it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does")
		},
	}
}
