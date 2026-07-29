package tui

import "charm.land/lipgloss/v2"

var (
	activeTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("230")).
		Background(lipgloss.Color("57")).
		Padding(0, 1)

	inactiveTabStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Background(lipgloss.Color("238")).
		Padding(0, 1)

	// unseenTabStyle highlights a background tab containing a pane that
	// finished a turn (or parked for user input) and hasn't been focused
	// since. Green background, bright text; clears when the pane is focused.
	unseenTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("28")).
		Padding(0, 1)

	activePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("57"))

	inactivePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238"))

	mcpHighlightBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("208")) // orange

	statusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Background(lipgloss.Color("236")).
		Padding(0, 1)

	// reconnectBannerStyle marks a session whose link is down. Amber, because
	// the state is recoverable — red would read as a failure the user has to
	// act on, and the whole point is that Quil is handling it.
	//
	// Deliberately no Padding: renderReconnectBanner sizes its content to the
	// exact terminal width, and lipgloss adds padding OUTSIDE .Width(), so any
	// here would push the row past the frame it is overlaid on.
	reconnectBannerStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("232")).
		Background(lipgloss.Color("214"))
)
