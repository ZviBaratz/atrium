package app

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const GlobalInstanceLimit = 10

// Run is the main entrypoint into the application.
func Run(ctx context.Context, program string, autoYes bool) error {
	p := tea.NewProgram(
		newHome(ctx, program, autoYes),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
	)
	_, err := p.Run()
	return err
}

type state int

const (
	stateDefault state = iota
	// stateNew is the state when the user is creating a new instance.
	stateNew
	// statePrompt is the state when the user is entering a prompt.
	statePrompt
	// stateHelp is the state when a help screen is displayed.
	stateHelp
	// stateConfirm is the state when a confirmation modal is displayed.
	stateConfirm
	// stateRename is the state when the user is editing a session's display label.
	stateRename
)

type home struct {
	ctx context.Context

	// -- Storage and Configuration --

	program string
	autoYes bool

	// storage is the interface for saving/loading data to/from the app's state
	storage *session.Storage
	// lostStrikes counts consecutive ticks each instance has been seen with a dead
	// tmux session, debouncing auto-recovery to Paused (see recoverLostInstances).
	lostStrikes map[*session.Instance]int
	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

	// -- State --

	// state is the current discrete state of the application
	state state
	// newInstanceFinalizer is called when the state is stateNew and then you press enter.
	// It registers the new instance in the list after the instance has been started.
	newInstanceFinalizer func()

	// newInstance is the session currently being created via the inline `n` flow (named in
	// stateNew). AddInstance may insert it mid-list (under its repo group) and a background
	// instanceStartedMsg may move the selection, so the naming step targets this stable
	// reference rather than GetSelectedInstance / the last list item. The `N` flow does not
	// use it — that session is created only on form submit.
	newInstance *session.Instance

	// newSessionPath is the target repo path for the session currently being created.
	// It defaults to the contextual repo (the highlighted session's repo, else cwd) and
	// can be re-pointed via the directory picker in the new-session overlay. It scopes the
	// branch search and is applied to the instance before Start.
	newSessionPath string

	// keySent is used to manage underlining menu items
	keySent bool

	// welcomeChecked guards the one-time first-launch welcome so it is only
	// attempted once per process (its seen-bit handles persistence across runs).
	welcomeChecked bool

	// instanceStarting is true while a background instance start is in progress.
	// Prevents double-submission and guards against interacting with a not-yet-started instance.
	instanceStarting bool
	// startingInstance holds a reference to the instance being started in the background.
	startingInstance *session.Instance

	// windowWidth/windowHeight cache the last terminal size so the layout can be
	// recomputed off a synthesized size event — e.g. when an error appears or
	// clears and the panes must give up or reclaim the error box's row.
	windowWidth, windowHeight int

	// -- UI Components --

	// list displays the list of instances
	list *ui.List
	// menu displays the bottom menu
	menu *ui.Menu
	// tabbedWindow displays the tabbed window with preview and diff panes
	tabbedWindow *ui.TabbedWindow
	// errBox displays error messages
	errBox *ui.ErrBox
	// global spinner instance. we plumb this down to where it's needed
	spinner spinner.Model
	// textInputOverlay handles text input with state
	textInputOverlay *overlay.TextInputOverlay
	// textOverlay displays text information
	textOverlay *overlay.TextOverlay
	// confirmationOverlay displays confirmation modals
	confirmationOverlay *overlay.ConfirmationOverlay
	// pendingConfirmAction is the action to run if the confirmation overlay is
	// confirmed. It is executed on the main loop and its returned message is fed
	// back through Update so errors surface in the error box.
	pendingConfirmAction tea.Cmd
	// renameOverlay handles editing a session's display label
	renameOverlay *overlay.RenameOverlay
	// renameTarget is the instance the rename overlay was opened for. It is captured
	// when the overlay opens so the new label lands on the right session even if the
	// list selection moves while the overlay is open (e.g. during async auto-naming).
	renameTarget *session.Instance
	// generatingName guards against launching a second auto-name request while one
	// is already in flight, and drives the "Generating name…" hint-bar state.
	generatingName bool
}

func newHome(ctx context.Context, program string, autoYes bool) *home {
	// Load application config
	appConfig := config.LoadConfig()

	// Activate the configured UI theme before any component is constructed, so
	// theme.Current() is correct everywhere it's read. Set once, never mutated.
	theme.Set(appConfig.Theme)

	// Load application state
	appState := config.LoadState()

	// Initialize storage
	storage, err := session.NewStorage(appState)
	if err != nil {
		fmt.Printf("Failed to initialize storage: %v\n", err)
		os.Exit(1)
	}

	h := &home{
		ctx: ctx,
		spinner: spinner.New(spinner.WithSpinner(spinner.Spinner{
			Frames: theme.Current().Glyphs.SpinnerFrames,
			FPS:    theme.Current().Glyphs.SpinnerFPS,
		})),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:       ui.NewErrBox(),
		storage:      storage,
		lostStrikes:  make(map[*session.Instance]int),
		appConfig:    appConfig,
		program:      program,
		autoYes:      autoYes,
		state:        stateDefault,
		appState:     appState,
	}
	h.list = ui.NewList(&h.spinner, autoYes)

	// Load saved instances
	instances, err := storage.LoadInstances()
	if err != nil {
		fmt.Printf("Failed to load instances: %v\n", err)
		os.Exit(1)
	}

	// Add loaded instances to the list
	for _, instance := range instances {
		// Call the finalizer immediately.
		h.list.AddInstance(instance)()
		if autoYes {
			instance.AutoYes = true
		}
	}
	// Restore folded groups only after every instance is loaded — AddInstance auto-expands the
	// group it inserts into, so applying persisted folds earlier would be undone by the loop.
	h.list.SetCollapsedRepos(appState.GetCollapsedRepos())

	return h
}

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// List takes 30% of width, preview takes 70%
	listWidth := int(float32(msg.Width) * 0.3)
	tabsWidth := msg.Width - listWidth

	m.windowWidth, m.windowHeight = msg.Width, msg.Height

	// The menu always takes one row at the bottom; the error box takes a row only
	// while an error is showing. With no error the help bar sits flush on the last
	// row. When an error appears the panes give up a row for it (and reclaim it once
	// the error clears via recomputeLayout), so the composed frame is always exactly
	// msg.Height tall and never floats in a centered band.
	menuHeight := 1
	errHeight := 0
	if m.errBox.HasError() {
		errHeight = 1
	}
	contentHeight := max(1, msg.Height-menuHeight-errHeight)
	m.errBox.SetSize(int(float32(msg.Width)*0.9), errHeight)

	m.tabbedWindow.SetSize(tabsWidth, contentHeight)
	m.list.SetSize(listWidth, contentHeight)

	if m.textInputOverlay != nil {
		// Pass the full terminal height: the create form sizes its own sections to fit (and the
		// plain prompt overlay applies its own fraction), so it needs to know the real height
		// rather than a pre-scaled slice of it.
		m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), msg.Height)
	}
	if m.textOverlay != nil {
		m.textOverlay.SetWidth(int(float32(msg.Width) * 0.6))
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, menuHeight)
}

// recomputeLayout re-runs the size calculation off the cached terminal size. Use
// it when something other than a resize changes the vertical budget — e.g. an
// error appearing or clearing toggles whether the error box claims a row.
func (m *home) recomputeLayout() {
	if m.windowWidth == 0 || m.windowHeight == 0 {
		return
	}
	m.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: m.windowWidth, Height: m.windowHeight})
}

func (m *home) Init() tea.Cmd {
	// Upon starting, we want to start the spinner. Whenever we get a spinner.TickMsg, we
	// update the spinner, which sends a new spinner.TickMsg. I think this lasts forever lol.
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()),
	)
}

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		m.errBox.Clear()
		m.recomputeLayout() // reclaim the error row; panes grow back by one
	case previewTickMsg:
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
	case keyupMsg:
		m.menu.ClearKeydown()
		return m, nil
	case instanceStartDoneMsg:
		m.instanceStarting = false
		inst := msg.instance
		m.startingInstance = nil

		if msg.err != nil {
			// Start failed — remove the instance from the list and show the error.
			m.list.Kill()
			return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), m.handleError(msg.err))
		}

		// Save after successful start.
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			return m, m.handleError(err)
		}
		m.recordRecentPath(inst.Path)

		return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
	case autoNameDoneMsg:
		m.generatingName = false
		if msg.err != nil {
			// Restore the normal hint bar and surface the failure; leave the name
			// untouched rather than applying a fallback or junk value.
			m.menu.SetState(ui.StateDefault)
			return m, m.handleError(msg.err)
		}
		// Offer the generated name through the existing rename overlay so the user
		// can confirm or edit it before it commits.
		m.renameTarget = msg.instance
		m.renameOverlay = overlay.NewRenameOverlay(msg.name)
		m.state = stateRename
		m.menu.SetState(ui.StatePrompt)
		return m, nil
	case metadataUpdateDoneMsg:
		if recoverLostInstances(msg.results, m.lostStrikes) {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				log.ErrorLog.Printf("failed to persist recovered sessions: %v", err)
			}
		}
		for _, r := range msg.results {
			// Skip instances that were paused while metadata was being computed, or
			// that were just recovered to Paused above because their session died.
			if r.sessionLost || r.instance.Status == session.Paused {
				continue
			}
			applyPaneState(r.instance, r.state)
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
		}
		cmds := deliverReadyPrompts(msg.results)
		cmds = append(cmds, tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()))
		return m, tea.Batch(cmds...)
	case tea.MouseMsg:
		// Handle mouse wheel events for scrolling the diff/preview pane
		if msg.Action == tea.MouseActionPress {
			if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
				selected := m.list.GetSelectedInstance()
				if selected == nil || selected.Status == session.Paused {
					return m, nil
				}

				switch msg.Button {
				case tea.MouseButtonWheelUp:
					m.tabbedWindow.ScrollUp()
				case tea.MouseButtonWheelDown:
					m.tabbedWindow.ScrollDown()
				}
			}
		}
		return m, nil
	case branchSearchDebounceMsg:
		// Debounce timer fired — check if this is still the current filter version
		if m.textInputOverlay == nil {
			return m, nil
		}
		if msg.version != m.textInputOverlay.BranchFilterVersion() {
			return m, nil // stale, a newer debounce is pending
		}
		return m, m.runBranchSearch(msg.filter, msg.version)
	case branchSearchResultMsg:
		if m.textInputOverlay != nil {
			m.textInputOverlay.SetBranchResults(msg.branches, msg.version)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		// First launch ever: show the one-time welcome once the size is known.
		m.maybeShowWelcome()
		return m, nil
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action
		return m, m.instanceChanged()
	case instanceStartedMsg:
		// Select the instance that just started (or failed)
		m.list.SelectInstance(msg.instance)

		if msg.err != nil {
			m.list.Kill()
			return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
		}

		// Save after successful start
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			return m, m.handleError(err)
		}
		m.recordRecentPath(msg.instance.Path)
		if m.autoYes {
			msg.instance.AutoYes = true
		}

		// A prompt from the N form is delivered later by the metadata tick loop,
		// once the agent is past its startup/trust screen and ready for input
		// (see deliverReadyPrompts). Sending here races the agent's boot and lands
		// keystrokes in the trust dialog instead of the input box.
		m.menu.SetState(ui.StateDefault)

		if m.shouldAutoOpen(msg.instance) {
			// Drop straight into the new session, mirroring the KeyEnter attach path.
			// Attach msg.instance directly rather than via m.list.Attach(): a background
			// instanceStartedMsg from another freshly-created session could have moved
			// the list selection by now. Attaching blocks until the user detaches
			// (ctrl-q); the hint bar carries the detach reminder, so no teaching modal.
			ch, err := msg.instance.Attach()
			if err != nil {
				return m, m.handleError(err)
			}
			<-ch
			m.state = stateDefault
			return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
		}

		return m, tea.Batch(tea.WindowSize(), m.instanceChanged())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
		return m, m.handleError(err)
	}
	return m, tea.Quit
}

func (m *home) handleMenuHighlighting(msg tea.KeyMsg) (cmd tea.Cmd, returnEarly bool) {
	// Handle menu highlighting when you press a button. We intercept it here and immediately return to
	// update the ui while re-sending the keypress. Then, on the next call to this, we actually handle the keypress.
	if m.keySent {
		m.keySent = false
		return nil, false
	}
	if m.state == statePrompt || m.state == stateHelp || m.state == stateConfirm || m.state == stateRename {
		return nil, false
	}
	// If it's in the global keymap, we should try to highlight it.
	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return nil, false
	}

	if m.list.GetSelectedInstance() != nil && m.list.GetSelectedInstance().Paused() && name == keys.KeyEnter {
		return nil, false
	}
	if name == keys.KeyShiftDown || name == keys.KeyShiftUp {
		return nil, false
	}

	// Skip the menu highlighting if the key is not in the map or we are using the shift up and down keys.
	// TODO: cleanup: when you press enter on stateNew, we use keys.KeySubmitName. We should unify the keymap.
	if name == keys.KeyEnter && m.state == stateNew {
		name = keys.KeySubmitName
	}
	m.keySent = true
	return tea.Batch(
		func() tea.Msg { return msg },
		m.keydownCallback(name)), true
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	cmd, returnEarly := m.handleMenuHighlighting(msg)
	if returnEarly {
		return m, cmd
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == stateNew {
		// Handle quit commands first. Don't handle q because the user might want to type that.
		if msg.String() == "ctrl+c" {
			m.state = stateDefault
			m.killNewInstance()
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		}

		// The inline `n` flow tracks the new instance by reference: AddInstance may insert it
		// mid-list (under its repo group) and a background instanceStartedMsg may move the
		// selection, so it is neither the last item nor reliably the selected one.
		instance := m.newInstance
		if instance == nil {
			m.state = stateDefault
			return m, nil
		}
		switch msg.Type {
		// Start the instance (enable previews etc) and go back to the main menu state.
		case tea.KeyEnter:
			if len(instance.Title) == 0 {
				return m, m.handleError(fmt.Errorf("title cannot be empty"))
			}

			// Set Loading status and finalize into the list immediately
			instance.SetStatus(session.Loading)
			m.newInstanceFinalizer()
			m.newInstance = nil // creation handed off to the background start
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)

			// Return a tea.Cmd that runs instance.Start in the background
			startCmd := func() tea.Msg {
				err := instance.Start(true)
				return instanceStartedMsg{instance: instance, err: err}
			}

			return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd)
		case tea.KeyRunes:
			if runewidth.StringWidth(instance.Title) >= 32 {
				return m, m.handleError(fmt.Errorf("title cannot be longer than 32 characters"))
			}
			if err := instance.SetTitle(instance.Title + string(msg.Runes)); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyBackspace:
			runes := []rune(instance.Title)
			if len(runes) == 0 {
				return m, nil
			}
			if err := instance.SetTitle(string(runes[:len(runes)-1])); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeySpace:
			if err := instance.SetTitle(instance.Title + " "); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyEsc:
			m.killNewInstance()
			m.state = stateDefault
			m.instanceChanged()

			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		default:
		}
		return m, nil
	} else if m.state == statePrompt {
		// Handle cancel via ctrl+c before delegating to the overlay
		if msg.String() == "ctrl+c" {
			return m, m.cancelPromptOverlay()
		}

		// Use the new TextInputOverlay component to handle all key events
		shouldClose, branchFilterChanged := m.textInputOverlay.HandleKeyPress(msg)

		// Check if the form was submitted or canceled
		if shouldClose {
			if m.textInputOverlay.IsCanceled() {
				return m, m.cancelPromptOverlay()
			}

			if !m.textInputOverlay.IsSubmitted() {
				m.textInputOverlay = nil
				m.state = stateDefault
				return m, nil
			}

			prompt := m.textInputOverlay.GetValue()

			// The new-session form creates the instance only now, on submit, so no row
			// appears in the list while the user is still filling it in.
			if m.textInputOverlay.IsCreateForm() {
				return m, m.createSessionFromForm(prompt)
			}

			// Quick-send overlay: fire the message at the selected running session and drop
			// straight back to the list (no new-session help — the session is already up).
			selected := m.list.GetSelectedInstance()
			if selected == nil {
				m.textInputOverlay = nil
				m.state = stateDefault
				return m, nil
			}
			if err := selected.SendPrompt(prompt); err != nil {
				return m, m.handleError(err)
			}
			m.textInputOverlay = nil
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)
			return m, tea.Sequence(tea.WindowSize(), m.instanceChanged())
		}

		// If the target directory changed in the picker, re-scope the branch search to
		// the new repo: invalidate in-flight results for the old repo, then schedule a
		// fresh (debounced) search with the current branch filter.
		if newPath := m.textInputOverlay.GetSelectedPath(); newPath != "" && newPath != m.newSessionPath {
			m.newSessionPath = newPath
			// Validate up front so the picker can flag a non-repo inline, rather than
			// only rejecting it at submit after the user has filled in the prompt.
			m.textInputOverlay.SetTargetValidity(git.IsGitRepo(newPath))
			version := m.textInputOverlay.InvalidateBranchSearch()
			return m, m.scheduleBranchSearch(m.textInputOverlay.BranchFilter(), version)
		}

		// Schedule a debounced branch search if the filter changed
		if branchFilterChanged {
			filter := m.textInputOverlay.BranchFilter()
			version := m.textInputOverlay.BranchFilterVersion()
			return m, m.scheduleBranchSearch(filter, version)
		}

		return m, nil
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
		if shouldClose {
			confirmed := m.confirmationOverlay.Confirmed
			action := m.pendingConfirmAction
			m.state = stateDefault
			m.confirmationOverlay = nil
			m.pendingConfirmAction = nil
			if confirmed && action != nil {
				// Run the action here, on the main loop, because it mutates shared
				// model state (list, terminals); a tea.Cmd would run it in a
				// goroutine and race Update. Feed only the resulting message back
				// through the runtime so a returned error reaches the error box.
				resultMsg := action()
				return m, func() tea.Msg { return resultMsg }
			}
			return m, nil
		}
		return m, nil
	}

	// Handle rename state. This must run before the global q/ctrl+c quit handling below so
	// those keys edit (or cancel) the label instead of quitting the app.
	if m.state == stateRename {
		shouldClose := m.renameOverlay.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}

		submitted := m.renameOverlay.IsSubmitted()
		value := m.renameOverlay.Value()
		deep := m.renameOverlay.IsDeep()
		// Apply to the instance the overlay was opened for, not the currently
		// selected one — they can differ if the selection moved while the overlay
		// was open (notably during async auto-naming).
		target := m.renameTarget
		m.renameOverlay = nil
		m.renameTarget = nil
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)

		if submitted && target != nil {
			if deep {
				if err := m.deepRename(target, value); err != nil {
					return m, m.handleError(err)
				}
			} else {
				target.SetDisplayName(value)
				if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
					return m, m.handleError(err)
				}
			}
		}
		return m, m.instanceChanged()
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
		// If in preview tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode() {
			// Use the selected instance from the list
			selected := m.list.GetSelectedInstance()
			err := m.tabbedWindow.ResetPreviewToNormalMode(selected)
			if err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		// If in terminal tab and in scroll mode, exit scroll mode
		if m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode() {
			m.tabbedWindow.ResetTerminalToNormalMode()
			return m, m.instanceChanged()
		}
	}

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeyPrompt:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}

		// Open the unified new-session form immediately. The session itself is not created
		// (and no list row appears) until the form is submitted — every parameter is reached
		// directly in the form. Derive the contextual target repo first and kick a background
		// fetch so branches are current by the time the user reaches the branch field.
		m.newSessionPath = m.defaultNewSessionPath()
		target := m.newSessionPath
		fetchCmd := func() tea.Msg {
			git.FetchBranches(target)
			return nil
		}

		m.state = statePrompt
		m.menu.SetState(ui.StatePrompt)
		m.textInputOverlay = m.newSessionFormOverlay()
		// Trigger the initial branch search (no debounce, version 0).
		initialSearch := m.runBranchSearch("", m.textInputOverlay.BranchFilterVersion())

		return m, tea.Batch(tea.WindowSize(), fetchCmd, initialSearch)
	case keys.KeyNew:
		if m.list.NumInstances() >= GlobalInstanceLimit {
			return m, m.handleError(
				fmt.Errorf("you can't create more than %d instances", GlobalInstanceLimit))
		}
		// Derive the contextual target repo before adding the new instance. The inline
		// `n` flow has no directory picker, so if there is no repo context (e.g. cs was
		// launched outside a repo with no sessions), guide the user to `N` instead of
		// letting session creation fail later at worktree time.
		m.newSessionPath = m.defaultNewSessionPath()
		if !git.IsGitRepo(m.newSessionPath) {
			return m, m.handleError(fmt.Errorf("not in a git repository; press N to choose a project"))
		}
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "",
			Path:    m.newSessionPath,
			Program: m.program,
		})
		if err != nil {
			return m, m.handleError(err)
		}

		m.newInstanceFinalizer = m.list.AddInstance(instance)
		// AddInstance may insert the session into the middle of the list (under its repo
		// group), so select it by identity rather than assuming it is last. Also track it by
		// reference: the naming/prompt flow operates on m.newInstance, not the selection,
		// which a background instanceStartedMsg can move.
		m.list.SelectInstance(instance)
		m.newInstance = instance
		m.state = stateNew
		m.menu.SetState(ui.StateNewInstance)
		m.menu.SetNewInstanceHint(filepath.Base(m.newSessionPath))

		return m, nil
	case keys.KeyQuickSend:
		// Open a compose box to fire an ad-hoc message at the selected running session
		// without attaching. Only meaningful when the agent is up and accepting input, so
		// this is a no-op for an empty/loading/paused selection.
		selected := m.list.GetSelectedInstance()
		if selected == nil || !selected.Started() || selected.Paused() || selected.Status == session.Loading {
			return m, nil
		}
		m.state = statePrompt
		m.menu.SetState(ui.StatePrompt)
		m.textInputOverlay = overlay.NewQuickSendOverlay("Send to " + selected.DisplayName())
		return m, tea.WindowSize()
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp()
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown()
		return m, m.instanceChanged()
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyShiftTab:
		m.tabbedWindow.ToggleReverse()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Create the kill action as a tea.Cmd
		killAction := func() tea.Msg {
			// Refuse to kill only when we can positively confirm the branch is
			// checked out in its primary repo (removing it would be destructive).
			// This is a teardown path: if the worktree or its repo is unreachable
			// — e.g. the user renamed/removed the project directory — fail open and
			// proceed, otherwise an orphaned session can never be deleted.
			if worktree, err := selected.GetGitWorktree(); err != nil {
				log.WarningLog.Printf("kill %s: cannot resolve worktree, proceeding: %v", selected.Title, err)
			} else if checkedOut, cerr := worktree.IsBranchCheckedOut(); cerr != nil {
				log.WarningLog.Printf("kill %s: cannot verify branch checkout, proceeding: %v", selected.Title, cerr)
			} else if checkedOut {
				return fmt.Errorf("instance %s is currently checked out", selected.DisplayName())
			}

			// Clean up terminal session for this instance
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)

			// Delete from storage first
			if err := m.storage.DeleteInstance(selected.Title); err != nil {
				return err
			}

			// Then kill the instance
			m.list.Kill()
			return instanceChangedMsg{}
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Kill session '%s'?", selected.DisplayName())
		return m, m.confirmAction(message, killAction)
	case keys.KeyRename:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}
		m.renameTarget = selected
		m.renameOverlay = overlay.NewRenameOverlay(selected.DisplayName())
		m.state = stateRename
		m.menu.SetState(ui.StatePrompt)
		return m, nil
	case keys.KeyAutoName:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading || m.generatingName {
			return m, nil
		}
		// The model call (and the full diff it needs) happen in the background Cmd so
		// the UI stays responsive; only the instance and prompt are captured here.
		m.generatingName = true
		m.menu.SetState(ui.StateGeneratingName)
		return m, runAutoNameCmd(m.ctx, selected, selected.Prompt)
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Create the push action as a tea.Cmd
		pushAction := func() tea.Msg {
			// Default commit message with timestamp
			commitMsg := fmt.Sprintf("[atrium] update from '%s' on %s", selected.DisplayName(), time.Now().Format(time.RFC822))
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err = worktree.PushChanges(commitMsg, true); err != nil {
				return err
			}
			return nil
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Push changes from session '%s'?", selected.DisplayName())
		return m, m.confirmAction(message, pushAction)
	case keys.KeyCheckout:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Checkout: commit changes and pause. The branch name is copied to the
		// clipboard inside Pause(); the always-on hint bar carries the reminder.
		if err := selected.Pause(); err != nil {
			return m, m.handleError(err)
		}
		m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
		return m, m.instanceChanged()
	case keys.KeyMoveUp:
		if m.list.MoveUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveDown:
		if m.list.MoveDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupUp:
		if m.list.MoveGroupUp() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveGroupDown:
		if m.list.MoveGroupDown() {
			if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapseToggle:
		if m.list.ToggleCollapse() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyCollapseAll:
		if m.list.ToggleCollapseAll() {
			if err := m.appState.SetCollapsedRepos(m.list.CollapsedRepos()); err != nil {
				return m, m.handleError(err)
			}
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}
		if err := selected.Resume(); err != nil {
			return m, m.handleError(err)
		}
		return m, tea.WindowSize()
	case keys.KeyEnter:
		if m.list.NumInstances() == 0 {
			return m, nil
		}
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Paused() || selected.Status == session.Loading || !selected.TmuxAlive() {
			return m, nil
		}
		// Terminal tab: attach to terminal session. Attaching blocks until the
		// user detaches (ctrl-q); the hint bar carries the detach reminder.
		if m.tabbedWindow.IsInTerminalTab() {
			ch, err := m.tabbedWindow.AttachTerminal()
			if err != nil {
				return m, m.handleError(err)
			}
			<-ch
			m.state = stateDefault
			return m, nil
		}
		ch, err := m.list.Attach()
		if err != nil {
			return m, m.handleError(err)
		}
		<-ch
		m.state = stateDefault
		return m, m.instanceChanged()
	default:
		return m, nil
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
// deepRename renames the selected instance's title, git branch, worktree directory, and tmux
// session to value, then clears the cosmetic label so the list shows the corrected name. It
// rejects an empty title or one already used by another instance (Title is the storage key).
// Runs synchronously on the main event loop — the rename is a handful of instant subprocesses,
// and the git/tmux structs guard the fields the background poll loop reads.
func (m *home) deepRename(selected *session.Instance, value string) error {
	if value == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	for _, inst := range m.list.GetInstances() {
		if inst != selected && inst.Title == value {
			return fmt.Errorf("a session named %q already exists", value)
		}
	}
	if err := selected.Rename(value); err != nil {
		return err
	}
	selected.SetDisplayName("")
	return m.storage.SaveInstances(m.list.GetInstances())
}

func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}
	return nil
}

type keyupMsg struct{}

// keydownCallback clears the menu option highlighting after 500ms.
func (m *home) keydownCallback(name keys.KeyName) tea.Cmd {
	m.menu.Keydown(name)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}

		return keyupMsg{}
	}
}

// hideErrMsg implements tea.Msg and clears the error text from the screen.
type hideErrMsg struct{}

// previewTickMsg implements tea.Msg and triggers a preview update
type previewTickMsg struct{}

type instanceChangedMsg struct{}

type instanceStartedMsg struct {
	instance *session.Instance
	err      error
}

// shouldAutoOpen reports whether a freshly started session should be attached
// automatically. It is gated by the auto_attach config flag and skipped when the
// instance carries an initial prompt (delivered asynchronously by the metadata tick,
// which is paused while attached). The Started/TmuxAlive guards avoid attaching a
// session that did not come up — and, because Started() short-circuits before
// TmuxAlive() (which dereferences tmuxSession), keep unstarted instances (e.g. in
// tests) off both the panic and the attach path.
func (m *home) shouldAutoOpen(inst *session.Instance) bool {
	return m.appConfig.GetAutoAttach() && inst.Prompt == "" && inst.Started() && inst.TmuxAlive()
}

// branchSearchDebounceMsg fires after the debounce interval to trigger a search.
type branchSearchDebounceMsg struct {
	filter  string
	version uint64
}

// branchSearchResultMsg carries search results back to Update.
type branchSearchResultMsg struct {
	branches []string
	version  uint64
}

const branchSearchDebounce = 150 * time.Millisecond

// scheduleBranchSearch returns a debounced tea.Cmd: sleeps, then triggers a search message.
func (m *home) scheduleBranchSearch(filter string, version uint64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return branchSearchDebounceMsg{filter: filter, version: version}
	}
}

// runBranchSearch returns a tea.Cmd that performs the git search in the background.
// It searches the current new-session target repo (m.newSessionPath), captured at call
// time so it reflects the directory chosen in the picker rather than the process cwd.
func (m *home) runBranchSearch(filter string, version uint64) tea.Cmd {
	target := m.newSessionPath
	return func() tea.Msg {
		if target == "" {
			var err error
			if target, err = os.Getwd(); err != nil {
				return nil
			}
		}
		branches, err := git.SearchBranches(target, filter)
		if err != nil {
			log.WarningLog.Printf("branch search failed: %v", err)
			return nil
		}
		return branchSearchResultMsg{branches: branches, version: version}
	}
}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance       *session.Instance
	state          tmux.PaneState
	readyForPrompt bool
	// sessionLost is set when a started, non-paused instance's tmux pane no longer
	// exists. The main thread recovers it to Paused (see recoverLostInstances).
	sessionLost bool
	diffStats   *git.DiffStats
}

// applyPaneState maps a polled pane state onto an instance's status. Prompt handling
// depends on AutoYes: with it on, auto-answer (TapEnter is a no-op otherwise); with it
// off the session is blocked on the user, so surface NeedsInput rather than a spinner.
// PaneUnknown (an unreadable pane) leaves the status untouched.
func applyPaneState(inst *session.Instance, state tmux.PaneState) {
	switch state {
	case tmux.PaneWorking:
		inst.SetStatus(session.Running)
	case tmux.PanePrompt:
		if inst.AutoYes {
			inst.TapEnter()
		} else {
			inst.SetStatus(session.NeedsInput)
		}
	case tmux.PaneIdle:
		inst.SetStatus(session.Ready)
	case tmux.PaneUnknown:
	}
}

// sendPromptCmd submits a queued initial prompt to an instance off the UI thread,
// so the SendKeys→Enter pause inside SendPrompt does not block rendering.
func sendPromptCmd(instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		if err := instance.SendPrompt(prompt); err != nil {
			log.ErrorLog.Printf("failed to send queued prompt: %v", err)
		}
		return nil
	}
}

// deliverReadyPrompts submits each ready instance's queued prompt and returns the
// commands that perform the sends. The prompt is cleared synchronously here so it
// is dispatched at most once, even if a later tick also reports the instance ready.
func deliverReadyPrompts(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range results {
		if r.readyForPrompt && r.instance.Prompt != "" {
			prompt := r.instance.Prompt
			r.instance.Prompt = ""
			cmds = append(cmds, sendPromptCmd(r.instance, prompt))
		}
	}
	return cmds
}

// lostSessionRecoverThreshold is how many consecutive ticks an instance must be seen
// with a dead tmux session before it is recovered to Paused. Recovery commits any WIP
// and removes the worktree, so a single transient `tmux has-session` miss (server
// blip, load spike) must not trigger it — require confirmation across ticks.
const lostSessionRecoverThreshold = 2

// recoverLostInstances moves instances whose tmux session has died (flagged
// sessionLost by the metadata tick) into Paused, so they stop being polled and can be
// brought back with Resume. It debounces using strikes (a per-instance count of
// consecutive dead observations, owned by the caller): a session is only recovered
// after lostSessionRecoverThreshold consecutive misses; any live observation resets
// the count. Returns whether any instance was recovered so the caller can persist.
// Runs on the main thread — the only place model state may be mutated.
func recoverLostInstances(results []instanceMetaResult, strikes map[*session.Instance]int) (recovered bool) {
	for _, r := range results {
		if !r.sessionLost || r.instance.Paused() {
			delete(strikes, r.instance) // alive (or already paused): clear any prior strikes
			continue
		}
		strikes[r.instance]++
		if strikes[r.instance] < lostSessionRecoverThreshold {
			continue // not yet confirmed dead; re-check next tick
		}
		delete(strikes, r.instance)
		if err := r.instance.RecoverLostSession(); err != nil {
			log.ErrorLog.Printf("failed to recover lost session %q: %v", r.instance.Title, err)
		}
		recovered = true
	}
	return recovered
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

// instanceStartDoneMsg is sent when the background instance start completes.
type instanceStartDoneMsg struct {
	instance *session.Instance
	err      error
}

// autoNameDoneMsg is sent when a background name generation completes. instance
// identifies which session the name was generated for, so the result lands on the
// right one even if the selection moved meanwhile.
type autoNameDoneMsg struct {
	instance *session.Instance
	name     string
	err      error
}

// runAutoNameCmd returns a Cmd that generates a display name in a background
// goroutine (the claude subprocess can take a few seconds) so the UI stays
// responsive.
func runAutoNameCmd(ctx context.Context, instance *session.Instance, prompt string) tea.Cmd {
	return func() tea.Msg {
		// Compute the full diff here, off the UI thread. The cached stats are often the
		// lightweight numstat form (Content empty) — that's all that's kept for a
		// session unless it is the selected one during a diff poll — which would starve
		// the namer of signal and yield a confabulated name. ComputeDiff is
		// goroutine-safe; fall back to the cached stats if it can't run (e.g. paused).
		stats := instance.ComputeDiff()
		if stats == nil || stats.Content == "" {
			if cached := instance.GetDiffStats(); cached != nil {
				stats = cached
			}
		}
		name, err := session.GenerateName(ctx, prompt, stats)
		return autoNameDoneMsg{instance: instance, name: name, err: err}
	}
}

// snapshotActiveInstances returns the currently active (started, not paused)
// instances. Called on the main thread so the filtering doesn't race with
// state mutations.
func (m *home) snapshotActiveInstances() []*session.Instance {
	var out []*session.Instance
	for _, inst := range m.list.GetInstances() {
		if inst.Started() && !inst.Paused() {
			out = append(out, inst)
		}
	}
	return out
}

// tickUpdateMetadataCmd returns a self-chaining Cmd that sleeps 500ms, then performs
// expensive metadata I/O (tmux capture, git diff) in parallel background goroutines.
// Because it only re-schedules after completing, overlapping ticks are impossible.
// The active instances slice should be snapshotted on the main thread via
// snapshotActiveInstances() before being passed here.
//
// Only the selected instance gets a full diff (with Content); the rest get a
// lightweight numstat-only summary. This keeps per-instance memory bounded
// since the diff pane only ever renders the selected one.
func tickUpdateMetadataCmd(active []*session.Instance, selected *session.Instance) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)

		if len(active) == 0 {
			return metadataUpdateDoneMsg{}
		}

		results := make([]instanceMetaResult, len(active))
		var wg sync.WaitGroup
		for idx, inst := range active {
			wg.Add(1)
			go func(i int, instance *session.Instance) {
				defer wg.Done()
				r := &results[i]
				r.instance = instance
				// A started session whose tmux pane has died would fail every probe
				// (capture, diff) and flood the log/error box. Detect it once here
				// (read-only) and skip polling; the main thread recovers it to Paused.
				if instance.Started() && !instance.Paused() && !instance.TmuxAlive() {
					r.sessionLost = true
					return
				}
				r.state = instance.Poll()
				// Only probe readiness while a prompt is actually queued (a brief
				// window after a new session), so the extra pane capture is rare.
				// Require the pane to not be mid-work to avoid the post-trust
				// "loading" transition window.
				if instance.Prompt != "" {
					r.readyForPrompt = r.state != tmux.PaneWorking && instance.IsReadyForPrompt()
				}
				if instance == selected {
					r.diffStats = instance.ComputeDiff()
				} else {
					r.diffStats = instance.ComputeDiffNumstat()
				}
			}(idx, inst)
		}
		wg.Wait()

		return metadataUpdateDoneMsg{results: results}
	}
}

// handleError handles all errors which get bubbled up to the app. sets the error message. We return a callback tea.Cmd that returns a hideErrMsg message
// which clears the error message after 3 seconds.
func (m *home) handleError(err error) tea.Cmd {
	log.ErrorLog.Printf("%v", err)
	m.errBox.SetError(err)
	m.recomputeLayout() // give the error its row; panes shrink by one
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(3 * time.Second):
		}

		return hideErrMsg{}
	}
}

// newSessionFormOverlay builds the unified new-session form (title, project, optional
// profile, branch, prompt) for the `N` flow.
func (m *home) newSessionFormOverlay() *overlay.TextInputOverlay {
	ov := overlay.NewSessionCreateOverlay(m.appConfig.GetProfiles(), m.candidateRepoPaths())
	// Seed the initial validity so the picker can flag a non-repo default target
	// (e.g. when cs was launched outside a git repo) before the user navigates.
	ov.SetTargetValidity(git.IsGitRepo(m.newSessionPath))
	return ov
}

// createSessionFromForm validates the submitted new-session form, creates the session,
// adds it to the list, and starts it in the background with the entered prompt. On a
// validation error it leaves the overlay open (clearing the submitted flag) and surfaces
// the error so the user can correct the offending field.
func (m *home) createSessionFromForm(prompt string) tea.Cmd {
	ov := m.textInputOverlay

	title := ov.GetTitle()
	if title == "" {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("title cannot be empty"))
	}

	path := ov.GetSelectedPath()
	if path == "" {
		path = m.newSessionPath
	}
	if !git.IsGitRepo(path) {
		ov.Submitted = false
		return m.handleError(fmt.Errorf("%q is not a git repository", path))
	}

	program := m.program
	if p := ov.GetSelectedProgram(); p != "" {
		program = p
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    path,
		Program: program,
	})
	if err != nil {
		ov.Submitted = false
		return m.handleError(err)
	}

	// Create the list row only now, on submit. AddInstance may insert it mid-list under its
	// repo group, so select it by identity.
	finalizer := m.list.AddInstance(instance)
	m.list.SelectInstance(instance)
	if branch := ov.GetSelectedBranch(); branch != "" {
		instance.SetBaseBranch(branch)
	}
	instance.Prompt = prompt
	instance.SetStatus(session.Loading)
	finalizer()

	m.textInputOverlay = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)

	startCmd := func() tea.Msg {
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err}
	}
	return tea.Batch(tea.WindowSize(), m.instanceChanged(), startCmd)
}

// defaultNewSessionPath returns the contextual target repo for a new session: the
// highlighted session's repo, falling back to the current working directory. The
// empty string is returned only if there is no repo context at all (no highlighted
// session and cwd is unavailable).
func (m *home) defaultNewSessionPath() string {
	if selected := m.list.GetSelectedInstance(); selected != nil && selected.Path != "" {
		return selected.Path
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// candidateRepoPaths returns the deduped candidate target paths for the directory
// picker: the current target first, then existing sessions' repos, then recently-used
// project directories, then cwd.
func (m *home) candidateRepoPaths() []string {
	seen := make(map[string]bool)
	var paths []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	add(m.newSessionPath)
	for _, inst := range m.list.GetInstances() {
		add(inst.Path)
	}
	for _, p := range m.appState.GetRecentPaths() {
		// Skip recent paths that no longer exist so deleted/moved repos don't clutter
		// the picker or error only when selected.
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			continue
		}
		add(p)
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	return paths
}

// recordRecentPath records a newly-started session's repo path in the MRU list. It is
// best-effort: a persistence error is logged but does not interrupt the session flow.
func (m *home) recordRecentPath(path string) {
	if err := m.appState.AddRecentPath(path); err != nil {
		log.WarningLog.Printf("failed to record recent path %q: %v", path, err)
	}
}

// cancelPromptOverlay cancels the prompt overlay, cleaning up the unstarted instance.
func (m *home) cancelPromptOverlay() tea.Cmd {
	m.killNewInstance()
	m.textInputOverlay = nil
	m.state = stateDefault
	return tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// killNewInstance removes the in-progress new instance from the list and clears the tracking
// reference. List.Kill removes the selected item, so we re-select the tracked instance first:
// a background instanceStartedMsg may have moved the selection onto an already-started one.
func (m *home) killNewInstance() {
	if m.newInstance != nil && !m.newInstance.Started() {
		m.list.SelectInstance(m.newInstance)
		m.list.Kill()
	}
	m.newInstance = nil
}

// confirmAction shows a confirmation modal and stores the action to execute on
// confirm. The action is run (and its result dispatched) by the stateConfirm key
// handler, not here, so its returned message — including any error — flows through
// Update instead of being discarded.
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm
	m.pendingConfirmAction = action

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	// Set a fixed width for consistent appearance
	m.confirmationOverlay.SetWidth(50)

	return nil
}

func (m *home) View() string {
	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, m.list.String(), m.tabbedWindow.String())

	parts := []string{listAndPreview, m.menu.String()}
	// The error box only claims a row while it has something to show; otherwise the
	// help bar is the last row and there is no trailing blank line. (JoinVertical
	// treats an empty string as a blank line, so it must be omitted, not just empty.)
	if m.errBox.HasError() {
		parts = append(parts, m.errBox.String())
	}
	mainView := lipgloss.JoinVertical(lipgloss.Left, parts...)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true, true)
	} else if m.state == stateHelp {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true, true)
	} else if m.state == stateConfirm {
		if m.confirmationOverlay == nil {
			log.ErrorLog.Printf("confirmation overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.confirmationOverlay.Render(), mainView, true, true)
	} else if m.state == stateRename {
		if m.renameOverlay == nil {
			log.ErrorLog.Printf("rename overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.renameOverlay.Render(), mainView, true, true)
	}

	return mainView
}
