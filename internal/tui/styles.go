package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Palette. lipgloss v2 takes image/color.Color values; lipgloss.Color(s)
// returns a concrete implementation that we then pass to Foreground /
// Background / BorderForeground. All ANSI 256 codes picked to match the
// design-tui.md verdict scheme.
var (
	colorBase     = lipgloss.Color("252") // bright gray, default fg
	colorMuted    = lipgloss.Color("244") // medium gray, good / neutral
	colorSubtle   = lipgloss.Color("238") // dim gray, borders
	colorAccent   = lipgloss.Color("111") // soft blue, active tab
	colorWarn     = lipgloss.Color("214") // amber, poor
	colorBad      = lipgloss.Color("196") // red, bad
	colorHeader   = lipgloss.Color("229") // parchment, table headers
	colorStatusBg = lipgloss.Color("236") // charcoal, status bar bg
	colorZebraBg  = lipgloss.Color("235") // very dim charcoal, zebra row stripe in matrix view
)

// Tab bar styles.
var (
	activeTabStyle = lipgloss.NewStyle().
			Foreground(colorHeader).
			Background(colorAccent).
			Bold(true).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 2)
)

// Content / chrome styles.
var (
	headerStyle = lipgloss.NewStyle().
			Foreground(colorHeader).
			Bold(true)

	tableHeaderStyle = lipgloss.NewStyle().
				Foreground(colorHeader).
				Bold(true).
				Padding(0, 1)

	tableCellStyle = lipgloss.NewStyle().
			Foreground(colorBase).
			Padding(0, 1)

	tableCursorStyle = lipgloss.NewStyle().
				Foreground(colorHeader).
				Background(colorAccent).
				Bold(true).
				Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorBase).
			Background(colorStatusBg).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorBad).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)
)

// VerdictColor maps a canonical verdict string ("Good" / "Poor" / "Bad")
// to its palette color. Any other value (including "") falls through to
// the muted gray so an unknown verdict is rendered without blowing up the
// view layer.
func VerdictColor(v string) color.Color {
	switch v {
	case "Good":
		return colorMuted
	case "Poor":
		return colorWarn
	case "Bad":
		return colorBad
	default:
		return colorMuted
	}
}
