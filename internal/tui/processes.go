package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/procscan"
)

// F1 → Processes: what quil is running, and which of it is stray.
//
// Scoped as a DIAGNOSTIC rather than a cleanup tool, because the measurement
// that motivated it found zero orphaned bridges — watchParentExit works. The
// runaway process in that session was an MCP server quil does not own and must
// not offer to kill. So this reports everything and offers termination only for
// what it can positively identify as quil's own orphan.

// processesWidth is wider than dialogWidth: a row carries a PID, a kind, a start
// time and a path, and eliding the path is what makes the dialog useless.
const processesWidth = 92

// processRow is one line of the dialog.
type processRow struct {
	proc procscan.Process
	kind procscan.Kind
}

// killable reports whether the dialog may offer to terminate this row.
//
// Only quil's own orphaned bridges. Everything else — including a parentless
// process that is not ours — is reported and left alone.
func (r processRow) killable() bool { return r.kind == procscan.KindOrphanBridge }

// processesState is the dialog's data. Lives on Model rather than in a package
// var so two TUIs in one process could not share it.
type processesState struct {
	rows    []processRow
	err     error
	scanned time.Time
}

type processesScannedMsg struct {
	rows []processRow
	err  error
}

// refreshProcesses enumerates the process table off the Update goroutine.
//
// A sweep walks every process on the machine and, on Windows, opens each one —
// hundreds of syscalls. Doing that inline would stall every pane for its
// duration, which is the same reason the daemon's filesystem dialogs run on
// workers.
func (m Model) refreshProcesses() tea.Cmd {
	self := os.Getpid()
	return func() tea.Msg {
		procs, err := procscan.Snapshot()
		if err != nil {
			return processesScannedMsg{err: err}
		}
		kinds := procscan.Classify(procs, self)

		rows := make([]processRow, 0, len(procs))
		for _, p := range procs {
			k := kinds[p.PID]
			if k == procscan.KindOther {
				continue // the machine's other processes are not our business
			}
			rows = append(rows, processRow{proc: p, kind: k})
		}
		// Orphans first — they are the reason to open this dialog — then by
		// kind, then oldest first so a long-lived stray is easy to spot.
		sort.SliceStable(rows, func(i, j int) bool {
			if a, b := rows[i].killable(), rows[j].killable(); a != b {
				return a
			}
			if rows[i].kind != rows[j].kind {
				return rows[i].kind < rows[j].kind
			}
			return rows[i].proc.Start.Before(rows[j].proc.Start)
		})
		return processesScannedMsg{rows: rows}
	}
}

func (m Model) openProcessesDialog() Model {
	m.dialog = dialogProcesses
	m.dialogCursor = 0
	m.processes = processesState{}
	return m
}

func (m Model) applyProcessesScan(msg processesScannedMsg) Model {
	m.processes = processesState{rows: msg.rows, err: msg.err, scanned: time.Now()}
	if m.dialogCursor >= len(msg.rows) {
		m.dialogCursor = 0
	}
	return m
}

func (m Model) handleProcessesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.dialog = dialogAbout
		m.dialogCursor = 4 // the Processes row, so Esc lands where it opened
		return m, nil
	case "up", "k":
		if m.dialogCursor > 0 {
			m.dialogCursor--
		}
	case "down", "j":
		if m.dialogCursor < len(m.processes.rows)-1 {
			m.dialogCursor++
		}
	case "r":
		return m, m.refreshProcesses()
	case "enter", "d":
		// Enter opens a CONFIRM, never kills. Same shape as every other
		// destructive action in this TUI.
		if m.dialogCursor < 0 || m.dialogCursor >= len(m.processes.rows) {
			return m, nil
		}
		row := m.processes.rows[m.dialogCursor]
		if !row.killable() {
			m.setFlash("only quil's own orphaned bridges can be stopped here")
			return m, nil
		}
		m.dialog = dialogConfirm
		m.confirmKind = confirmKindKillProcess
		m.confirmID = fmt.Sprintf("%d", row.proc.PID)
		m.confirmName = processLabel(row.proc)
		m.dialogCursor = 0
		return m, nil
	}
	return m, nil
}

// killProcess terminates one PID. Called only from the confirm handler, and only
// for a row killable() accepted.
//
// Re-validates against a FRESH snapshot rather than trusting the row: the scan
// that classified it may be minutes old, the process may have exited, and its
// PID may have been reused by something the user very much does not want killed.
func (m Model) killProcess(pid int) tea.Cmd {
	self := os.Getpid()
	return func() tea.Msg {
		procs, err := procscan.Snapshot()
		if err != nil {
			return processesScannedMsg{err: err}
		}
		stillOrphan := false
		for _, p := range procscan.Orphans(procs, self) {
			if p.PID == pid {
				stillOrphan = true
				break
			}
		}
		if !stillOrphan {
			// Either it exited on its own or the PID now belongs to something
			// else. Refusing is the only safe answer; the refreshed list shows
			// the user what is actually there.
			return processesScannedMsg{err: fmt.Errorf("pid %d is no longer an orphaned quil bridge — not killed", pid)}
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
		procs, err = procscan.Snapshot()
		if err != nil {
			return processesScannedMsg{err: err}
		}
		kinds := procscan.Classify(procs, self)
		rows := make([]processRow, 0, len(procs))
		for _, p := range procs {
			if k := kinds[p.PID]; k != procscan.KindOther {
				rows = append(rows, processRow{proc: p, kind: k})
			}
		}
		return processesScannedMsg{rows: rows}
	}
}

// processLabel is the human name for a process in confirms and rows.
func processLabel(p procscan.Process) string {
	name := p.Name
	if name == "" {
		f := strings.Fields(p.Cmdline)
		if len(f) > 0 {
			name = f[0]
		}
	}
	return fmt.Sprintf("%s (pid %d)", name, p.PID)
}

func (m Model) renderProcessesDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Processes"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  quil's own processes; only orphaned bridges can be stopped"))
	b.WriteString("\n\n")

	switch {
	case m.processes.err != nil:
		b.WriteString(dialogNormal.Render("  " + sanitizeRemoteText(m.processes.err.Error())))
		b.WriteByte('\n')
	case m.processes.scanned.IsZero():
		b.WriteString(dialogSubtle.Render("  scanning…"))
		b.WriteByte('\n')
	case len(m.processes.rows) == 0:
		b.WriteString(dialogNormal.Render("  no quil processes found"))
		b.WriteByte('\n')
	default:
		orphans := 0
		for _, r := range m.processes.rows {
			if r.killable() {
				orphans++
			}
		}
		summary := fmt.Sprintf("  %d quil process(es), %d orphaned", len(m.processes.rows), orphans)
		b.WriteString(dialogSubtle.Render(summary))
		b.WriteString("\n\n")

		for i, r := range m.processes.rows {
			cursor := "  "
			style := dialogNormal
			if i == m.dialogCursor {
				cursor = "> "
				style = dialogSelected
			}
			b.WriteString(cursor + style.Render(processRowText(r)) + "\n")
		}
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("↑↓ navigate  Enter stop orphan  r rescan  Esc back"))
	return b.String()
}

// processRowText formats one row. The path is elided in the MIDDLE because the
// informative half is the tail — a bridge running quil.exe.old.3 is exactly what
// this dialog exists to make visible, and cutting the end hides it.
func processRowText(r processRow) string {
	age := "—"
	if !r.proc.Start.IsZero() {
		age = time.Since(r.proc.Start).Round(time.Minute).String()
	}
	path := r.proc.Cmdline
	if path == "" {
		path = r.proc.Name
	}
	const pathW = 46
	return fmt.Sprintf("%-14s pid %-7d up %-10s %s",
		r.kind.String(), r.proc.PID, age,
		elideMiddle(sanitizeRemoteText(path), pathW))
}
