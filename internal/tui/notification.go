package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/artyomsv/aethel/internal/ipc"
)

// NotificationCenter manages the notification sidebar state.
type NotificationCenter struct {
	events    []ipc.PaneEventPayload
	cursor    int
	visible   bool
	focused   bool
	width     int
	maxEvents int
}

// NewNotificationCenter creates a notification center with the given sidebar width and max events.
func NewNotificationCenter(width, maxEvents int) *NotificationCenter {
	if width <= 0 {
		width = 30
	}
	if maxEvents <= 0 {
		maxEvents = 50
	}
	return &NotificationCenter{width: width, maxEvents: maxEvents}
}

// AddEvent prepends an event, deduplicating by ID.
func (nc *NotificationCenter) AddEvent(e ipc.PaneEventPayload) {
	for _, existing := range nc.events {
		if existing.ID == e.ID {
			return
		}
	}
	nc.events = append([]ipc.PaneEventPayload{e}, nc.events...)
	if len(nc.events) > nc.maxEvents {
		nc.events = nc.events[:nc.maxEvents]
	}
}

// DismissSelected removes the selected event and returns its ID.
func (nc *NotificationCenter) DismissSelected() string {
	if nc.cursor >= len(nc.events) {
		return ""
	}
	id := nc.events[nc.cursor].ID
	nc.events = append(nc.events[:nc.cursor], nc.events[nc.cursor+1:]...)
	if nc.cursor >= len(nc.events) && len(nc.events) > 0 {
		nc.cursor = len(nc.events) - 1
	} else if len(nc.events) == 0 {
		nc.cursor = 0
	}
	return id
}

// DismissAll removes all events.
func (nc *NotificationCenter) DismissAll() {
	nc.events = nil
	nc.cursor = 0
}

// SelectedEvent returns the currently selected event, or nil.
func (nc *NotificationCenter) SelectedEvent() *ipc.PaneEventPayload {
	if nc.cursor >= len(nc.events) {
		return nil
	}
	return &nc.events[nc.cursor]
}

// Count returns the number of pending events.
func (nc *NotificationCenter) Count() int {
	return len(nc.events)
}

// HandleKey processes a key press when the sidebar is focused.
// Returns: action ("navigate", "dismiss", "dismiss_all", "unfocus", "none"),
// eventID (for dismiss), paneID (for navigate).
func (nc *NotificationCenter) HandleKey(key string) (action, eventID, paneID string) {
	switch key {
	case "up", "k":
		if nc.cursor > 0 {
			nc.cursor--
		}
		return "none", "", ""
	case "down", "j":
		if nc.cursor < len(nc.events)-1 {
			nc.cursor++
		}
		return "none", "", ""
	case "enter":
		if e := nc.SelectedEvent(); e != nil {
			return "navigate", e.ID, e.PaneID
		}
		return "none", "", ""
	case "d":
		id := nc.DismissSelected()
		return "dismiss", id, ""
	case "D":
		nc.DismissAll()
		return "dismiss_all", "", ""
	case "esc", "escape":
		return "unfocus", "", ""
	default:
		return "none", "", ""
	}
}

// View renders the sidebar at the given height.
func (nc *NotificationCenter) View(height int) string {
	innerW := nc.width - 2
	innerH := height - 2
	if innerW < 5 || innerH < 3 {
		return ""
	}

	var lines []string

	// Title
	title := " Notifications "
	title = truncateRunes(title, innerW)
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Render(title))

	separator := lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render(
		truncateRunes(strings.Repeat("·", innerW), innerW),
	)

	if len(nc.events) == 0 {
		lines = append(lines, separator)
		noEvents := "No notifications"
		noEvents = truncateRunes(noEvents, innerW)
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(noEvents))
	} else {
		// Each event = separator + name/time + title = 3 lines
		maxVisible := (innerH - 3) / 3
		if maxVisible < 1 {
			maxVisible = 1
		}
		start := 0
		if nc.cursor >= maxVisible {
			start = nc.cursor - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(nc.events) {
			end = len(nc.events)
		}

		for i := start; i < end; i++ {
			e := nc.events[i]
			selected := i == nc.cursor && nc.focused

			// Separator
			lines = append(lines, separator)

			// Pane name or ID
			name := e.PaneName
			if name == "" {
				name = e.PaneID
				if len(name) > 12 {
					name = name[:12]
				}
			}

			// Relative time (right-aligned)
			age := relativeTime(time.UnixMilli(e.Timestamp))

			// Line 1: colored name + right-aligned time
			nameStyle := severityNameStyle(e.Severity)
			if selected {
				nameStyle = nameStyle.Bold(true).Reverse(true)
			}
			styledName := nameStyle.Render(name)

			// Pad between name and time
			nameLen := len([]rune(name))
			ageLen := len([]rune(age))
			gap := innerW - nameLen - ageLen
			if gap < 1 {
				gap = 1
			}
			line1 := styledName + strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(age)

			// Line 2: title (indented)
			titleText := "  " + e.Title
			titleText = truncateRunes(titleText, innerW)
			if selected {
				titleText = lipgloss.NewStyle().Reverse(true).Render(titleText)
			} else {
				titleText = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(titleText)
			}

			lines = append(lines, line1)
			lines = append(lines, titleText)
		}

		// Trailing separator
		if len(lines) < innerH-1 {
			lines = append(lines, separator)
		}
	}

	// Pad to fill height
	for len(lines) < innerH-1 {
		lines = append(lines, "")
	}

	// Key hints at bottom
	hints := "^!N Focus  Enter Go"
	if nc.focused {
		hints = "Up/Dn  Enter  d/D  Esc"
	}
	hints = truncateRunes(hints, innerW)
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(hints))

	content := strings.Join(lines, "\n")

	borderColor := lipgloss.Color("63")
	if nc.focused {
		borderColor = lipgloss.Color("57")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(nc.width).
		Height(height).
		Render(content)
}

// truncateRunes truncates a string to maxWidth runes.
func truncateRunes(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth])
}

// severityNameStyle returns a style for the pane name colored by severity.
func severityNameStyle(severity string) lipgloss.Style {
	switch severity {
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	case "warning":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	}
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
