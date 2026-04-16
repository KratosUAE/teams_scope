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

// drillFetchTimeout bounds the GetCall Cmd, mirroring callsFetchTimeout.
const drillFetchTimeout = 30 * time.Second

// drillChromeRowsStats is the number of non-table rows reserved in stats
// view: tab bar (1), blank (1), header block (3 lines), blank (1), table
// header (1), blank (1), help (1), blank (1), status (1) = 11.
const drillChromeRowsStats = 11

// drillChromeRowsMatrix reserves rows for matrix view chrome: tab bar (1),
// blank (1), header block (3), blank (1), time ruler (1), blank (1),
// help (1), blank (1), status (1) = 11. Each user takes 2 rows so the
// visible row calculation halves the remaining space.
const drillChromeRowsMatrix = 11

const (
	drillMatrixMinBuckets = 30
	drillMatrixMaxBuckets = 120
)

// drillMatrixLabelPrefix is the column budget reserved on the left of
// each matrix row for cursor marker (2) + UPN label (28) + arrow (2).
const drillMatrixLabelPrefix = 32

// drillMatrixMinWidth is the smallest terminal width at which the matrix
// view can render. Derived from the prefix budget + minimum bucket count
// so it stays in sync if those constants change.
const drillMatrixMinWidth = drillMatrixLabelPrefix + drillMatrixMinBuckets

// drillView selects between the streams table and the user×time heatmap.
type drillView int

const (
	drillViewStats  drillView = iota // sortable streams table (default)
	drillViewMatrix                  // user × time heatmap
)

// drillSortKey identifies the sort column in stats view.
type drillSortKey int

const (
	drillSortSeverity drillSortKey = iota
	drillSortUser
	drillSortDirection
	drillSortLabel
	drillSortJitter
	drillSortLoss
	drillSortLossMax
	drillSortRtt
	drillSortSegment
)

// drillFilterMode cycles through visibility filters for both views.
type drillFilterMode int

const (
	drillFilterAll     drillFilterMode = iota
	drillFilterPoorBad                 // Poor + Bad only
	drillFilterBad                     // Bad only
)

// drillFetchedMsg delivers GetCall results. callID is carried through so
// the model can drop stale responses when the user navigates between
// different calls mid-fetch.
type drillFetchedMsg struct {
	callID  string
	call    *store.Call
	streams []store.StreamRow
	err     error
}

// drillMatrix is the projection used by the matrix view: users on the Y
// axis, time buckets on the X axis, one worst-verdict cell per (user,
// direction, bucket). It is recomputed on load, filter cycle and resize.
type drillMatrix struct {
	users   []string               // participant UPNs, alphabetical
	buckets int                    // number of time slices across width
	bucketW time.Duration          // duration of one bucket
	start   time.Time              // call start (anchor for bucketing)
	cells   map[string][2][]string // upn → [↑/↓][bucket] → verdict
	info    map[string]string      // upn → "wifi/win" — most-frequent conn/platform pair
}

// drillModel is the single tab model that hosts both stats and matrix
// views. One fetch per call id feeds both projections.
type drillModel struct {
	client *Client

	// Identity / fetch
	callID   string // last id we were asked to open
	loadedID string // id currently rendered (stale-fetch guard)
	loading  bool
	err      error

	// Data
	call    *store.Call
	streams []store.StreamRow // raw fetch result, never mutated after load

	// Stats view state
	sorted   []store.StreamRow
	cursor   int
	offset   int
	sortKey  drillSortKey
	sortDesc bool
	filter   drillFilterMode
	channel  string // "" = all, otherwise the formatStreamLabelShort alias

	// Matrix view state
	view      drillView
	matrix    drillMatrix
	matrixCur int

	// Layout
	width  int
	height int
}

func newDrillTab(c *Client) *drillModel {
	return &drillModel{
		client:   c,
		view:     drillViewStats,
		sortKey:  drillSortSeverity,
		sortDesc: true,
		filter:   drillFilterAll,
	}
}

func (m *drillModel) Title() string { return "Drill" }

// Init is a no-op: the drill tab is lazy — it only fetches once the user
// triggers OpenDrillMsg from the calls tab.
func (m *drillModel) Init() tea.Cmd { return nil }

func (m *drillModel) Update(msg tea.Msg) (tabView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.view == drillViewMatrix && m.call != nil {
			m.rebuildMatrix()
		}

	case OpenDrillMsg:
		if msg.CallId == "" {
			return m, nil
		}
		if msg.CallId == m.loadedID && m.call != nil {
			// Same call reopened — preserve scroll/sort/filter/view.
			m.callID = msg.CallId
			return m, nil
		}
		m.callID = msg.CallId
		m.loading = true
		m.err = nil
		m.call = nil
		m.streams = nil
		m.sorted = nil
		m.cursor = 0
		m.offset = 0
		m.matrixCur = 0
		return m, m.fetchCmd(msg.CallId)

	case RefreshMsg:
		if m.callID == "" {
			return m, nil
		}
		m.loading = true
		return m, m.fetchCmd(m.callID)

	case drillFetchedMsg:
		if msg.callID != m.callID {
			return m, nil // stale response
		}
		m.loading = false
		m.err = msg.err
		m.call = msg.call
		m.streams = msg.streams
		m.loadedID = msg.callID
		if m.err == nil && m.call != nil {
			m.applySort()
			m.rebuildMatrix()
		}

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *drillModel) fetchCmd(id string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), drillFetchTimeout)
		defer cancel()
		call, streams, err := client.GetCall(ctx, id)
		return drillFetchedMsg{callID: id, call: call, streams: streams, err: err}
	}
}

func (m *drillModel) handleKey(msg tea.KeyPressMsg) (tabView, tea.Cmd) {
	switch msg.String() {
	case "b", "backspace":
		return m, func() tea.Msg { return BackToCallsMsg{} }
	case "v":
		if m.view == drillViewStats {
			m.view = drillViewMatrix
		} else {
			m.view = drillViewStats
		}
		return m, nil
	case "f":
		m.filter = (m.filter + 1) % 3
		m.applySort()
		m.rebuildMatrix()
		return m, nil
	case "c":
		m.cycleChannel()
		return m, nil
	case "up", "k":
		if m.view == drillViewStats {
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		} else {
			if m.matrixCur > 0 {
				m.matrixCur--
			}
		}
		return m, nil
	case "down", "j":
		if m.view == drillViewStats {
			if m.cursor < len(m.sorted)-1 {
				m.cursor++
				vis := m.visibleRows()
				if m.cursor >= m.offset+vis {
					m.offset = m.cursor - vis + 1
				}
			}
		} else {
			if m.matrixCur < len(m.matrix.users)-1 {
				m.matrixCur++
			}
		}
		return m, nil
	}

	// Stats-only sort keys; ignored in matrix view.
	if m.view == drillViewStats {
		switch msg.String() {
		case "s":
			m.toggleSort(drillSortSeverity, true)
		case "u":
			m.toggleSort(drillSortUser, false)
		case "y":
			m.toggleSort(drillSortDirection, false)
		case "a":
			m.toggleSort(drillSortLabel, false)
		case "i":
			m.toggleSort(drillSortJitter, true)
		case "l":
			m.toggleSort(drillSortLoss, true)
		case "x":
			m.toggleSort(drillSortLossMax, true)
		case "p":
			m.toggleSort(drillSortRtt, true)
		case "m":
			m.toggleSort(drillSortSegment, false)
		}
	}
	return m, nil
}

// toggleSort toggles the direction when the same key is pressed twice,
// otherwise switches key with the supplied default direction.
// Cursor and offset are reset inside applySort.
func (m *drillModel) toggleSort(key drillSortKey, defaultDesc bool) {
	if m.sortKey == key {
		m.sortDesc = !m.sortDesc
	} else {
		m.sortKey = key
		m.sortDesc = defaultDesc
	}
	m.applySort()
}

// applySort rebuilds m.sorted from the current filter + sort key and
// resets the cursor and scroll offset unconditionally so a sort or filter
// change always starts from the top of the result set.
func (m *drillModel) applySort() {
	m.sorted = m.filteredRows()
	m.sortStreams()
	m.cursor = 0
	m.offset = 0
}

// filteredRows returns the streams slice masked by the current verdict
// filter and channel filter. Both filters compose via AND.
func (m *drillModel) filteredRows() []store.StreamRow {
	if len(m.streams) == 0 {
		return nil
	}
	verdictPass := func(v string) bool {
		switch m.filter {
		case drillFilterAll:
			return true
		case drillFilterPoorBad:
			return v == "Poor" || v == "Bad"
		case drillFilterBad:
			return v == "Bad"
		}
		return true
	}
	channelPass := func(label string) bool {
		if m.channel == "" {
			return true
		}
		return formatStreamLabelShort(label) == m.channel
	}
	out := make([]store.StreamRow, 0, len(m.streams))
	for _, s := range m.streams {
		if !verdictPass(s.Verdict) || !channelPass(s.StreamLabel) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// channelMatches reports whether a stream label passes the current channel
// filter. Used by rebuildMatrix which intentionally ignores the verdict
// filter (matrix always shows full Good baseline) but honors the channel
// filter so the user can see audio-only or data-only patterns.
func (m *drillModel) channelMatches(label string) bool {
	if m.channel == "" {
		return true
	}
	return formatStreamLabelShort(label) == m.channel
}

// channelCycle is the cycling order for the `c` hotkey: all → audio →
// data → video → screen → all. The order matches what users typically
// look at first.
var channelCycle = []string{"", "audio", "data", "video", "scr"}

func (m *drillModel) cycleChannel() {
	idx := 0
	for i, c := range channelCycle {
		if c == m.channel {
			idx = i
			break
		}
	}
	m.channel = channelCycle[(idx+1)%len(channelCycle)]
	m.applySort()
	m.rebuildMatrix()
	m.matrixCur = 0
}

// sortStreams sorts m.sorted in place per the active sort key / direction.
func (m *drillModel) sortStreams() {
	less := func(i, j int) bool {
		a, b := m.sorted[i], m.sorted[j]
		var lt bool
		switch m.sortKey {
		case drillSortSeverity:
			lt = verdictRank(a.Verdict) < verdictRank(b.Verdict)
		case drillSortUser:
			lt = strings.ToLower(a.User) < strings.ToLower(b.User)
		case drillSortDirection:
			lt = a.Direction < b.Direction
		case drillSortLabel:
			lt = strings.ToLower(a.StreamLabel) < strings.ToLower(b.StreamLabel)
		case drillSortJitter:
			lt = floatPtrLess(a.AvgJitterMs, b.AvgJitterMs)
		case drillSortLoss:
			lt = floatPtrLess(a.AvgLossPct, b.AvgLossPct)
		case drillSortLossMax:
			lt = floatPtrLess(a.MaxLossPct, b.MaxLossPct)
		case drillSortRtt:
			lt = floatPtrLess(a.AvgRttMs, b.AvgRttMs)
		case drillSortSegment:
			lt = a.SegmentStart.Before(b.SegmentStart)
		}
		if m.sortDesc {
			return !lt
		}
		return lt
	}
	sort.SliceStable(m.sorted, less)
}

// rebuildMatrix re-projects m.streams (respecting the filter) onto the
// user×bucket grid used by the matrix view.
func (m *drillModel) rebuildMatrix() {
	m.matrix = drillMatrix{}
	if m.call == nil || len(m.streams) == 0 {
		return
	}
	// Reserve drillMatrixLabelPrefix cols on the left for the user label column.
	available := m.width - drillMatrixLabelPrefix
	if available < drillMatrixMinBuckets {
		available = drillMatrixMinBuckets
	}
	if available > drillMatrixMaxBuckets {
		available = drillMatrixMaxBuckets
	}

	duration := m.call.EndTimeUtc.Sub(m.call.StartTimeUtc)
	if duration <= 0 {
		duration = 60 * time.Second
	}
	bucketW := duration / time.Duration(available)
	if bucketW <= 0 {
		bucketW = time.Second
	}

	// Matrix view ignores the verdict filter (which would hide the Good
	// baseline and break visual patterns) but DOES honor the channel
	// filter so the user can isolate audio-only or data-only matrices.
	filtered := make([]store.StreamRow, 0, len(m.streams))
	for _, s := range m.streams {
		if m.channelMatches(s.StreamLabel) {
			filtered = append(filtered, s)
		}
	}
	users := uniqueParticipants(filtered)
	cells := make(map[string][2][]string, len(users))
	for _, u := range users {
		var pair [2][]string
		pair[0] = make([]string, available) // ↑ send
		pair[1] = make([]string, available) // ↓ recv
		cells[u] = pair
	}

	// Tally per-user (conn, platform) combinations across that user's
	// streams to surface the dominant one in the matrix prefix label.
	// Note: this tally runs over the same channel-filtered slice as the
	// cell builder, so when channel != "" the displayed conn/platform
	// reflects the dominant endpoint *for that channel*, which can differ
	// from the user's overall session if they use distinct devices for
	// audio vs data (rare but possible in dual-endpoint sessions).
	type infoKey struct{ conn, plat string }
	tally := make(map[string]map[infoKey]int, len(users))

	for _, s := range filtered {
		if s.User == "" || s.User == "<server/unknown>" {
			continue
		}
		// Tally device/conn for the prefix label, even if direction is unknown.
		if s.ConnType != "" || s.Platform != "" {
			if tally[s.User] == nil {
				tally[s.User] = make(map[infoKey]int, 2)
			}
			tally[s.User][infoKey{conn: s.ConnType, plat: s.Platform}]++
		}
		dirIdx := -1
		switch s.Direction {
		case "send", "upload":
			dirIdx = 0
		case "recv", "download":
			dirIdx = 1
		default:
			continue
		}
		pair, ok := cells[s.User]
		if !ok {
			continue
		}
		startB, endB := bucketRange(s.SegmentStart, s.SegmentEnd, m.call.StartTimeUtc, bucketW, available)
		for b := startB; b < endB; b++ {
			pair[dirIdx][b] = worseVerdict(pair[dirIdx][b], s.Verdict)
		}
		// The map value is a fixed-size [2][]string array, which Go copies on
		// read. Mutating pair[dirIdx][b] would still be visible without the
		// write-back (because the slice header inside pair points at the same
		// backing array), but we write pair back defensively so a future reader
		// does not remove the line thinking it is a no-op.
		cells[s.User] = pair
	}

	// Pick the dominant (conn, platform) per user and format as "wifi/win".
	info := make(map[string]string, len(users))
	for upn, combos := range tally {
		var bestKey infoKey
		bestCount := -1
		for k, c := range combos {
			if c > bestCount {
				bestKey = k
				bestCount = c
			}
		}
		conn := formatConn(bestKey.conn)
		plat := formatPlatform(bestKey.plat)
		info[upn] = conn + "/" + plat
	}

	m.matrix = drillMatrix{
		users:   users,
		buckets: available,
		bucketW: bucketW,
		start:   m.call.StartTimeUtc,
		cells:   cells,
		info:    info,
	}
	if len(users) == 0 {
		m.matrixCur = 0
	} else if m.matrixCur >= len(users) {
		m.matrixCur = len(users) - 1
	}
}

// visibleRows returns how many stream rows fit in stats view (or how many
// matrix user rows fit in matrix view — each user is 2 lines).
func (m *drillModel) visibleRows() int {
	if m.height <= 0 {
		return 20
	}
	switch m.view {
	case drillViewMatrix:
		if v := (m.height - drillChromeRowsMatrix) / 2; v >= 3 {
			return v
		}
		return 3
	default:
		if v := m.height - drillChromeRowsStats; v >= 5 {
			return v
		}
		return 5
	}
}

// View dispatches to the appropriate renderer for the active view mode.
func (m *drillModel) View() string {
	if m.callID == "" {
		return helpStyle.Render("no call selected — press enter on a row in the Calls tab")
	}
	if m.loading && m.call == nil {
		return helpStyle.Render("loading call " + truncate(m.callID, 16) + "...")
	}
	if m.err != nil {
		var b strings.Builder
		b.WriteString(errorStyle.Render("error: " + m.err.Error()))
		b.WriteString("\n")
		b.WriteString(m.renderHelpLine())
		return b.String()
	}
	if m.call == nil {
		return helpStyle.Render("no call data")
	}

	var b strings.Builder
	b.WriteString(m.renderHeaderBlock())
	b.WriteString("\n\n")
	switch m.view {
	case drillViewStats:
		b.WriteString(m.renderStatsTable())
	case drillViewMatrix:
		b.WriteString(m.renderMatrix())
	}
	b.WriteString("\n")
	b.WriteString(m.renderHelpLine())
	return b.String()
}

// renderHeaderBlock draws the 3-line header shared by both views.
func (m *drillModel) renderHeaderBlock() string {
	c := m.call
	good, poor, bad := streamSummaryCounts(m.streams)
	total := len(m.streams)

	modalities := "-"
	if len(c.Modalities) > 0 {
		modalities = strings.Join(c.Modalities, ",")
	}
	callType := c.CallType
	if callType == "" {
		callType = "-"
	}
	organizer := c.Organizer
	if organizer == "" {
		organizer = "-"
	}
	line1 := fmt.Sprintf("Call %s  %s · %s  ·  %s  ·  %d ppl",
		c.CallId, callType, modalities, organizer, c.ParticipantCount)

	verdictTok := lipgloss.NewStyle().
		Foreground(VerdictColor(c.Verdict)).
		Bold(true).
		Render(strings.ToUpper(c.Verdict))
	line2 := fmt.Sprintf("Started %s UTC  ·  Duration %s  ·  Verdict %s",
		formatTime(c.StartTimeUtc), formatDuration(c.DurationSec), verdictTok)

	badTok := lipgloss.NewStyle().Foreground(colorBad).Bold(true).Render(fmt.Sprintf("%d Bad", bad))
	poorTok := lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render(fmt.Sprintf("%d Poor", poor))
	goodTok := lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%d Good", good))

	viewName := "stats"
	if m.view == drillViewMatrix {
		viewName = "matrix"
	}
	channelName := m.channel
	if channelName == "" {
		channelName = "all"
	}
	filterName := "all"
	switch m.filter {
	case drillFilterPoorBad:
		filterName = "poor+bad"
	case drillFilterBad:
		filterName = "bad"
	}
	line3 := fmt.Sprintf("Streams: %s · %s · %s · %d total      view: %s   filter: %s   channel: %s",
		badTok, poorTok, goodTok, total, viewName, filterName, channelName)

	return lipgloss.JoinVertical(lipgloss.Left,
		headerStyle.Render(line1),
		line2,
		line3,
	)
}

// renderStatsTable builds the sortable streams table shown in stats view.
func (m *drillModel) renderStatsTable() string {
	if len(m.streams) == 0 {
		return helpStyle.Render("no streams in this call")
	}
	if len(m.sorted) == 0 {
		return helpStyle.Render("no streams match filter (press f to cycle)")
	}

	total := m.width
	if total <= 0 {
		total = 120
	}
	widths := computeWidths(total, drillColumns)

	var lines []string
	lines = append(lines, renderHeader(drillColumns, widths, int(m.sortKey), m.sortDesc))

	verdictStyle := tableCellStyle.Bold(true)

	vis := m.visibleRows()
	end := min(m.offset+vis, len(m.sorted))
	for i := m.offset; i < end; i++ {
		r := m.sorted[i]
		cells := []string{
			r.User,
			formatStreamDir(r.Direction),
			formatStreamLabelShort(r.StreamLabel),
			r.Verdict,
			formatJit(r.AvgJitterMs, r.MaxJitterMs),
			formatLoss(r.AvgLossPct),
			formatLoss(r.MaxLossPct),
			formatRtt(r.AvgRttMs),
			formatConn(r.ConnType),
			formatPlatform(r.Platform),
			r.Subnet,
			formatSegmentTime(r.SegmentStart, r.SegmentEnd),
		}
		styled := verdictStyle.Foreground(VerdictColor(r.Verdict))
		perCell := make([]*lipgloss.Style, len(drillColumns))
		perCell[3] = &styled
		lines = append(lines, renderRow(widths, cells, i == m.cursor, perCell))
	}
	return strings.Join(lines, "\n")
}

// renderMatrix builds the user×time heatmap shown in matrix view.
func (m *drillModel) renderMatrix() string {
	if len(m.streams) == 0 {
		return helpStyle.Render("no streams in this call")
	}
	if len(m.matrix.users) == 0 {
		return helpStyle.Render("no participants with attributed streams (press v for stats view)")
	}
	if m.matrix.buckets <= 0 || m.width < drillMatrixMinWidth {
		return helpStyle.Render("terminal too narrow for matrix view (press v for stats)")
	}

	// Layout per user (2 visual lines):
	//   line 1: "▶ <full upn padded 28>↑ <cells>"
	//   line 2: "  <conn/platform padded 28>↓ <cells>"
	// Total prefix budget = prefixWidth + labelWidth + arrowWidth = 32.
	const labelWidth = 28 // UPN line / info line — full width, no truncation in normal cases
	const prefixWidth = 2 // "▶ " or "  "
	const arrowWidth = 2  // "↑ " or "↓ "

	// Time ruler: HH:MM stamps placed at fixed bucket offsets.
	step := 15
	if m.matrix.buckets < step*2 {
		step = max(5, m.matrix.buckets/3)
	}
	rulerBuf := make([]rune, m.matrix.buckets)
	for i := range rulerBuf {
		rulerBuf[i] = ' '
	}
	for b := 0; b < m.matrix.buckets; b += step {
		ts := m.matrix.start.Add(time.Duration(b) * m.matrix.bucketW).UTC().Format("15:04")
		for k, r := range ts {
			if b+k < m.matrix.buckets {
				rulerBuf[b+k] = r
			}
		}
	}
	rulerPrefix := strings.Repeat(" ", prefixWidth+labelWidth+arrowWidth)
	rulerLine := rulerPrefix + helpStyle.Render(string(rulerBuf))

	// Determine visible slice of users.
	vis := m.visibleRows()
	start := 0
	if m.matrixCur >= vis {
		start = m.matrixCur - vis + 1
	}
	end := min(start+vis, len(m.matrix.users))

	var lines []string
	lines = append(lines, rulerLine)
	// Zebra striping: every other user (by index in the full list, so the
	// stripe doesn't reflow on scroll) gets a slightly darker bg on BOTH
	// their visible rows. To keep the bg continuous across the inevitable
	// ANSI resets that lipgloss emits at the end of every Render() call,
	// we paint each individual segment of a zebra row with a style that
	// CARRIES the bg internally — including the otherwise-unstyled UPN
	// text, arrows, and prefix spaces. The ↑ row appeared continuous with
	// the old code only because no styled segment came before the cells;
	// the ↓ row's italic info segment broke the bg before the `↓ ` arrow.
	for i := start; i < end; i++ {
		upn := m.matrix.users[i]
		pair := m.matrix.cells[upn]
		zebra := i%2 == 1

		// Pick the style set for this row.
		curS := cursorStyle
		infoS := infoStyle
		plainSeg := func(s string) string { return s }
		if zebra {
			curS = cursorStyleZebra
			infoS = infoStyleZebra
			plainSeg = func(s string) string { return plainBgZebra.Render(s) }
		}

		// Line 1: full UPN (truncate only if absurdly long).
		upnLabel := upn
		if utf8.RuneCountInString(upnLabel) > labelWidth {
			upnLabel = truncate(upnLabel, labelWidth)
		}
		upnPadded := fmt.Sprintf("%-*s", labelWidth, upnLabel)

		// Line 2: conn/platform info, dim italic so it reads as metadata.
		info := m.matrix.info[upn]
		if info == "" || info == "-/-" {
			info = "—"
		}
		if utf8.RuneCountInString(info) > labelWidth {
			info = truncate(info, labelWidth)
		}
		infoText := fmt.Sprintf("%-*s", labelWidth, info)

		// Cursor prefix vs plain "  ".
		var prefix string
		if i == m.matrixCur {
			prefix = curS.Render("▶ ")
		} else {
			prefix = plainSeg("  ")
		}

		upRow := prefix +
			plainSeg(upnPadded) +
			plainSeg("↑ ") +
			renderMatrixCells(pair[0], m.matrix.buckets, zebra)
		downRow := plainSeg("  ") +
			infoS.Render(infoText) +
			plainSeg("↓ ") +
			renderMatrixCells(pair[1], m.matrix.buckets, zebra)

		// Pad each row to terminal width. The visible width of the
		// content equals m.width when buckets fill the available area,
		// but at very wide terminals m.matrix.buckets is capped at
		// drillMatrixMaxBuckets and the row falls short — fill the rest
		// with bg-styled spaces (zebra) or plain spaces (non-zebra) so
		// every row ends at the right edge of the terminal.
		if pad := m.width - lipgloss.Width(upRow); pad > 0 {
			upRow += plainSeg(strings.Repeat(" ", pad))
		}
		if pad := m.width - lipgloss.Width(downRow); pad > 0 {
			downRow += plainSeg(strings.Repeat(" ", pad))
		}
		lines = append(lines, upRow, downRow)
	}
	return strings.Join(lines, "\n")
}

// matrixCellStyles caches the foreground styles used by renderMatrixCells
// so we don't allocate three lipgloss.Style values per bucket per row. The
// zebra variants additionally carry the background colour so the stripe
// stays continuous: every styled chunk in a row ends with an ANSI reset,
// and only by re-establishing the background on the very next chunk does
// the row keep an unbroken stripe.
var (
	matrixBadStyle  = lipgloss.NewStyle().Foreground(colorBad)
	matrixPoorStyle = lipgloss.NewStyle().Foreground(colorWarn)
	matrixGoodStyle = lipgloss.NewStyle().Foreground(colorMuted)

	matrixBadStyleZ   = lipgloss.NewStyle().Foreground(colorBad).Background(colorZebraBg)
	matrixPoorStyleZ  = lipgloss.NewStyle().Foreground(colorWarn).Background(colorZebraBg)
	matrixGoodStyleZ  = lipgloss.NewStyle().Foreground(colorMuted).Background(colorZebraBg)
	matrixSpaceStyleZ = lipgloss.NewStyle().Background(colorZebraBg)

	// Used to wrap unstyled chunks (UPN text, arrows, plain prefix) of a
	// zebra row so the bg survives across the ANSI resets emitted by
	// neighbouring styled segments.
	plainBgZebra = lipgloss.NewStyle().Background(colorZebraBg)

	// infoStyle / infoStyleZebra — italic muted label that shows
	// conn/platform on the second line of each user. Two precomputed
	// variants because lipgloss styles are immutable copies.
	infoStyle      = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	infoStyleZebra = lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Background(colorZebraBg)

	// Cursor prefix variants for `▶ `.
	cursorStyle      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	cursorStyleZebra = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Background(colorZebraBg)
)

// renderMatrixCells renders exactly bucketCount horizontal heatmap cells.
// The bucketCount cap is defensive: every users' slices are sized to
// matrix.buckets at build time, but this guarantees a fixed row width
// even if a future refactor accidentally grows one of the slices. When
// zebra is true, empty cells are rendered as bg-styled spaces and the
// verdict cells use bg-included styles so the row's zebra stripe stays
// continuous.
func renderMatrixCells(cells []string, bucketCount int, zebra bool) string {
	n := len(cells)
	if n > bucketCount {
		n = bucketCount
	}
	badS, poorS, goodS := matrixBadStyle, matrixPoorStyle, matrixGoodStyle
	if zebra {
		badS, poorS, goodS = matrixBadStyleZ, matrixPoorStyleZ, matrixGoodStyleZ
	}
	var b strings.Builder
	emit := func() {} // declared below; closure over zebra
	zebraSpace := matrixSpaceStyleZ.Render(" ")
	emit = func() {
		if zebra {
			b.WriteString(zebraSpace)
		} else {
			b.WriteByte(' ')
		}
	}
	for i := 0; i < n; i++ {
		switch cells[i] {
		case "Bad":
			b.WriteString(badS.Render("█"))
		case "Poor":
			b.WriteString(poorS.Render("▓"))
		case "Good":
			b.WriteString(goodS.Render("░"))
		default:
			emit()
		}
	}
	// Pad short slices to bucketCount so a row whose data ended early
	// still occupies the same horizontal extent as a full-length row.
	for i := n; i < bucketCount; i++ {
		emit()
	}
	return b.String()
}

// renderHelpLine builds the per-view help/legend line.
func (m *drillModel) renderHelpLine() string {
	if m.view == drillViewMatrix {
		return renderHotkeyHelp("↑/↓ select user  v stats  f filter  c channel  b back  r refresh", nil)
	}
	return renderHotkeyHelp("↑/↓ move  v matrix  f filter  c channel  b back  r refresh  ·  sort:", []hotkey{
		{"s", "sev"}, {"u", "user"}, {"y", "dir"}, {"a", "label"},
		{"i", "jit"}, {"l", "loss"}, {"p", "rtt"}, {"m", "time"},
	})
}

// drillColumns defines the stats-view table layout.
var drillColumns = []column{
	{title: "User", key: "u", min: 18, weight: 3},
	{title: "Dir", key: "y", min: 5, weight: 0},
	{title: "Label", key: "a", min: 7, weight: 0},
	{title: "Verdict", key: "s", min: 10, weight: 0},
	{title: "Jit a/m", key: "i", min: 11, weight: 0},
	{title: "Loss", key: "l", min: 8, weight: 0},
	{title: "LossMax", key: "x", min: 9, weight: 0},
	{title: "Rtt", key: "p", min: 8, weight: 0},
	{title: "Conn", min: 8, weight: 0},
	{title: "Client", min: 9, weight: 0},
	{title: "Subnet", min: 14, weight: 2},
	{title: "Segment", key: "m", min: 14, weight: 0},
}

// ---------------------------------------------------------------------------
// Formatters and helpers
// ---------------------------------------------------------------------------

// formatJit renders "avg/max ms" from optional float pointers.
func formatJit(avg, max *float64) string {
	if avg == nil && max == nil {
		return "-"
	}
	a := "-"
	if avg != nil {
		a = fmt.Sprintf("%.0f", *avg)
	}
	mx := "-"
	if max != nil {
		mx = fmt.Sprintf("%.0f", *max)
	}
	return a + "/" + mx + " ms"
}

// formatLoss renders a loss percentage with one decimal.
func formatLoss(avg *float64) string {
	if avg == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", *avg)
}

// formatRtt renders an RTT average rounded to milliseconds.
func formatRtt(avg *float64) string {
	if avg == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f ms", *avg)
}

// formatSegmentTime renders "HH:MM→HH:MM" UTC from a segment window.
func formatSegmentTime(start, end time.Time) string {
	if start.IsZero() && end.IsZero() {
		return "-"
	}
	s := "--:--"
	e := "--:--"
	if !start.IsZero() {
		s = start.UTC().Format("15:04")
	}
	if !end.IsZero() {
		e = end.UTC().Format("15:04")
	}
	return s + "→" + e
}

// formatStreamDir maps the direction vocabulary to a compact glyph.
func formatStreamDir(dir string) string {
	switch dir {
	case "recv", "download":
		return "↓"
	case "send", "upload":
		return "↑"
	default:
		return "·"
	}
}

// formatStreamLabelShort normalizes a raw Graph stream label to a 3-5 char
// alias suitable for the narrow drill-table column. Extracted from the
// original inline logic in formatWorstSummary (tab_calls.go) so both
// call-sites share the same vocabulary.
func formatStreamLabelShort(label string) string {
	short := strings.ToLower(label)
	switch {
	case short == "":
		return "-"
	case strings.Contains(short, "screen") || strings.Contains(short, "vbss"):
		return "scr"
	case strings.Contains(short, "video"):
		return "video"
	case strings.Contains(short, "audio"):
		return "audio"
	case strings.Contains(short, "data"):
		return "data"
	}
	return short
}

// formatPlatform normalizes a Graph endpoint platform string into a 3-4
// char alias for the narrow drill-table column. Examples seen in real
// data: "windows", "android", "macOS", "iOS", "web", "linux", "unknown".
func formatPlatform(p string) string {
	s := strings.ToLower(p)
	switch {
	case s == "" || s == "unknown":
		return "-"
	case strings.HasPrefix(s, "win"):
		return "win"
	case strings.HasPrefix(s, "mac") || strings.HasPrefix(s, "osx"):
		return "mac"
	case strings.HasPrefix(s, "lin"):
		return "linux"
	case strings.HasPrefix(s, "and"):
		return "android"
	case strings.HasPrefix(s, "ios") || strings.HasPrefix(s, "iphone") || strings.HasPrefix(s, "ipad"):
		return "ios"
	case strings.Contains(s, "web") || strings.Contains(s, "browser"):
		return "web"
	}
	return s
}

// formatConn normalizes a connection type to the short vocabulary.
func formatConn(c string) string {
	s := strings.ToLower(c)
	switch {
	case s == "":
		return "-"
	case strings.Contains(s, "wired") || strings.Contains(s, "ethernet"):
		return "wired"
	case strings.Contains(s, "wifi") || strings.Contains(s, "wireless"):
		return "wifi"
	case strings.Contains(s, "mobile") || strings.Contains(s, "cell"):
		return "mobile"
	case strings.Contains(s, "vpn"):
		return "vpn"
	}
	return s
}

// streamSummaryCounts tallies Good/Poor/Bad streams for the header block.
func streamSummaryCounts(rows []store.StreamRow) (good, poor, bad int) {
	for _, r := range rows {
		switch r.Verdict {
		case "Good":
			good++
		case "Poor":
			poor++
		case "Bad":
			bad++
		}
	}
	return
}

// floatPtrLess is a nil-safe less comparator. nil values sort to the tail
// regardless of ascending/descending direction so missing metrics cluster
// together and don't drown out real data.
func floatPtrLess(a, b *float64) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil:
		return false // nil > anything → tail
	case b == nil:
		return true // anything < nil → keep non-nil earlier
	default:
		return *a < *b
	}
}

// worseVerdict returns the more severe of two verdict strings. "" is
// treated as "no data" and loses to anything. Order: "" < Good < Poor < Bad.
func worseVerdict(a, b string) string {
	if verdictRank(a) >= verdictRank(b) {
		return a
	}
	return b
}

// bucketRange clamps [start, end] to [callStart, callStart + max*bucketW]
// and maps the clamped window to integer bucket indices. A zero-duration
// stream (or single-bucket span) yields a single bucket so degenerate
// slices still paint exactly one cell.
func bucketRange(start, end, callStart time.Time, bucketW time.Duration, max int) (int, int) {
	if bucketW <= 0 || max <= 0 {
		return 0, 0
	}
	if start.IsZero() && end.IsZero() {
		return 0, 0
	}
	if start.IsZero() {
		start = callStart
	}
	if end.IsZero() || !end.After(start) {
		end = start.Add(bucketW)
	}
	callEnd := callStart.Add(time.Duration(max) * bucketW)
	if start.Before(callStart) {
		start = callStart
	}
	if end.After(callEnd) {
		end = callEnd
	}
	if !end.After(start) {
		return 0, 0
	}
	startB := int(start.Sub(callStart) / bucketW)
	endB := int((end.Sub(callStart) + bucketW - 1) / bucketW)
	if startB < 0 {
		startB = 0
	}
	if endB > max {
		endB = max
	}
	if endB <= startB {
		endB = startB + 1
		if endB > max {
			return 0, 0
		}
	}
	return startB, endB
}

// uniqueParticipants extracts unique non-server users from a stream slice,
// sorted alphabetically so the matrix row order is stable between renders.
func uniqueParticipants(rows []store.StreamRow) []string {
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.User == "" || r.User == "<server/unknown>" {
			continue
		}
		seen[r.User] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
