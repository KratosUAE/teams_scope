package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// column describes one logical table column. Columns are either fixed (when
// weight == 0 the min width is also the final width) or flexible (when
// weight > 0 the leftover horizontal space is distributed between flex
// columns proportional to their weight after every column has been given
// its min width). Optional `key` carries the hotkey letter that sorts this
// column, used to colour-link the header to the help line.
type column struct {
	title  string
	key    string
	min    int // minimum width including 1ch left + 1ch right padding
	weight int // 0 = fixed, >0 = flex
}

// hotkeyColors maps each sort hotkey to a distinct accent colour. Both the
// help line legend and the corresponding column header use this colour so
// the user can visually trace "key → header section" without reading text.
var hotkeyColors = map[string]color.Color{
	// calls tab
	"t": lipgloss.Color("75"),  // sky blue   – Time
	"d": lipgloss.Color("87"),  // cyan       – Duration
	"v": lipgloss.Color("213"), // magenta    – Verdict
	"n": lipgloss.Color("114"), // green      – N participants / users-total
	"o": lipgloss.Color("221"), // soft amber – Organizer
	"w": lipgloss.Color("210"), // pink/red   – WorstUser
	// users tab
	"u": lipgloss.Color("75"),  // sky blue   – UPN (reused by drill for user sort)
	"g": lipgloss.Color("114"), // green      – Good
	"p": lipgloss.Color("221"), // amber      – Poor (reused by drill for rtt sort)
	"b": lipgloss.Color("210"), // red        – Bad
	// drill tab
	"s": lipgloss.Color("210"), // red        – Severity / Verdict (drill)
	"y": lipgloss.Color("87"),  // cyan       – Direction (drill)
	"a": lipgloss.Color("75"),  // sky        – Label (drill)
	"i": lipgloss.Color("221"), // amber      – Jitter (drill)
	"l": lipgloss.Color("213"), // magenta    – Loss (drill)
	"m": lipgloss.Color("114"), // green      – Segment time (drill)
}

// hotkeyColor returns the accent colour for a hotkey letter, falling back
// to the generic accent palette colour for unknown keys.
func hotkeyColor(k string) color.Color {
	if c, ok := hotkeyColors[k]; ok {
		return c
	}
	return colorAccent
}

// hotkey is one entry in a help line: the single-letter shortcut and the
// human-readable label. renderHotkeyHelp colours the letter with
// hotkeyColor(key) so it matches the column header in the table above.
type hotkey struct{ key, label string }

// renderHotkeyHelp builds the standard "prefix · sort: K1 label1  K2 label2 …"
// help line. The prefix is rendered with helpStyle (italic muted) and each
// hotkey letter is bolded in its accent colour to match the table header.
func renderHotkeyHelp(prefix string, keys []hotkey) string {
	var b strings.Builder
	b.WriteString(helpStyle.Render(prefix))
	for _, hk := range keys {
		b.WriteString("  ")
		b.WriteString(lipgloss.NewStyle().
			Foreground(hotkeyColor(hk.key)).
			Bold(true).
			Render(hk.key))
		b.WriteString(helpStyle.Render(" " + hk.label))
	}
	return b.String()
}

// computeWidths assigns a final width to each column such that the sum
// equals total, every column has at least its min width, and the leftover
// is shared between flex columns proportional to their weight. If total is
// smaller than the sum of mins (very narrow terminal), columns keep their
// min widths and the table will overflow horizontally — terminal widths
// below ~70 cols are not a supported configuration.
func computeWidths(total int, cols []column) []int {
	widths := make([]int, len(cols))
	used := 0
	weightSum := 0
	for i, c := range cols {
		widths[i] = c.min
		used += c.min
		weightSum += c.weight
	}
	if total <= used || weightSum == 0 {
		return widths
	}
	leftover := total - used
	// Distribute by weight; assign rounded shares, give the remainder to
	// the last flex column so the row is exactly `total` wide.
	assigned := 0
	lastFlex := -1
	for i, c := range cols {
		if c.weight == 0 {
			continue
		}
		share := (leftover * c.weight) / weightSum
		widths[i] += share
		assigned += share
		lastFlex = i
	}
	if lastFlex >= 0 && assigned < leftover {
		widths[lastFlex] += leftover - assigned
	}
	return widths
}

// cellPad is the horizontal padding (left + right) the cell/header/cursor
// styles add via lipgloss Padding(0, 1). The text budget inside a column of
// width W is therefore W - cellPad. Keep this in sync with styles.go.
const cellPad = 2

// renderHeader builds a single header row from the supplied titles + widths.
// The sort indicator (↓/↑) is appended to the active column's title; columns
// with a sort hotkey are coloured with that hotkey's accent so they visually
// match the help-line legend below the table. Truncation keeps the header on
// a single line even on narrow terminals.
func renderHeader(cols []column, widths []int, sortIdx int, sortDesc bool) string {
	cells := make([]string, len(cols))
	for i, c := range cols {
		title := c.title
		if i == sortIdx {
			if sortDesc {
				title += " ↓"
			} else {
				title += " ↑"
			}
		}
		text := truncate(title, max(0, widths[i]-cellPad))
		style := tableHeaderStyle.Width(widths[i])
		if c.key != "" {
			style = style.Foreground(hotkeyColor(c.key))
		}
		cells[i] = style.Render(text)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

// renderRow builds a single styled data row. cells must have the same length
// as widths. perCellFG[i] is applied to the i-th cell after width and
// truncation; pass nil at index i for the default tableCellStyle.
func renderRow(widths []int, cells []string, isCursor bool, perCellFG []*lipgloss.Style) string {
	out := make([]string, len(cells))
	for i, val := range cells {
		w := widths[i]
		text := truncate(val, max(0, w-cellPad))
		switch {
		case isCursor:
			out[i] = tableCursorStyle.Width(w).Render(text)
		case perCellFG != nil && perCellFG[i] != nil:
			out[i] = perCellFG[i].Width(w).Render(text)
		default:
			out[i] = tableCellStyle.Width(w).Render(text)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, out...)
}

