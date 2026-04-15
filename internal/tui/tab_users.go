package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"teams_con/internal/store"
)

const (
	usersChromeRows   = 7
	usersFetchTimeout = 30 * time.Second
)

func (t *usersTab) visibleRows() int {
	if t.height <= 0 {
		return 20
	}
	if v := t.height - usersChromeRows; v >= 5 {
		return v
	}
	return 5
}

// Portrait-view styles. Pre-built once at package init so renderPortrait
// does not allocate lipgloss.Style values on every frame — the users tab
// re-renders on every key press.
var (
	portraitHeaderStyle      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	portraitSectionStyle     = lipgloss.NewStyle().Foreground(colorHeader).Bold(true)
	portraitMutedStyle       = lipgloss.NewStyle().Foreground(colorMuted)
	portraitDeviceBadStyle   = lipgloss.NewStyle().Foreground(colorBad)
	patternBadStyle          = lipgloss.NewStyle().Foreground(colorBad).Bold(true)
	patternHealthyStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	patternInsufficientStyle = lipgloss.NewStyle().Foreground(colorSubtle)
	patternMixedStyle        = lipgloss.NewStyle().Foreground(colorWarn)
)

// usersMode is the sub-view state of the users tab. List is the default
// table view; portrait is the per-user health dossier view opened via
// shift+P or an incoming OpenUserPortraitMsg.
type usersMode int

const (
	usersModeList usersMode = iota
	usersModePortrait
)

// usersSortKey identifies the column the users table is currently sorted
// by. Pressing the same hotkey twice toggles ascending/descending.
type usersSortKey int

const (
	usersSortUPN usersSortKey = iota
	usersSortTotal
	usersSortGood
	usersSortPoor
	usersSortBad
)

// usersTab is the "per-user stats" page. It fetches once on Init and on
// every RefreshMsg; enter on a row dispatches a SwitchToCallsMsg so the
// root Model can flip to tab_calls with a pre-applied UPN filter.
//
// Pressing shift+P on a row opens the portrait sub-view, which fetches
// /users/{upn}/health and renders a multi-section dossier. The list state
// (users, cursor, sort) is preserved so pressing b/backspace restores the
// previous view instantly.
type usersTab struct {
	client *Client

	users  []store.UserStat
	cursor int
	offset int

	sortKey  usersSortKey
	sortDesc bool

	windowDays int // 1, 7, or 30; 0 means server default (7)

	loading bool
	err     error

	// Portrait sub-view state.
	mode              usersMode
	portraitUPN       string // currently-requested upn
	portraitLoadedUPN string // last successfully loaded upn
	portraitFetchSeq  int    // monotonic counter; each openPortrait increments it
	portraitReport    *UserHealthReportDTO
	portraitLoading   bool
	portraitErr       error

	width  int
	height int
}

func newUsersTab(c *Client) *usersTab {
	return &usersTab{client: c, sortKey: usersSortUPN, sortDesc: false, windowDays: 7}
}

func (t *usersTab) Title() string { return "Users" }

func (t *usersTab) Init() tea.Cmd {
	t.loading = true
	return t.fetchCmd()
}

func (t *usersTab) fetchCmd() tea.Cmd {
	client := t.client
	days := t.windowDays
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), usersFetchTimeout)
		defer cancel()
		var from, to *time.Time
		if days > 0 {
			now := time.Now()
			f := now.Add(-time.Duration(days) * 24 * time.Hour)
			from, to = &f, &now
		}
		users, err := client.ListUsers(ctx, from, to)
		return usersLoadedMsg{users: users, err: err}
	}
}

func (t *usersTab) Update(msg tea.Msg) (tabView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width = msg.Width
		t.height = msg.Height

	case usersLoadedMsg:
		t.loading = false
		t.err = msg.err
		t.users = msg.users
		t.sortUsers()
		if t.cursor >= len(t.users) {
			t.cursor = 0
		}
		t.offset = 0

	case OpenUserPortraitMsg:
		return t.openPortrait(msg.UPN)

	case userHealthFetchedMsg:
		// Stale-guard by monotonic seq so a stale response from a cancelled
		// fetch cannot land in place of the real one. Reopening the same
		// UPN bumps the seq just like reopening a different one.
		if msg.seq != t.portraitFetchSeq {
			return t, nil
		}
		t.portraitLoading = false
		t.portraitErr = msg.err
		if msg.err == nil {
			t.portraitReport = msg.report
			t.portraitLoadedUPN = msg.upn
		}
		return t, nil

	case RefreshMsg:
		if t.mode == usersModePortrait && t.portraitUPN != "" {
			t.portraitFetchSeq++
			t.portraitLoading = true
			t.portraitErr = nil
			return t, t.fetchPortraitCmd(t.portraitUPN, t.portraitFetchSeq)
		}
		t.loading = true
		return t, t.fetchCmd()

	case tea.KeyPressMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

// openPortrait flips the tab into portrait mode for upn, re-using the
// cached report when the caller asks for the same upn twice in a row so
// a Flaky → Portrait → Flaky → Portrait loop is instant. Every open
// bumps portraitFetchSeq so any in-flight reply to a previous open is
// dropped by the stale guard in Update.
func (t *usersTab) openPortrait(upn string) (tabView, tea.Cmd) {
	if upn == "" {
		return t, nil
	}
	t.mode = usersModePortrait
	t.portraitUPN = upn
	t.portraitErr = nil
	if t.portraitLoadedUPN == upn && t.portraitReport != nil {
		// Cached hit — bump the seq anyway so any pending stale response
		// for this UPN is still rejected when it arrives.
		t.portraitFetchSeq++
		t.portraitLoading = false
		return t, nil
	}
	t.portraitFetchSeq++
	t.portraitReport = nil
	t.portraitLoading = true
	return t, t.fetchPortraitCmd(upn, t.portraitFetchSeq)
}

func (t *usersTab) fetchPortraitCmd(upn string, seq int) tea.Cmd {
	client := t.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), usersFetchTimeout)
		defer cancel()
		report, err := client.GetUserHealth(ctx, upn, nil, nil)
		return userHealthFetchedMsg{seq: seq, upn: upn, report: report, err: err}
	}
}

func (t *usersTab) handleKey(msg tea.KeyPressMsg) (tabView, tea.Cmd) {
	if t.mode == usersModePortrait {
		return t.handlePortraitKey(msg)
	}
	switch msg.String() {
	case "up", "k":
		if t.cursor > 0 {
			t.cursor--
			if t.cursor < t.offset {
				t.offset = t.cursor
			}
		}
	case "down", "j":
		if t.cursor < len(t.users)-1 {
			t.cursor++
			vis := t.visibleRows()
			if t.cursor >= t.offset+vis {
				t.offset = t.cursor - vis + 1
			}
		}
	case "u":
		t.applySort(usersSortUPN)
		return t, nil
	case "n":
		t.applySort(usersSortTotal)
		return t, nil
	case "g":
		t.applySort(usersSortGood)
		return t, nil
	case "p":
		t.applySort(usersSortPoor)
		return t, nil
	case "b":
		t.applySort(usersSortBad)
		return t, nil
	case "D":
		return t.setWindow(1)
	case "W":
		return t.setWindow(7)
	case "M":
		return t.setWindow(30)
	case "P":
		// Shift+P on the list opens the portrait sub-view for the cursor.
		// Lowercase p is still the Poor-sort hotkey.
		if len(t.users) == 0 {
			return t, nil
		}
		return t.openPortrait(t.users[t.cursor].Upn)
	case "enter":
		if len(t.users) == 0 {
			return t, nil
		}
		upn := t.users[t.cursor].Upn
		return t, func() tea.Msg { return SwitchToCallsMsg{UPN: upn} }
	}
	return t, nil
}

// handlePortraitKey handles keys while the portrait sub-view is active.
// The list's own hotkeys (sort, cursor nav) are intentionally swallowed —
// pressing b or backspace restores list mode, everything else is a no-op.
func (t *usersTab) handlePortraitKey(msg tea.KeyPressMsg) (tabView, tea.Cmd) {
	switch msg.String() {
	case "b", "backspace":
		t.mode = usersModeList
		t.portraitErr = nil
		return t, nil
	}
	return t, nil
}

// setWindow switches the fetch window and triggers a reload. A no-op when
// the window is already at the requested value.
func (t *usersTab) setWindow(days int) (tabView, tea.Cmd) {
	if t.windowDays == days {
		return t, nil
	}
	t.windowDays = days
	t.loading = true
	return t, t.fetchCmd()
}

// applySort cycles direction when the same key is pressed twice, otherwise
// switches to the new key. Counts default to descending; UPN defaults to
// ascending so the alphabetical view matches the server-side default.
func (t *usersTab) applySort(key usersSortKey) {
	if t.sortKey == key {
		t.sortDesc = !t.sortDesc
	} else {
		t.sortKey = key
		t.sortDesc = key != usersSortUPN
	}
	t.sortUsers()
	t.cursor = 0
	t.offset = 0
}

func (t *usersTab) sortUsers() {
	less := func(i, j int) bool {
		a, b := t.users[i], t.users[j]
		var lt bool
		switch t.sortKey {
		case usersSortUPN:
			lt = strings.ToLower(a.Upn) < strings.ToLower(b.Upn)
		case usersSortTotal:
			lt = a.CallCount < b.CallCount
		case usersSortGood:
			lt = a.GoodCount < b.GoodCount
		case usersSortPoor:
			lt = a.PoorCount < b.PoorCount
		case usersSortBad:
			lt = a.BadCount < b.BadCount
		}
		if t.sortDesc {
			return !lt
		}
		return lt
	}
	sort.SliceStable(t.users, less)
}

func (t *usersTab) View() string {
	if t.mode == usersModePortrait {
		return t.renderPortrait()
	}

	var b strings.Builder

	switch {
	case t.loading && len(t.users) == 0:
		b.WriteString(helpStyle.Render("loading users..."))
		return b.String()
	case t.err != nil:
		b.WriteString(errorStyle.Render("error: " + t.err.Error()))
		return b.String()
	case len(t.users) == 0:
		b.WriteString(helpStyle.Render("no users in window"))
		return b.String()
	}

	b.WriteString(renderUsersTable(t.users, t.cursor, t.offset, t.visibleRows(), t.width, int(t.sortKey), t.sortDesc))
	b.WriteString("\n")
	b.WriteString(renderHotkeyHelp(fmt.Sprintf("↑/↓ move  enter → Calls  P portrait  r refresh  ·  window: %dd (D/W/M)  ·  sort:", t.windowDays), []hotkey{
		{"u", "upn"}, {"n", "total"}, {"g", "good"}, {"p", "poor"}, {"b", "bad"},
	}))
	return b.String()
}

// renderPortrait composes the user-health dossier as a series of labeled
// sections. It is static text, no cursor — the purpose is "one page
// summary of everything we know about this user".
func (t *usersTab) renderPortrait() string {
	var b strings.Builder

	b.WriteString(portraitHeaderStyle.Render(fmt.Sprintf("Portrait: %s", t.portraitUPN)))
	b.WriteString("\n")

	switch {
	case t.portraitLoading:
		b.WriteString(helpStyle.Render("loading portrait..."))
		b.WriteString("\n\n")
		b.WriteString(renderHotkeyHelp("", []hotkey{{"b", "back"}}))
		return b.String()
	case t.portraitErr != nil:
		b.WriteString(errorStyle.Render("error: " + t.portraitErr.Error()))
		b.WriteString("\n\n")
		b.WriteString(renderHotkeyHelp("", []hotkey{{"b", "back"}}))
		return b.String()
	case t.portraitReport == nil:
		b.WriteString(helpStyle.Render("no report"))
		b.WriteString("\n\n")
		b.WriteString(renderHotkeyHelp("", []hotkey{{"b", "back"}}))
		return b.String()
	}

	r := t.portraitReport

	// Header line: window + total + verdict breakdown + pattern.
	window := fmt.Sprintf("%s .. %s",
		r.WindowFrom.Format("2006-01-02 15:04"),
		r.WindowTo.Format("2006-01-02 15:04"))
	summary := fmt.Sprintf("window: %s   calls: %d   good=%d  poor=%d  bad=%d   pattern: %s",
		window, r.TotalCalls,
		r.ByVerdict.Good, r.ByVerdict.Poor, r.ByVerdict.Bad,
		renderPattern(r.Pattern),
	)
	b.WriteString(tableCellStyle.Render(summary))
	b.WriteString("\n")

	// Phase 3: one-line peer baseline summary. Only shown when a real
	// cohort exists (>=3 peers); insufficient_peers or nil suppresses
	// the line entirely so the header stays compact.
	if r.PeerBaseline != nil && r.PeerBaseline.CohortSize >= 3 {
		peerLine := fmt.Sprintf("  peers: %d  ·  their bad ratio %.0f%%  ·  yours %.0f%%  →  %s",
			r.PeerBaseline.CohortSize,
			r.PeerBaseline.CohortBadRatio*100,
			r.PeerBaseline.TargetBadRatio*100,
			r.PeerBaseline.Assessment,
		)
		b.WriteString(portraitMutedStyle.Render(peerLine))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if r.TotalCalls == 0 {
		b.WriteString(helpStyle.Render("no calls in window"))
		b.WriteString("\n\n")
		b.WriteString(renderHotkeyHelp("", []hotkey{{"b", "back"}, {"r", "refresh"}}))
		return b.String()
	}

	// Phase 2: operator-maintained annotation card. Rendered above the
	// Capture devices section so the context ("vip", "escalated",
	// "uses personal macbook") is the first thing the operator sees.
	// Nil Card (no annotation exists) suppresses the section entirely.
	if r.Card != nil {
		b.WriteString(portraitSectionStyle.Render("Card"))
		b.WriteString("\n")
		b.WriteString(tableCellStyle.Render("  location: " + dashIfEmpty(r.Card.Location)))
		b.WriteString("\n")
		b.WriteString(tableCellStyle.Render("  tags: " + dashIfEmpty(strings.Join(r.Card.Tags, ", "))))
		b.WriteString("\n")
		notes := r.Card.Notes
		if notes == "" {
			notes = "-"
		} else {
			// Cap the portrait excerpt at 200 runes so a long note does
			// not blow the height budget. The full notes value stays on
			// the DTO for JSON clients.
			const portraitNotesMax = 200
			runes := []rune(notes)
			if len(runes) > portraitNotesMax {
				notes = string(runes[:portraitNotesMax]) + "..."
			}
		}
		b.WriteString(tableCellStyle.Render("  notes: " + notes))
		b.WriteString("\n\n")
	}

	// Devices — most relevant to the "bad mic" story.
	b.WriteString(portraitSectionStyle.Render("Capture devices"))
	b.WriteString("\n")
	if len(r.Devices) == 0 {
		b.WriteString(portraitMutedStyle.Render("  (none)"))
	} else {
		for _, d := range r.Devices {
			line := fmt.Sprintf("  %-32s  calls=%d  bad=%d  avg concealed=%.1f%%  worst=%.1f%%",
				truncate(d.Device, 32), d.CallCount, d.BadCallCount, d.AvgConcealedPct, d.WorstConcealedPct)
			if d.AvgConcealedPct >= 10.0 || d.WorstConcealedPct >= 15.0 {
				b.WriteString(portraitDeviceBadStyle.Render(line))
			} else {
				b.WriteString(tableCellStyle.Render(line))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// Subnets — surfaces wifi vs wired / office vs home / remote.
	b.WriteString(portraitSectionStyle.Render("Subnets"))
	b.WriteString("\n")
	if len(r.Subnets) == 0 {
		b.WriteString(portraitMutedStyle.Render("  (none)"))
	} else {
		for _, s := range r.Subnets {
			// Phase 1 enrichment: when the resolver matched a configured
			// subnet, render the friendly name on the main line and the
			// office/kind on a continuation line. Unknown blocks fall
			// back to the bare CIDR with the existing single-line shape.
			var label string
			if s.Name != "" {
				label = fmt.Sprintf("  %s (%s)", s.Name, s.Subnet)
			} else {
				label = fmt.Sprintf("  (%s)", s.Subnet)
			}
			b.WriteString(tableCellStyle.Render(label))
			b.WriteString("\n")
			// M3: render kind and connType as separate columns so the
			// operator-configured label (Kind) is never silently shadowed
			// by the Teams-reported stream value (ConnType).
			meta := fmt.Sprintf("    office=%s  kind=%s  connType=%s  calls=%d",
				dashIfEmpty(s.Office),
				dashIfEmpty(s.Kind),
				dashIfEmpty(s.ConnType),
				s.CallCount)
			b.WriteString(portraitMutedStyle.Render(meta))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// Platforms + clients — side-by-side single column for now.
	b.WriteString(portraitSectionStyle.Render("Platforms"))
	b.WriteString("\n")
	if len(r.Platforms) == 0 {
		b.WriteString(portraitMutedStyle.Render("  (none)"))
	} else {
		for _, p := range r.Platforms {
			line := fmt.Sprintf("  %-16s  calls=%d", p.Platform, p.CallCount)
			b.WriteString(tableCellStyle.Render(line))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	b.WriteString(portraitSectionStyle.Render("Clients"))
	b.WriteString("\n")
	if len(r.Clients) == 0 {
		b.WriteString(portraitMutedStyle.Render("  (none)"))
	} else {
		for _, c := range r.Clients {
			line := fmt.Sprintf("  %-64s  calls=%d", truncate(c.UserAgent, 64), c.CallCount)
			b.WriteString(tableCellStyle.Render(line))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	// Top issues (Phase 5) — Teams diagnostic tags aggregated from
	// StreamRow.Issues. Section is skipped entirely when the server
	// reported no issues for this user so legacy crawler output does not
	// leave an empty "Top issues" stub. Reuses existing portrait styles.
	if len(r.TopIssues) > 0 {
		b.WriteString(portraitSectionStyle.Render("Top issues"))
		b.WriteString("\n")
		for _, ic := range r.TopIssues {
			line := fmt.Sprintf("  %-32s  calls=%d", truncate(ic.Issue, 32), ic.CallCount)
			b.WriteString(portraitMutedStyle.Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Avg metrics — one compact line per direction.
	b.WriteString(portraitSectionStyle.Render("Avg metrics"))
	b.WriteString("\n")
	b.WriteString(tableCellStyle.Render("  send: " + formatMetricLine(r.AvgMetrics.JitterSendMs, r.AvgMetrics.LossSendPct, r.AvgMetrics.RttSendMs)))
	b.WriteString("\n")
	b.WriteString(tableCellStyle.Render("  recv: " + formatMetricLine(r.AvgMetrics.JitterRecvMs, r.AvgMetrics.LossRecvPct, r.AvgMetrics.RttRecvMs)))
	b.WriteString("\n\n")

	b.WriteString(renderHotkeyHelp("", []hotkey{{"b", "back"}, {"r", "refresh"}}))
	return b.String()
}

// renderPattern colours the classification label so chronic/broken
// patterns stand out from healthy/insufficient.
func renderPattern(p string) string {
	switch p {
	case "chronic_mic", "remote_path", "wifi_only_issue", "wifi_suspected", "org_wide_issue":
		return patternBadStyle.Render(p)
	case "healthy":
		return patternHealthyStyle.Render(p)
	case "insufficient_data":
		return patternInsufficientStyle.Render(p)
	default:
		return patternMixedStyle.Render(p)
	}
}

// dashIfEmpty returns "-" when s is empty, else s. Used by the portrait
// renderer so absent labels render as a visual placeholder instead of a
// confusing blank gap.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatMetricLine renders jitter/loss/rtt for one direction. Nil pointers
// render as "-" so the reader can tell "no data" from a real zero reading.
func formatMetricLine(jitter, loss, rtt *float64) string {
	fmtPtr := func(p *float64, unit string) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprintf("%.1f%s", *p, unit)
	}
	return fmt.Sprintf("jitter=%s  loss=%s  rtt=%s",
		fmtPtr(jitter, "ms"), fmtPtr(loss, "%"), fmtPtr(rtt, "ms"))
}

// usersColumns: UPN takes all the slack; the count columns are fixed-width
// because they hold short integers.
var usersColumns = []column{
	{title: "UPN", key: "u", min: 14, weight: 1},
	{title: "Calls", key: "n", min: 9, weight: 0},
	{title: "Good", key: "g", min: 9, weight: 0},
	{title: "Poor", key: "p", min: 9, weight: 0},
	{title: "Bad", key: "b", min: 9, weight: 0},
}

func renderUsersTable(rows []store.UserStat, cursor, offset, visible, totalWidth, sortIdx int, sortDesc bool) string {
	if totalWidth <= 0 {
		totalWidth = 80
	}
	widths := computeWidths(totalWidth, usersColumns)

	var lines []string
	lines = append(lines, renderHeader(usersColumns, widths, sortIdx, sortDesc))

	goodStyle := tableCellStyle.Foreground(colorMuted)
	poorStyle := tableCellStyle.Foreground(colorWarn)
	badStyle := tableCellStyle.Foreground(colorBad).Bold(true)

	end := min(offset+visible, len(rows))
	for i := offset; i < end; i++ {
		row := rows[i]
		cells := []string{
			row.Upn,
			fmt.Sprintf("%d", row.CallCount),
			fmt.Sprintf("%d", row.GoodCount),
			fmt.Sprintf("%d", row.PoorCount),
			fmt.Sprintf("%d", row.BadCount),
		}
		perCell := []*lipgloss.Style{nil, nil, &goodStyle, &poorStyle, &badStyle}
		lines = append(lines, renderRow(widths, cells, i == cursor, perCell))
	}

	return strings.Join(lines, "\n")
}
