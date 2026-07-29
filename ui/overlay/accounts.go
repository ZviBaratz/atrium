package overlay

import (
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
)

type accountsTab int

const (
	tabClaude accountsTab = iota
	tabGH
	tabAgy
	numTabs = iota // count of tabs above; keep last
)

type accountsMode int

const (
	modeList accountsMode = iota
	modeEdit
	modeConfirmDelete
	modePreview
)

// AccountsOverlay is the in-TUI manager for Claude, GitHub, and Antigravity (agy)
// accounts. It holds the same *config.Config the app holds and mutates
// ClaudeAccounts/GHAccounts/AgyAccounts in place; the app persists (SaveConfig)
// whenever HandleKeyPress reports dirty. It also holds the app's config.AppState to
// read/write per-account rate-limit availability (state.json); those setters
// self-persist, so no dirty flag is needed for them.
type AccountsOverlay struct {
	cfg    *config.Config
	state  config.AppState
	tab    accountsTab
	mode   accountsMode
	cursor int

	width, height int

	lastErr string

	form      *accountForm
	editIndex int // -1 = new (append); >=0 = replace at index

	previewInputs []textinput.Model // [remote, path]
	previewFocus  int

	// logins caches which login each Claude config DIR holds, keyed by
	// NormalizedConfigDir and filled when the overlay opens (see loadIdentities),
	// because Render runs on every keystroke and must not do IO.
	//
	// What is cached is the READ, never a per-row verdict: this panel reorders (J/K),
	// edits and deletes accounts in place, so a snapshot parallel by index to
	// cfg.ClaudeAccounts would name another account's login the moment any of those
	// ran — the exact wrong-login failure the feature exists to surface. Directories
	// are what a read is about, and they survive every one of those mutations.
	//
	// Nil when nothing was read, which renders as if the feature were off.
	logins map[string]acctLogin
	// readIdentity is retained so commit() can fill in a dir the roster did not name
	// when the panel opened. It is a keypress, not a render, so it may do IO.
	readIdentity config.IdentityReadFunc
}

// NewAccountsOverlay creates the account manager over cfg, using state to read and
// toggle per-account rate-limit availability. It seeds a default 80x24 so Render
// works before the first SetSize.
func NewAccountsOverlay(cfg *config.Config, state config.AppState) *AccountsOverlay {
	o := &AccountsOverlay{cfg: cfg, state: state, width: 80, height: 24} // floor so Render works pre-SetSize
	// Read the accounts' real logins now, while the user is opening the panel, so
	// every later render is pure. Opening is also exactly when the snapshot should
	// be taken: it is the moment the user asked what their accounts are.
	o.loadIdentities(config.ReadAccountIdentity)
	return o
}

// SetSize records the terminal dimensions; the overlay caps its own box width and
// windows its rows to fit a short terminal.
func (o *AccountsOverlay) SetSize(w, h int) { o.width, o.height = w, h }

// test-only accessors
func (o *AccountsOverlay) selectTab(t accountsTab) { o.tab = t; o.clampCursor() }
func (o *AccountsOverlay) cursorIndex() int        { return o.cursor }

type acctRow struct {
	name, dir string
	catchAll  bool
}

func (o *AccountsOverlay) rows() []acctRow {
	var rows []acctRow
	switch o.tab {
	case tabClaude:
		for _, a := range o.cfg.ClaudeAccounts {
			rows = append(rows, acctRow{a.Name, a.ConfigDir, a.IsCatchAll()})
		}
	case tabGH:
		for _, a := range o.cfg.GHAccounts {
			rows = append(rows, acctRow{a.Name, a.ConfigDir, a.IsCatchAll()})
		}
	case tabAgy:
		for _, a := range o.cfg.AgyAccounts {
			rows = append(rows, acctRow{a.Name, a.ConfigDir, a.IsCatchAll()})
		}
	}
	return rows
}

// tabKind is the human label for the active tab, used in headings and empty-state
// hints ("No <kind> accounts").
func (o *AccountsOverlay) tabKind() string {
	switch o.tab {
	case tabGH:
		return "GitHub"
	case tabAgy:
		return "Antigravity"
	default:
		return "Claude"
	}
}

func (o *AccountsOverlay) activeLen() int { return len(o.rows()) }

func (o *AccountsOverlay) clampCursor() {
	n := o.activeLen()
	if n == 0 {
		o.cursor = 0
		return
	}
	if o.cursor >= n {
		o.cursor = n - 1
	}
	if o.cursor < 0 {
		o.cursor = 0
	}
}

// moveAccount swaps the cursored account with its neighbour delta slots away and
// moves the cursor with it, reporting whether the config changed. Account order is
// routing precedence — first-match wins and the first rule-less account is the
// catch-all (config.matchRouteIndex) — so this is a routing edit, not a cosmetic
// one, and the caller persists it. A move off either end is a no-op that reports
// no change, so a boundary press never triggers a config write.
func (o *AccountsOverlay) moveAccount(delta int) bool {
	i, j := o.cursor, o.cursor+delta
	if i < 0 || j < 0 || i >= o.activeLen() || j >= o.activeLen() {
		return false
	}
	switch o.tab {
	case tabClaude:
		a := o.cfg.ClaudeAccounts
		a[i], a[j] = a[j], a[i]
	case tabAgy:
		a := o.cfg.AgyAccounts
		a[i], a[j] = a[j], a[i]
	default: // tabGH
		a := o.cfg.GHAccounts
		a[i], a[j] = a[j], a[i]
	}
	o.cursor = j
	return true
}

// HandleKeyPress routes a key to the active mode and reports whether the overlay
// should close and whether the config was mutated (the app persists on dirty).
func (o *AccountsOverlay) HandleKeyPress(msg tea.KeyPressMsg) (closed bool, dirty bool) {
	switch o.mode {
	case modeEdit:
		return o.handleEditKey(msg)
	case modeConfirmDelete:
		return o.handleConfirmKey(msg)
	case modePreview:
		return o.handlePreviewKey(msg)
	default:
		return o.handleListKey(msg)
	}
}

// HandlePaste inserts pasted text into the record form, the only mode that takes
// text. The list, the delete confirmation and the preview are keyboard surfaces:
// a paste there is inert rather than being read as the keys its characters spell —
// "esc" in the list would close the panel, and in the confirmation the wrong
// character would answer a delete prompt.
func (o *AccountsOverlay) HandlePaste(msg tea.PasteMsg) {
	if o.mode == modeEdit && o.form != nil {
		o.form.HandlePaste(msg)
	}
}

func (o *AccountsOverlay) handleListKey(msg tea.KeyPressMsg) (closed bool, dirty bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true, false
	case "up", "k":
		if o.cursor > 0 {
			o.cursor--
		}
	case "down", "j":
		if o.cursor < o.activeLen()-1 {
			o.cursor++
		}
	case "K", "shift+up":
		return false, o.moveAccount(-1)
	case "J", "shift+down":
		return false, o.moveAccount(+1)
	case "tab", "right":
		o.tab = (o.tab + 1) % numTabs
		o.clampCursor()
		o.lastErr = ""
	case "shift+tab", "left":
		o.tab = (o.tab + numTabs - 1) % numTabs
		o.clampCursor()
		o.lastErr = ""
	case "n":
		o.openForm(-1)
	case "e", "enter":
		if o.activeLen() > 0 {
			o.openForm(o.cursor)
		}
	case "d":
		if o.activeLen() > 0 {
			o.mode = modeConfirmDelete
		}
	case "l":
		if o.tab == tabClaude && o.activeLen() > 0 {
			name := o.cfg.ClaudeAccounts[o.cursor].Name
			if o.state.GetAccountAvailability()[name].Limited {
				_ = o.state.ClearAccountLimited(name)
			} else {
				_ = o.state.SetAccountLimited(name, "") // indefinite; reset-time entry is a future polish
			}
		}
	case "t":
		o.previewInputs = []textinput.Model{newFieldInput("remote URL (optional)"), newFieldInput("path (optional)")}
		o.previewInputs[0].Focus()
		o.previewFocus = 0
		o.mode = modePreview
	}
	return false, false
}

func (o *AccountsOverlay) showToken() bool { return o.tab == tabGH }

func (o *AccountsOverlay) openForm(index int) {
	o.editIndex = index
	o.lastErr = ""
	switch {
	case index < 0:
		// A new account: the Pool field belongs only to the Claude tab, the Token
		// field only to GH. The Antigravity tab gets neither.
		o.form = newAccountForm(o.showToken(), o.tab == tabClaude, "", "", "", "", "", "")
	case o.tab == tabClaude:
		a := o.cfg.ClaudeAccounts[index]
		o.form = newAccountForm(false, true, a.Name, a.ConfigDir,
			strings.Join(a.RemoteMatches, ", "), strings.Join(a.PathMatches, ", "), "", a.Pool)
	case o.tab == tabAgy:
		a := o.cfg.AgyAccounts[index]
		o.form = newAccountForm(false, false, a.Name, a.ConfigDir,
			strings.Join(a.RemoteMatches, ", "), strings.Join(a.PathMatches, ", "), "", "")
	default: // tabGH
		a := o.cfg.GHAccounts[index]
		o.form = newAccountForm(true, false, a.Name, a.ConfigDir,
			strings.Join(a.RemoteMatches, ", "), strings.Join(a.PathMatches, ", "),
			strings.Join(a.TokenEnv, ", "), "")
	}
	o.mode = modeEdit
}

func (o *AccountsOverlay) handleEditKey(msg tea.KeyPressMsg) (closed bool, dirty bool) {
	if !o.form.HandleKeyPress(msg) {
		return false, false
	}
	if o.form.Canceled() {
		o.form = nil
		o.mode = modeList
		return false, false
	}
	// submitted → validate then commit
	if err := o.validate(); err != "" {
		o.lastErr = err
		o.form.submitted = false // stay in edit
		return false, false
	}
	o.commit()
	o.form = nil
	o.mode = modeList
	o.lastErr = ""
	return false, true
}

// carryAvailability moves a Claude account's rate-limit flag (the `l` toggle) with
// it across a rename. State keys availability by account NAME, and this commit is
// the only place that still knows the old one — without the carry, renaming an
// account silently clears its flag and the panel reports an exhausted login as
// available (#470). Not a config-anchored heal like the session labels: nothing else
// records which account an availability entry belonged to.
//
// The renamed account is the sole authority on its new name. An entry may already
// be sitting there — validate() only rejects a name a LIVE account holds, so a
// rename can land on one of the orphans `atrium doctor` reports (a login deleted
// while limited, a hand-edited state.json) — and such an entry is stale by
// construction. It is overwritten when there is a flag to carry and cleared when
// there is not, so a rename can neither lose the account's real reset time to an
// orphan's nor resurrect an orphan's exhaustion onto a login that has none.
//
// Write errors are dropped, as everywhere the `l` toggle writes this flag: it is a
// hint the next keypress can correct, not something worth failing a rename over.
func (o *AccountsOverlay) carryAvailability(oldName, newName string) {
	if oldName == newName {
		return
	}
	avail := o.state.GetAccountAvailability()
	from, carry := avail[oldName]
	if carry && from.Limited {
		_ = o.state.SetAccountLimited(newName, from.Until)
	} else if _, stale := avail[newName]; stale {
		_ = o.state.ClearAccountLimited(newName)
	}
	if carry {
		_ = o.state.ClearAccountLimited(oldName)
	}
}

// validate rejects an empty or duplicate (within the active tab) name.
func (o *AccountsOverlay) validate() string {
	name := o.form.Name()
	if name == "" {
		return "name is required"
	}
	for i, r := range o.rows() {
		if i != o.editIndex && r.name == name {
			return "an account named '" + name + "' already exists"
		}
	}
	return ""
}

func (o *AccountsOverlay) commit() {
	switch o.tab {
	case tabClaude:
		a := config.ClaudeAccount{
			Name: o.form.Name(), ConfigDir: o.form.ConfigDir(),
			RemoteMatches: o.form.RemoteMatches(), PathMatches: o.form.PathMatches(),
			Pool: o.form.Pool(),
		}
		if o.editIndex < 0 {
			o.cfg.ClaudeAccounts = append(o.cfg.ClaudeAccounts, a)
		} else {
			// This form has no expect_account field, and the assignment below
			// replaces the whole struct — so carry the existing pin across or a
			// rename, a pool change, any edit at all would silently drop it. The
			// account would go back to unverified exactly when someone was paying
			// it enough attention to edit it.
			a.ExpectAccount = o.cfg.ClaudeAccounts[o.editIndex].ExpectAccount
			o.carryAvailability(o.cfg.ClaudeAccounts[o.editIndex].Name, a.Name)
			o.cfg.ClaudeAccounts[o.editIndex] = a
		}
		// An add or an edit is the one mutation that can name a dir nobody held when
		// the panel opened; without this it would stay blank until the panel is
		// reopened. Safe to read here — commit runs on a keypress, not a render.
		o.cacheLogins()
	case tabAgy:
		a := config.AgyAccount{
			Name: o.form.Name(), ConfigDir: o.form.ConfigDir(),
			RemoteMatches: o.form.RemoteMatches(), PathMatches: o.form.PathMatches(),
		}
		if o.editIndex < 0 {
			o.cfg.AgyAccounts = append(o.cfg.AgyAccounts, a)
		} else {
			o.cfg.AgyAccounts[o.editIndex] = a
		}
	default: // tabGH
		a := config.GHAccount{
			Name: o.form.Name(), ConfigDir: o.form.ConfigDir(),
			RemoteMatches: o.form.RemoteMatches(), PathMatches: o.form.PathMatches(),
			TokenEnv: o.form.TokenEnv(),
		}
		if o.editIndex < 0 {
			o.cfg.GHAccounts = append(o.cfg.GHAccounts, a)
		} else {
			o.cfg.GHAccounts[o.editIndex] = a
		}
	}
}

func (o *AccountsOverlay) handleConfirmKey(msg tea.KeyPressMsg) (closed bool, dirty bool) {
	switch msg.String() {
	case "y", "enter":
		switch o.tab {
		case tabClaude:
			// Drop the deleted account's rate-limit flag: state keys it by NAME, so a
			// leftover entry would silently apply to a future account that reuses the
			// name (and would otherwise linger as an orphan `atrium doctor` reports).
			_ = o.state.ClearAccountLimited(o.cfg.ClaudeAccounts[o.cursor].Name)
			o.cfg.ClaudeAccounts = append(o.cfg.ClaudeAccounts[:o.cursor], o.cfg.ClaudeAccounts[o.cursor+1:]...)
		case tabAgy:
			o.cfg.AgyAccounts = append(o.cfg.AgyAccounts[:o.cursor], o.cfg.AgyAccounts[o.cursor+1:]...)
		default: // tabGH
			o.cfg.GHAccounts = append(o.cfg.GHAccounts[:o.cursor], o.cfg.GHAccounts[o.cursor+1:]...)
		}
		o.clampCursor()
		o.mode = modeList
		return false, true
	case "n", "esc", "ctrl+c":
		o.mode = modeList
	}
	return false, false
}

func (o *AccountsOverlay) handlePreviewKey(msg tea.KeyPressMsg) (closed bool, dirty bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		o.previewInputs = nil
		o.mode = modeList
	case "tab", "shift+tab":
		o.previewFocus = (o.previewFocus + 1) % 2
		for i := range o.previewInputs {
			if i == o.previewFocus {
				o.previewInputs[i].Focus()
			} else {
				o.previewInputs[i].Blur()
			}
		}
	default:
		o.previewInputs[o.previewFocus], _ = o.previewInputs[o.previewFocus].Update(msg)
	}
	return false, false
}

func (o *AccountsOverlay) boxWidth() int {
	w := o.width - 2
	// Capped higher than SettingsOverlay's 64 (which has no pool/availability
	// columns): a Claude row's `pool:<name>` and availability tail needs the extra
	// room. The cap is a comfort measure, not a correctness one — it was never
	// enough for the worst case (#478's row wanted 96 against this cap's inner 80,
	// and a pool name is free text, so no cap can be), so what actually keeps rows
	// inside the box is the flexible dir column in accounts_layout.go and Render's
	// line clip. What the cap still buys is that a wide terminal does not stretch
	// this panel across the whole screen.
	if w > 84 {
		w = 84
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (o *AccountsOverlay) inner() int { return o.boxWidth() - 4 } // Padding(1,2) → 4 cols

// listNotes are the conditional footer lines renderList prints beneath the account
// rows. Each one that renders must also be charged a row of rowWindow's height
// budget: a note printed but uncounted makes the box one line taller than the
// terminal it was measured against (9b25662, #499). The two agree here by
// construction rather than by inspection — both notes are computed ONCE per frame,
// in renderList, and handed to rowWindow, instead of each function deciding for
// itself and having two chances to disagree (#479).
type listNotes struct {
	splitPool string // "" when no pool is split, or off the Claude tab
	identity  string // "" when there is nothing to warn about, or off the Claude tab
}

// listNotes builds this frame's notes. Claude-tab only, because both the pool and
// the identity features are: this is the single gate renderList and rowWindow now
// share, where there used to be one apiece.
func (o *AccountsOverlay) listNotes() listNotes {
	if o.tab != tabClaude {
		return listNotes{}
	}
	return listNotes{
		splitPool: splitPoolNote(splitPools(o.cfg.ClaudeAccounts), o.inner()),
		identity:  o.identityNote(o.inner()),
	}
}

// lines is how many rows of the height budget these notes claim. splitPoolNote
// returns "" exactly when there are no split pools, so an empty field means the
// note cannot render, never merely that it is terse.
func (n listNotes) lines() int {
	count := 0
	if n.splitPool != "" {
		count++
	}
	if n.identity != "" {
		count++
	}
	return count
}

// rowWindow returns the [start, end) slice of account rows to display so the
// list fits a short terminal with the cursor kept in view. Everything outside
// the rows costs a fixed number of lines (border 2 + padding 2 + title/blank 2
// + tabs/blank 2 + trailing blank 1 + two hint lines 2 + the unmatched-repos
// hint 1 = 12), so the remaining height budgets the rows. The unmatched-repos
// hint is itself conditional but predates this feature, so it keeps its
// unconditional allowance for continuity — a pool-free config must keep
// scrolling exactly as it always has. The split-pool and identity notes are
// newer, so THEIR allowance is conditional on the note actually rendering:
// charging every config a row for a note that only some configs print would cost
// a pool-free config a visible row it never used to lose. Which of them render is
// not decided here — notes comes from renderList, which is also what prints them.
// Mirrors the windowing SettingsOverlay applies to its own body.
func (o *AccountsOverlay) rowWindow(n int, notes listNotes) (start, end int) {
	budget := o.height - (12 + notes.lines())
	if budget < 3 {
		budget = 3
	}
	if n <= budget {
		return 0, n
	}
	start = 0
	if o.cursor >= budget {
		start = o.cursor - budget + 1
	}
	end = start + budget
	if end > n {
		end = n
	}
	return start, end
}

// Render draws the overlay's current mode (list, edit, delete-confirm, or preview)
// as a centered bordered box.
func (o *AccountsOverlay) Render() string {
	t := theme.Current()
	style := lipgloss.NewStyle().
		Border(t.Borders.Style).
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		// +2 for the left/right border — v2 counts it inside Width. See theme.Panel.
		Width(o.boxWidth() + 2)
	var body string
	switch o.mode {
	case modeEdit:
		body = o.renderEdit()
	case modePreview:
		body = o.renderPreview()
	default:
		body = o.renderList()
	}
	title := t.OverlayTitleStyle().Render("Accounts")
	return style.Render(clipLines(title+"\n\n"+body, o.inner()))
}

// clipLines truncates every line of s to width cells. It is the overlay's last rung
// and the only unconditional one: the row layout sizes its flexible column so rows
// fit (accounts_layout.go), and each note picks a wording that fits (fitOneOf), but
// several strings in this box have no ladder at all — a delete-confirm carrying an
// arbitrary account name, the legend's longest line on a narrow terminal, the
// preview's resolved dirs and token lists. Any one of them wrapping costs a line the
// height budget never counted, which is #478's actual failure mode rather than the
// cosmetic dangling chip. So the guarantee is taken here, once, for every mode.
//
// Same shape and helper as the create-form overlay's fitOverlay
// (textInput_render.go) — truncate.StringWithTail is ANSI-aware, so a styled line
// keeps its escape sequences intact.
func clipLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			lines[i] = truncate.StringWithTail(l, uint(width), "…")
		}
	}
	return strings.Join(lines, "\n")
}

func (o *AccountsOverlay) renderEdit() string {
	t := theme.Current()
	kind := o.tabKind()
	verb := "New"
	if o.editIndex >= 0 {
		verb = "Edit"
	}
	var b strings.Builder
	b.WriteString(t.AccentStyle().Render(verb+" "+kind+" account") + "\n\n")
	b.WriteString(o.form.Render(o.inner()) + "\n")
	if o.lastErr != "" {
		b.WriteString(t.DangerStyle().Render(o.lastErr) + "\n")
	}
	b.WriteString(t.OverlayHintStyle().Render("tab/⇧tab field · ⌃o browse dir · ↵ save · esc cancel"))
	return b.String()
}

func (o *AccountsOverlay) renderPreview() string {
	t := theme.Current()
	remote := strings.TrimSpace(o.previewInputs[0].Value())
	path := strings.TrimSpace(o.previewInputs[1].Value())

	// ResolveClaudePool answers the same routing question as ResolveClaudeAccount
	// but names the WHOLE pool the matched account belongs to. Fewer than two
	// members — no pool, a singleton pool, an ungrouped account, zero accounts,
	// or the synthetic ("default", [{Name:"default"}], true) sentinel — means
	// the pool feature is dormant here, so the preview falls back to today's
	// exact ResolveClaudeAccount rendering, unchanged.
	pool, members, _ := o.cfg.ResolveClaudePool(remote, path)
	claude := "inherit ambient env"
	poolBlock := ""
	// claudeDir is the config dir this routing actually lands on, carried out of
	// both branches so the login line below can name who a session created here
	// would bill. Empty means no dir is injected, which has no login to report.
	claudeDir := ""
	if len(members) < 2 {
		name, cdir, isDefault := o.cfg.ResolveClaudeAccount(remote, path)
		claudeDir = cdir
		switch {
		case name == "":
			// 0 accounts configured.
		case cdir != "":
			claude = name + " (" + cdir + ")"
		case !isDefault:
			// A rule matched a named account that has no config dir.
			claude = name + " (inherit ambient env)"
		case name != "default":
			// A real catch-all with an empty dir (non-sentinel name) — show its name.
			claude = name + " (inherit ambient env)"
		default:
			// Either the synthetic sentinel ("default", "", true) that
			// ResolveClaudeAccount returns when nothing matches and there's no
			// catch-all, or a real catch-all account literally named "default"
			// with an empty config dir — the two are byte-identical in this
			// return value and genuinely indistinguishable here, an inherent
			// limitation of ResolveClaudeAccount's (name, dir, isDefault) shape.
			// Rendering them the same is cosmetic only: both mean no
			// CLAUDE_CONFIG_DIR is injected. claude keeps its initial value.
		}
	} else {
		// A real rotation pool: resolve which member creating a session here
		// would actually use (availability-aware, never the raw first match)
		// and render the pool block beneath the Claude line.
		claude, poolBlock, claudeDir = o.renderPoolDecision(pool, members, time.Now())
	}

	ghDir, ghTok := o.cfg.ResolveGHAccount(remote, path)
	gh := "inherit ambient env"
	if ghDir != "" {
		gh = ghDir
	}
	// A matched GH account can set TokenEnv without a config dir (the token is
	// injected into the ambient gh account); surface it either way rather than
	// dropping it when the dir is empty.
	if len(ghTok) > 0 {
		gh += " [" + strings.Join(ghTok, ", ") + "]"
	}

	// Antigravity has no synthetic "default" sentinel (ResolveAgyAccount returns
	// ("","",false) when nothing matches and there's no catch-all), so the three
	// cases map cleanly: no account, a routed/catch-all dir, or a named account
	// with no dir (bwrap isolation off — inherits the ambient config).
	agyName, agyDir, _ := o.cfg.ResolveAgyAccount(remote, path)
	agy := "inherit ambient env"
	switch {
	case agyName == "":
	case agyDir != "":
		agy = agyName + " (" + agyDir + ")"
	default:
		agy = agyName + " (inherit ambient env)"
	}

	var b strings.Builder
	b.WriteString(t.AccentStyle().Render("Test routing") + "\n\n")
	b.WriteString(t.DimStyle().Render("Remote URL") + "\n" + o.previewInputs[0].View() + "\n")
	b.WriteString(t.DimStyle().Render("Path") + "\n" + o.previewInputs[1].View() + "\n\n")
	b.WriteString(t.DimStyle().Render("Claude → ") + claude + "\n")
	if line := o.previewIdentityLine(claudeDir, o.inner()-previewIndentWidth); line != "" {
		b.WriteString(previewIndent + t.DimStyle().Render(line) + "\n")
	}
	b.WriteString(poolBlock)
	b.WriteString(t.DimStyle().Render("GitHub → ") + gh + "\n")
	b.WriteString(t.DimStyle().Render("Antigravity → ") + agy + "\n\n")
	b.WriteString(t.OverlayHintStyle().Render("tab switch field · esc back"))
	return b.String()
}

func (o *AccountsOverlay) renderTabs() string {
	t := theme.Current()
	label := func(tab accountsTab, name string) string {
		if o.tab == tab {
			return t.AccentStyle().Render("‹" + name + "›")
		}
		return t.DimStyle().Render(name)
	}
	return label(tabClaude, "Claude") + "  " + label(tabGH, "GitHub") + "  " + label(tabAgy, "Antigravity")
}

// gutterCellWidth is the display width of one poolGutter cell ("┌ ", "│ ", "└ ",
// or the blank "  "). dirTruncWidthBase/dirPadWidthBase are the dir column's
// truncTail/padRight widths at their widest: no gutter, and a tail short enough
// to leave the column whole. They are a ceiling, not the rendered width — dirWidths
// (accounts_layout.go) is what renderList actually calls, and it takes the gutter
// AND the widest tail in the list out of this base, clamping at dirMinWidth. So the
// dir is the row's one flexible column: the gutter and the badge/chip tail come OUT
// of it rather than adding to the row, which is what keeps the row inside the box.
const (
	gutterCellWidth   = 2
	dirTruncWidthBase = 26
	dirPadWidthBase   = 28
)

func (o *AccountsOverlay) renderList() string {
	t := theme.Current()
	var b strings.Builder
	b.WriteString(o.renderTabs() + "\n\n")

	rows := o.rows()
	if len(rows) == 0 {
		b.WriteString(t.DimStyle().Render("No "+o.tabKind()+" accounts — press n to add") + "\n")
	} else {
		// Built here, inside the branch that prints them, and handed to rowWindow: one
		// decision per frame, taken where the printing happens. An empty tab reaches
		// neither, so no note is ever counted for a frame that prints none.
		notes := o.listNotes()
		start, end := o.rowWindow(len(rows), notes)
		// Fetch the availability snapshot (a full maps.Clone) and the clock once for
		// the whole list rather than per row — the map is indexed per account below.
		avail := o.state.GetAccountAvailability()
		now := time.Now()
		// Both of these are computed over the WHOLE slice and indexed by absolute
		// config index, never re-sliced to the window: a pool run can straddle the
		// window boundary, and a catch-all badge depends on rows that scrolled off the
		// top (rowTails threads that ordering, which is why there is no longer a
		// separate pre-scan here).
		var gut []string
		if o.tab == tabClaude {
			gut = poolGutter(o.cfg.ClaudeAccounts)
		}
		tails, tailMax := o.rowTails(rows, avail, now)
		// The gutter's columns come out of the dir column rather than adding to the
		// row (#475), and so does everything the tail needs beyond its usual share
		// (#478) — see accounts_layout.go.
		gutterWidth := 0
		if gut != nil {
			gutterWidth = gutterCellWidth
		}
		dirTruncWidth, dirPadWidth := dirWidths(o.inner(), gutterWidth, tailMax)
		for i := start; i < end; i++ {
			r := rows[i]
			marker := "  "
			if i == o.cursor {
				marker = t.AccentStyle().Render("› ")
			}
			gutter := ""
			if gut != nil {
				gutter = t.DimStyle().Render(gut[i])
			}
			// Bound BEFORE styling, in both columns: clipWidth and truncTail slice
			// runes, so clipping an already-styled string would cut its escape
			// sequence in half and bleed colour across the rest of the row.
			name := clipWidth(r.name, nameWidth)
			if r.name == "" {
				name = t.DangerStyle().Render(clipWidth("(unnamed)", nameWidth))
			}
			// A real path keeps its leaf (truncTail); the placeholder is a phrase, so
			// it keeps its head (clipWidth) — the same split accounts_identity.go makes
			// between paths and sentences.
			dir := truncTail(r.dir, dirTruncWidth)
			if r.dir == "" {
				dir = t.DimStyle().Render(clipWidth("(inherit ambient env)", dirTruncWidth))
			}
			b.WriteString(marker + gutter + padRight(name, nameWidth) + " " + padRight(dir, dirPadWidth) + " " + tails[i].rendered + "\n")
		}
		if !o.hasCatchAll() {
			b.WriteString(t.DimStyle().Render("unmatched repos inherit the ambient account") + "\n")
		}
		if notes.splitPool != "" {
			b.WriteString(t.DimStyle().Render(notes.splitPool) + "\n")
		}
		// Danger, not dim: unlike the notes above it, this one says something
		// is wrong right now rather than suggesting a tidier arrangement.
		if notes.identity != "" {
			b.WriteString(t.DangerStyle().Render(notes.identity) + "\n")
		}
	}

	b.WriteString("\n")
	if o.mode == modeConfirmDelete {
		b.WriteString(theme.Current().DangerStyle().Render("Delete '" + o.rows()[o.cursor].name + "'?  y / n"))
	} else {
		hint, extras := o.legendHints()
		b.WriteString(t.OverlayHintStyle().Render(hint) + "\n")
		b.WriteString(t.OverlayHintStyle().Render(extras))
	}
	return b.String()
}

// legendHints returns the list legend's two hint lines, unstyled. J/K reorder
// only does something with a second row to swap against, and "l limited" only
// toggles availability for Claude accounts — advertise each only where it's
// live, so the legend never names a dead key. l limited lives on line 2 (not
// alongside J/K reorder on line 1) purely to keep line 1 under the box's inner
// width at an 80-column terminal; it isn't grouped there by key purpose.
// Returned unstyled (not yet passed through OverlayHintStyle) so callers —
// renderList and tests — can measure the raw text against o.inner() directly;
// once lipgloss pads a rendered line to the box's full width, every line reads
// the same width whether or not it wrapped, so that measurement can't be taken
// after rendering.
func (o *AccountsOverlay) legendHints() (hint, extras string) {
	hint = "↑/↓ select"
	if o.activeLen() > 1 {
		hint += " · J/K reorder"
	}
	hint += " · tab switch · n new · e edit · d delete"
	extras = "t test routing · esc close"
	if o.tab == tabClaude {
		// The mark, not a literal, and named here because it is now the only thing a
		// row says about availability — the words moved out of the rows to buy back
		// the columns #478 needed, so the legend is where they survive.
		extras = "l limited " + theme.Current().Glyphs.AcctLimited + " · " + extras
	}
	return hint, extras
}

func (o *AccountsOverlay) hasCatchAll() bool {
	for _, r := range o.rows() {
		if r.catchAll {
			return true
		}
	}
	return false
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// truncTail bounds s to at most maxLen display cells, keeping its TAIL and marking
// the cut with a leading ellipsis — the right end for a path, whose leaf is the
// informative part. clipWidth is its mirror for sentences.
//
// It counts cells, not runes: a rune budget lets a path of wide characters
// (~/项目/…) pass a 26-rune cap at up to 52 cells, and the dir column is the row's
// width guarantee (accounts_layout.go), which a rune count would quietly void. For
// the same reason the degenerate cases return a bounded string rather than s — a
// guarantee whose edge case hands back the unbounded input is not one.
func truncTail(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > maxLen {
		r = r[1:]
	}
	return "…" + string(r)
}
