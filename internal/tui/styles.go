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
	// finished a turn and hasn't been focused since. Green background,
	// bright text; clears when the pane is focused.
	unseenTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("28")).
		Padding(0, 1)

	// blockedTabStyle marks a tab holding a pane parked on the user
	// (permission prompt / option select). Amber, matching the sidebar's
	// sidebarBlockedStyle so the tab bar and the strip use one vocabulary.
	// Outranks unseenTabStyle: "needs you" beats "finished while you were
	// away", and unlike unseen it names something you can act on.
	blockedTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("232")).
		Background(lipgloss.Color("214")).
		Padding(0, 1)

	// blockedActiveTabStyle is the ACTIVE tab's blocked variant. Active and
	// inactive differ by BACKGROUND alone (57 vs 238) and blockedTabStyle
	// replaces the background wholesale, so with two or more agents parked —
	// the ordinary case this feature exists for — the amber tabs carried no
	// which-one-am-I-on signal beyond tabLabel's "* " prefix.
	//
	// Underline is the second channel, deliberately: renderTabBar MEASURES
	// style.Render(name) for hit-testing, so the variant must render at exactly
	// the width blockedTabStyle does. An SGR attribute costs no cells, where
	// extra Padding would shift every tab after it. A different Foreground
	// would be width-safe too, but the 232-on-214 pair is already chosen for
	// contrast — changing it trades legibility for the signal instead of
	// adding one.
	blockedActiveTabStyle = blockedTabStyle.Underline(true)

	// pinnedTabStyle marks a tab holding a pane the USER pinned by hand.
	// Purple, matching the sidebar's sidebarPinnedStyle so the tab bar and the
	// strip use one vocabulary — the same relationship blockedTabStyle's 214
	// has to sidebarBlockedStyle, background here and foreground there.
	//
	// It exists because a pin and an unseen mark shared unseenTabStyle's green,
	// so the one thing the user set by hand was indistinguishable from the one
	// the agent caused — and the pin is the one that does not clear itself, so
	// confusing the two means waiting for a green that never goes away.
	// Outranked by blocked (a parked agent is a question costing you time,
	// where a pin is a note to self) and outranks unseen. The ◆ in tabLabel is
	// deliberately NOT gated on this precedence: a pinned-and-blocked tab is
	// amber AND carries the ◆, because those are two facts and a colour can
	// only carry one.
	pinnedTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("232")).
			Background(lipgloss.Color("141")).
			Padding(0, 1)

	// pinnedActiveTabStyle is the ACTIVE tab's pinned variant, underlined for
	// exactly the reason blockedActiveTabStyle is: the background is replaced
	// wholesale, which is the only thing active and inactive differ by, and an
	// SGR attribute costs no cells where extra padding would shift every tab
	// after it and desync hitTestTab.
	pinnedActiveTabStyle = pinnedTabStyle.Underline(true)

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
