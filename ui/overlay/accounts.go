package overlay

import (
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
}

// NewAccountsOverlay creates the account manager over cfg, using state to read and
// toggle per-account rate-limit availability. It seeds a default 80x24 so Render
// works before the first SetSize.
func NewAccountsOverlay(cfg *config.Config, state config.AppState) *AccountsOverlay {
	return &AccountsOverlay{cfg: cfg, state: state, width: 80, height: 24} // floor so Render works pre-SetSize
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
func (o *AccountsOverlay) HandleKeyPress(msg tea.KeyMsg) (closed bool, dirty bool) {
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

func (o *AccountsOverlay) handleListKey(msg tea.KeyMsg) (closed bool, dirty bool) {
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

func (o *AccountsOverlay) handleEditKey(msg tea.KeyMsg) (closed bool, dirty bool) {
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
			o.cfg.ClaudeAccounts[o.editIndex] = a
		}
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

func (o *AccountsOverlay) handleConfirmKey(msg tea.KeyMsg) (closed bool, dirty bool) {
	switch msg.String() {
	case "y", "enter":
		switch o.tab {
		case tabClaude:
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

func (o *AccountsOverlay) handlePreviewKey(msg tea.KeyMsg) (closed bool, dirty bool) {
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
	// columns): a Claude row's "pool:<name>  ⛔ limited" tail needs the extra
	// room, or a full 30-row list wraps every line and blows the row-window
	// budget (see TestAccountsOverlay_ListWindowsRowsOnShortTerminal).
	if w > 84 {
		w = 84
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (o *AccountsOverlay) inner() int { return o.boxWidth() - 4 } // Padding(1,2) → 4 cols

// rowWindow returns the [start, end) slice of account rows to display so the
// list fits a short terminal with the cursor kept in view. Everything outside
// the rows costs a fixed number of lines (border 2 + padding 2 + title/blank 2
// + tabs/blank 2 + trailing blank 1 + two hint lines 2 + the unmatched-repos
// hint 1 = 12), so the remaining height budgets the rows. Mirrors the windowing
// SettingsOverlay applies to its own body.
func (o *AccountsOverlay) rowWindow(n int) (start, end int) {
	const chrome = 12
	budget := o.height - chrome
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
		Width(o.boxWidth())
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
	return style.Render(title + "\n\n" + body)
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

	name, cdir, isDefault := o.cfg.ResolveClaudeAccount(remote, path)
	claude := "inherit ambient env"
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

func (o *AccountsOverlay) renderList() string {
	t := theme.Current()
	var b strings.Builder
	b.WriteString(o.renderTabs() + "\n\n")

	rows := o.rows()
	if len(rows) == 0 {
		b.WriteString(t.DimStyle().Render("No "+o.tabKind()+" accounts — press n to add") + "\n")
	} else {
		start, end := o.rowWindow(len(rows))
		// Catch-all badges depend on order across the whole list (the first
		// rule-less account is "default", later ones "unreachable"), so account
		// for any catch-all scrolled off the top before rendering the window.
		seenCatchAll := false
		for i := 0; i < start; i++ {
			if rows[i].catchAll {
				seenCatchAll = true
			}
		}
		// Fetch the availability snapshot (a full maps.Clone) and the clock once for
		// the whole window rather than per row — the map is indexed per account below.
		avail := o.state.GetAccountAvailability()
		now := time.Now()
		for i := start; i < end; i++ {
			r := rows[i]
			marker := "  "
			if i == o.cursor {
				marker = t.AccentStyle().Render("› ")
			}
			name := r.name
			if name == "" {
				name = t.DangerStyle().Render("(unnamed)")
			}
			dir := r.dir
			if dir == "" {
				dir = t.DimStyle().Render("(inherit ambient env)")
			} else {
				dir = truncTail(dir, 26)
			}
			extra := ""
			if o.tab == tabClaude {
				acct := o.cfg.ClaudeAccounts[i]
				if acct.Pool != "" {
					extra += "  " + t.DimStyle().Render("pool:"+acct.Pool)
				}
				if config.AccountAvailable(avail[acct.Name], now) {
					extra += "  " + t.DimStyle().Render("● available")
				} else {
					extra += "  " + t.DangerStyle().Render("⛔ limited")
				}
			}
			b.WriteString(marker + padRight(name, 12) + " " + padRight(dir, 28) + " " + o.badge(r.catchAll, &seenCatchAll) + extra + "\n")
		}
		if !o.hasCatchAll() {
			b.WriteString(t.DimStyle().Render("unmatched repos inherit the ambient account") + "\n")
		}
	}

	b.WriteString("\n")
	if o.mode == modeConfirmDelete {
		b.WriteString(theme.Current().DangerStyle().Render("Delete '" + o.rows()[o.cursor].name + "'?  y / n"))
	} else {
		// "l limited" toggles per-account availability, which only Claude accounts
		// carry — advertise it only on that tab so the legend never names a dead key.
		hint := "↑/↓ move · tab switch · n new · e edit · d delete"
		if o.tab == tabClaude {
			hint += " · l limited"
		}
		b.WriteString(t.OverlayHintStyle().Render(hint) + "\n")
		b.WriteString(t.OverlayHintStyle().Render("t test routing · esc close"))
	}
	return b.String()
}

func (o *AccountsOverlay) badge(catchAll bool, seen *bool) string {
	t := theme.Current()
	if !catchAll {
		return t.AccentStyle().Render("routed")
	}
	if *seen {
		return t.DangerStyle().Render("catch-all (unreachable)")
	}
	*seen = true
	return t.DimStyle().Render("default")
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

func truncTail(s string, maxLen int) string {
	r := []rune(s)
	if maxLen <= 1 || len(r) <= maxLen {
		return s
	}
	return "…" + string(r[len(r)-maxLen+1:])
}
