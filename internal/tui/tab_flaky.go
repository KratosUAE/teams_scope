package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	flakyChromeRows   = 7
	flakyFetchTimeout = 30 * time.Second
	flakyDefaultLimit = 50

	// flakyTabMinIncidents is the TUI-specific floor. The server default
	// for /mics/flaky is 3 (tight "definitely chronic" signal for LLM
	// clients), but on a fresh week of data 2 incidents is already a
	// worth-a-look pattern and the dashboard's job is surfacing those
	// early. The operator can still filter further by eye.
	flakyTabMinIncidents = 2
)

// flakyTab is the "flaky microphones over the last 7 days" dashboard. It
// calls /mics/flaky on Init/Refresh with the server's defaults (7-day
// window, min_concealed_pct=5, min_incidents=3) and renders each finding
// as a table row. Enter on a row dispatches OpenUserPortraitMsg so the
// operator can drill straight from "who's flaky" into the full dossier.
type flakyTab struct {
	client *Client

	mics   []FlakyMicDTO
	cursor int
	offset int

	loading bool
	err     error

	width  int
	height int
}

func newFlakyTab(c *Client) *flakyTab { return &flakyTab{client: c} }

func (t *flakyTab) Title() string { return "Flaky mics" }

func (t *flakyTab) Init() tea.Cmd {
	t.loading = true
	return t.fetchCmd()
}

func (t *flakyTab) fetchCmd() tea.Cmd {
	client := t.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), flakyFetchTimeout)
		defer cancel()
		mics, err := client.ListFlakyMics(ctx, FlakyMicsParams{
			Limit:        flakyDefaultLimit,
			MinIncidents: flakyTabMinIncidents,
		})
		return flakyMicsLoadedMsg{mics: mics, err: err}
	}
}

func (t *flakyTab) Update(msg tea.Msg) (tabView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width = msg.Width
		t.height = msg.Height
	case flakyMicsLoadedMsg:
		t.loading = false
		t.err = msg.err
		t.mics = msg.mics
		if t.cursor >= len(t.mics) {
			t.cursor = 0
		}
		t.offset = 0
	case RefreshMsg:
		t.loading = true
		return t, t.fetchCmd()
	case tea.KeyPressMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *flakyTab) visibleRows() int {
	if t.height <= 0 {
		return 20
	}
	if v := t.height - flakyChromeRows; v >= 5 {
		return v
	}
	return 5
}

func (t *flakyTab) handleKey(msg tea.KeyPressMsg) (tabView, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if t.cursor > 0 {
			t.cursor--
			if t.cursor < t.offset {
				t.offset = t.cursor
			}
		}
	case "down", "j":
		if t.cursor < len(t.mics)-1 {
			t.cursor++
			vis := t.visibleRows()
			if t.cursor >= t.offset+vis {
				t.offset = t.cursor - vis + 1
			}
		}
	case "enter":
		if len(t.mics) == 0 {
			return t, nil
		}
		upn := t.mics[t.cursor].User
		return t, func() tea.Msg { return OpenUserPortraitMsg{UPN: upn} }
	}
	return t, nil
}

func (t *flakyTab) View() string {
	var b strings.Builder
	switch {
	case t.loading && len(t.mics) == 0:
		b.WriteString(helpStyle.Render("loading flaky microphones..."))
		return b.String()
	case t.err != nil:
		b.WriteString(errorStyle.Render("error: " + t.err.Error()))
		return b.String()
	case len(t.mics) == 0:
		b.WriteString(helpStyle.Render("no flaky microphones found in window"))
		return b.String()
	}

	b.WriteString(renderFlakyTable(t.mics, t.cursor, t.offset, t.visibleRows(), t.width))
	b.WriteString("\n")
	b.WriteString(renderHotkeyHelp("↑/↓ move  enter → portrait  r refresh  ·  window: last 7 days, concealed ≥ 5%, incidents ≥ 2", nil))
	return b.String()
}

var flakyColumns = []column{
	{title: "User", min: 20, weight: 3},
	{title: "Device", min: 16, weight: 2},
	{title: "Incidents", min: 11, weight: 0},
	{title: "Calls", min: 8, weight: 0},
	{title: "Worst%", min: 9, weight: 0},
	{title: "Avg%", min: 8, weight: 0},
	{title: "Severity", min: 10, weight: 0},
}

func renderFlakyTable(rows []FlakyMicDTO, cursor, offset, visible, totalWidth int) string {
	if totalWidth <= 0 {
		totalWidth = 80
	}
	widths := computeWidths(totalWidth, flakyColumns)

	var lines []string
	lines = append(lines, renderHeader(flakyColumns, widths, -1, false))

	badStyle := tableCellStyle.Foreground(colorBad).Bold(true)
	poorStyle := tableCellStyle.Foreground(colorWarn)

	end := min(offset+visible, len(rows))
	for i := offset; i < end; i++ {
		m := rows[i]
		cells := []string{
			m.User,
			m.CaptureDevice,
			fmt.Sprintf("%d", m.Incidents),
			fmt.Sprintf("%d", m.DistinctCalls),
			fmt.Sprintf("%.1f", m.WorstConcealedPct),
			fmt.Sprintf("%.1f", m.AvgConcealedPct),
			m.Severity,
		}
		var sevStyle *lipgloss.Style
		switch m.Severity {
		case "Bad":
			sevStyle = &badStyle
		case "Poor":
			sevStyle = &poorStyle
		}
		perCell := []*lipgloss.Style{nil, nil, nil, nil, nil, nil, sevStyle}
		lines = append(lines, renderRow(widths, cells, i == cursor, perCell))
	}
	return strings.Join(lines, "\n")
}
