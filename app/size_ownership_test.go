package app

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/ui/overlay"
)

// sizeOwnershipExempt lists the home overlay fields that deliberately carry no
// size entry, each with its reason. An exempted field must stay unclaimed —
// the walk below fails when a target starts returning one — so promoting a
// field into the resize walk is a deliberate row deletion here, never drift.
var sizeOwnershipExempt = map[string]string{
	"renameOverlay": "sized once at open (the constructor) — deliberately outside the resize walk",
	"stashedDraft":  "a stashed create-form draft, not a live surface; statePrompt's entry sizes textInputOverlay",
}

// TestEverySizedOverlayFieldHasOneOwner closes #856's third debt: exactly one
// surfaceSpecs entry must size each overlay field. Because the walk sizes
// whatever pointer an entry's target returns, the target IS the mechanism —
// so the guard arms every overlay field with a distinct instance, calls every
// target, and checks the returned pointers against home's fields by identity.
// Mutants this kills: a second entry given an existing field's target (the
// field's owner list grows past one, and the distinct-pointer count drops), a
// deleted target (its field is unclaimed and not exempt), a target re-pointed
// at another entry's field (one field double-claimed AND one unclaimed), and
// an exemption kept after its field gained a target (exempt-yet-claimed —
// reachable today only through stashedDraft: RenameOverlay has no SetSize, so
// a target claiming it does not compile, which enforces that exemption
// structurally), and a hand-written target that boxes a typed nil (the
// unarmed pass at the bottom).
func TestEverySizedOverlayFieldHasOneOwner(t *testing.T) {
	h := &home{
		textInputOverlay:      &overlay.TextInputOverlay{},
		promptHistoryOverlay:  &overlay.PromptHistoryOverlay{},
		queueOverlay:          &overlay.QueueOverlay{},
		cmdLogOverlay:         &overlay.CmdLogOverlay{},
		checkpointOverlay:     &overlay.CheckpointOverlay{},
		imageOverlay:          &overlay.ImageOverlay{},
		commandPaletteOverlay: &overlay.CommandPaletteOverlay{},
		customCommandsOverlay: &overlay.CustomCommandsOverlay{},
		stashedDraft:          &overlay.TextInputOverlay{},
		textOverlay:           &overlay.TextOverlay{},
		confirmationOverlay:   &overlay.ConfirmationOverlay{},
		renameOverlay:         &overlay.RenameOverlay{},
		settingsOverlay:       &overlay.SettingsOverlay{},
		accountsOverlay:       &overlay.AccountsOverlay{},
		welcomeOverlay:        &overlay.WelcomeOverlay{},
	}

	claimed := map[uintptr][]state{}
	for st := stateDefault; st < numStates; st++ {
		entry := surfaceSpecs[st].size
		if entry.target == nil {
			continue
		}
		r := entry.target(h)
		require.NotNilf(t, r, "state %d: every overlay field is armed above, so a nil here means the target read a field this fixture missed — arm it and classify it", st)
		p := reflect.ValueOf(r).Pointer()
		claimed[p] = append(claimed[p], st)
	}

	overlayPkg := reflect.TypeOf(overlay.TextOverlay{}).PkgPath()
	hv := reflect.ValueOf(h).Elem()
	ht := hv.Type()
	overlayFields := 0
	for i := 0; i < ht.NumField(); i++ {
		f := ht.Field(i)
		if f.Type.Kind() != reflect.Pointer || f.Type.Elem().PkgPath() != overlayPkg {
			continue
		}
		overlayFields++
		fv := hv.Field(i)
		require.Falsef(t, fv.IsNil(),
			"home.%s is an overlay field this fixture does not arm — arm it with a distinct instance and classify it (one size entry, or a reasoned sizeOwnershipExempt row)", f.Name)
		owners := claimed[fv.Pointer()]
		if reason, ok := sizeOwnershipExempt[f.Name]; ok {
			assert.Emptyf(t, owners,
				"home.%s is exempt (%s) yet sized by states %v — delete either the exemption row or the extra target, whichever is the lie", f.Name, reason, owners)
			continue
		}
		assert.Lenf(t, owners, 1, "home.%s must have exactly one size owner; sized by states %v", f.Name, owners)
	}

	for name := range sizeOwnershipExempt {
		_, ok := ht.FieldByName(name)
		assert.Truef(t, ok, "sizeOwnershipExempt names %q, which is not a home field — delete the stale row", name)
	}
	assert.Equal(t, overlayFields-len(sizeOwnershipExempt), len(claimed),
		"the distinct sized pointers must be exactly the non-exempt overlay fields")

	// Handed an unarmed home, every target must return an untyped nil: that is
	// sizeTarget's typed nil check working. A hand-written entry that returns a
	// nil field directly boxes a typed nil into a non-nil interface, passes the
	// resize walk's nil guard, and panics inside SetSize on the first
	// WindowSizeMsg with its overlay unopened — the armed fixture above cannot
	// see that, because it never hands a target a nil field.
	// The comparison must be the walk's own (r != nil on the interface), not
	// assert.Nil: testify unwraps a typed nil by reflection and calls it nil —
	// exactly the value this pass exists to reject.
	unarmed := &home{}
	for st := stateDefault; st < numStates; st++ {
		entry := surfaceSpecs[st].size
		if entry.target == nil {
			continue
		}
		if r := entry.target(unarmed); r != nil {
			t.Errorf("state %d: the size target returned a typed nil in a non-nil interface — route it through sizeTarget", st)
		}
	}
}
