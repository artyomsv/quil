package clientauth

import "github.com/artyomsv/quil/internal/ipc"

// Class is a message type's rights class. Every type the daemon accepts is in
// exactly one; the sweep test enforces it.
type Class int

const (
	// ClassView: every level. Reads the workspace, never mutates it.
	ClassView Class = iota + 1
	// ClassAct: standard and full. Input, workspace mutations, and disclosure
	// beyond the workspace (filesystem dialogs, transcripts, input history).
	ClassAct
	// ClassAdmin: full only. Daemon lifecycle and daemon-wide settings. Also
	// what an UNCLASSIFIED type is treated as at runtime (fail closed).
	ClassAdmin
	// ClassLocal: the local socket only, whatever the level.
	ClassLocal
	// ClassNever: daemon→client types, and auth_proof, which only the login
	// code reads. Never dispatched to a handler.
	ClassNever
)

var classes = map[string]Class{
	// view
	ipc.MsgHello: ClassView, ipc.MsgClientHello: ClassView, ipc.MsgClientStat: ClassView,
	ipc.MsgVersionReq: ClassView, ipc.MsgStateReq: ClassView, ipc.MsgAttach: ClassView,
	ipc.MsgDetach: ClassView, ipc.MsgSubscribe: ClassView, ipc.MsgListPanesReq: ClassView,
	ipc.MsgReadPaneOutputReq: ClassView, ipc.MsgPaneStatusReq: ClassView, ipc.MsgScreenshotPaneReq: ClassView,
	ipc.MsgPaneSearchReq: ClassView, ipc.MsgListTabsReq: ClassView, ipc.MsgListProjectsReq: ClassView,
	ipc.MsgListClientsReq: ClassView, ipc.MsgGetNotificationsReq: ClassView, ipc.MsgWatchNotificationsReq: ClassView,
	ipc.MsgGetTaskReq: ClassView, ipc.MsgWaitTaskReq: ClassView, ipc.MsgListTasksReq: ClassView,
	ipc.MsgMemoryReportReq: ClassView, ipc.MsgPluginListReq: ClassView, ipc.MsgPluginCatalogReq: ClassView,
	ipc.MsgSandboxCapReq: ClassView, ipc.MsgNoteGet: ClassView,

	// act — input and workspace mutations
	ipc.MsgPaneInput: ClassAct, ipc.MsgCreatePane: ClassAct, ipc.MsgCreatePaneReq: ClassAct, ipc.MsgSplitPaneReq: ClassAct,
	ipc.MsgDestroyPane: ClassAct, ipc.MsgDestroyPaneReq: ClassAct, ipc.MsgRestartPaneReq: ClassAct,
	ipc.MsgUpdatePane: ClassAct, ipc.MsgUpdateLayout: ClassAct, ipc.MsgMovePane: ClassAct,
	ipc.MsgResizePane: ClassAct, ipc.MsgResizePanes: ClassAct, ipc.MsgClientGeometry: ClassAct,
	ipc.MsgTakeControl: ClassAct, ipc.MsgCreateTab: ClassAct, ipc.MsgCreateTabReq: ClassAct,
	ipc.MsgDestroyTab: ClassAct, ipc.MsgUpdateTab: ClassAct, ipc.MsgReorderTab: ClassAct,
	ipc.MsgMoveTab: ClassAct, ipc.MsgSwitchTab: ClassAct, ipc.MsgSwitchTabReq: ClassAct,
	ipc.MsgSetActivePane: ClassAct, ipc.MsgCloseTUI: ClassAct, ipc.MsgCreateProject: ClassAct,
	ipc.MsgCreateProjectReq: ClassAct, ipc.MsgDestroyProject: ClassAct, ipc.MsgUpdateProject: ClassAct,
	ipc.MsgMergeProjects: ClassAct, ipc.MsgSwitchProject: ClassAct, ipc.MsgReorderProject: ClassAct,
	ipc.MsgDismissEvent: ClassAct, ipc.MsgDelegateTaskReq: ClassAct, ipc.MsgSetProjectGroup: ClassAct,
	ipc.MsgGroupOp: ClassAct, ipc.MsgNoteSet: ClassAct, ipc.MsgSharedImport: ClassAct,
	ipc.MsgCreateFromTemplateReq: ClassAct,
	// act — disclosure beyond the workspace
	ipc.MsgBrowseDirReq: ClassAct, ipc.MsgDirsExistReq: ClassAct, ipc.MsgGitReposReq: ClassAct,
	ipc.MsgKubeCtxReq: ClassAct, ipc.MsgWorktreeListReq: ClassAct, ipc.MsgWorktreeStatusReq: ClassAct,
	ipc.MsgClaudeSessionsReq: ClassAct, ipc.MsgClaudeSessionDetailReq: ClassAct,
	ipc.MsgPaneHistoryReq: ClassAct, ipc.MsgPaneHistoryEntryReq: ClassAct, ipc.MsgResourceReportReq: ClassAct,

	// admin
	ipc.MsgShutdown: ClassAdmin, ipc.MsgReloadPlugins: ClassAdmin, ipc.MsgOverlayPolicy: ClassAdmin,
	ipc.MsgKillProcessReq: ClassAdmin, ipc.MsgStageUpdateReq: ClassAdmin, ipc.MsgUpdateCheckReq: ClassAdmin,

	// local
	ipc.MsgTokenCreateReq: ClassLocal, ipc.MsgTokenListReq: ClassLocal, ipc.MsgTokenRevokeReq: ClassLocal,
}

// neverAccepted is the "never accepted" row, merged into the table by
// ClassOf/Classified rather than an init function.
var neverAccepted = map[string]bool{
	ipc.MsgPaneInputResp: true, ipc.MsgListPanesResp: true, ipc.MsgReadPaneOutputResp: true,
	ipc.MsgPaneStatusResp: true, ipc.MsgCreatePaneResp: true, ipc.MsgSplitPaneResp: true, ipc.MsgRestartPaneResp: true,
	ipc.MsgScreenshotPaneResp: true, ipc.MsgSwitchTabResp: true, ipc.MsgListTabsResp: true,
	ipc.MsgDestroyPaneResp: true, ipc.MsgGetNotificationsResp: true, ipc.MsgWatchNotificationsResp: true,
	ipc.MsgVersionResp: true, ipc.MsgMemoryReportResp: true, ipc.MsgResourceReportResp: true,
	ipc.MsgKillProcessResp: true, ipc.MsgPaneHistoryResp: true, ipc.MsgPaneHistoryEntryResp: true,
	ipc.MsgPaneSearchResp: true, ipc.MsgClaudeSessionsResp: true, ipc.MsgClaudeSessionDetailResp: true,
	ipc.MsgBrowseDirResp: true, ipc.MsgGitReposResp: true, ipc.MsgWorktreeListResp: true,
	ipc.MsgWorktreeStatusResp: true, ipc.MsgDirsExistResp: true, ipc.MsgSandboxCapResp: true,
	ipc.MsgStageUpdateResp: true, ipc.MsgKubeCtxResp: true, ipc.MsgPluginListResp: true,
	ipc.MsgListProjectsResp: true, ipc.MsgCreateProjectResp: true, ipc.MsgProjectOpResp: true,
	ipc.MsgTabOpResp: true, ipc.MsgPaneOpResp: true, ipc.MsgCreateTabResp: true,
	ipc.MsgPluginCatalogResp: true, ipc.MsgDelegateTaskResp: true, ipc.MsgGetTaskResp: true,
	ipc.MsgWaitTaskResp: true, ipc.MsgListTasksResp: true, ipc.MsgListClientsResp: true,
	ipc.MsgGroupOpResp: true, ipc.MsgNoteResp: true, ipc.MsgNoteSetResp: true,
	ipc.MsgSharedImportResp: true, ipc.MsgCreateFromTemplateResp: true, ipc.MsgHelloResp: true,
	ipc.MsgTokenCreateResp: true, ipc.MsgTokenListResp: true, ipc.MsgTokenRevokeResp: true,
	ipc.MsgError: true, ipc.MsgWorkspaceState: true, ipc.MsgPaneOutput: true, ipc.MsgPaneEvent: true,
	ipc.MsgPaneSizes: true, ipc.MsgPluginError: true, ipc.MsgLinkLost: true, ipc.MsgHighlightPane: true,
	ipc.MsgEventDismissed: true, ipc.MsgPaneSeen: true, ipc.MsgAuthChallenge: true,
	// auth_proof is client→daemon, but only the login code (daemon/auth.go)
	// reads it. On a logged-in conn it is refused like any outbound type.
	ipc.MsgAuthProof: true,
}

// ClassOf answers a type's class. An unclassified type is admin: a type
// nobody classified is refused to everything but full (fail closed).
func ClassOf(msgType string) Class {
	if neverAccepted[msgType] {
		return ClassNever
	}
	if c, ok := classes[msgType]; ok {
		return c
	}
	return ClassAdmin
}

// Classified reports whether msgType is in the table (any class).
func Classified(msgType string) bool {
	_, ok := classes[msgType]
	return ok || neverAccepted[msgType]
}

// ClassTable returns a copy of every classified type and its class.
func ClassTable() map[string]Class {
	out := make(map[string]Class, len(classes)+len(neverAccepted))
	for t, c := range classes {
		out[t] = c
	}
	for t := range neverAccepted {
		out[t] = ClassNever
	}
	return out
}

// IsView reports whether every level may send msgType. The TUI uses it to
// drop everything else for a read-only destination.
func IsView(msgType string) bool { return ClassOf(msgType) == ClassView }

// Allows is the ONE rights check. transport is "local" or "tcp".
func Allows(level Level, transport string, msg *ipc.Message) (bool, string) {
	switch ClassOf(msg.Type) {
	case ClassNever:
		return false, "not a request"
	case ClassLocal:
		if transport != "local" {
			return false, "token management is local-socket only"
		}
		return true, ""
	case ClassView:
		return true, ""
	case ClassAct:
		switch level {
		case LevelFull:
			return true, ""
		case LevelStandard:
			return checkStandardPayload(msg)
		}
		return false, "read-only token"
	default:
		if level == LevelFull {
			return true, ""
		}
		return false, "needs full rights"
	}
}

// CarriesRawArgs reports a create that names raw instance arguments, for the
// `privileged` audit line, from any transport.
func CarriesRawArgs(msg *ipc.Message) bool {
	args, _, _ := carriers(msg)
	return args
}

// checkStandardPayload refuses, for a standard conn, every create that
// carries a raw argument list or an overlay flag: instance_args REPLACE
// the plugin's Command.Args, so `type: terminal, instance_args:
// ["-c", …]` runs `bash -c` with no shell and no screen trail.
func checkStandardPayload(msg *ipc.Message) (bool, string) {
	args, overlay, ok := carriers(msg)
	switch {
	case !ok:
		return false, "malformed " + msg.Type
	case args:
		return false, "raw instance arguments need full rights"
	case overlay:
		return false, "overlay panes need full rights"
	}
	return true, ""
}

// carriers decodes the five create shapes. ok is false only for a payload
// that is present and does not decode; an absent payload is the zero value.
func carriers(msg *ipc.Message) (rawArgs, overlay, ok bool) {
	switch msg.Type {
	case ipc.MsgCreatePane:
		var p ipc.CreatePanePayload
		if !decodeIfPresent(msg, &p) {
			return false, false, false
		}
		return len(p.InstanceArgs) > 0, p.Overlay, true
	case ipc.MsgCreatePaneReq:
		var p ipc.CreatePaneReqPayload
		if !decodeIfPresent(msg, &p) {
			return false, false, false
		}
		return len(p.InstanceArgs) > 0, false, true
	case ipc.MsgCreateTab:
		var p ipc.CreateTabPayload
		if !decodeIfPresent(msg, &p) {
			return false, false, false
		}
		return p.FirstPane != nil && len(p.FirstPane.InstanceArgs) > 0, false, true
	case ipc.MsgCreateTabReq:
		var p ipc.CreateTabReqPayload
		if !decodeIfPresent(msg, &p) {
			return false, false, false
		}
		return p.FirstPane != nil && len(p.FirstPane.InstanceArgs) > 0, false, true
	case ipc.MsgSplitPaneReq:
		var p ipc.SplitPaneReqPayload
		if !decodeIfPresent(msg, &p) {
			return false, false, false
		}
		return len(p.Pane.InstanceArgs) > 0, p.Placement == ipc.PlacementOverlay, true
	}
	return false, false, true
}

func decodeIfPresent(msg *ipc.Message, into any) bool {
	if len(msg.Payload) == 0 {
		return true
	}
	return msg.DecodePayload(into) == nil
}
