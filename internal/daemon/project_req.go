package daemon

import (
	"log"

	"github.com/artyomsv/quil/internal/ipc"
)

// answerOp answers an ID-bearing mutation with an OpRespPayload and does
// nothing for a request without an ID. That is the whole compatibility story:
// the TUI never sets an ID on these messages and keeps getting no answer, while
// the MCP bridge sets one on every call and learns whether the operation
// applied instead of inferring it from the next broadcast.
func answerOp(conn *ipc.Conn, msg *ipc.Message, respType, id string, ok bool, errText string) {
	if msg.ID == "" {
		return
	}
	respondTo(conn, msg.ID, respType, ipc.OpRespPayload{ID: id, OK: ok, Error: errText})
}

func (d *Daemon) buildProjectInfos() ([]ipc.ProjectInfo, string) {
	_, _, _, projects, active := d.session.SnapshotState()
	out := make([]ipc.ProjectInfo, 0, len(projects))
	for _, p := range projects {
		out = append(out, ipc.ProjectInfo{
			ID:        p.ID,
			Name:      p.Name,
			RootDir:   p.RootDir,
			Active:    p.ID == active,
			Bootstrap: p.Bootstrap,
			TabIDs:    append([]string(nil), p.TabIDs...),
			ActiveTab: p.ActiveTab,
		})
	}
	return out, active
}

func (d *Daemon) handleListProjectsReq(conn *ipc.Conn, msg *ipc.Message) {
	projects, active := d.buildProjectInfos()
	respondTo(conn, msg.ID, ipc.MsgListProjectsResp, ipc.ListProjectsRespPayload{
		Projects:      projects,
		ActiveProject: active,
	})
}

// handleCreateProjectReq is the request-response twin of the MsgCreateProject
// arm in handleMessage: same create, same empty-project recovery, same
// broadcast and snapshot — plus an answer naming the id.
func (d *Daemon) handleCreateProjectReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.CreateProjectReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgCreateProjectResp, ipc.CreateProjectRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	if req.Name == "" {
		respondTo(conn, msg.ID, ipc.MsgCreateProjectResp, ipc.CreateProjectRespPayload{Error: "name is required"})
		return
	}
	rootDir := d.resolveRequestedCWD(req.RootDir, d.defaultCWD(conn))
	proj := d.session.CreateProject(req.Name, rootDir)
	d.recoverEmptyProject(conn, proj.ID)
	d.broadcastState()
	d.requestSnapshot()
	log.Printf("project created over IPC request: %s %q", proj.ID, proj.Name)
	respondTo(conn, msg.ID, ipc.MsgCreateProjectResp, ipc.CreateProjectRespPayload{
		ProjectID: proj.ID,
		Name:      proj.Name,
	})
}

// tabIDKnown decodes just the tab id out of a tab message and reports whether
// the daemon holds that tab. Used by the op-resp arms for handlers that return
// nothing and may have removed the tab by the time they return.
func tabIDKnown(d *Daemon, msg *ipc.Message, _ string) (string, bool) {
	var p struct {
		TabID string `json:"tab_id"`
	}
	if err := msg.DecodePayload(&p); err != nil || p.TabID == "" {
		return "", false
	}
	return p.TabID, d.session.Tab(p.TabID) != nil
}

func paneIDKnown(d *Daemon, msg *ipc.Message) (string, bool) {
	var p struct {
		PaneID string `json:"pane_id"`
	}
	if err := msg.DecodePayload(&p); err != nil || p.PaneID == "" {
		return "", false
	}
	return p.PaneID, d.session.Pane(p.PaneID) != nil
}

func opErrUnless(ok bool, errText string) string {
	if ok {
		return ""
	}
	return errText
}

// projectExists is the pre-check the destroy arm needs: DestroyProject
// returns the detached panes, which is empty for an unknown id AND for a
// project holding no panes, so it cannot say which happened.
func (d *Daemon) projectExists(id string) bool {
	for _, p := range d.session.Projects() {
		if p.ID == id {
			return true
		}
	}
	return false
}
