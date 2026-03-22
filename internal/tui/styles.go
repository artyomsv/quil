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

	activePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("57"))

	inactivePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238"))

	statusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Background(lipgloss.Color("236")).
		Padding(0, 1)
)
