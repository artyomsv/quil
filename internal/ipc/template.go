package ipc

const (
	MsgCreateFromTemplateReq  = "create_from_template_req"
	MsgCreateFromTemplateResp = "create_from_template_resp"
)

type CreateFromTemplateReqPayload struct {
	Template  string `json:"template"`
	Task      string `json:"task,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Branch    string `json:"branch,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type CreateFromTemplateRespPayload struct {
	TabID             string   `json:"tab_id,omitempty"`
	PaneIDs           []string `json:"pane_ids,omitempty"`
	PreparingWorktree string   `json:"preparing_worktree,omitempty"`
	Error             string   `json:"error,omitempty"`
}
