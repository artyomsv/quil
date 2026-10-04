package clientauth

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func msgOf(t *testing.T, typ string, payload any) *ipc.Message {
	t.Helper()
	if payload == nil {
		return &ipc.Message{Type: typ}
	}
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAllows_ClassTable(t *testing.T) {
	tests := []struct {
		typ                string
		ro, std, full, loc bool
	}{
		{ipc.MsgListPanesReq, true, true, true, true},
		{ipc.MsgAttach, true, true, true, true},
		{ipc.MsgNoteGet, true, true, true, true},
		{ipc.MsgPaneInput, false, true, true, true},
		{ipc.MsgSwitchTab, false, true, true, true},
		{ipc.MsgBrowseDirReq, false, true, true, true},
		{ipc.MsgPaneHistoryReq, false, true, true, true},
		{ipc.MsgShutdown, false, false, true, true},
		{ipc.MsgKillProcessReq, false, false, true, true},
		{ipc.MsgTokenCreateReq, false, false, false, true},
		{ipc.MsgWorkspaceState, false, false, false, false},
		{ipc.MsgAuthProof, false, false, false, false},
		{"made_up_type", false, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			m := msgOf(t, tt.typ, nil)
			got := func(l Level, tr string) bool { ok, _ := Allows(l, tr, m); return ok }
			if got(LevelReadOnly, "tcp") != tt.ro || got(LevelStandard, "tcp") != tt.std ||
				got(LevelFull, "tcp") != tt.full || got(LevelFull, "local") != tt.loc {
				t.Fatalf("ro=%v std=%v full=%v local=%v; want %v %v %v %v",
					got(LevelReadOnly, "tcp"), got(LevelStandard, "tcp"), got(LevelFull, "tcp"), got(LevelFull, "local"),
					tt.ro, tt.std, tt.full, tt.loc)
			}
		})
	}
}

func TestAllows_StandardPayloadCarriers(t *testing.T) {
	// field names the audited "<Struct>.<json>" a row proves refused; "" for
	// a row that is not a field (the malformed payload).
	refused := []struct {
		name, field string
		msg         *ipc.Message
	}{
		{"create_pane args", "CreatePanePayload.instance_args", msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{InstanceArgs: []string{"-c", "id"}})},
		{"create_pane replace args", "CreatePanePayload.instance_args", msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{ReplacePaneID: "p", InstanceArgs: []string{"x"}})},
		{"create_pane overlay", "CreatePanePayload.overlay", msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{Overlay: true})},
		{"create_pane_req terminal args", "CreatePaneReqPayload.instance_args", msgOf(t, ipc.MsgCreatePaneReq, ipc.CreatePaneReqPayload{Type: "terminal", InstanceArgs: []string{"-c", "id"}})},
		{"create_tab first pane args", "FirstPaneSpec.instance_args", msgOf(t, ipc.MsgCreateTab, ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{InstanceArgs: []string{"x"}}})},
		{"create_tab_req first pane args", "CreatePaneReqPayload.instance_args", msgOf(t, ipc.MsgCreateTabReq, ipc.CreateTabReqPayload{FirstPane: &ipc.CreatePaneReqPayload{InstanceArgs: []string{"x"}}})},
		{"malformed create_pane", "", &ipc.Message{Type: ipc.MsgCreatePane, Payload: json.RawMessage(`"x"`)}},
		{"split_pane_req args", "SplitPaneReqPayload.pane.instance_args", msgOf(t, ipc.MsgSplitPaneReq, ipc.SplitPaneReqPayload{Placement: ipc.PlacementRight, Pane: ipc.SplitPaneSpec{Type: "ssh", InstanceArgs: []string{"u@h"}}})},
		{"split_pane_req overlay", "SplitPaneReqPayload.placement", msgOf(t, ipc.MsgSplitPaneReq, ipc.SplitPaneReqPayload{Placement: ipc.PlacementOverlay, OverlayKind: "lazygit"})},
		{"malformed split_pane_req", "", &ipc.Message{Type: ipc.MsgSplitPaneReq, Payload: json.RawMessage(`"x"`)}},
	}
	covered := map[string]bool{}
	for _, tt := range refused {
		if ok, reason := Allows(LevelStandard, "tcp", tt.msg); ok || reason == "" {
			t.Errorf("%s: allowed for standard", tt.name)
		} else if tt.field != "" {
			covered[tt.field] = true
		}
		if ok, _ := Allows(LevelFull, "tcp", tt.msg); !ok && !strings.HasPrefix(tt.name, "malformed") {
			t.Errorf("%s: refused for full", tt.name)
		}
	}
	// A test per refused field: every "refuse" entry of the audit needs a row
	// above that proves it.
	for typ, fields := range standardFieldAudit {
		for field, fa := range fields {
			if fa.verdict == "refuse" && !covered[typ+"."+field] {
				t.Errorf("%s.%s is audited refuse but no refused row covers it", typ, field)
			}
		}
	}
	allowed := []*ipc.Message{
		msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{Type: "k9s", Toggles: []string{"readonly"}, KubeContext: "prod"}),
		msgOf(t, ipc.MsgCreateTab, ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{Toggles: []string{"chrome"}}}),
		msgOf(t, ipc.MsgCreateTab, nil),
		msgOf(t, ipc.MsgSplitPaneReq, ipc.SplitPaneReqPayload{Placement: ipc.PlacementBelow, Pane: ipc.SplitPaneSpec{Type: "claude-code", Toggles: []string{"chrome"}, InstanceName: "work"}}),
	}
	for _, m := range allowed {
		if ok, reason := Allows(LevelStandard, "tcp", m); !ok {
			t.Errorf("%s with named selections refused: %s", m.Type, reason)
		}
	}
}

// fieldAudit pairs a verdict ("allow" or "refuse") with the reason behind it.
type fieldAudit struct {
	verdict, reason string
}

// standardFieldAudit is the field audit: EVERY JSON field of the three create
// payloads and what a standard conn may send, with the reason for each
// verdict. Keyed by dotted JSON path, so a nested struct field (worktree,
// sandbox) is listed by its own leaves — "worktree.repo_root", not
// "worktree" — rather than as one opaque entry. "refuse" entries are
// enforced by carriers() and each has a row in
// TestAllows_StandardPayloadCarriers.
var standardFieldAudit = map[string]map[string]fieldAudit{
	"CreatePanePayload": {
		"quil_mcp":              {"allow", "a named feature (per-spawn Quil MCP), no argument list"},
		"tab_id":                {"allow", "names a tab; creating a pane is act"},
		"cwd":                   {"allow", "a directory; a shell in the pane can cd anywhere anyway"},
		"type":                  {"allow", "a plugin NAME; its Command.Args come from the daemon's registry"},
		"instance_name":         {"allow", "a display label; never executed"},
		"instance_args":         {"refuse", "REPLACES Command.Args: any program, any argv, no shell, no screen trail"},
		"replace_pane_id":       {"allow", "destroying a pane is act (destroy_pane is allowed); combined with instance_args the row above refuses it"},
		"overlay":               {"refuse", "overlay panes (lazygit/hunk) are full-only"},
		"resume_session_id":     {"allow", "validated daemon-side (applyResumeSessionID): an id, not an argument"},
		"worktree.repo_root":    {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"worktree.branch":       {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"worktree.subdir":       {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"sandbox.image":         {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.auth":          {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.claude_config": {"allow", "own or shared, validated daemon-side; a standard conn can already open a host shell that reaches the shared directory"},
		"toggles":               {"allow", "names, resolved by resolveToggles; unknown or conflicting names are refused"},
		"kube_context":          {"allow", "one value after --context, validated against discover = \"kube\""},
	},
	"FirstPaneSpec": {
		"type":                  {"allow", "a plugin NAME; its Command.Args come from the daemon's registry"},
		"cwd":                   {"allow", "a directory; a shell in the pane can cd anywhere anyway"},
		"instance_name":         {"allow", "a display label; never executed"},
		"instance_args":         {"refuse", "REPLACES Command.Args: any program, any argv, no shell, no screen trail"},
		"resume_session_id":     {"allow", "validated daemon-side (applyResumeSessionID): an id, not an argument"},
		"worktree.repo_root":    {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"worktree.branch":       {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"worktree.subdir":       {"allow", "git worktree add on a repo the client names — no more than typing it into a shell grants"},
		"sandbox.image":         {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.auth":          {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.claude_config": {"allow", "own or shared, validated daemon-side; a standard conn can already open a host shell that reaches the shared directory"},
		"toggles":               {"allow", "names, resolved by resolveToggles; unknown or conflicting names are refused"},
		"kube_context":          {"allow", "one value after --context, validated against discover = \"kube\""},
	},
	"CreatePaneReqPayload": {
		"tab_id":                {"allow", "names a tab; creating a pane is act"},
		"cwd":                   {"allow", "a directory; a shell in the pane can cd anywhere anyway"},
		"type":                  {"allow", "a plugin NAME; its Command.Args come from the daemon's registry"},
		"instance_name":         {"allow", "a display label; never executed"},
		"instance_args":         {"refuse", "REPLACES Command.Args: any program, any argv, no shell, no screen trail — for EVERY plugin, terminal included"},
		"name":                  {"allow", "a display name"},
		"toggles":               {"allow", "names, resolved by resolveToggles; unknown or conflicting names are refused"},
		"resume_session_id":     {"allow", "validated daemon-side (applyResumeSessionID): an id, not an argument"},
		"worktree_branch":       {"allow", "a branch name; the repo root is resolved daemon-side from the CWD"},
		"sandbox.image":         {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.auth":          {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"sandbox.claude_config": {"allow", "own or shared, validated daemon-side; a standard conn can already open a host shell that reaches the shared directory"},
		"kube_context":          {"allow", "one value after --context, validated against discover = \"kube\""},
	},
	"SplitPaneReqPayload": {
		"target_pane_id":              {"allow", "names a pane; splitting or replacing it is act (destroy_pane is allowed)"},
		"tab_id":                      {"allow", "names a tab; creating a pane is act"},
		"placement":                   {"refuse", "the value \"overlay\" is full-only, as CreatePanePayload.overlay is; the other values are act"},
		"new_tab.name":                {"allow", "a display name"},
		"new_tab.project_id":          {"allow", "names a project; creating a tab is act"},
		"overlay_kind":                {"allow", "read only with placement overlay, which the placement row refuses"},
		"pane.type":                   {"allow", "a plugin NAME; its Command.Args come from the daemon's registry"},
		"pane.name":                   {"allow", "a display name"},
		"pane.cwd":                    {"allow", "a directory; a shell in the pane can cd anywhere anyway"},
		"pane.toggles":                {"allow", "names, resolved by resolveToggles; unknown or conflicting names are refused"},
		"pane.instance_name":          {"allow", "a display label; never executed"},
		"pane.instance_args":          {"refuse", "REPLACES Command.Args: any program, any argv, no shell, no screen trail"},
		"pane.kube_context":           {"allow", "one value after --context, validated against discover = \"kube\""},
		"pane.resume_session_id":      {"allow", "validated daemon-side (format, transcript, claim): an id, not an argument"},
		"pane.worktree.branch":        {"allow", "a branch name; the repo root is resolved daemon-side from the CWD"},
		"pane.worktree.existing_path": {"allow", "a directory; a shell in the pane can cd anywhere anyway"},
		"pane.sandbox.image":          {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"pane.sandbox.auth":           {"allow", "any image the owner's Docker can reach, inside the sandbox mount boundary"},
		"pane.sandbox.claude_config":  {"allow", "own or shared, validated daemon-side; a standard conn can already open a host shell that reaches the shared directory"},
	},
}

// GUARD (ruling P-7): passes on the tree as it stands today. A field added to
// a create payload — a future overlay on a nested request, a new raw
// argument list, anywhere in the nested worktree/sandbox structs — fails
// here until it is classified, and a "refuse" verdict then needs its own
// carriers() check and refused row.
func TestCreatePayloads_EveryFieldAudited(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(ipc.CreatePanePayload{}), reflect.TypeOf(ipc.FirstPaneSpec{}), reflect.TypeOf(ipc.CreatePaneReqPayload{}),
		reflect.TypeOf(ipc.SplitPaneReqPayload{}),
	} {
		audit := standardFieldAudit[typ.Name()]
		seen := map[string]bool{}
		auditJSONPaths(t, typ, "", seen)
		for path := range seen {
			if _, ok := audit[path]; !ok {
				t.Errorf("%s.%s is not in standardFieldAudit: decide allow or refuse for standard", typ.Name(), path)
			}
		}
		for path := range audit {
			if !seen[path] {
				t.Errorf("standardFieldAudit lists %s.%s, which the struct no longer has", typ.Name(), path)
			}
		}
	}
}

// auditJSONPaths walks rt's fields, recursing into a struct or *struct field
// (worktree, sandbox) so a field added INSIDE one of those is swept too, keyed
// by dotted path ("worktree.repo_root"). A plain scalar/slice field is a leaf
// and is recorded in seen.
func auditJSONPaths(t *testing.T, rt reflect.Type, prefix string, seen map[string]bool) {
	t.Helper()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Errorf("%s.%s has no JSON name: audit it by hand", rt.Name(), f.Name)
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			auditJSONPaths(t, ft, path, seen)
			continue
		}
		seen[path] = true
	}
}

func TestCarriesRawArgs(t *testing.T) {
	if !CarriesRawArgs(msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{InstanceArgs: []string{"u@h"}})) {
		t.Error("instance args not detected")
	}
	if CarriesRawArgs(msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{Toggles: []string{"x"}})) {
		t.Error("toggle names counted as raw args")
	}
}

func TestIsView(t *testing.T) {
	if !IsView(ipc.MsgAttach) || IsView(ipc.MsgPaneInput) || IsView("made_up") {
		t.Fatal("IsView disagrees with the table")
	}
}
