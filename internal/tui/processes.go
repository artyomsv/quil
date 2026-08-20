package tui

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/memreport"
	"github.com/artyomsv/quil/internal/proctree"
)

// The Processes dialog replaces the old Memory dialog. It answers two questions
// on one surface: what is running under each pane and eating this machine, and
// what quil processes are alive right now.
//
// Everything it shows about processes is produced DAEMON-side. The previous
// attempt at this feature scanned the client's own process table and inferred
// identity from image paths, which was guesswork on the wrong machine — in
// remote mode the panes' children run on the daemon's host, not here.

// processesDialogWidth is the dialog box width. Wider than the memory dialog it
// replaces because the rows carry a CPU column and a PID.
const processesDialogWidth = 92

// resourceTickInterval is how often the TUI polls the daemon for the resource
// report. Matches the daemon's own collector cadence.
const resourceTickInterval = 5 * time.Second

// resourceRequestTimeout bounds one in-flight report request.
//
// The single-flight below suppresses a second request while one is out, and
// WITHOUT this bound one lost response would freeze refresh permanently — and
// with it the daemon's collector, whose gate is renewed by these very requests.
// The dialog would sit showing stale numbers with no way back short of closing
// it. Matches the 8 s the daemon-backed browse and discover dialogs use.
const resourceRequestTimeout = 8 * time.Second

// procMinRows and procChromeRows bound the scrolling row window.
//
// renderDialog places the box with lipgloss.Place, which does NOT clip — a box
// taller than the terminal is drawn past the edge, and what falls off this
// dialog is the footer telling the user which keys work. Without a window the
// row count is unbounded: a workspace of 33 tabs produces ~42 rows before
// anything is expanded, and one expanded pane running a build adds hundreds.
// The cursor would then walk into rows that are never painted.
//
// procMinRows is 1 for the reason historyMinRows is: any floor above the height
// actually available manufactures the overflow it looks like it prevents.
const (
	procMinRows = 1
	// procChromeRows is every row the modal spends outside the list: border (2),
	// Padding(1,2) top and bottom (2), the title, the blank row above the
	// footer, the footer, the platform footnote, and one spare so the centered
	// box never sits flush against the terminal edge.
	procChromeRows = 9
)

// procStaleAfter is how old a tree snapshot may be before the dialog says so.
//
// The daemon's collector skips a tick if the previous one is still running, so
// a wedged enumeration leaves the snapshot silently frozen. Presenting old
// numbers as current is the kind of confidently-wrong answer this whole feature
// exists to remove, so past this age the header says the data is stale.
const procStaleAfter = 30 * time.Second

// procRowKind distinguishes the row types in the flattened view.
type procRowKind int

const (
	procRowSectionQuil procRowKind = iota
	procRowQuil
	procRowUnidentified
	procRowSectionWorkspace
	procRowTotal
	procRowTab
	procRowPane
	procRowProc
	procRowTUILocal
)

// procRow is one visible line.
type procRow struct {
	kind   procRowKind
	tabID  string
	paneID string
	label  string
	// pid, start and depth are only meaningful for procRowProc, and start is
	// what the kill request echoes back so the daemon can confirm the target is
	// still the same process.
	pid     int
	startMS int64
	depth   int
	rss     uint64
	cpu     float64
	// version, uptime and exeName are the quil-section columns. Without them
	// the section shows a role and a PID, which answers neither question it
	// exists for — "is this binary current" and "how long has it been up".
	version string
	uptime  time.Duration
	exeName string
	// expandable and expanded drive the ▸/▾ indicator. Nothing else tells the
	// user a row can be opened; the memory dialog this replaces had them.
	expandable bool
	expanded   bool
	// killable marks a row the kill key may act on: strictly below a pane's
	// own direct child.
	killable bool
	// flag is a trailing marker (stale binary, not killable).
	flag string
}

// processesState is the dialog's own state.
type processesState struct {
	resp    *ipc.ResourceReportRespPayload
	loading bool
	cursor  int
	// expandedTabs and expandedPanes drive the tree. Kept across refreshes so a
	// 5 s poll does not collapse what the user just opened.
	expandedTabs  map[string]bool
	expandedPanes map[string]bool
	// scroll is the index of the first rendered row. Without it the dialog
	// draws every row and overflows the terminal on any real workspace.
	scroll int
	// inFlight and sentAt implement the single-flight and its timeout.
	inFlight bool
	sentAt   time.Time
	// notice carries the daemon's refusal reason after a kill that did not
	// happen. A refusal is a normal outcome, so it renders as a line rather
	// than an error dialog.
	notice string
}

// resourceTickCmd schedules the next resourceTickMsg.
func resourceTickCmd() tea.Cmd {
	return tea.Tick(resourceTickInterval, func(time.Time) tea.Msg {
		return resourceTickMsg{}
	})
}

// openProcessesDialog transitions into the dialog and asks for a fresh report.
func (m Model) openProcessesDialog() Model {
	m.dialog = dialogProcesses
	m.proc.loading = true
	m.proc.cursor = 0
	m.proc.notice = ""
	if m.proc.expandedTabs == nil {
		m.proc.expandedTabs = map[string]bool{}
	}
	if m.proc.expandedPanes == nil {
		m.proc.expandedPanes = map[string]bool{}
	}
	return m
}

// refreshResources issues MsgResourceReportReq.
//
// withTrees is both what puts process trees in the response and what keeps the
// daemon's process collector running — it is gated on requests carrying this
// flag, so the dialog's own polling is what renews it. A status-bar poll must
// therefore never set it, or the collector would run for the life of the
// session with nobody looking.
func (m Model) refreshResources(withTrees bool) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgResourceReportReq, ipc.ResourceReportReqPayload{
			WithTrees: withTrees,
		})
		if err != nil {
			log.Printf("refreshResources: marshal: %v", err)
			return nil
		}
		msg.ID = fmt.Sprintf("res-%d", time.Now().UnixNano())
		if err := m.client.Send(msg); err != nil {
			log.Printf("refreshResources: send: %v", err)
		}
		return nil
	}
}

// requestTrees issues a tree request under single-flight, or returns nil.
//
// Suppression is bounded by resourceRequestTimeout: a response that never
// arrives releases the slot on the next tick rather than wedging the dialog and
// starving the daemon's collector gate.
func (m Model) requestTrees(now time.Time) (Model, tea.Cmd) {
	if m.proc.inFlight && now.Sub(m.proc.sentAt) < resourceRequestTimeout {
		return m, nil
	}
	m.proc.inFlight = true
	m.proc.sentAt = now
	return m, m.refreshResources(true)
}

// applyResourceReport stores a fresh report.
//
// The status-bar total is updated unconditionally, because it is fed by the
// same message whether or not the dialog is open. Everything touching the
// dialog's own state is GUARDED on the dialog being open: a response arriving
// after the user pressed Esc must not move another dialog's cursor. The memory
// dialog this replaces already guarded the same way, and the removed process
// dialog did not — which is how a late scan moved the About cursor.
func (m Model) applyResourceReport(resp ipc.ResourceReportRespPayload) Model {
	stored := resp
	m.lastResourceResp = &stored

	if m.dialog == dialogProcesses {
		// A TREELESS response must not replace a tree-bearing one, and must not
		// clear the single-flight it does not answer.
		//
		// The status bar and the dialog share this message; a status-bar poll
		// (WithTrees false) can still be in flight when the dialog opens. Its
		// response carries no Quil section and no trees, so adopting it blanks
		// the whole quil list and turns every CPU cell into an em dash for a
		// round trip — a flicker locally, a visible wipe over ssh — while
		// clearing inFlight for a request that was never the dialog's.
		if !stored.WithTrees && m.proc.resp != nil {
			return m
		}
		m.proc.resp = &stored
		m.proc.loading = false
		m.proc.inFlight = false
		rows := m.procRows()
		if m.proc.cursor >= len(rows) {
			m.proc.cursor = len(rows) - 1
		}
		if m.proc.cursor < 0 {
			m.proc.cursor = 0
		}
	}
	return m
}

// procRows flattens the report into visible rows.
func (m Model) procRows() []procRow {
	var rows []procRow
	if m.proc.resp == nil {
		return rows
	}
	resp := m.proc.resp

	// --- quil's own processes ---
	rows = append(rows, procRow{kind: procRowSectionQuil, label: "QUIL"})
	quil := append([]ipc.QuilProcInfo(nil), resp.Quil...)
	sort.SliceStable(quil, func(i, j int) bool {
		return procRoleRank(quil[i].Role) < procRoleRank(quil[j].Role)
	})
	for _, q := range quil {
		flag := ""
		if q.Stale {
			flag = "⚠ stale"
		}
		rows = append(rows, procRow{
			kind:    procRowQuil,
			label:   q.Role,
			pid:     q.PID,
			version: q.Version,
			uptime:  time.Duration(q.UptimeMS) * time.Millisecond,
			exeName: q.ExeName,
			flag:    flag,
		})
	}
	if resp.Unidentified > 0 {
		rows = append(rows, procRow{
			kind: procRowUnidentified,
			label: fmt.Sprintf("%d unidentified client(s) — predates this feature or failed to identify",
				resp.Unidentified),
		})
	}

	// --- the workspace ---
	rows = append(rows, procRow{kind: procRowSectionWorkspace, label: "WORKSPACE"})
	rows = append(rows, procRow{
		kind:  procRowTotal,
		label: "Total",
		rss:   resp.Total + m.tuiLocalMemTotal(),
		cpu:   proctree.UnknownCPU,
	})

	byTab := map[string][]ipc.PaneResourceInfo{}
	tabTotals := map[string]uint64{}
	for _, p := range resp.Panes {
		byTab[p.TabID] = append(byTab[p.TabID], p)
		tabTotals[p.TabID] += p.TotalBytes
	}

	order, names := m.tabOrderAndNames()
	seen := map[string]bool{}
	tabIDs := make([]string, 0, len(byTab))
	for _, id := range order {
		if _, ok := byTab[id]; ok {
			tabIDs = append(tabIDs, id)
			seen[id] = true
		}
	}
	for id := range byTab {
		if !seen[id] {
			tabIDs = append(tabIDs, id)
		}
	}
	sort.SliceStable(tabIDs, func(i, j int) bool {
		return tabTotals[tabIDs[i]] > tabTotals[tabIDs[j]]
	})

	for _, tabID := range tabIDs {
		name := names[tabID]
		if name == "" {
			name = tabID
		}
		rows = append(rows, procRow{
			kind:       procRowTab,
			tabID:      tabID,
			label:      name,
			rss:        tabTotals[tabID],
			cpu:        proctree.UnknownCPU,
			expandable: true,
			expanded:   m.proc.expandedTabs[tabID],
		})
		if !m.proc.expandedTabs[tabID] {
			continue
		}

		panes := byTab[tabID]
		sort.SliceStable(panes, func(i, j int) bool {
			return panes[i].TotalBytes > panes[j].TotalBytes
		})
		for _, p := range panes {
			expanded := m.proc.expandedPanes[p.PaneID]
			rows = append(rows, procRow{
				kind:   procRowPane,
				tabID:  tabID,
				paneID: p.PaneID,
				label:  m.paneRowLabel(p.PaneID),
				rss:    p.TotalBytes + m.tuiLocalMem(p.PaneID),
				// Only walk the tree for a pane the user has opened. Summing a
				// collapsed pane's whole subtree on every render makes the cost
				// independent of what is actually on screen.
				cpu:        paneRowCPU(p.Tree, expanded),
				expandable: p.Tree != nil,
				expanded:   expanded,
			})
			if !expanded || p.Tree == nil {
				continue
			}
			rows = appendProcNodeRows(rows, p.Tree, p.PaneID, tabID)
		}
	}

	if tuiLocal := m.tuiLocalMemTotal(); tuiLocal > 0 {
		rows = append(rows, procRow{
			kind:  procRowTUILocal,
			label: "TUI-local",
			rss:   tuiLocal,
			cpu:   proctree.UnknownCPU,
		})
	}
	return rows
}

// appendProcNodeRows walks one pane's tree into rows.
//
// Depth 1 is the pane's own direct child and is NOT killable — that is
// restart-pane, which already exists. Everything below it was started by the
// user inside their own pane, which is where the kill's blast radius stops.
func appendProcNodeRows(rows []procRow, n *ipc.ProcNode, paneID, tabID string) []procRow {
	if n == nil {
		return rows
	}
	killable := n.Depth >= 2
	flag := ""
	if !killable {
		flag = "not killable"
	}
	rows = append(rows, procRow{
		kind:     procRowProc,
		tabID:    tabID,
		paneID:   paneID,
		label:    n.Name,
		pid:      n.PID,
		startMS:  n.StartMS,
		depth:    clampDepth(n.Depth),
		rss:      n.RSSBytes,
		cpu:      n.CPUPct,
		killable: killable,
		flag:     flag,
	})
	for i := range n.Children {
		rows = appendProcNodeRows(rows, &n.Children[i], paneID, tabID)
	}
	return rows
}

// maxProcIndentDepth bounds how far a process row is indented.
const maxProcIndentDepth = 12

// clampDepth bounds a wire-supplied depth into a renderable range.
//
// This is not defensive tidiness. `Depth` is a plain int decoded straight off
// the socket, and the indent is `strings.Repeat("  ", depth+2)` — which PANICS
// on a negative count. A daemon answering with `"depth": -3` therefore kills the
// TUI outright the moment the user expands a pane, which is the dialog's own
// happy path; verified empirically. In remote mode that daemon is a machine the
// user may not control. A large positive value is the other half: it allocates
// a multi-megabyte indent string per row, per render.
//
// A legitimate daemon emits neither — proctree's walk starts at 1 and only
// increments — which is exactly why nothing else catches it.
func clampDepth(d int) int {
	if d < 1 {
		return 1
	}
	if d > maxProcIndentDepth {
		return maxProcIndentDepth
	}
	return d
}

// paneRowCPU is procTreeCPU for an expanded pane, unknown for a collapsed one.
//
// A collapsed pane's rows are not on screen, so walking its whole subtree to
// total a number nobody is looking at makes render cost scale with the
// workspace rather than with the viewport.
func paneRowCPU(tree *ipc.ProcNode, expanded bool) float64 {
	if !expanded {
		return proctree.UnknownCPU
	}
	return procTreeCPU(tree)
}

// procTreeCPU sums a tree's CPU, or reports unknown when nothing in it has an
// answer.
//
// Summing only the KNOWN values and reporting unknown when there are none keeps
// the distinction the whole CPU model rests on: a pane whose processes have not
// been sampled twice yet reads as unknown, not as idle.
func procTreeCPU(n *ipc.ProcNode) float64 {
	if n == nil {
		return proctree.UnknownCPU
	}
	total, any := 0.0, false
	var walk func(*ipc.ProcNode)
	walk = func(x *ipc.ProcNode) {
		if x == nil {
			return
		}
		if x.CPUPct >= 0 {
			total += x.CPUPct
			any = true
		}
		for i := range x.Children {
			walk(&x.Children[i])
		}
	}
	walk(n)
	if !any {
		return proctree.UnknownCPU
	}
	return total
}

// procRoleRank orders the quil section: TUI, daemon, then bridges.
func procRoleRank(role string) int {
	switch role {
	case "tui":
		return 0
	case "daemon":
		return 1
	case "bridge":
		return 2
	}
	return 3
}

// paneRowLabel names a pane for the dialog.
func (m Model) paneRowLabel(paneID string) string {
	if pane := m.findPaneModel(paneID); pane != nil {
		if name := paneDisplayName(pane); name != "" {
			return name
		}
	}
	return paneID
}

// procVisibleRows is how many rows fit under this terminal height.
func (m Model) procVisibleRows() int {
	if avail := m.height - procChromeRows; avail > procMinRows {
		return avail
	}
	return procMinRows
}

// procWindow returns the half-open row range to render.
//
// Pure, and called by BOTH the cursor sync and the renderer, because render
// must not depend on Update having run — a WindowSizeMsg can change the row
// budget between them. Same shape and same reasoning as historyWindow.
func procWindow(total, cursor, scroll, visible int) (start, end int) {
	if total <= 0 || visible <= 0 {
		return 0, 0
	}
	if visible > total {
		visible = total
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > total-1 {
		cursor = total - 1
	}
	// Keep the cursor inside the window, then pull the window back inside the
	// list. Order matters: a list that shrank under a refresh can leave a
	// stored scroll past the last valid origin, which draws blank rows under
	// the final row.
	if cursor < scroll {
		scroll = cursor
	}
	if cursor >= scroll+visible {
		scroll = cursor - visible + 1
	}
	if scroll > total-visible {
		scroll = total - visible
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll, scroll + visible
}

// syncProcScroll re-derives the scroll origin after a cursor move.
func (m Model) syncProcScroll(total int) Model {
	start, _ := procWindow(total, m.proc.cursor, m.proc.scroll, m.procVisibleRows())
	m.proc.scroll = start
	return m
}

// procTreesStale reports whether the tree snapshot is old enough to say so.
func (m Model) procTreesStale(now time.Time) bool {
	if m.proc.resp == nil || m.proc.resp.TreesAt == 0 {
		return false
	}
	return now.Sub(time.Unix(0, m.proc.resp.TreesAt)) > procStaleAfter
}

// handleProcessesDialogKey handles a key press while the dialog is open.
func (m Model) handleProcessesDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.procRows()

	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		return m, nil

	case "r", "R":
		m.proc.notice = ""
		return m.requestTreesNow()

	case "up", "k":
		if m.proc.cursor > 0 {
			m.proc.cursor--
		}
		return m.syncProcScroll(len(rows)), nil

	case "down", "j":
		if m.proc.cursor < len(rows)-1 {
			m.proc.cursor++
		}
		return m.syncProcScroll(len(rows)), nil

	case "pgup":
		m.proc.cursor -= m.procVisibleRows()
		if m.proc.cursor < 0 {
			m.proc.cursor = 0
		}
		return m.syncProcScroll(len(rows)), nil

	case "pgdown":
		m.proc.cursor += m.procVisibleRows()
		if m.proc.cursor > len(rows)-1 {
			m.proc.cursor = len(rows) - 1
		}
		if m.proc.cursor < 0 {
			m.proc.cursor = 0
		}
		return m.syncProcScroll(len(rows)), nil

	case "home":
		m.proc.cursor = 0
		return m.syncProcScroll(len(rows)), nil

	case "end":
		m.proc.cursor = len(rows) - 1
		if m.proc.cursor < 0 {
			m.proc.cursor = 0
		}
		return m.syncProcScroll(len(rows)), nil

	case "enter", "right", "l":
		m = m.toggleProcRow(rows, true)
		return m.syncProcScroll(len(m.procRows())), nil

	case "left", "h":
		m = m.toggleProcRow(rows, false)
		return m.syncProcScroll(len(m.procRows())), nil

	case "K":
		// Uppercase K only. Lowercase k is the vim-style cursor-up binding
		// three cases above, and binding a destructive action to a navigation
		// key is how a user kills a process while scrolling.
		return m.confirmKillProcess(rows)
	}
	return m, nil
}

// requestTreesNow refreshes immediately, bypassing the tick.
func (m Model) requestTreesNow() (tea.Model, tea.Cmd) {
	m.proc.loading = m.proc.resp == nil
	updated, cmd := m.requestTrees(time.Now())
	return updated, cmd
}

// ensureExpandMaps makes the expand maps writable.
//
// Reading a nil map is fine; WRITING to one panics. The maps are created in
// openProcessesDialog, but two paths reach dialogProcesses without passing
// through it — the kill confirm's accept and cancel arms both return to the
// dialog directly. Neither is reachable today without having opened the dialog
// first, so this is not a live crash; it is a guarantee that does not depend on
// that reachability argument staying true. The previous version of this feature
// was full of correct-by-reachability claims, and they are what broke.
func (m Model) ensureExpandMaps() Model {
	if m.proc.expandedTabs == nil {
		m.proc.expandedTabs = map[string]bool{}
	}
	if m.proc.expandedPanes == nil {
		m.proc.expandedPanes = map[string]bool{}
	}
	return m
}

// toggleProcRow expands or collapses the row under the cursor.
func (m Model) toggleProcRow(rows []procRow, expand bool) Model {
	if m.proc.cursor < 0 || m.proc.cursor >= len(rows) {
		return m
	}
	m = m.ensureExpandMaps()
	row := rows[m.proc.cursor]
	switch row.kind {
	case procRowTab:
		m.proc.expandedTabs[row.tabID] = expand
	case procRowPane:
		m.proc.expandedPanes[row.paneID] = expand
	}
	return m
}

// confirmKillProcess routes a kill through the standard confirm dialog.
//
// The confirm carries the PID and the start time the dialog is looking at, so
// the daemon can refuse if either has changed by the time the user says yes.
func (m Model) confirmKillProcess(rows []procRow) (tea.Model, tea.Cmd) {
	if m.proc.cursor < 0 || m.proc.cursor >= len(rows) {
		return m, nil
	}
	row := rows[m.proc.cursor]
	if row.kind != procRowProc || !row.killable {
		m.proc.notice = "only processes started inside a pane can be stopped here"
		return m, nil
	}

	m.dialog = dialogConfirm
	m.confirmKind = confirmKindKillProcess
	m.confirmName = sanitizeRemoteText(row.label)
	m.confirmID = row.paneID
	m.killPID = row.pid
	m.killStartMS = row.startMS
	return m, nil
}

// killRequestPayload is what the accepted confirm will send.
//
// Pulled out of the Cmd so a test can assert the PANE, PID and START TIME that
// actually leave — the removed version of this feature had a kill path no test
// reached at all, and disabling its confirm branch left the whole suite green.
// Reading these from the confirm's own fields is what carries the identity the
// user was shown across the dialog.
func (m Model) killRequestPayload() ipc.KillProcessReqPayload {
	return ipc.KillProcessReqPayload{
		PaneID:  m.confirmID,
		PID:     m.killPID,
		StartMS: m.killStartMS,
	}
}

// sendKillProcess issues the kill request the confirm accepted.
func (m Model) sendKillProcess() tea.Cmd {
	payload := m.killRequestPayload()
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		msg, err := ipc.NewMessage(ipc.MsgKillProcessReq, payload)
		if err != nil {
			log.Printf("kill process %d: marshal: %v", payload.PID, err)
			return nil
		}
		msg.ID = fmt.Sprintf("kill-%d", time.Now().UnixNano())
		if err := m.sendForPane(payload.PaneID, msg); err != nil {
			log.Printf("kill process %d: send: %v", payload.PID, err)
		}
		return nil
	}
}

// applyKillProcessResp surfaces the outcome as a line in the dialog.
//
// Guarded on the dialog being open for the same reason applyResourceReport is:
// a response landing after Esc would otherwise set a notice that surfaces the
// next time the dialog opens, describing something the user did earlier.
func (m Model) applyKillProcessResp(resp ipc.KillProcessRespPayload) Model {
	if m.dialog != dialogProcesses {
		return m
	}
	if resp.Refused != "" {
		m.proc.notice = resp.Refused
		return m
	}
	m.proc.notice = fmt.Sprintf("stopped %d process(es)", resp.Signalled)
	if resp.Escalated > 0 {
		m.proc.notice += fmt.Sprintf(" (%d forced)", resp.Escalated)
	}
	return m
}

// --- rendering ---

func (m Model) renderProcessesDialog() string {
	var b strings.Builder
	inner := dialogInnerWidth(m.lastWidth, processesDialogWidth)

	b.WriteString(dialogTitle.Render("Processes"))
	b.WriteByte('\n')

	if m.proc.resp == nil {
		if m.proc.loading {
			b.WriteString("Loading...\n")
		} else {
			b.WriteString(dialogSubtle.Render("No report yet.\n"))
		}
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render("Esc close"))
		return b.String()
	}

	now := time.Now()
	if m.procTreesStale(now) {
		b.WriteString(dialogSubtle.Render(truncateToWidth(
			"process data is stale — the daemon has not completed a scan recently", inner)))
		b.WriteByte('\n')
	}

	rows := m.procRows()
	start, end := procWindow(len(rows), m.proc.cursor, m.proc.scroll, m.procVisibleRows())
	for i := start; i < end; i++ {
		b.WriteString(renderProcRow(rows[i], i == m.proc.cursor, inner))
		b.WriteByte('\n')
	}
	// Say what is off-screen rather than letting the list end silently — a
	// workspace with more panes than rows otherwise looks like it has fewer.
	if hidden := len(rows) - end + start; hidden > 0 && end > start {
		if above, below := start, len(rows)-end; above > 0 || below > 0 {
			b.WriteString(dialogSubtle.Render(truncateToWidth(
				fmt.Sprintf("  … %d above, %d below", above, below), inner)))
			b.WriteByte('\n')
		}
	}

	if m.proc.notice != "" {
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render(truncateToWidth(
			sanitizeRemoteText(m.proc.notice), inner)))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("r refresh · enter/←→ expand · K stop process · esc close"))
	if m.proc.resp != nil && m.proc.resp.CPUSupported && !m.proc.resp.CPUSampled {
		b.WriteByte('\n')
		b.WriteString(dialogSubtle.Render(truncateToWidth(
			"CPU here is a kernel average, not usage over the sample window.", inner)))
	}
	return b.String()
}

// Column widths for a process row. The name column takes whatever is left, so
// every row is exactly inner cells wide.
const (
	procColMem  = 10
	procColCPU  = 8
	procColPID  = 7
	procColFlag = 14
)

// renderProcRow draws one row at exactly inner cells.
//
// Sized against dialogInnerWidth rather than a hardcoded number. The removed
// process dialog emitted 89-cell rows into an 86-cell budget, so every row
// wrapped and the dialog rendered at double height.
func renderProcRow(row procRow, selected bool, inner int) string {
	style := dialogNormal
	if selected {
		style = dialogSelected
	}

	switch row.kind {
	case procRowSectionQuil:
		// Column headers, so the values below are not unlabelled numbers.
		return dialogSubtle.Render(procQuilLine("QUIL", "VERSION", "UPTIME", "PID", "BINARY", inner))

	case procRowSectionWorkspace:
		return dialogSubtle.Render(procLine("WORKSPACE", "MEM", "CPU", 0, "", inner))

	case procRowUnidentified:
		return dialogSubtle.Render(truncateToWidth("    "+sanitizeRemoteText(row.label), inner))

	case procRowQuil:
		// Version, uptime and the binary name are the whole point of this
		// section: a bridge still executing quil.exe.old.3 after an in-place
		// upgrade renamed the binary aside is what it exists to surface, and a
		// role plus a PID cannot show that.
		role := "    " + sanitizeRemoteText(row.label)
		if row.flag != "" {
			role = "  " + row.flag + " " + sanitizeRemoteText(row.label)
		}
		return style.Render(procQuilLine(
			role,
			sanitizeRemoteText(row.version),
			formatUptime(row.uptime),
			strconv.Itoa(row.pid),
			sanitizeRemoteText(row.exeName),
			inner,
		))
	}

	indent := ""
	switch row.kind {
	case procRowTab:
		indent = "  "
	case procRowPane:
		indent = "    "
	case procRowProc:
		// depth 1 sits under the pane; each level below indents further.
		indent = strings.Repeat("  ", clampDepth(row.depth)+2)
	case procRowTotal, procRowTUILocal:
		indent = "  "
	}

	// The expand indicator. Nothing else tells the user a row can be opened.
	marker := ""
	if row.expandable {
		if row.expanded {
			marker = "▾ "
		} else {
			marker = "▸ "
		}
	} else if row.kind == procRowTab || row.kind == procRowPane {
		marker = "  "
	}

	name := indent + marker + sanitizeRemoteText(row.label)
	return style.Render(procLine(name, memreport.HumanBytes(row.rss), formatCPU(row.cpu), row.pid, row.flag, inner))
}

// formatUptime renders a duration compactly: 6d 03h, 2h 14m, 12m, 45s.
func formatUptime(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %02dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// padCell pads or truncates s to exactly w CELLS.
//
// fmt's %-*s pads to a RUNE count, which is not the same thing and is wrong for
// every value on this path. A pane named 构建 is 2 runes and 4 cells; truncating
// it to a cell budget and then padding by runes overshoots, the row exceeds the
// dialog's content width, and it soft-wraps — the exact 89-cells-into-86 defect
// this dialog's predecessor was pulled for. sanitizeRemoteText deliberately
// preserves printable non-ASCII byte-identically, so a remote host's process
// names arrive here unchanged and this is not a rare path.
//
// Mirrors padOrTrunc in sidebar.go, which exists for the same reason.
func padCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = truncateToWidth(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// padCellRight is padCell, right-aligned.
func padCellRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = truncateToWidth(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s = strings.Repeat(" ", pad) + s
	}
	return s
}

// procLine lays out one row's columns to exactly inner cells.
//
// EVERY column goes through the cell-exact padders, numeric ones included. The
// numbers come off the wire from a daemon that in remote mode is a machine the
// user may not control: a PID of math.MaxInt64 is 19 characters into a 7-cell
// column, and an absurd CPU float formats to hundreds. Bounding only the
// strings would leave the row width dependent on values nobody validates.
func procLine(name, mem, cpu string, pid int, flag string, inner int) string {
	pidStr := ""
	if pid > 0 {
		pidStr = strconv.Itoa(pid)
	}
	nameW := inner - procColMem - procColCPU - procColPID - procColFlag
	if nameW < 8 {
		nameW = 8
	}
	line := padCell(name, nameW) +
		padCellRight(mem, procColMem) +
		padCellRight(cpu, procColCPU) +
		padCellRight(pidStr, procColPID) +
		"  " + padCell(flag, procColFlag-2)

	// The final, unconditional bound.
	//
	// The column arithmetic above cannot satisfy a narrow terminal on its own:
	// nameW has a floor, so below ~48 cells the FIXED columns already exceed
	// the budget and the row came out 47 cells wide whatever `inner` said.
	// Clamping the assembled line is a guarantee that holds regardless of how
	// the columns are later retuned — which matters, because every previous
	// version of this row overflowed for a different reason each time.
	return truncateToWidth(line, inner)
}

// quilNameCol is the role column's width in the quil section.
//
// Fixed and narrow because the roles are "tui", "daemon" and "bridge"; the
// space that buys goes to the binary name, which has to fit
// "quil.exe.old.3" — the whole reason the column exists.
const quilNameCol = 22

// procQuilLine lays out one quil-section row to exactly inner cells.
func procQuilLine(role, version, uptime, pid, binary string, inner int) string {
	binW := inner - quilNameCol - procColMem - procColCPU - procColPID - 2
	if binW < 4 {
		binW = 4
	}
	line := padCell(role, quilNameCol) +
		padCellRight(version, procColMem) +
		padCellRight(uptime, procColCPU) +
		padCellRight(pid, procColPID) +
		"  " + padCell(binary, binW)
	return truncateToWidth(line, inner)
}

// cpuDisplayCeiling bounds what the CPU column will render.
//
// A machine cannot exceed 100% per core and no plausible box has 10,000 of
// them, so anything above this is a wire value nobody should be formatting —
// %.0f on 1e308 produces a 300-character column.
const cpuDisplayCeiling = 1_000_000.0

// formatCPU renders a percentage, or an em dash when there is no answer.
//
// The em dash is the whole point of proctree's UnknownCPU convention reaching
// the screen: a process we have not sampled twice, or one on a platform with no
// CPU source, must not render as "0%" — that reads as idle, which is precisely
// the wrong claim in a dialog for finding something that is spinning.
func formatCPU(pct float64) string {
	if pct < 0 || math.IsNaN(pct) {
		return "—"
	}
	if pct > cpuDisplayCeiling || math.IsInf(pct, 1) {
		return "!"
	}
	return fmt.Sprintf("%.0f%%", pct)
}

// --- helpers carried over from the memory dialog this replaces ---

// tabOrderAndNames extracts the current tab order and name map so rows render
// with user-visible tab names rather than IDs.
func (m Model) tabOrderAndNames() ([]string, map[string]string) {
	tabs := m.allTabs()
	order := make([]string, 0, len(tabs))
	names := make(map[string]string, len(tabs))
	for _, t := range tabs {
		if t == nil {
			continue
		}
		order = append(order, t.ID)
		names[t.ID] = t.Name
	}
	return order, names
}

// tuiLocalMem returns TUI-side memory attributable to a pane.
//
// Today that is the notes editor buffer when the pane has one open. VT grid
// state is not counted — the emulator owns it and exposes no accessor.
func (m Model) tuiLocalMem(paneID string) uint64 {
	if m.notesEditor != nil && m.notesEditor.PaneID() == paneID {
		return m.notesEditor.ApproxBytes()
	}
	return 0
}

// tuiLocalMemTotal returns the single non-zero contribution from a notes
// editor, if any.
//
// Kept from the memory dialog deliberately: it is the ONLY surface for
// TUI-local bytes, and it also feeds the status-bar total. Deleting the dialog
// without carrying this forward would have silently dropped both.
func (m Model) tuiLocalMemTotal() uint64 {
	if m.notesEditor != nil {
		return m.notesEditor.ApproxBytes()
	}
	return 0
}

// findPaneModel returns the pane with the given ID across every project.
func (m Model) findPaneModel(paneID string) *PaneModel {
	for _, tab := range m.allTabs() {
		if tab == nil {
			continue
		}
		for _, p := range tab.Leaves() {
			if p != nil && p.ID == paneID {
				return p
			}
		}
	}
	return nil
}

// --- messages ---

// resourceTickMsg drives the 5 s poll for the resource report.
//
// One tick serves both consumers: the status-bar total (always) and the
// dialog's trees (only while it is open). Two tickers would poll the same
// daemon twice for overlapping data.
type resourceTickMsg struct{}

// resourceReportMsg carries the daemon's resource report.
type resourceReportMsg struct{ Resp ipc.ResourceReportRespPayload }

// killProcessRespMsg carries the outcome of a kill request, including a
// refusal — which is a normal outcome here, not an error.
type killProcessRespMsg struct{ Resp ipc.KillProcessRespPayload }
