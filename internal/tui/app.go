package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// tabID identifies the three pages in stable display order. The underlying
// int is used as an index into Model.tabs so the order matters.
type tabID int

const (
	tabCalls tabID = iota
	tabUsers
	tabDrill
	tabFlaky
)

const tabCount = 4

// healthFetchTimeout bounds the /health poll issued from the root Model.
// 10s is stricter than the per-request default because the status bar
// polling should fail fast when the api service is down.
const healthFetchTimeout = 10 * time.Second

// tabView is the interface implemented by every page. The root Model
// stores []tabView so it can iterate and dispatch messages without caring
// about concrete types. Each tab owns its own Init/Update/View and may
// return Cmds that the root batches back into the program loop.
type tabView interface {
	Init() tea.Cmd
	Update(tea.Msg) (tabView, tea.Cmd)
	View() string
	Title() string
}

// Model is the root bubbletea model. It intercepts global keys, forwards
// everything else to the active tab, and owns the status bar state.
type Model struct {
	client *Client
	log    *slog.Logger

	tabs   [tabCount]tabView
	active tabID

	width  int
	height int

	health *HealthDTO
	status string
	err    error
}

// NewModel wires a ready-to-run root model. Tabs are constructed eagerly
// so their initial fetches fire from Model.Init in a single batch.
func NewModel(client *Client, log *slog.Logger) *Model {
	if log == nil {
		log = slog.Default()
	}
	m := &Model{
		client: client,
		log:    log,
		active: tabCalls,
	}
	m.tabs[tabCalls] = newCallsTab(client)
	m.tabs[tabUsers] = newUsersTab(client)
	m.tabs[tabDrill] = newDrillTab(client)
	m.tabs[tabFlaky] = newFlakyTab(client)
	return m
}

// Init returns a batch of:
//   - each data-bearing tab's initial fetch Cmd
//   - an initial /health poll for the status bar
//
// Loading both calls and users upfront makes the first tab switch feel
// instant; the drill tab has a nil Init so it does not contribute.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.tabs[tabCalls].Init(),
		m.tabs[tabUsers].Init(),
		m.tabs[tabDrill].Init(),
		m.tabs[tabFlaky].Init(),
		m.fetchHealthCmd(),
	}
	return tea.Batch(cmds...)
}

// Update is the message pump. Ordering matters: window and quit keys are
// handled before anything is forwarded to tabs, then cross-tab navigation
// messages are intercepted, and finally whatever is left is delegated to
// the active tab's Update.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Broadcast to every tab so they can recompute layout.
		cmds := make([]tea.Cmd, 0, tabCount)
		for i := range m.tabs {
			updated, cmd := m.tabs[i].Update(msg)
			m.tabs[i] = updated
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case healthLoadedMsg:
		m.health = msg.health
		if msg.err != nil {
			m.log.Debug("tui: health poll failed", slog.String("err", msg.err.Error()))
			m.err = msg.err
		} else {
			m.err = nil
		}
		return m, nil

	case SwitchToCallsMsg:
		// Flip to the calls tab and forward the message. tab_calls.Update
		// handles the filter and re-fetch internally via its SwitchToCallsMsg
		// case — a second ApplyFilter here would be redundant and requires an
		// unsafe concrete type assertion.
		m.active = tabCalls
		updated, cmd := m.tabs[tabCalls].Update(msg)
		m.tabs[tabCalls] = updated
		return m, cmd

	case OpenDrillMsg:
		// Flip to the drill tab and forward so it records the callId.
		m.active = tabDrill
		updated, cmd := m.tabs[tabDrill].Update(msg)
		m.tabs[tabDrill] = updated
		return m, cmd

	case BackToCallsMsg:
		// Return from drill to calls tab without touching any tab state.
		// The calls tab retains its cursor/sort/filter from before drill.
		m.active = tabCalls
		return m, nil

	case OpenUserPortraitMsg:
		// Flip to the users tab and forward so it enters portrait mode
		// and fetches /users/{upn}/health. Emitted by tab_users on shift+P
		// and by tab_flaky on enter.
		m.active = tabUsers
		updated, cmd := m.tabs[tabUsers].Update(msg)
		m.tabs[tabUsers] = updated
		return m, cmd
	}

	// Everything else goes to the active tab only. Data-load messages
	// (callsLoadedMsg, usersLoadedMsg) are routed by type rather than
	// active-tab state so a background fetch completing after a tab
	// switch still lands in the right place.
	return m.forwardToRelevantTab(msg)
}

// forwardToRelevantTab routes typed messages to the tab that issued them.
// This is safer than broadcasting because tabs hold their own cursors and
// loading flags — a stray message into the wrong tab would corrupt state.
func (m *Model) forwardToRelevantTab(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case callsLoadedMsg:
		updated, cmd := m.tabs[tabCalls].Update(msg)
		m.tabs[tabCalls] = updated
		return m, cmd
	case usersLoadedMsg:
		updated, cmd := m.tabs[tabUsers].Update(msg)
		m.tabs[tabUsers] = updated
		return m, cmd
	case drillFetchedMsg:
		updated, cmd := m.tabs[tabDrill].Update(msg)
		m.tabs[tabDrill] = updated
		return m, cmd
	case userHealthFetchedMsg:
		updated, cmd := m.tabs[tabUsers].Update(msg)
		m.tabs[tabUsers] = updated
		return m, cmd
	case flakyMicsLoadedMsg:
		updated, cmd := m.tabs[tabFlaky].Update(msg)
		m.tabs[tabFlaky] = updated
		return m, cmd
	}
	// Fallback — deliver to the active tab. Keeps custom per-tab messages
	// working without a type switch explosion.
	updated, cmd := m.tabs[m.active].Update(msg)
	m.tabs[m.active] = updated
	return m, cmd
}

// handleKey intercepts global key bindings. Unhandled keys are forwarded
// to the active tab so each page can wire its own bindings.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "1":
		m.active = tabCalls
		return m, nil
	case "2":
		m.active = tabUsers
		return m, nil
	case "3":
		m.active = tabDrill
		return m, nil
	case "4":
		m.active = tabFlaky
		return m, nil
	case "tab":
		m.active = tabID((int(m.active) + 1) % tabCount)
		return m, nil
	case "shift+tab":
		m.active = tabID((int(m.active) - 1 + tabCount) % tabCount)
		return m, nil
	case "r":
		// Refresh both the active tab's data AND the status bar health.
		updated, cmd := m.tabs[m.active].Update(RefreshMsg{})
		m.tabs[m.active] = updated
		return m, tea.Batch(cmd, m.fetchHealthCmd())
	}

	// Unknown key — forward to active tab.
	updated, cmd := m.tabs[m.active].Update(msg)
	m.tabs[m.active] = updated
	return m, cmd
}

// fetchHealthCmd issues one /health request and yields a healthLoadedMsg.
// The ctx is built per-call so cancellation is bounded; the Cmd is run on
// bubbletea's goroutine pool.
func (m *Model) fetchHealthCmd() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), healthFetchTimeout)
		defer cancel()
		h, err := client.Health(ctx)
		return healthLoadedMsg{health: h, err: err}
	}
}

// View composes the full frame: tab bar on top, active tab body in the
// middle, status bar at the bottom. The return type is tea.View (not
// string) because bubbletea v2 wraps the string in a View struct that
// carries AltScreen + background color hints.
func (m *Model) View() tea.View {
	// Guard against the initial render before the first WindowSizeMsg arrives.
	// Without this the width/height are 0 and lipgloss produces a blank frame.
	if m.width == 0 || m.height == 0 {
		return tea.NewView("initializing...")
	}

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderTabBar(),
		"",
		m.tabs[m.active].View(),
	)

	status := m.renderStatusBar()
	full := lipgloss.JoinVertical(lipgloss.Left, body, "", status)

	// Pad the rendered frame to the full terminal area so the alt screen
	// fills edge-to-edge instead of leaving the bottom/right blank.
	placed := lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, full)

	v := tea.NewView(placed)
	v.AltScreen = true
	return v
}

// renderTabBar draws the three tab titles with the active one highlighted.
// A numeric prefix ("1 Calls", etc.) makes the number shortcut discoverable
// without a separate legend.
func (m *Model) renderTabBar() string {
	cells := make([]string, tabCount)
	for i := range tabCount {
		title := fmt.Sprintf("%d %s", i+1, m.tabs[i].Title())
		if tabID(i) == m.active {
			cells[i] = activeTabStyle.Render(title)
		} else {
			cells[i] = inactiveTabStyle.Render(title)
		}
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, cells...)
	return bar
}

// renderStatusBar shows last crawl timestamp, mongo health, and any
// recent error. The format intentionally fits on one line at 80 cols.
func (m *Model) renderStatusBar() string {
	var parts []string

	if m.health != nil {
		if m.health.LastCrawlAt != nil {
			parts = append(parts,
				fmt.Sprintf("last crawl: %s", m.health.LastCrawlAt.UTC().Format("15:04:05")))
		} else {
			parts = append(parts, "last crawl: never")
		}
		if m.health.MongoOk {
			parts = append(parts, "mongo: ok")
		} else {
			parts = append(parts, "mongo: DOWN")
		}
		if m.health.LastCrawlError != "" {
			parts = append(parts, "crawl err: "+truncate(m.health.LastCrawlError, 40))
		}
	} else {
		parts = append(parts, "health: ...")
	}

	if m.err != nil {
		parts = append(parts, "err: "+truncate(m.err.Error(), 40))
	}

	help := "[1/2/3/4 tabs · tab/shift+tab cycle · r refresh · q quit]"
	line := strings.Join(parts, "  ·  ") + "   " + help
	return statusBarStyle.Render(line)
}

// Run boots a tea.Program on the supplied context and blocks until the
// user quits. The TUI owns the alt screen via View.AltScreen — the
// program option for WithAltScreen no longer exists in bubbletea v2.
func Run(ctx context.Context, apiURL string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	client := NewClient(apiURL)
	model := NewModel(client, log)

	prog := tea.NewProgram(model, tea.WithContext(ctx))
	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("tui: program exited: %w", err)
	}
	return nil
}
