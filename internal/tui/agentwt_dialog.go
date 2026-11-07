package tui

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The "Worktrees…" view: the daemon's record of worktrees and branches that
// appeared while agents ran, and a way to clean them up.
//
// Picking an entry opens the CLOSE dialog's own confirm (confirmKindAgentWT),
// so the file list, the branch rows, the remote gate and the `y` rule are the
// same code a pane close uses. Every request names the entry by the daemon's
// id; the daemon re-checks it against git before acting.

// confirmKindAgentWT is the confirm kind for one record entry.
const confirmKindAgentWT = "agentwt"

// agentWTTimeout bounds the list request; the facts request uses
// worktreeStatusTimeout like the close dialog.
var agentWTTimeout = 6 * time.Second

// agentWTView is the view's state. Zero value = closed (m.dialog decides).
type agentWTView struct {
	dest     string
	entries  []ipc.AgentWorktreeInfo
	loading  bool
	err      string
	notice   string
	gen      string
	cursor   int
	showGone bool
	// entry is the one the confirm is about.
	entry ipc.AgentWorktreeInfo
}

type agentWTListMsg struct {
	Resp ipc.AgentWorktreesRespPayload
	Gen  string
}

type agentWTListTimeoutMsg struct{ gen string }

type agentWTFactsMsg struct {
	Resp ipc.AgentWorktreeFactsRespPayload
	Gen  string
}

type agentWTActMsg struct {
	Resp ipc.AgentWorktreeActRespPayload
}

// openAgentWorktrees opens the view on the active project's destination.
func (m Model) openAgentWorktrees() (tea.Model, tea.Cmd) {
	m.agentWT = agentWTView{dest: m.activeDest()}
	m.dialog = dialogAgentWorktrees
	return m, tea.Batch(tea.ClearScreen, m.requestAgentWTList())
}

// requestAgentWTList asks the daemon for the record.
func (m *Model) requestAgentWTList() tea.Cmd {
	gen := m.nextReqGen()
	m.agentWT.gen, m.agentWT.loading, m.agentWT.err = gen, true, ""
	return m.sendAgentWT(ipc.MsgAgentWorktreesReq, struct{}{}, gen,
		tea.Tick(agentWTTimeout, func(time.Time) tea.Msg { return agentWTListTimeoutMsg{gen: gen} }))
}

// sendAgentWT sends one id-bearing request to the view's destination, with
// an optional timeout command beside it. The destination and the client are
// read here, on the Update goroutine: a Cmd runs on its own goroutine and must
// not reach back into the Model (requestWorktreeStatus's rule).
func (m *Model) sendAgentWT(typ string, payload any, gen string, timeout tea.Cmd) tea.Cmd {
	dest, client := m.agentWT.dest, m.client
	send := func() tea.Msg {
		msg, err := ipc.NewMessage(typ, payload)
		if err != nil {
			log.Printf("agent worktrees: encode %s: %v", typ, err)
			return nil
		}
		msg.ID = gen
		if client == nil {
			return nil
		}
		stampDest(msg, dest)
		if err := client.Send(msg); err != nil {
			log.Printf("agent worktrees: send %s: %v", typ, err)
		}
		return nil
	}
	if timeout == nil {
		return send
	}
	return tea.Batch(send, timeout)
}

func (m *Model) applyAgentWTList(msg agentWTListMsg) {
	if msg.Gen != m.agentWT.gen {
		return
	}
	m.agentWT.loading = false
	m.agentWT.err = msg.Resp.Error
	m.agentWT.entries = msg.Resp.Entries
	m.agentWT.cursor = max(0, min(m.agentWT.cursor, len(m.visibleAgentWT())-1))
}

func (m *Model) applyAgentWTListTimeout(msg agentWTListTimeoutMsg) {
	if msg.gen != m.agentWT.gen || !m.agentWT.loading {
		return
	}
	m.agentWT.loading = false
	m.agentWT.err = "the daemon did not answer — is it running, and new enough?"
}

// applyAgentWTFacts lands the facts on the confirm's one row, through the
// close dialog's own status path.
func (m *Model) applyAgentWTFacts(msg agentWTFactsMsg) {
	st := msg.Resp.Status
	st.Path = m.agentWT.entry.Path
	m.applyWorktreeStatus(worktreeStatusMsg{
		Resp: ipc.WorktreeStatusRespPayload{Paths: []string{st.Path}, Statuses: []ipc.WorktreeStatus{st}, Error: msg.Resp.Error},
		Gen:  msg.Gen,
	})
}

func (m *Model) applyAgentWTAct(msg agentWTActMsg) tea.Cmd {
	if msg.Resp.Error != "" {
		m.agentWT.notice = "Refused: " + msg.Resp.Error
	} else {
		m.agentWT.notice = msg.Resp.Notice
	}
	if m.dialog != dialogAgentWorktrees {
		return nil
	}
	return m.requestAgentWTList()
}

// visibleAgentWT is the list the cursor walks: gone entries only when asked
// for, the active project's repository first, newest first inside.
func (m Model) visibleAgentWT() []ipc.AgentWorktreeInfo {
	root := ""
	if p := m.cur(); p != nil {
		root = filepath.Clean(p.RootDir)
	}
	var out []ipc.AgentWorktreeInfo
	for _, e := range m.agentWT.entries {
		if e.Gone && !m.agentWT.showGone {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := filepath.Clean(out[i].Repo) == root, filepath.Clean(out[j].Repo) == root
		if ai != aj {
			return ai
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

// agentWTLabel is what an entry is called: its branch, else its path.
func agentWTLabel(e ipc.AgentWorktreeInfo) string {
	if e.Branch != "" {
		return e.Branch
	}
	return e.Path
}

func (m Model) handleAgentWTKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.visibleAgentWT()
	switch msg.String() {
	case "esc":
		m.dialog = dialogNone
		m.agentWT = agentWTView{}
		return m, tea.ClearScreen
	case "up", "k":
		if m.agentWT.cursor > 0 {
			m.agentWT.cursor--
		}
	case "down", "j":
		if m.agentWT.cursor < len(rows)-1 {
			m.agentWT.cursor++
		}
	case "g":
		m.agentWT.showGone = !m.agentWT.showGone
		m.agentWT.cursor = 0
	case "r":
		return m, m.requestAgentWTList()
	case "f":
		// Forget drops the record only; git is untouched, so no confirm.
		if len(rows) > 0 {
			e := rows[m.agentWT.cursor]
			m.agentWT.notice = "Forgetting…"
			return m, m.sendAgentWT(ipc.MsgAgentWorktreeActReq, ipc.AgentWorktreeActReqPayload{ID: e.ID, Forget: true}, m.nextReqGen(), nil)
		}
	case "enter":
		if len(rows) > 0 {
			e := rows[m.agentWT.cursor]
			if e.Gone {
				m.agentWT.notice = "That one is already gone; f forgets it."
				return m, nil
			}
			return m.openAgentWTConfirm(e)
		}
	}
	return m, nil
}

// openAgentWTConfirm opens the close dialog's confirm for one entry and asks
// the daemon for its facts.
func (m Model) openAgentWTConfirm(e ipc.AgentWorktreeInfo) (tea.Model, tea.Cmd) {
	m.resetConfirmWorktrees()
	m.dialog = dialogConfirm
	m.confirmKind = confirmKindAgentWT
	m.confirmID = e.ID
	m.confirmName = agentWTLabel(e)
	m.agentWT.entry = e
	m.confirmWorktrees = []confirmWorktree{{path: e.Path, label: agentWTLabel(e)}}
	if e.Kind == "branch" {
		// No worktree to tick: the branch rows stand on their own.
		m.confirmRemoveWorktree = true
		m.confirmRow = confirmRowLocal
	}
	gen := m.nextReqGen()
	m.confirmWorktreeGen = gen
	cmd := m.sendAgentWT(ipc.MsgAgentWorktreeFactsReq, ipc.AgentWorktreeFactsReqPayload{ID: e.ID}, gen,
		tea.Tick(worktreeStatusTimeout, func(time.Time) tea.Msg { return worktreeStatusTimeoutMsg{gen: gen} }))
	return m, tea.Batch(tea.ClearScreen, cmd)
}

// agentWTBranchEntry reports that the open confirm is about a branch entry.
func (m Model) agentWTBranchEntry() bool {
	return m.confirmKind == confirmKindAgentWT && m.agentWT.entry.Kind == "branch"
}

// sendAgentWTAct sends the confirm's choice and returns to the list.
func (m Model) sendAgentWTAct(removeWorktree, local, remote bool, confirmed []ipc.BranchConfirmation) (tea.Model, tea.Cmd) {
	e := m.agentWT.entry
	m.dialog = dialogAgentWorktrees
	req := ipc.AgentWorktreeActReqPayload{ID: e.ID, DeleteBranch: local, DeleteRemoteBranch: remote}
	if e.Kind != "branch" {
		req.RemoveWorktree = removeWorktree
	}
	if len(confirmed) > 0 {
		req.Confirmation = confirmed[0]
	}
	if !req.RemoveWorktree && !local && !remote {
		m.agentWT.notice = "Nothing was ticked."
		return m, tea.ClearScreen
	}
	m.agentWT.notice = "Working…"
	return m, tea.Batch(tea.ClearScreen,
		m.sendAgentWT(ipc.MsgAgentWorktreeActReq, req, m.nextReqGen(), nil))
}

// renderAgentWTDialog draws the list.
func (m Model) renderAgentWTDialog() string {
	inner := dialogInnerWidth(m.width, dialogWidth)
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Worktrees and branches from agents"))
	b.WriteString("\n")
	b.WriteString(dialogSubtle.Render(truncateToWidth("Each appeared while the named pane ran.", inner)))
	b.WriteString("\n\n")

	rows := m.visibleAgentWT()
	switch {
	case m.agentWT.loading:
		b.WriteString("  " + dialogSubtle.Render("Loading…"))
	case m.agentWT.err != "":
		b.WriteString("  " + dialogWarn.Render(truncateToWidth("⚠ "+sanitizeRemoteText(m.agentWT.err), inner-2)))
	case len(rows) == 0:
		b.WriteString("  " + dialogSubtle.Render("Nothing recorded."))
	default:
		// Lines left for rows and repository headers: the terminal minus the
		// box chrome and the seven fixed lines (title, subtitle, blank,
		// blank + notice, blank, footer). Each row may bring a header, so the
		// window is half the budget — which keeps the cursor row on screen —
		// and rows are drawn until the budget is spent.
		budget := 24
		if m.height > 0 {
			budget = max(2, m.height-dialogVChrome-7)
		}
		start, _ := historyWindow(len(rows), m.agentWT.cursor, 0, max(1, budget/2))
		used, lastRepo := 0, ""
		for i := start; i < len(rows); i++ {
			e := rows[i]
			need := 1
			if e.Repo != lastRepo {
				need = 2
			}
			if used+need > budget {
				break
			}
			if e.Repo != lastRepo {
				lastRepo = e.Repo
				b.WriteString(dialogSubtle.Render(truncateToWidth(elideMiddle(sanitizeRemoteText(e.Repo), inner), inner)) + "\n")
			}
			b.WriteString(m.agentWTRow(e, i == m.agentWT.cursor, inner) + "\n")
			used += need
		}
	}
	if m.agentWT.notice != "" {
		b.WriteString("\n" + dialogSubtle.Render(truncateToWidth(sanitizeRemoteText(m.agentWT.notice), inner)))
	}
	b.WriteString("\n\n")
	b.WriteString(dialogSubtle.Render(truncateToWidth("Enter clean up  f forget  g gone  r refresh  Esc close", inner)))
	return b.String()
}

// agentWTRow draws one entry: the cursor mark, the name, who it appeared
// under, its age, and its state.
func (m Model) agentWTRow(e ipc.AgentWorktreeInfo, selected bool, inner int) string {
	mark := "  "
	if selected {
		mark = "> "
	}
	who := sanitizeRemoteText(e.PaneName)
	if who == "" {
		who = "a pane"
	}
	if !e.PaneLive {
		who += " (closed)"
	}
	kind := "worktree"
	if e.Kind == "branch" {
		kind = "branch"
	}
	text := fmt.Sprintf("%s %s · while %s ran", kind, sanitizeRemoteText(agentWTLabel(e)), who)
	if e.Source != "" {
		text += " (" + sanitizeRemoteText(e.Source) + ")"
	}
	if age := agentWTAge(e.FirstSeen); age != "" {
		text += " · " + age
	}
	switch {
	case e.Gone:
		text += " · gone"
	case e.InUseBy != "":
		text += " · in use"
	}
	style := dialogNormal
	if selected {
		style = dialogSelected
	}
	if e.Gone {
		style = dialogSubtle
	}
	return mark + style.Render(truncateToWidth(text, inner-len(mark)))
}

// agentWTAge is a short age: 5m, 3h, 2d.
func agentWTAge(firstSeenMs int64) string {
	if firstSeenMs <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(firstSeenMs))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
