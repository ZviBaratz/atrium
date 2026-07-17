package keys

import (
	"strings"
	"testing"
)

// Direction 1 of the drift guard: every registered binding must be documented
// by the cheatsheet layout — in a row's key column (Keys) or, rarely, named
// inside a row's prose (Mentions, verified rendered below). A new action
// without a row fails here, structurally, before any rendering happens.
func TestHelpGroups_CoverEveryBinding(t *testing.T) {
	covered := map[KeyName]bool{}
	for _, g := range HelpGroups {
		for _, r := range g.Rows {
			for _, k := range r.Keys {
				covered[k] = true
			}
			for _, k := range r.Mentions {
				covered[k] = true
			}
		}
	}
	for name := range GlobalKeyBindings {
		if !covered[name] {
			t.Errorf("binding %v has no cheatsheet row (add it to a HelpGroup, "+
				"or a Mentions if it is taught inside another row's prose)", name)
		}
	}
}

// Direction 2, structurally: a row can only document keys the registry knows.
// Referencing a name with no binding renders an empty key label — and
// KeyScreensaver, whose absence from the registry is the easter egg's
// exclusion contract, fails here if someone tries to document it.
func TestHelpGroups_RefsResolve(t *testing.T) {
	for _, g := range HelpGroups {
		for _, r := range g.Rows {
			if len(r.Keys) == 0 {
				t.Errorf("group %q: row %q has no Keys — desc-only rows can't be "+
					"tied to the registry", g.Title, r.Desc)
			}
			for _, k := range append(append([]KeyName{}, r.Keys...), r.Mentions...) {
				if _, ok := GlobalKeyBindings[k]; !ok {
					t.Errorf("group %q: row %q references %v, which has no registry "+
						"binding", g.Title, r.Desc, k)
				}
			}
		}
	}
}

// A Mention is a key taught inside another row's prose (space lives in the
// multi-select row). The mention only counts as documentation if the prose
// actually names the key — delete the words and this fails, which is what
// keeps Mentions from becoming a silent exclusion list.
func TestHelpGroups_MentionsAreRendered(t *testing.T) {
	for _, g := range HelpGroups {
		for _, r := range g.Rows {
			for _, k := range r.Mentions {
				label := GlobalKeyBindings[k].Help().Key
				if !strings.Contains(r.Desc, label) {
					t.Errorf("group %q: row %q mentions %v but its desc does not "+
						"contain %q", g.Title, r.Desc, k, label)
				}
			}
		}
	}
}

// A LayerBoth key acts in both layers, so its cheatsheet row gets no generated
// "in a session: " prefix (that is only for LayerAttached) — the prose itself
// has to say what the key does on the attached side, or that behavior goes
// undocumented. This pins the attached-side phrase per key so a reworded desc
// can't quietly drop it, and requires every LayerBoth entry to carry a pin so a
// new one can't slip in unguarded.
func TestHelpGroups_LayerBothStateAttachedSide(t *testing.T) {
	// The attached-side phrase each LayerBoth row's desc must contain.
	wantPhrase := map[KeyName]string{
		KeyAttachToggle: "detach",   // a toggle: while attached it detaches
		KeyKill:         "attached", // kills the attached session, not just a listed one
	}
	// Guard the map against the registry: every LayerBoth key must be pinned
	// here, so adding one without an attached-side phrase fails loudly.
	for _, e := range Registry {
		if e.Layer == LayerBoth {
			if _, ok := wantPhrase[e.Name]; !ok {
				t.Errorf("LayerBoth key %v has no attached-side phrase pinned here; "+
					"add one so its cheatsheet prose can't drop the attached behavior", e.Name)
			}
		}
	}
	for _, g := range HelpGroups {
		for _, r := range g.Rows {
			for _, k := range r.Keys {
				phrase, ok := wantPhrase[k]
				if !ok {
					continue
				}
				if !strings.Contains(r.Desc, phrase) {
					t.Errorf("row for %v: desc %q must state the attached side (contain %q)",
						k, r.Desc, phrase)
				}
				delete(wantPhrase, k)
			}
		}
	}
	for k := range wantPhrase {
		t.Errorf("no cheatsheet row keyed by LayerBoth key %v", k)
	}
}

// The reorder rows must name their units exactly (the #357 class): a session
// row, a repo group, an account cluster. Exact matches — a Contains("account")
// check would have passed the very bug this guards against.
func TestHelpGroups_ReorderRowDescs(t *testing.T) {
	want := map[KeyName]string{
		KeyMoveUp:        "reorder within a repo group",
		KeyMoveGroupUp:   "move a whole group up / down",
		KeyMoveAccountUp: "move an account cluster up / down",
	}
	for _, g := range HelpGroups {
		for _, r := range g.Rows {
			for _, k := range r.Keys {
				if desc, ok := want[k]; ok {
					if r.Desc != desc {
						t.Errorf("row for %v: desc %q, want %q", k, r.Desc, desc)
					}
					delete(want, k)
				}
			}
		}
	}
	for k := range want {
		t.Errorf("no cheatsheet row keyed by %v", k)
	}
}
