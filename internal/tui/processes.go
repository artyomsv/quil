package tui

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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
			flag = "stale"
		}
		rows = append(rows, procRow{
			kind:  procRowQuil,
			label: q.Role,
			pid:   q.PID,
			flag:  flag,
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
			kind:  procRowTab,
			tabID: tabID,
			label: name,
			rss:   tabTotals[tabID],
			cpu:   proctree.UnknownCPU,
		})
		if !m.proc.expandedTabs[tabID] {
			continue
		}

		panes := byTab[tabID]
		sort.SliceStable(panes, func(i, j int) bool {
			return panes[i].TotalBytes > panes[j].TotalBytes
		})
		for _, p := range panes {
			rows = append(rows, procRow{
				kind:   procRowPane,
				tabID:  tabID,
				paneID: p.PaneID,
				label:  m.paneRowLabel(p.PaneID),
				rss:    p.TotalBytes + m.tuiLocalMem(p.PaneID),
				cpu:    procTreeCPU(p.Tree),
			})
			if !m.proc.expandedPanes[p.PaneID] || p.Tree == nil {
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
		depth:    n.Depth,
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
		return m, nil

	case "down", "j":
		if m.proc.cursor < len(rows)-1 {
			m.proc.cursor++
		}
		return m, nil

	case "enter", "right", "l":
		return m.toggleProcRow(rows, true), nil

	case "left", "h":
		return m.toggleProcRow(rows, false), nil

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

// toggleProcRow expands or collapses the row under the cursor.
func (m Model) toggleProcRow(rows []procRow, expand bool) Model {
	if m.proc.cursor < 0 || m.proc.cursor >= len(rows) {
		return m
	}
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
func (m Model) applyKillProcessResp(resp ipc.KillProcessRespPayload) Model {
	if resp.Refused != "" {
		m.proc.notice = resp.Refused
		return m
	}
	m.proc.notice = fmt.Sprintf("signalled %d process(es)", resp.Signalled)
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

	for i, row := range m.procRows() {
		b.WriteString(renderProcRow(row, i == m.proc.cursor, inner))
		b.WriteByte('\n')
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
	case procRowSectionQuil, procRowSectionWorkspace:
		return dialogSubtle.Render(truncateToWidth(row.label, inner))

	case procRowUnidentified:
		return dialogSubtle.Render(truncateToWidth("    "+sanitizeRemoteText(row.label), inner))

	case procRowQuil:
		name := "    " + sanitizeRemoteText(row.label)
		flag := row.flag
		if flag == "stale" {
			flag = "⚠ stale"
		}
		return style.Render(procLine(name, "", "", row.pid, flag, inner))
	}

	indent := ""
	switch row.kind {
	case procRowTab:
		indent = "  "
	case procRowPane:
		indent = "    "
	case procRowProc:
		// depth 1 sits under the pane; each level below indents further.
		indent = strings.Repeat("  ", row.depth+2)
	case procRowTotal, procRowTUILocal:
		indent = "  "
	}

	name := indent + sanitizeRemoteText(row.label)
	return style.Render(procLine(name, memreport.HumanBytes(row.rss), formatCPU(row.cpu), row.pid, row.flag, inner))
}

// procLine lays out one row's columns to exactly inner cells.
func procLine(name, mem, cpu string, pid int, flag string, inner int) string {
	pidStr := ""
	if pid > 0 {
		pidStr = fmt.Sprintf("%d", pid)
	}
	nameW := inner - procColMem - procColCPU - procColPID - procColFlag
	if nameW < 8 {
		nameW = 8
	}
	return fmt.Sprintf("%-*s%*s%*s%*s  %-*s",
		nameW, truncateToWidth(name, nameW),
		procColMem, mem,
		procColCPU, cpu,
		procColPID, pidStr,
		procColFlag-2, truncateToWidth(flag, procColFlag-2),
	)
}

// formatCPU renders a percentage, or an em dash when there is no answer.
//
// The em dash is the whole point of proctree's UnknownCPU convention reaching
// the screen: a process we have not sampled twice, or one on a platform with no
// CPU source, must not render as "0%" — that reads as idle, which is precisely
// the wrong claim in a dialog for finding something that is spinning.
func formatCPU(pct float64) string {
	if pct < 0 {
		return "—"
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
