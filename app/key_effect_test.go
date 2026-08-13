package app

import (
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/stretchr/testify/assert"
)

// This file is what #521 is for. keyAllowedWhileBusy's doc comment states a rule
// about every key in the registry — "nothing that mutates a session, opens an
// overlay, or drives tmux/git" — and until the registry carried an Effect there was
// nothing to hold it to. A key added to that switch by someone who had not read the
// handler it admits was a silent correctness bug: `go vet`, lint and the suite all
// pass, and the failure only shows up as an action racing an in-flight one.

// busyGateMutationExempt names the keys keyAllowedWhileBusy admits *even though*
// they are EffectMutate, and why. An exemption is a claim that admitting the key
// is deliberate and that its reason survives review — never that the
// classification is wrong — and TestBusyGateExemptionsAreRealAndReasoned rejects
// the ways that claim could rot.
//
// It lives here rather than beside the switch because the assertion below is its
// only reader; the reason itself is stated in keyAllowedWhileBusy's own doc
// comment, where an author editing the switch will meet it.
var busyGateMutationExempt = map[keys.KeyName]string{
	keys.KeyQuit: "quitting persists state and can launch the autoyes daemon, so it is a " +
		"mutation — but it is the escape hatch from a wedged action, and swallowing it " +
		"with a busy notice would leave no way out but ctrl+c. Stated at " +
		"keyAllowedWhileBusy's doc comment",
}

// TestBusyAllowlistNeverAdmitsAMutation is the subset check: every key the
// busy-gate lets through mid-action must be observing or view-only.
//
// The assertion is membership in a set, not `!= EffectMutate`, so an unclassified
// key fails here too — a name whose Effect nobody wrote is exactly the case that
// must not read back as safe.
func TestBusyAllowlistNeverAdmitsAMutation(t *testing.T) {
	var admitted []string
	for _, e := range keys.Registry {
		if !keyAllowedWhileBusy(e.Name) {
			continue
		}
		if busyGateMutationExempt[e.Name] != "" {
			continue
		}
		switch keys.EffectOf(e.Name) {
		case keys.EffectObserve, keys.EffectView:
			continue
		}
		admitted = append(admitted, fmt.Sprintf("%q (key %q) is %s",
			e.Binding.Help().Desc, keys.PrimaryKey(e.Name), keys.EffectOf(e.Name)))
	}
	assert.Empty(t, admitted, "keyAllowedWhileBusy admits these mid-action, but their "+
		"Effect says they change a session, repo, config or agent — which is what the "+
		"gate exists to hold back. Either the key does not belong in the allowlist, or "+
		"its classification is wrong, or admitting it is deliberate and belongs in "+
		"busyGateMutationExempt with the reason")
}

// exemptLabel names a KeyName the way the subset check above does — the help desc
// plus the key — because keys.LabelOf alone reports the merge action as "m", which
// is the identifier a reader is least able to search for.
func exemptLabel(name keys.KeyName) string {
	for _, e := range keys.Registry {
		if e.Name == name {
			return fmt.Sprintf("%q (key %q)", e.Binding.Help().Desc, keys.PrimaryKey(name))
		}
	}
	return fmt.Sprintf("unregistered KeyName(%d)", int(name))
}

// TestBusyGateExemptionsAreRealAndReasoned is the other direction. The exemption
// map is the one place this guard can be silenced, so it must not become a place to
// park a key nobody re-read: an entry may not name a key the allowlist does not
// admit, one that needs no exemption because it is not a mutation, or one that
// carries no reason.
func TestBusyGateExemptionsAreRealAndReasoned(t *testing.T) {
	registered := map[keys.KeyName]bool{}
	for _, e := range keys.Registry {
		registered[e.Name] = true
	}

	for name, reason := range busyGateMutationExempt {
		label := exemptLabel(name)
		assert.NotEmptyf(t, reason, "%s is exempted with no reason", label)
		assert.Truef(t, registered[name],
			"%s is exempted but has no Registry entry — nothing to exempt", label)
		assert.Truef(t, keyAllowedWhileBusy(name),
			"%s is exempted from the busy-gate subset check but the allowlist does not "+
				"admit it, so the exemption is stale and now hides a real one", label)
		assert.Equalf(t, keys.EffectMutate, keys.EffectOf(name),
			"%s is %s, which the subset check already allows — an exemption for it "+
				"claims a tension that no longer exists", label, keys.EffectOf(name))
	}
}

// The absence of undo from the allowlist is load-bearing, not an oversight: the
// busy-gate is the only thing making a restore single-flight, and two presses
// before the first returns would recreate a branch and a worktree the first has
// already claimed. Pinned here because it is exactly the kind of "obviously
// harmless, it only reads" addition the guard above cannot object to — undo is
// EffectMutate, so adding it would fail there, but only as long as nothing
// reclassifies it on the way.
func TestBusyGateStillExcludesUndo(t *testing.T) {
	assert.False(t, keyAllowedWhileBusy(keys.KeyUndoKill),
		"undo must stay outside the busy-gate: the gate is what makes it single-flight")
	assert.Equal(t, keys.EffectMutate, keys.EffectOf(keys.KeyUndoKill))
}
