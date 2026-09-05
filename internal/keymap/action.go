package keymap

import "sort"

// ActionID is the stable, data-addressable name of one bindable action.
// Presets and bindings.toml key off these strings — never rename a shipped one.
type ActionID string

// Tier says which of handleKey's two switches an action dispatches from.
// The split matters because tryPluginRawKey runs between them: an early action
// beats a plugin's raw_keys claim, a late action loses to it.
type Tier uint8

const (
	TierEarly Tier = iota
	TierLate
)

// Action is one registry entry.
type Action struct {
	ID      ActionID
	Label   string // shown in F1 and the command palette
	Group   string // F1 section heading
	Tier    Tier
	Order   int    // legacy case rank: duplicate tie-break + F1 row order
	Default string // shipped spec; the per-action fallback for a bad config
	Hidden  bool   // omitted from F1 (registered but not dispatched)
}

var registry = []Action{
	// --- Early tier: handleKey's first switch, before tryPluginRawKey ---
	{ID: "notification.toggle", Label: "Toggle notification sidebar", Group: "Notifications", Tier: TierEarly, Order: 100, Default: "alt+n"},
	{ID: "sidebar.toggle", Label: "Toggle project sidebar", Group: "Projects", Tier: TierEarly, Order: 200, Default: "alt+shift+s"},
	{ID: "notification.focus", Label: "Focus notification sidebar", Group: "Notifications", Tier: TierEarly, Order: 300, Default: "f3"},
	{ID: "pane.go_back", Label: "Pane history back", Group: "Panes", Tier: TierEarly, Order: 400, Default: "alt+backspace"},
	{ID: "pane.mute", Label: "Mute / unmute pane notifications", Group: "Panes", Tier: TierEarly, Order: 500, Default: "alt+m"},
	{ID: "pane.toggle_eager", Label: "Toggle eager restore (active pane)", Group: "Panes", Tier: TierEarly, Order: 600, Default: "alt+shift+e"},
	{ID: "pane.toggle_wrap", Label: "Toggle preview soft-wrap (AI pane)", Group: "Panes", Tier: TierEarly, Order: 700, Default: "alt+shift+w"},
	{ID: "pane.toggle_lazygit", Label: "Toggle lazygit overlay for current repo", Group: "Panes", Tier: TierEarly, Order: 800, Default: "alt+g"},
	// Order 850 rather than a round number: it sits between lazygit and input
	// history because that is where handleOverlayKey and the early switch check
	// it, and Order is what reproduces that arm order. The default is alt+d, not
	// the more obvious alt+h — plain Alt+H is deliberately left unbound so it
	// reaches the running program, and vim-style layouts rebind it to pane-left.
	{ID: "pane.toggle_hunk", Label: "Toggle hunk (diff review) overlay for current repo", Group: "Panes", Tier: TierEarly, Order: 850, Default: "alt+d"},
	{ID: "pane.command_history", Label: "Open pane input history", Group: "Panes", Tier: TierEarly, Order: 900, Default: "alt+shift+i"},
	{ID: "pane.quick_actions", Label: "Pane context menu (also mouse right-click)", Group: "Panes", Tier: TierEarly, Order: 1000, Default: "alt+a"},
	{ID: "project.new", Label: "New project", Group: "Projects", Tier: TierEarly, Order: 1100, Default: "alt+shift+n"},
	{ID: "project.destroy", Label: "Remove active project (destroy / disconnect)", Group: "Projects", Tier: TierEarly, Order: 1200, Default: "alt+shift+x"},
	{ID: "project.picker", Label: "Project picker (fuzzy-find by name)", Group: "Projects", Tier: TierEarly, Order: 1300, Default: "alt+p"},
	{ID: "project.next", Label: "Next project", Group: "Projects", Tier: TierEarly, Order: 1400, Default: "alt+shift+right"},
	{ID: "project.prev", Label: "Previous project", Group: "Projects", Tier: TierEarly, Order: 1500, Default: "alt+shift+left"},
	{ID: "project.toggle", Label: "Bounce to the previous project", Group: "Projects", Tier: TierEarly, Order: 1600, Default: "alt+o"},
	{ID: "project.attention_queue", Label: "Jump to the agent blocked longest", Group: "Projects", Tier: TierEarly, Order: 1700, Default: "alt+shift+a"},
	// Up/Down because the sidebar lists projects vertically; the same Alt+Shift
	// layer as project.next/prev (Left/Right) so the four arrows read as one
	// family: plain moves you, with the cursor keys' other axis it moves the
	// project. Early tier with the rest of the project keys.
	{ID: "project.move_up", Label: "Move project up", Group: "Projects", Tier: TierEarly, Order: 1800, Default: "alt+shift+up"},
	{ID: "project.move_down", Label: "Move project down", Group: "Projects", Tier: TierEarly, Order: 1900, Default: "alt+shift+down"},

	// --- Late tier: handleKey's second switch, after tryPluginRawKey ---
	{ID: "app.quit", Label: "Quit", Group: "System", Tier: TierLate, Order: 2000, Default: "ctrl+q"},
	{ID: "tab.new", Label: "New tab", Group: "Tabs", Tier: TierLate, Order: 2100, Default: "ctrl+t"},
	{ID: "pane.close", Label: "Close pane", Group: "Panes", Tier: TierLate, Order: 2200, Default: "ctrl+w"},
	{ID: "pane.restart", Label: "Restart pane process (sessions resume)", Group: "Panes", Tier: TierLate, Order: 2300, Default: "alt+r"},
	{ID: "tab.close", Label: "Close tab", Group: "Tabs", Tier: TierLate, Order: 2400, Default: "alt+w"},
	{ID: "pane.split_h", Label: "Split side-by-side", Group: "Panes", Tier: TierLate, Order: 2500, Default: "alt+shift+h"},
	{ID: "pane.split_v", Label: "Split top/bottom", Group: "Panes", Tier: TierLate, Order: 2600, Default: "alt+shift+v"},
	{ID: "tab.rename", Label: "Rename tab", Group: "Tabs", Tier: TierLate, Order: 2700, Default: "f2"},
	{ID: "pane.rename", Label: "Rename pane", Group: "Panes", Tier: TierLate, Order: 2800, Default: "alt+f2,alt+shift+r"},
	{ID: "tab.cycle_color", Label: "Cycle tab color", Group: "Tabs", Tier: TierLate, Order: 2900, Default: "alt+c"},
	{ID: "app.redraw", Label: "Force screen redraw", Group: "System", Tier: TierLate, Order: 3000, Default: "alt+shift+l"},
	{ID: "pane.scroll_page_up", Label: "Scroll page up", Group: "Panes", Tier: TierLate, Order: 3100, Default: "alt+pgup"},
	{ID: "pane.scroll_page_down", Label: "Scroll page down", Group: "Panes", Tier: TierLate, Order: 3200, Default: "alt+pgdown"},
	{ID: "pane.next", Label: "Next pane", Group: "Pane navigation", Tier: TierLate, Order: 3300, Default: ""},
	{ID: "pane.prev", Label: "Previous pane", Group: "Pane navigation", Tier: TierLate, Order: 3400, Default: ""},
	{ID: "pane.left", Label: "Focus pane left", Group: "Pane navigation", Tier: TierLate, Order: 3500, Default: "alt+left"},
	{ID: "pane.right", Label: "Focus pane right", Group: "Pane navigation", Tier: TierLate, Order: 3600, Default: "alt+right"},
	{ID: "pane.up", Label: "Focus pane up", Group: "Pane navigation", Tier: TierLate, Order: 3700, Default: "alt+up"},
	{ID: "pane.down", Label: "Focus pane down", Group: "Pane navigation", Tier: TierLate, Order: 3800, Default: "alt+down"},
	{ID: "pane.paste", Label: "Paste clipboard", Group: "Panes", Tier: TierLate, Order: 3900, Default: "ctrl+v"},
	{ID: "pane.focus_toggle", Label: "Toggle focus mode", Group: "Panes", Tier: TierLate, Order: 4000, Default: "ctrl+e"},
	{ID: "pane.notes_toggle", Label: "Toggle pane notes", Group: "Panes", Tier: TierLate, Order: 4100, Default: "alt+e"},
	{ID: "app.command_palette", Label: "Command palette (fuzzy-find any action)", Group: "System", Tier: TierLate, Order: 4200, Default: "alt+shift+p"},

	// No dispatch site anywhere: ctrl+j is a config field left over from M5's
	// unfinished JSON transformer. Registered so a configured value survives
	// and conflict detection sees the key; Hidden so F1 does not advertise a
	// shortcut that does nothing.
	{ID: "json.transform", Label: "Transform selection as JSON", Group: "System", Tier: TierLate, Order: 4300, Default: "ctrl+j", Hidden: true},

	// --- Promoted out of handleKey's reserved-key switch ---
	//
	// Order sits above every pre-existing action because alt+1..9 was checked
	// LAST in handleKey, once both tier switches had declined. Order is what
	// reproduces that rank for the duplicate tie-break.
	//
	// tab.next/tab.prev ship UNBOUND: Quil never had them, and inventing a
	// default chord here would claim two keys from every existing user to
	// serve a preset they have not selected.
	{ID: "tab.next", Label: "Next tab", Group: "Tabs", Tier: TierLate, Order: 4400, Default: ""},
	{ID: "tab.prev", Label: "Previous tab", Group: "Tabs", Tier: TierLate, Order: 4500, Default: ""},
	// Bound, unlike tab.next/prev: reordering from the keyboard was asked for,
	// and PgUp/PgDn with a modifier is what browsers use to move a tab. Alt+Shift
	// rather than Ctrl+Shift because Windows Terminal owns Ctrl+Shift+PgUp/PgDn
	// for its own scrollback and never delivers them.
	{ID: "tab.move_left", Label: "Move tab left", Group: "Tabs", Tier: TierLate, Order: 4520, Default: "alt+shift+pgup"},
	{ID: "tab.move_right", Label: "Move tab right", Group: "Tabs", Tier: TierLate, Order: 4540, Default: "alt+shift+pgdown"},
	{ID: "tab.switch_1", Label: "Switch to tab 1", Group: "Tabs", Tier: TierLate, Order: 4600, Default: "alt+1"},
	{ID: "tab.switch_2", Label: "Switch to tab 2", Group: "Tabs", Tier: TierLate, Order: 4700, Default: "alt+2"},
	{ID: "tab.switch_3", Label: "Switch to tab 3", Group: "Tabs", Tier: TierLate, Order: 4800, Default: "alt+3"},
	{ID: "tab.switch_4", Label: "Switch to tab 4", Group: "Tabs", Tier: TierLate, Order: 4900, Default: "alt+4"},
	{ID: "tab.switch_5", Label: "Switch to tab 5", Group: "Tabs", Tier: TierLate, Order: 5000, Default: "alt+5"},
	{ID: "tab.switch_6", Label: "Switch to tab 6", Group: "Tabs", Tier: TierLate, Order: 5100, Default: "alt+6"},
	{ID: "tab.switch_7", Label: "Switch to tab 7", Group: "Tabs", Tier: TierLate, Order: 5200, Default: "alt+7"},
	{ID: "tab.switch_8", Label: "Switch to tab 8", Group: "Tabs", Tier: TierLate, Order: 5300, Default: "alt+8"},
	{ID: "tab.switch_9", Label: "Switch to tab 9", Group: "Tabs", Tier: TierLate, Order: 5400, Default: "alt+9"},
	// Reachable today only through F1 -> About. Ships unbound for the same
	// reason as tab.next: the tmux preset is what wants a key for it.
	{ID: "system.shortcuts", Label: "Show keyboard shortcuts", Group: "System", Tier: TierLate, Order: 5500, Default: ""},
}

// Actions returns every registered action. The slice is a copy.
func Actions() []Action {
	out := make([]Action, len(registry))
	copy(out, registry)
	return out
}

var byID = func() map[ActionID]Action {
	m := make(map[ActionID]Action, len(registry))
	for _, a := range registry {
		m[a.ID] = a
	}
	return m
}()

// Lookup returns the action with the given ID.
func Lookup(id ActionID) (Action, bool) {
	a, ok := byID[id]
	return a, ok
}

// GroupOrder is the order F1 renders groups in. A group missing here sorts
// last, alphabetically — a new group shows up rather than vanishing.
var GroupOrder = []string{
	"System", "Projects", "Tabs", "Panes", "Pane navigation", "Notifications",
}

// ActionsByGroup buckets actions by Group, each bucket sorted by Order, with
// groups in GroupOrder. Hidden actions are included; callers filter.
func ActionsByGroup() (groups []string, byGroup map[string][]Action) {
	byGroup = make(map[string][]Action)
	for _, a := range registry {
		byGroup[a.Group] = append(byGroup[a.Group], a)
	}
	for g := range byGroup {
		bucket := byGroup[g]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Order < bucket[j].Order })
		byGroup[g] = bucket
	}
	seen := make(map[string]bool, len(GroupOrder))
	for _, g := range GroupOrder {
		if _, ok := byGroup[g]; ok {
			groups = append(groups, g)
			seen[g] = true
		}
	}
	var rest []string
	for g := range byGroup {
		if !seen[g] {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	return append(groups, rest...), byGroup
}
