package clientauth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// daemonToClient is the explicit list of the "never accepted" class. It is
// written out rather than derived from a "_resp" suffix so a new outbound
// type without that suffix is a decision, not an accident.
var daemonToClient = []string{
	ipc.MsgPaneInputResp, ipc.MsgListPanesResp, ipc.MsgReadPaneOutputResp, ipc.MsgPaneStatusResp,
	ipc.MsgCreatePaneResp, ipc.MsgRestartPaneResp, ipc.MsgScreenshotPaneResp, ipc.MsgSwitchTabResp,
	ipc.MsgListTabsResp, ipc.MsgDestroyPaneResp, ipc.MsgGetNotificationsResp, ipc.MsgWatchNotificationsResp,
	ipc.MsgVersionResp, ipc.MsgMemoryReportResp, ipc.MsgResourceReportResp, ipc.MsgKillProcessResp,
	ipc.MsgPaneHistoryResp, ipc.MsgPaneHistoryEntryResp, ipc.MsgPaneSearchResp, ipc.MsgClaudeSessionsResp,
	ipc.MsgClaudeSessionDetailResp, ipc.MsgBrowseDirResp, ipc.MsgGitReposResp, ipc.MsgWorktreeListResp,
	ipc.MsgWorktreeStatusResp, ipc.MsgDirsExistResp, ipc.MsgSandboxCapResp, ipc.MsgStageUpdateResp,
	ipc.MsgKubeCtxResp, ipc.MsgPluginListResp, ipc.MsgListProjectsResp, ipc.MsgCreateProjectResp,
	ipc.MsgProjectOpResp, ipc.MsgTabOpResp, ipc.MsgPaneOpResp, ipc.MsgCreateTabResp, ipc.MsgPluginCatalogResp,
	ipc.MsgDelegateTaskResp, ipc.MsgGetTaskResp, ipc.MsgWaitTaskResp, ipc.MsgListTasksResp, ipc.MsgListClientsResp,
	ipc.MsgGroupOpResp, ipc.MsgNoteResp, ipc.MsgNoteSetResp, ipc.MsgSharedImportResp, ipc.MsgCreateFromTemplateResp,
	ipc.MsgHelloResp, ipc.MsgTokenCreateResp, ipc.MsgTokenListResp, ipc.MsgTokenRevokeResp,
	ipc.MsgError, ipc.MsgWorkspaceState, ipc.MsgPaneOutput, ipc.MsgPaneEvent, ipc.MsgPaneSizes,
	ipc.MsgPluginError, ipc.MsgLinkLost, ipc.MsgHighlightPane, ipc.MsgEventDismissed, ipc.MsgPaneSeen,
	ipc.MsgAuthChallenge,
}

// msgConstants reads every Msg* string constant declared in internal/ipc's
// non-test files, so a type added in a NEW file is swept too.
func msgConstants(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("../ipc/*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Msg") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if v, err := strconv.Unquote(lit.Value); err == nil {
						out[name.Name] = v
					}
				}
			}
		}
	}
	return out
}

func TestSweep_EveryMessageTypeHasAClass(t *testing.T) {
	consts := msgConstants(t)
	if len(consts) < 100 {
		t.Fatalf("parsed only %d Msg constants — the glob or parser broke", len(consts))
	}
	declared := map[string]bool{}
	for _, v := range consts {
		declared[v] = true
	}
	outbound := map[string]bool{}
	for _, v := range daemonToClient {
		outbound[v] = true
		if !declared[v] {
			t.Errorf("daemonToClient lists %q, which no ipc constant declares", v)
		}
		if ClassOf(v) != ClassNever {
			t.Errorf("%q is daemon→client but its class is %d, want ClassNever", v, ClassOf(v))
		}
	}
	for name, v := range consts {
		if outbound[v] {
			continue
		}
		if !Classified(v) {
			t.Errorf("%s (%q) has no class: add it to clientauth's table (view/act/admin/local)", name, v)
			continue
		}
		if ClassOf(v) == ClassNever && v != ipc.MsgAuthProof {
			t.Errorf("%s (%q) is a client→daemon type classed never", name, v)
		}
	}
}

// wantClassTable is written out independently of rights.go's own classes and
// neverAccepted maps, by hand, from the design's class table (view, act,
// admin, local, never — one entry per type). The sweep test above only
// proves every type has SOME class; it would not notice a type moved to the
// wrong one (demoting claude_sessions_req to view, say, would still pass it).
// This test catches that by comparing the two independently-built tables.
var wantClassTable = map[string]Class{
	// view: every level, read-only.
	ipc.MsgHello:                 ClassView,
	ipc.MsgClientHello:           ClassView,
	ipc.MsgClientStat:            ClassView,
	ipc.MsgVersionReq:            ClassView,
	ipc.MsgStateReq:              ClassView,
	ipc.MsgAttach:                ClassView,
	ipc.MsgDetach:                ClassView,
	ipc.MsgSubscribe:             ClassView,
	ipc.MsgListPanesReq:          ClassView,
	ipc.MsgReadPaneOutputReq:     ClassView,
	ipc.MsgPaneStatusReq:         ClassView,
	ipc.MsgScreenshotPaneReq:     ClassView,
	ipc.MsgPaneSearchReq:         ClassView,
	ipc.MsgListTabsReq:           ClassView,
	ipc.MsgListProjectsReq:       ClassView,
	ipc.MsgListClientsReq:        ClassView,
	ipc.MsgGetNotificationsReq:   ClassView,
	ipc.MsgWatchNotificationsReq: ClassView,
	ipc.MsgGetTaskReq:            ClassView,
	ipc.MsgWaitTaskReq:           ClassView,
	ipc.MsgListTasksReq:          ClassView,
	ipc.MsgMemoryReportReq:       ClassView,
	ipc.MsgPluginListReq:         ClassView,
	ipc.MsgPluginCatalogReq:      ClassView,
	ipc.MsgSandboxCapReq:         ClassView,
	ipc.MsgNoteGet:               ClassView,

	// act: standard and full. Input and workspace mutations.
	ipc.MsgPaneInput:             ClassAct,
	ipc.MsgCreatePane:            ClassAct,
	ipc.MsgCreatePaneReq:         ClassAct,
	ipc.MsgDestroyPane:           ClassAct,
	ipc.MsgDestroyPaneReq:        ClassAct,
	ipc.MsgRestartPaneReq:        ClassAct,
	ipc.MsgUpdatePane:            ClassAct,
	ipc.MsgUpdateLayout:          ClassAct,
	ipc.MsgMovePane:              ClassAct,
	ipc.MsgResizePane:            ClassAct,
	ipc.MsgResizePanes:           ClassAct,
	ipc.MsgClientGeometry:        ClassAct,
	ipc.MsgTakeControl:           ClassAct,
	ipc.MsgCreateTab:             ClassAct,
	ipc.MsgCreateTabReq:          ClassAct,
	ipc.MsgDestroyTab:            ClassAct,
	ipc.MsgUpdateTab:             ClassAct,
	ipc.MsgReorderTab:            ClassAct,
	ipc.MsgMoveTab:               ClassAct,
	ipc.MsgSwitchTab:             ClassAct,
	ipc.MsgSwitchTabReq:          ClassAct,
	ipc.MsgSetActivePane:         ClassAct,
	ipc.MsgCloseTUI:              ClassAct,
	ipc.MsgCreateProject:         ClassAct,
	ipc.MsgCreateProjectReq:      ClassAct,
	ipc.MsgDestroyProject:        ClassAct,
	ipc.MsgUpdateProject:         ClassAct,
	ipc.MsgMergeProjects:         ClassAct,
	ipc.MsgSwitchProject:         ClassAct,
	ipc.MsgReorderProject:        ClassAct,
	ipc.MsgDismissEvent:          ClassAct,
	ipc.MsgDelegateTaskReq:       ClassAct,
	ipc.MsgSetProjectGroup:       ClassAct,
	ipc.MsgGroupOp:               ClassAct,
	ipc.MsgNoteSet:               ClassAct,
	ipc.MsgSharedImport:          ClassAct,
	ipc.MsgCreateFromTemplateReq: ClassAct,
	// act: disclosure beyond the workspace.
	ipc.MsgBrowseDirReq:           ClassAct,
	ipc.MsgDirsExistReq:           ClassAct,
	ipc.MsgGitReposReq:            ClassAct,
	ipc.MsgKubeCtxReq:             ClassAct,
	ipc.MsgWorktreeListReq:        ClassAct,
	ipc.MsgWorktreeStatusReq:      ClassAct,
	ipc.MsgClaudeSessionsReq:      ClassAct,
	ipc.MsgClaudeSessionDetailReq: ClassAct,
	ipc.MsgPaneHistoryReq:         ClassAct,
	ipc.MsgPaneHistoryEntryReq:    ClassAct,
	ipc.MsgResourceReportReq:      ClassAct,

	// admin: full only. Daemon lifecycle and daemon-wide settings.
	ipc.MsgShutdown:       ClassAdmin,
	ipc.MsgReloadPlugins:  ClassAdmin,
	ipc.MsgOverlayPolicy:  ClassAdmin,
	ipc.MsgKillProcessReq: ClassAdmin,
	ipc.MsgStageUpdateReq: ClassAdmin,
	ipc.MsgUpdateCheckReq: ClassAdmin,

	// local: the local socket only, whatever the level.
	ipc.MsgTokenCreateReq: ClassLocal,
	ipc.MsgTokenListReq:   ClassLocal,
	ipc.MsgTokenRevokeReq: ClassLocal,

	// never accepted: every daemon→client type, plus auth_proof (client→daemon,
	// but read only by the login code, never dispatched to a handler).
	ipc.MsgPaneInputResp:           ClassNever,
	ipc.MsgListPanesResp:           ClassNever,
	ipc.MsgReadPaneOutputResp:      ClassNever,
	ipc.MsgPaneStatusResp:          ClassNever,
	ipc.MsgCreatePaneResp:          ClassNever,
	ipc.MsgRestartPaneResp:         ClassNever,
	ipc.MsgScreenshotPaneResp:      ClassNever,
	ipc.MsgSwitchTabResp:           ClassNever,
	ipc.MsgListTabsResp:            ClassNever,
	ipc.MsgDestroyPaneResp:         ClassNever,
	ipc.MsgGetNotificationsResp:    ClassNever,
	ipc.MsgWatchNotificationsResp:  ClassNever,
	ipc.MsgVersionResp:             ClassNever,
	ipc.MsgMemoryReportResp:        ClassNever,
	ipc.MsgResourceReportResp:      ClassNever,
	ipc.MsgKillProcessResp:         ClassNever,
	ipc.MsgPaneHistoryResp:         ClassNever,
	ipc.MsgPaneHistoryEntryResp:    ClassNever,
	ipc.MsgPaneSearchResp:          ClassNever,
	ipc.MsgClaudeSessionsResp:      ClassNever,
	ipc.MsgClaudeSessionDetailResp: ClassNever,
	ipc.MsgBrowseDirResp:           ClassNever,
	ipc.MsgGitReposResp:            ClassNever,
	ipc.MsgWorktreeListResp:        ClassNever,
	ipc.MsgWorktreeStatusResp:      ClassNever,
	ipc.MsgDirsExistResp:           ClassNever,
	ipc.MsgSandboxCapResp:          ClassNever,
	ipc.MsgStageUpdateResp:         ClassNever,
	ipc.MsgKubeCtxResp:             ClassNever,
	ipc.MsgPluginListResp:          ClassNever,
	ipc.MsgListProjectsResp:        ClassNever,
	ipc.MsgCreateProjectResp:       ClassNever,
	ipc.MsgProjectOpResp:           ClassNever,
	ipc.MsgTabOpResp:               ClassNever,
	ipc.MsgPaneOpResp:              ClassNever,
	ipc.MsgCreateTabResp:           ClassNever,
	ipc.MsgPluginCatalogResp:       ClassNever,
	ipc.MsgDelegateTaskResp:        ClassNever,
	ipc.MsgGetTaskResp:             ClassNever,
	ipc.MsgWaitTaskResp:            ClassNever,
	ipc.MsgListTasksResp:           ClassNever,
	ipc.MsgListClientsResp:         ClassNever,
	ipc.MsgGroupOpResp:             ClassNever,
	ipc.MsgNoteResp:                ClassNever,
	ipc.MsgNoteSetResp:             ClassNever,
	ipc.MsgSharedImportResp:        ClassNever,
	ipc.MsgCreateFromTemplateResp:  ClassNever,
	ipc.MsgHelloResp:               ClassNever,
	ipc.MsgTokenCreateResp:         ClassNever,
	ipc.MsgTokenListResp:           ClassNever,
	ipc.MsgTokenRevokeResp:         ClassNever,
	ipc.MsgError:                   ClassNever,
	ipc.MsgWorkspaceState:          ClassNever,
	ipc.MsgPaneOutput:              ClassNever,
	ipc.MsgPaneEvent:               ClassNever,
	ipc.MsgPaneSizes:               ClassNever,
	ipc.MsgPluginError:             ClassNever,
	ipc.MsgLinkLost:                ClassNever,
	ipc.MsgHighlightPane:           ClassNever,
	ipc.MsgEventDismissed:          ClassNever,
	ipc.MsgPaneSeen:                ClassNever,
	ipc.MsgAuthChallenge:           ClassNever,
	ipc.MsgAuthProof:               ClassNever,
}

func TestClassTable_MatchesSpec(t *testing.T) {
	got := ClassTable()
	if reflect.DeepEqual(got, wantClassTable) {
		return
	}
	// DeepEqual already failed; this pass only narrows down which entries
	// differ, since a bare map diff is unreadable at 146 entries.
	for k, want := range wantClassTable {
		if g, ok := got[k]; !ok {
			t.Errorf("%s: missing from ClassTable(), want class %d", k, want)
		} else if g != want {
			t.Errorf("%s: class %d, want %d", k, g, want)
		}
	}
	for k, g := range got {
		if _, ok := wantClassTable[k]; !ok {
			t.Errorf("%s: class %d, not in the spec's table at all", k, g)
		}
	}
	t.Errorf("ClassTable() does not match the spec's class table")
}
