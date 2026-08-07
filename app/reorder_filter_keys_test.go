package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reorder key whose neighbor the filter hides used to move — and persist — an order
// with nothing on screen changing (#339). Every scope now refuses and explains itself,
// and (the other half of the contract) a swap the user can actually see still happens.

// filterReorderHome builds a stateDefault home with working in-memory storage whose
// sessions are (displayName, repo, account) triples, so a filter can hide the neighbor of
// any reorder scope. The menu is visible in stateDefault, so the refusal notices land.
func filterReorderHome(t *testing.T, specs ...[3]string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	st := config.DefaultState()
	storage, err := session.NewStorage(st)
	require.NoError(t, err)
	h.appState = st
	h.storage = storage
	for _, spec := range specs {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: spec[0], Path: "/tmp/" + spec[1], Program: "echo",
		})
		require.NoError(t, err)
		if spec[2] != "" {
			inst.SetClaudeAccount(spec[2], "", false)
		}
		h.list.AddInstance(inst)
	}
	h.state = stateDefault
	return h
}

// J toward a filtered-out sibling must explain itself rather than silently rewriting the
// persisted order.
func TestReorderKeys_SessionMoveRefusesPastFilterHiddenSibling(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"zzz-hidden", "repoA", ""},
		[3]string{"api-two", "repoA", ""})
	h.list.SetFilter("api") // zzz-hidden sits between the two matches
	before := instanceTitles(h)

	pressKey(h, 'J') // KeyMoveDown

	require.True(t, h.menu.HasNotice(), "the refusal must explain itself")
	assert.Contains(t, h.menu.String(), "filter-hidden session")
	assert.Equal(t, before, instanceTitles(h), "and must not touch the persisted order")
}

// The other half: a sibling that renders is a swap the user can see, so it must still
// move and persist. Refusing here would trade #339 for "J stopped working".
func TestReorderKeys_SessionMoveStillMovesWhenTheNeighborIsVisible(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"zzz-other", "repoA", ""})
	h.list.SetFilter("api") // both neighbors render

	pressKey(h, 'J')

	assert.False(t, h.menu.HasNotice(), "a visible swap needs no explanation")
	assert.Equal(t, []string{"api-two", "api-one", "zzz-other"}, instanceTitles(h),
		"the visible swap still happens and persists")
}

// } toward a block the filter has emptied renders nothing, so the transpose would be
// invisible.
func TestReorderKeys_GroupMoveRefusesPastEmptiedGroup(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", "work"},
		[3]string{"zzz-hidden", "repoB", "work"})
	h.list.SetFilter("api") // the whole repoB block renders nothing
	before := instanceTitles(h)

	pressKey(h, '}') // KeyMoveGroupDown

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "filter-hidden group")
	assert.Equal(t, before, instanceTitles(h))
}

// The issue's repro, end-to-end through handleKeyPress: the guard counts two clusters
// (it counts them in items, regardless of visibility), so before the fix ] reported
// success and wrote account_order to state with the rendered list unchanged.
func TestReorderKeys_AccountMoveRefusesPastEmptiedCluster(t *testing.T) {
	h := accountGroupedHome(t) // api|work, infra|personal
	h.state = stateDefault
	h.list.SetFilter("api") // empties the whole personal cluster

	require.True(t, h.list.AccountReorderEnabled(), "precondition: the guard still says available")
	pressKey(h, ']') // KeyMoveAccountDown

	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "filter-hidden cluster")
	assert.Empty(t, h.appState.GetAccountOrder(),
		"nothing may reach state.json when the screen cannot change")
}

// A folded sibling is the same shape reached without a filter: J inside a fold was a
// silent dead key (the move refused, SessionReorderEnabled said otherwise, and
// moveAndPersist swallows a false). It now names the fold and the key that undoes it.
func TestReorderKeys_SessionMoveRefusesPastFoldedSiblingAndSaysSo(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"alpha", "repoA", ""},
		[3]string{"apex", "repoA", ""},
		[3]string{"bravo", "repoB", ""})
	h.list.SetSelectedInstance(0)
	require.True(t, h.list.Collapse(), "precondition: repoA is folded")

	pressKey(h, 'J')

	require.True(t, h.menu.HasNotice(), "a folded refusal must not stay silent")
	assert.Contains(t, h.menu.String(), "folded session")
	assert.Contains(t, h.menu.String(), "expand")
}

// Notice precedence. A status sort owns within-block order whether or not a filter is
// live, so clearing the filter would not restore J — naming the filter would promise a
// fix that does not arrive.
func TestReorderKeys_SortRefusalOutranksTheFilterRefusal(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"zzz-hidden", "repoA", ""},
		[3]string{"api-two", "repoA", ""})
	h.list.SetSortMode("status")
	h.list.SetFilter("api")

	pressKey(h, 'J')

	assert.Contains(t, h.menu.String(), "sorting by status",
		"the durable reason wins over the transient one")
	assert.NotContains(t, h.menu.String(), "esc to clear")
	assert.Contains(t, h.menu.String(), "session reorder",
		"the refusal names the one ladder the sort disables (#346)")
}

// The status-sort refusal names the session ladder, and that scoping has to stay true:
// the sort owns within-group order only, so { / } keeps working under it. Nothing pinned
// the two halves together, which is how the hint drifted to the unscoped "manual reorder
// is off" — a claim the settings screen contradicts ("Group order stays manual ({ / })").
// ui.TestGroupMode_StatusSortGroupMoveCrossesAccountsFreely pins the move; this pins that
// the notice does not disown it.
func TestReorderKeys_StatusSortRefusalScopesItselfToSessions(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')
	require.True(t, h.menu.HasNotice(), "J under a status sort must explain itself")
	notice := h.menu.String()
	assert.Contains(t, notice, "session reorder", "only the session ladder is off")
	assert.NotContains(t, notice, "manual reorder",
		"'manual' is the settings screen's word for group order too, so it over-claims")
	assert.Contains(t, notice, ", to switch",
		"the only fixable refusal that named no key; , opens the sort setting (#346)")

	// The other half: the ladders the notice does not disown still move, silently.
	h.menu.ClearNotice()
	pressKey(h, '}') // KeyMoveGroupDown — repoA past repoB, under the same status sort
	assert.Equal(t, []string{"infra-one", "api-one", "api-two"}, instanceTitles(h),
		"a status sort owns within-group order only, so { / } still moves")
	assert.False(t, h.menu.HasNotice(), "a group move that works needs no explanation")
}

// Same rule at the block level: the account boundary is filter-independent, and its
// advice ([ / ]) would itself be refused while filtering — so it must be named first.
func TestReorderKeys_AccountBoundaryRefusalOutranksTheFilterRefusal(t *testing.T) {
	h := accountGroupedHome(t) // api|work, infra|personal
	h.state = stateDefault
	h.list.SetSelectedInstance(1) // infra|personal, whose neighbor above is the work cluster
	h.list.SetFilter("infra")     // and which the filter has also emptied

	pressKey(h, '{') // KeyMoveGroupUp — crosses into work, which is also hidden

	assert.Contains(t, h.menu.String(), "within an account",
		"the boundary a cleared filter would not lift is named first")
}

// The refusal notices that advertise ',' land on the setting they are about. Advertising a
// key that opens a 13-entry rail is barely better than not advertising it — the notice knows
// exactly which row it means.
func TestReorderNoticesDeepLinkToTheirSetting(t *testing.T) {
	cases := []struct {
		name, want string
		setup      func(h *home)
		key        rune
	}{
		{
			name: "status sort refusal", want: "session_sort", key: 'J',
			setup: func(h *home) { h.list.SetSortMode("status") },
		},
		{
			name: "cluster reorder refusal", want: "group_mode", key: '[',
			setup: func(h *home) { h.list.SetGroupMode("repo") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := filterReorderHome(t,
				[3]string{"api-one", "repoA", ""},
				[3]string{"api-two", "repoA", ""},
				[3]string{"infra-one", "repoB", ""})
			tc.setup(h)
			h.list.SetSelectedInstance(0)

			pressKey(h, tc.key)
			require.True(t, h.menu.HasNotice(), "precondition: the key must be refused with a notice")
			require.Contains(t, h.menu.String(), ", to switch")

			pressKey(h, ',')
			require.Equal(t, stateSettings, h.state)
			require.NotNil(t, h.settingsOverlay)
			assert.Equal(t, tc.want, h.settingsOverlay.SelectedRowKey())
		})
	}
}

// The arm lives exactly as long as the advice. A ',' pressed after the notice has gone opens
// the panel where the user left it, not on a row a refusal mentioned a minute ago.
func TestSettingsJumpExpiresWithItsNotice(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')
	require.True(t, h.menu.HasNotice())
	h.Update(hideErrMsg{gen: h.noticeGen}) // the toast's own timer fires

	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey(),
		"an expired notice must not still be steering ','")
}

// A BACKGROUND notice disarms the jump too. This is the path that made scheduleNoticeHide the
// clear site rather than flashNotice: the drift, agent and update notices reach the hint row
// through showMenuNotice directly, and each bumps noticeGen — so the original toast's timer
// mismatches, hideErrMsg skips its clear, and ',' stays pointed at a setting whose advice left
// the screen five seconds ago.
func TestABackgroundNoticeDisarmsTheSettingsJump(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')
	require.True(t, h.menu.HasNotice())
	gen := h.noticeGen

	// A background notice that never passes through flashNotice.
	_ = h.showMenuNotice("⚠ agent heuristics may be stale", ui.NoticeInfo)
	require.NotEqual(t, gen, h.noticeGen, "precondition: the background notice bumped the generation")
	h.Update(hideErrMsg{gen: gen}) // the ORIGINAL timer fires and is ignored as stale

	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey(),
		"a notice the user can no longer see must not still be steering ','")
}

// A second notice replaces the first one's arm rather than stacking on it.
func TestANewNoticeReplacesTheSettingsJump(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')                         // arms session_sort
	_ = h.handleInfoNotice("something else") // an unarmed notice takes the row
	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey())
}

// Every notice that advertises the settings key must go through settingNotice, so the key
// it teaches lands on the setting the notice is about. flashNotice and handleInfoNotice are
// the generic paths and actively DISARM a jump, so such a notice built on one of them is the
// bug this catches — five exist today and they read identically at a glance.
//
// The detector matches a keys.KeySettings reference as well as the old ',' literals, and the
// reference is now the one that matters: the notices stopped spelling ',' when their prose
// moved to keys.LabelOf, so a literal-only detector would have gone on passing over a tree it
// could no longer see into. TestCommaNoticeDetectorSeesBothSpellings pins both arms.
//
// The scope is the call plus, when its text argument is a bare identifier, the literals
// assigned to that identifier in the same function. Both narrower and wider rules were tried
// and rejected against the real tree: a call-only check misses warnMissingProgram, which
// builds its text into a variable across two branches (verified by reverting that site and
// watching the check stay green), while a whole-function check flags handleKeyPress, a
// 400-line switch that legitimately holds both converted ','-notices and a dozen unrelated
// ones.
//
// Dialog copy is out of scope by construction: overCapMessage advertises ',' but calls no
// notice path at all — its ',' is armed by pendingConfirmSettingKey at the confirmAction site
// instead — so it never trips this.
func TestEveryCommaNoticeGoesThroughSettingNotice(t *testing.T) {
	hasCommaLiteral := advertisesSettingsKey
	// assignsCommaLiteral reports whether fn assigns a settings-key-advertising expression to name.
	assignsCommaLiteral := func(fn *ast.FuncDecl, name string) bool {
		found := false
		ast.Inspect(fn.Body, func(x ast.Node) bool {
			assign, ok := x.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != name || i >= len(assign.Rhs) {
					continue
				}
				if hasCommaLiteral(assign.Rhs[i]) {
					found = true
				}
			}
			return true
		})
		return found
	}

	// An explicit walk rather than parser.ParseDir, which staticcheck flags as deprecated
	// (SA1019) since Go 1.25.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoErrorf(t, err, "parsing %s", name)
		files = append(files, f)
	}
	require.NotEmpty(t, files, "the package must have source files to walk")

	var offenders []string
	checked := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(x ast.Node) bool {
				call, ok := x.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "flashNotice" && sel.Sel.Name != "handleInfoNotice" {
					return true
				}
				checked++
				bad := hasCommaLiteral(call)
				if !bad && len(call.Args) > 0 {
					if id, ok := call.Args[0].(*ast.Ident); ok {
						bad = assignsCommaLiteral(fn, id.Name)
					}
				}
				if bad {
					offenders = append(offenders, fmt.Sprintf("%s: %s advertises ',' — use settingNotice",
						fset.Position(call.Pos()), sel.Sel.Name))
				}
				return true
			})
		}
	}
	// Without this the walk could stop finding calls at all and the test would still pass.
	require.Positive(t, checked, "the walk must actually visit the generic notice paths")
	assert.Empty(t, offenders,
		"a notice advertising ',' must use settingNotice so the key lands on its setting")
}

// advertisesSettingsKey reports whether n teaches the settings key, in either
// spelling: the ',' literals the notices used to carry, or a keys.KeySettings
// reference, which is what generated prose looks like.
func advertisesSettingsKey(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		switch v := x.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING &&
				(strings.Contains(v.Value, "press ,") || strings.Contains(v.Value, "(, to")) {
				found = true
			}
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "keys" && v.Sel.Name == "KeySettings" {
				found = true
			}
		}
		return true
	})
	return found
}

// The positive control. A source-scanning guard whose detector has stopped
// matching passes silently and forever, and this one has already outlived one
// spelling of what it looks for — so assert it still fires on both, and does not
// fire on an ordinary notice.
func TestCommaNoticeDetectorSeesBothSpellings(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      bool
	}{
		{"literal", `package p; func f() { g("press , to change the limit") }`, true},
		{"dialog literal", `package p; func f() { g("Create it anyway? (, to change the limit)") }`, true},
		{"registry reference", `package p; func f() { g(keys.LabelOf(keys.KeySettings)) }`, true},
		{"unrelated notice", `package p; func f() { g("session is paused") }`, false},
		{"another registry key", `package p; func f() { g(keys.LabelOf(keys.KeyResume)) }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "x.go", tc.src, 0)
			require.NoError(t, err)
			assert.Equal(t, tc.want, advertisesSettingsKey(f))
		})
	}
}
