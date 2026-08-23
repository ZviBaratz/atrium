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
	"io/fs"
	"os"
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
// A profile name containing a comma cannot be expressed at all, and there is no refusal
// that says so: the separator has to be something, and such a name splits into entries
// here rather than being recognised, so what the caller sees is whichever refusal the
// halves earn — an unknown profile, most often. The remedy is --profile, which takes a
// name whole, one session at a time.
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
		// Bounded per entry, and the total bounded inside the loop rather than after it,
		// because the addition below is the part that can wrap: two counts near the top of
		// int sum to a NEGATIVE total, which passes a ceiling test spelled `total >
		// MaxVariantBatch` and hands resolveVariantPrograms a loop that allocates one
		// string per requested session until the process dies. One entry can never exceed
		// the bound the whole batch is held to, so applying it here costs nothing and
		// leaves no addition that can reach a number the ceiling cannot see.
		if count > session.MaxVariantBatch {
			return nil, fmt.Errorf("--variants entry %q asks for %d sessions; one atrium new fans out "+
				"to at most %d", entry, count, session.MaxVariantBatch)
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
		if total > session.MaxVariantBatch {
			return nil, fmt.Errorf("--variants asks for %d sessions; one atrium new fans out to at most %d",
				total, session.MaxVariantBatch)
		}
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
	branches, err := variantBranchSet(ctx, path)
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, total)
	for n := 1; len(titles) < total && n <= total+session.VariantTitleScan; n++ {
		cand := session.VariantTitle(stem, n)
		// Terminates the scan rather than skipping the candidate: suffixes only grow, so
		// once one is over the cap every later one is too, and reporting this as "not
		// enough free names" would send the caller looking for a collision that is not
		// there.
		// How much shorter deliberately goes unstated. The obvious arithmetic — this
		// candidate's overflow — is a lower bound and nothing more: a stem trimmed by
		// exactly that much overflows again at the next wider suffix, so a caller who
		// followed the number would be refused a second time by the same rule.
		if got := len([]rune(cand)); got > session.MaxTitleLen {
			return nil, fmt.Errorf("variant %q is %d characters and the limit is %d: a fan-out of %d "+
				"needs a title with room for a numbered suffix, so shorten %q",
				cand, got, session.MaxTitleLen, total, stem)
		}
		if checkTitleFree(prefix, cand, path, instances) != nil {
			continue
		}
		if branches[git.BranchNameForSession(prefix, cand)] {
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

// variantBranchSet reads the target repo's local branches once, so the suffix scan can
// ask about a name without asking git, and refuses the fan-out when git could not be
// asked at all.
//
// git.LocalBranchSet rather than git.LocalBranchExists per candidate, and the difference
// is not only the forks: LocalBranchExists is `err == nil`, so it answers "free" for a
// repo it cannot read — and this caller acts on the negative by CHOOSING the name, which
// would hand every variant a name the drain then refuses.
//
// git.ProbeGitRepo separates the two things that failure can mean, and it exists for
// exactly this decision. A fan-out does need a git repository — every variant wants its
// own worktree, the same refusal the create form makes for a direct target — but saying
// so about a target that IS one, when git merely could not be run (off PATH mid-upgrade,
// a fork failure under memory pressure, a cold checkout past gitLocalTimeout), sends a CI
// retry after a missing .git for a condition that clears by itself.
func variantBranchSet(ctx context.Context, path string) (map[string]bool, error) {
	branches, err := git.LocalBranchSet(ctx, path)
	if err == nil {
		return branches, nil
	}
	if isRepo, known := git.ProbeGitRepo(ctx, path); known && !isRepo {
		return nil, fmt.Errorf("%s is not a git repository, and a fan-out needs one: every variant "+
			"gets its own worktree, so --variants has no branches to derive free names against "+
			"(one session at a time works there — drop --variants)", path)
	}
	return nil, fmt.Errorf("could not read the branches of %s to pick free variant names: %w", path, err)
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
func spoolBatch(reqs []outbox.Request) ([]spooledVariant, error) {
	members := make([]spooledVariant, 0, len(reqs))
	for _, r := range reqs {
		record, err := writeCreateRecord(r)
		if err != nil {
			errs := []error{fmt.Errorf("failed to queue variant %q: %w", r.Title, err)}
			return nil, errors.Join(append(errs, withdrawSpooled(members)...)...)
		}
		members = append(members, spooledVariant{title: r.Title, record: record})
	}
	return members, nil
}

// withdrawSpooled takes back the members this command wrote before it gave up, and names
// every one it could not.
//
// os.Remove rather than outbox.Remove, whose "already gone is not an error" is the one
// reading this caller must not make. A record vanishes from under this command for
// exactly one ordinary reason: a running drain CLAIMED it, which is to say the session is
// being built right now — the outcome the whole withdrawal exists to warn about.
// Swallowing it would let the command exit non-zero saying nothing was queued while a
// session, a branch and a worktree come up minutes later with nothing pointing at them.
//
// Nothing is un-claimed from here, and nothing tries. A claimed record belongs to the
// drain holding it and the session behind it is real; the honest answer is to say so and
// let the caller go and look. DiscardCreate is the recovery for a claim, and it is the
// drain's to run — see rejectCreateRequest for what spending it costs.
func withdrawSpooled(members []spooledVariant) []error {
	var errs []error
	for _, member := range members {
		err := os.Remove(member.record)
		switch {
		case err == nil:
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, fmt.Errorf("queued variant %q could not be withdrawn and may still "+
				"be created: %w", member.title, err))
		case claimExists(member.record):
			errs = append(errs, fmt.Errorf("queued variant %q was claimed by a running atrium before "+
				"this command could take it back, so that session is being created now", member.title))
		default:
			errs = append(errs, fmt.Errorf("queued variant %q left the outbox before this command "+
				"could take it back, so it may already have been created", member.title))
		}
	}
	return errs
}

// claimExists reports whether a running drain holds the claim for record. Best-effort by
// construction — it is read after the record has already gone, to say which of two things
// took it — so an unreadable claim path answers "not claimed" and the caller falls back
// to the wider wording, which is true either way.
func claimExists(record string) bool {
	_, err := os.Stat(outbox.ClaimPath(record))
	return err == nil
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
	var failures []error
	for _, member := range members {
		err := awaitSpool(member.record, outbox.ClaimPath(member.record), time.Until(deadline),
			batchWaitCopy(member.title, timeout, len(members)))
		if err == nil {
			d, storeErr := storedSession(member.title, repo)
			if storeErr != nil {
				failures = append(failures, storeErr)
				continue
			}
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

// batchWaitCopy is one member's wording, and it deliberately carries no tally of what the
// batch has achieved.
//
// It could not carry an honest one. Members are awaited in variant order, so at the moment
// one times out this loop has not looked at anything after it — a count taken here is the
// members BEFORE this one that landed, which for the first member is always zero, and the
// loop then goes on to print `created` lines for the ones it had not reached. The command
// would exit saying nothing was created two lines under its own stdout saying otherwise.
// waitForCreates' return value is the tally, and it is taken once every member has been
// accounted for.
//
// The "queued, or being built right now" clause is waitForCreate's and is kept word for
// word: a member held in the outbox for the length of its own build is the common reason a
// batch outruns its --wait, and dropping the clause would read as untouched.
func batchWaitCopy(title string, timeout time.Duration, total int) spoolWaitCopy {
	return spoolWaitCopy{
		refused: fmt.Sprintf("atrium did not create %q", title),
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s without session %q appearing; it is one of %d "+
				"this atrium new asked for and is still in the outbox — either queued, or being built "+
				"right now, since a create is held there until its worktree, branch and agent exist. A "+
				"batch is built one session at a time, so it needs a --wait sized for all of them",
				timeout, title, total),
				"A running atrium drains it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does")
		},
	}
}
