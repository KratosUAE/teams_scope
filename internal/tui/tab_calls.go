package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"teams_con/internal/store"
)

// callsSortKey identifies one of the columns the user can sort the calls
// table by. Pressing the same hotkey twice toggles ascending/descending.
type callsSortKey int

const (
	callsSortTime callsSortKey = iota
	callsSortDuration
	callsSortVerdict
	callsSortParticipants
	callsSortOrganizer
	callsSortWorstUser
)

// callsChromeRows is the number of vertical rows reserved for non-table
// chrome when no filter is active: tab bar (1), blank after tab bar (1),
// table header (1), blank after table (1), help line (1), blank before
// status (1), status bar (1) = 7. When the UPN filter banner is shown,
// add one row for the banner itself.
const callsChromeRows = 7

func (t *callsTab) visibleRows() int {
	if t.height <= 0 {
		return 20
	}
	chrome := callsChromeRows
	if t.upnFilter != "" {
		chrome++
	}
	if v := t.height - chrome; v >= 5 {
		return v
	}
	return 5
}

// callsFetchTimeout bounds the ListCalls Cmd independent of the root
// context — the TUI should surface a timeout error rather than sit on a
// hung request for minutes.
const callsFetchTimeout = 30 * time.Second

// callsTab is the "list of calls" page. It owns the currently displayed
// rows, the cursor, and an optional UPN filter set by tab_users when the
// user drills in from the users tab.
type callsTab struct {
	client *Client

	calls     []store.Call
	cursor    int
	offset    int // top-of-window index for scroll
	upnFilter string

	sortKey  callsSortKey
	sortDesc bool

	windowDays int // 1, 7, 30; 0 means server default

	loading bool
	err     error

	width  int
	height int
}

func newCallsTab(c *Client) *callsTab {
	return &callsTab{client: c, sortKey: callsSortTime, sortDesc: true, windowDays: 7}
}

func (t *callsTab) Title() string { return "Calls" }

// Init kicks off the initial fetch. The command is wrapped in a closure so
// the fetch itself runs inside bubbletea's goroutine pool, not during
// model construction.
func (t *callsTab) Init() tea.Cmd {
	t.loading = true
	return t.fetchCmd()
}

// ApplyFilter mutates the UPN filter in place. The caller (app.go) is
// expected to issue a follow-up refresh Cmd — ApplyFilter itself returns
// nothing so it can be called inside a message handler without building
// Cmd chains.
func (t *callsTab) ApplyFilter(upn string) {
	t.upnFilter = upn
	t.cursor = 0
	t.offset = 0
}

// fetchCmd builds a tea.Cmd that executes ListCalls with the current
// filter state and yields a callsLoadedMsg.
func (t *callsTab) fetchCmd() tea.Cmd {
	upn := t.upnFilter
	days := t.windowDays
	client := t.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callsFetchTimeout)
		defer cancel()
		p := ListCallsParams{Upn: upn}
		if days > 0 {
			now := time.Now()
			f := now.Add(-time.Duration(days) * 24 * time.Hour)
			p.From, p.To = &f, &now
		}
		calls, err := client.ListCalls(ctx, p)
		return callsLoadedMsg{calls: calls, err: err}
	}
}

func (t *callsTab) setWindow(days int) (tabView, tea.Cmd) {
	if t.windowDays == days {
		return t, nil
	}
	t.windowDays = days
	t.loading = true
	return t, t.fetchCmd()
}

func (t *callsTab) Update(msg tea.Msg) (tabView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width = msg.Width
		t.height = msg.Height

	case callsLoadedMsg:
		t.loading = false
		t.err = msg.err
		t.calls = msg.calls
		t.sortCalls()
		if t.cursor >= len(t.calls) {
			t.cursor = 0
		}
		t.offset = 0

	case RefreshMsg:
		t.loading = true
		return t, t.fetchCmd()

	case SwitchToCallsMsg:
		// The root Model also calls ApplyFilter directly before forwarding
		// this message to guarantee ordering, but we handle it here too so
		// a stray message can't desync the filter.
		t.ApplyFilter(msg.UPN)
		t.loading = true
		return t, t.fetchCmd()

	case tea.KeyPressMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

func (t *callsTab) handleKey(msg tea.KeyPressMsg) (tabView, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if t.cursor > 0 {
			t.cursor--
			if t.cursor < t.offset {
				t.offset = t.cursor
			}
		}
	case "down", "j":
		if t.cursor < len(t.calls)-1 {
			t.cursor++
			vis := t.visibleRows()
			if t.cursor >= t.offset+vis {
				t.offset = t.cursor - vis + 1
			}
		}
	case "c":
		if t.upnFilter != "" {
			t.ApplyFilter("")
			t.loading = true
			return t, t.fetchCmd()
		}
	case "t":
		t.applySort(callsSortTime)
		return t, nil
	case "d":
		t.applySort(callsSortDuration)
		return t, nil
	case "v":
		t.applySort(callsSortVerdict)
		return t, nil
	case "n":
		t.applySort(callsSortParticipants)
		return t, nil
	case "o":
		t.applySort(callsSortOrganizer)
		return t, nil
	case "w":
		t.applySort(callsSortWorstUser)
		return t, nil
	case "D":
		return t.setWindow(1)
	case "W":
		return t.setWindow(7)
	case "M":
		return t.setWindow(30)
	case "enter":
		if len(t.calls) == 0 {
			return t, nil
		}
		id := t.calls[t.cursor].CallId
		return t, func() tea.Msg { return OpenDrillMsg{CallId: id} }
	}
	return t, nil
}

// applySort cycles direction when the same key is pressed twice, otherwise
// switches to the new key with a sensible default direction (descending for
// numeric columns, ascending for alphabetical ones).
func (t *callsTab) applySort(key callsSortKey) {
	if t.sortKey == key {
		t.sortDesc = !t.sortDesc
	} else {
		t.sortKey = key
		t.sortDesc = key == callsSortTime || key == callsSortDuration ||
			key == callsSortVerdict || key == callsSortParticipants
	}
	t.sortCalls()
	t.cursor = 0
	t.offset = 0
}

// verdictRank maps the verdict vocabulary to a numeric scale so sort can
// treat Bad as "biggest". Unknown values rank lowest so they cluster
// together regardless of direction.
func verdictRank(v string) int {
	switch v {
	case "Bad":
		return 3
	case "Poor":
		return 2
	case "Good":
		return 1
	default:
		return 0
	}
}

func (t *callsTab) sortCalls() {
	less := func(i, j int) bool {
		a, b := t.calls[i], t.calls[j]
		var lt bool
		switch t.sortKey {
		case callsSortTime:
			lt = a.StartTimeUtc.Before(b.StartTimeUtc)
		case callsSortDuration:
			lt = a.DurationSec < b.DurationSec
		case callsSortVerdict:
			lt = verdictRank(a.Verdict) < verdictRank(b.Verdict)
		case callsSortParticipants:
			lt = a.ParticipantCount < b.ParticipantCount
		case callsSortOrganizer:
			lt = strings.ToLower(a.Organizer) < strings.ToLower(b.Organizer)
		case callsSortWorstUser:
			lt = strings.ToLower(a.WorstUser) < strings.ToLower(b.WorstUser)
		}
		if t.sortDesc {
			return !lt
		}
		return lt
	}
	sort.SliceStable(t.calls, less)
}

func (t *callsTab) View() string {
	var b strings.Builder

	if t.upnFilter != "" {
		header := fmt.Sprintf("filter: %s  (press c to clear)", t.upnFilter)
		b.WriteString(headerStyle.Render(header))
		b.WriteString("\n")
	}

	switch {
	case t.loading && len(t.calls) == 0:
		b.WriteString(helpStyle.Render("loading calls..."))
		return b.String()
	case t.err != nil:
		b.WriteString(errorStyle.Render("error: " + t.err.Error()))
		return b.String()
	case len(t.calls) == 0:
		b.WriteString(helpStyle.Render("no calls in window"))
		return b.String()
	}

	b.WriteString(renderCallsTable(t.calls, t.cursor, t.offset, t.visibleRows(), t.width, int(t.sortKey), t.sortDesc))
	b.WriteString("\n")
	b.WriteString(renderHotkeyHelp(fmt.Sprintf("↑/↓ move  enter drill  r refresh  c clear filter  ·  window: %dd (D/W/M)  ·  sort:", t.windowDays), []hotkey{
		{"t", "time"}, {"d", "dur"}, {"v", "verdict"},
		{"n", "count"}, {"o", "organizer"}, {"w", "worst"},
	}))
	return b.String()
}

// callsColumns defines the calls-table layout. Min widths fit the header
// title plus its sort indicator and a 1ch padding margin. WorstStream gets
// the largest weight because the metric string is the most informative
// cell and benefits from any free horizontal space on wide terminals.
var callsColumns = []column{
	{title: "Time", key: "t", min: 14, weight: 0},
	{title: "Dur", key: "d", min: 9, weight: 0},
	{title: "Verdict", key: "v", min: 11, weight: 0},
	{title: "N", key: "n", min: 5, weight: 0},
	{title: "Organizer", key: "o", min: 14, weight: 1},
	{title: "Worst", min: 12, weight: 0},
	{title: "WorstUser", key: "w", min: 14, weight: 1},
	{title: "WorstStream", min: 18, weight: 3},
}

func renderCallsTable(rows []store.Call, cursor, offset, visible, totalWidth, sortIdx int, sortDesc bool) string {
	if totalWidth <= 0 {
		totalWidth = 100
	}
	widths := computeWidths(totalWidth, callsColumns)

	var lines []string
	lines = append(lines, renderHeader(callsColumns, widths, sortIdx, sortDesc))

	verdictStyle := tableCellStyle.Bold(true)

	end := min(offset+visible, len(rows))
	for i := offset; i < end; i++ {
		row := rows[i]
		cells := []string{
			formatTime(row.StartTimeUtc),
			formatDuration(row.DurationSec),
			row.Verdict,
			fmt.Sprintf("%d", row.ParticipantCount),
			row.Organizer,
			formatWorstSummary(row.WorstDirection, row.WorstStreamLabel),
			row.WorstUser,
			formatWorstStream(row.WorstStream),
		}
		// Verdict column gets its own color.
		styled := verdictStyle.Foreground(VerdictColor(row.Verdict))
		perCell := []*lipgloss.Style{nil, nil, &styled, nil, nil, nil, nil, nil}
		lines = append(lines, renderRow(widths, cells, i == cursor, perCell))
	}

	return strings.Join(lines, "\n")
}

// formatTime renders a UTC timestamp in the short "Jan 02 15:04" form.
// Longer ISO strings consume too much horizontal real estate for a 16ch
// column and the TUI is only used interactively (no log ingest).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("Jan 02 15:04")
}

// formatWorstSummary builds the compact "Worst" column cell — direction
// arrow + short stream-label — that lets you see at a glance whether the
// problem was upload or download and on which media (audio/video/screen).
//
// Direction vocabulary (per quality.CallRow.WorstDirection):
//   recv  →  ↓  (download — bytes coming into the user's device)
//   send  →  ↑  (upload   — bytes leaving the user's device, e.g. mic)
//   ?     →  ·  (unknown / server leg)
//
// Stream label is normalized to a 4-char alias so the column stays narrow:
//   audio  → audio   video → video   screen sharing → scr   data → data
func formatWorstSummary(direction, label string) string {
	if direction == "" && label == "" {
		return ""
	}
	var arrow string
	switch direction {
	case "recv":
		arrow = "↓"
	case "send":
		arrow = "↑"
	default:
		arrow = "·"
	}
	return arrow + " " + formatStreamLabelShort(label)
}

// formatWorstStream takes the raw "loss=X;jitter=Yms;rtt=Zms;mosDeg=W"
// blob the quality package emits and produces a compact, human-readable
// version: empty fields are dropped, the loss value gets a "%" suffix, and
// the keys use short labels ("jit"/"rtt"/"mos"). Field order is preserved.
//
// Example in:  "loss=;jitter=71ms;rtt=83ms;mosDeg="
// Example out: "jit 71ms · rtt 83ms"
func formatWorstStream(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ key, val string }
	parsed := make([]kv, 0, 4)
	for _, segment := range strings.Split(raw, ";") {
		eq := strings.IndexByte(segment, '=')
		if eq < 0 {
			continue
		}
		k := segment[:eq]
		v := segment[eq+1:]
		// jitter/rtt always carry a trailing "ms" — discard a value that
		// is just "ms" with no number, which means the underlying metric
		// was nil (PowerShell parity with empty string interpolation).
		if v == "" || v == "ms" {
			continue
		}
		parsed = append(parsed, kv{k, v})
	}
	if len(parsed) == 0 {
		return ""
	}
	out := make([]string, 0, len(parsed))
	for _, p := range parsed {
		switch p.key {
		case "loss":
			out = append(out, "loss "+p.val+"%")
		case "jitter":
			out = append(out, "jit "+p.val)
		case "rtt":
			out = append(out, "rtt "+p.val)
		case "mosDeg":
			out = append(out, "mos "+p.val)
		default:
			out = append(out, p.key+" "+p.val)
		}
	}
	return strings.Join(out, " · ")
}

// formatDuration pretty-prints an integer second count as "Xm YYs" or
// "YYs" when it fits. Anything above an hour gets "Hh MMm".
func formatDuration(sec int) string {
	if sec <= 0 {
		return "-"
	}
	d := time.Duration(sec) * time.Second
	switch {
	case d >= time.Hour:
		h := d / time.Hour
		m := (d % time.Hour) / time.Minute
		return fmt.Sprintf("%dh%02dm", h, m)
	case d >= time.Minute:
		m := d / time.Minute
		s := (d % time.Minute) / time.Second
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// truncate cuts s to at most n runes, appending "…" when characters are
// dropped. Operates on runes so multi-byte UTF-8 sequences (e.g. Cyrillic
// UPNs) are never split mid-character. n ≤ 0 returns ""; n == 1 returns "…"
// rather than a partial rune to keep the cell width predictable.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}
