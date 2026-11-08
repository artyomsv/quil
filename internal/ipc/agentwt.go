package ipc

// The record of worktrees and branches that appeared while agents ran
// (daemon agentwt.go), and the cleanup view's requests on it. Every request
// names an entry by its DAEMON-MINTED id: no client can point a delete at a
// path or a branch the daemon did not record and re-check against git.
const (
	MsgAgentWorktreesReq      = "agent_worktrees_req"
	MsgAgentWorktreesResp     = "agent_worktrees_resp"
	MsgAgentWorktreeFactsReq  = "agent_worktree_facts_req"
	MsgAgentWorktreeFactsResp = "agent_worktree_facts_resp"
	MsgAgentWorktreeActReq    = "agent_worktree_act_req"
	MsgAgentWorktreeActResp   = "agent_worktree_act_resp"
)

// AgentWorktreeInfo is one recorded worktree or branch.
//
// It APPEARED while PaneID ran; nothing here says the pane's agent made it.
type AgentWorktreeInfo struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // "worktree" or "branch"
	Repo      string `json:"repo"`
	Path      string `json:"path,omitempty"`
	Branch    string `json:"branch,omitempty"`
	FirstSeen int64  `json:"first_seen"` // unix ms
	Gone      bool   `json:"gone,omitempty"`
	PaneID    string `json:"pane_id,omitempty"`
	PaneName  string `json:"pane_name,omitempty"`
	// PaneLive reports that the pane still exists on this daemon.
	PaneLive bool   `json:"pane_live,omitempty"`
	Source   string `json:"source,omitempty"`
	Trigger  string `json:"trigger,omitempty"`
	// InUseBy names a live pane sitting in the worktree, which blocks its
	// removal.
	InUseBy string `json:"in_use_by,omitempty"`
}

// AgentWorktreesRespPayload answers the list, newest first.
type AgentWorktreesRespPayload struct {
	Entries []AgentWorktreeInfo `json:"entries,omitempty"`
	// UncertainRepos are repositories whose record may miss items: their
	// baseline was read only after an agent could have acted.
	UncertainRepos []string `json:"uncertain_repos,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// AgentWorktreeFactsReqPayload asks what deleting one entry would cost: its
// files (a worktree) and its branch facts.
type AgentWorktreeFactsReqPayload struct {
	ID string `json:"id"`
}

// AgentWorktreeFactsRespPayload answers with the close dialog's own status
// shape, so the view draws it with the same rows. ID echoes the request.
type AgentWorktreeFactsRespPayload struct {
	ID     string         `json:"id"`
	Status WorktreeStatus `json:"status"`
	Error  string         `json:"error,omitempty"`
}

// AgentWorktreeActReqPayload acts on one entry. Forget drops the record only.
// A worktree entry takes RemoveWorktree (and the branch deletes with it); a
// branch entry takes only the branch deletes. Confirmation is CHECK-ONLY:
// the daemon acts on the branch it reads from git, and only when it matches.
type AgentWorktreeActReqPayload struct {
	ID                 string `json:"id"`
	Forget             bool   `json:"forget,omitempty"`
	RemoveWorktree     bool   `json:"remove_worktree,omitempty"`
	DeleteBranch       bool   `json:"delete_branch,omitempty"`
	DeleteRemoteBranch bool   `json:"delete_remote_branch,omitempty"`
	// Confirmation is CHECK-ONLY: the branch and remote target the view
	// showed for this entry (its WorktreePath is ignored: the entry names
	// the worktree).
	Confirmation BranchConfirmation `json:"confirmation"`
	// AcceptUncertain acknowledges that this entry's repository record may
	// miss items (UncertainRepos). The daemon refuses a removal or delete in
	// such a repository without it.
	AcceptUncertain bool `json:"accept_uncertain,omitempty"`
}

// AgentWorktreeActRespPayload answers an act. Error is a refusal that changed
// nothing; Notice describes what was started (each delete reports on its own
// sidebar card).
type AgentWorktreeActRespPayload struct {
	ID     string `json:"id"`
	Error  string `json:"error,omitempty"`
	Notice string `json:"notice,omitempty"`
}
