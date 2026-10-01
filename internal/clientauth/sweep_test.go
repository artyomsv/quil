package clientauth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
