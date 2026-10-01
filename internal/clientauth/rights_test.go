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
		for field, verdict := range fields {
			if verdict == "refuse" && !covered[typ+"."+field] {
				t.Errorf("%s.%s is audited refuse but no refused row covers it", typ, field)
			}
		}
	}
	allowed := []*ipc.Message{
		msgOf(t, ipc.MsgCreatePane, ipc.CreatePanePayload{Type: "k9s", Toggles: []string{"readonly"}, KubeContext: "prod"}),
		msgOf(t, ipc.MsgCreateTab, ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{Toggles: []string{"chrome"}}}),
		msgOf(t, ipc.MsgCreateTab, nil),
	}
	for _, m := range allowed {
		if ok, reason := Allows(LevelStandard, "tcp", m); !ok {
			t.Errorf("%s with named selections refused: %s", m.Type, reason)
		}
	}
}

// standardFieldAudit is the field audit: EVERY JSON field of the three create
// payloads and what a standard conn may send, with the reason it is allowed
// or refused given in the table in rights.go. "refuse" entries are enforced
// by carriers() and each has a row in TestAllows_StandardPayloadCarriers.
var standardFieldAudit = map[string]map[string]string{
	"CreatePanePayload": {
		"quil_mcp": "allow", "tab_id": "allow", "cwd": "allow", "type": "allow",
		"instance_name": "allow", "instance_args": "refuse", "replace_pane_id": "allow",
		"overlay": "refuse", "resume_session_id": "allow", "worktree": "allow",
		"sandbox": "allow", "toggles": "allow", "kube_context": "allow",
	},
	"FirstPaneSpec": {
		"type": "allow", "cwd": "allow", "instance_name": "allow", "instance_args": "refuse",
		"resume_session_id": "allow", "worktree": "allow", "sandbox": "allow",
		"toggles": "allow", "kube_context": "allow",
	},
	"CreatePaneReqPayload": {
		"tab_id": "allow", "cwd": "allow", "type": "allow", "instance_name": "allow",
		"instance_args": "refuse", "name": "allow", "toggles": "allow",
		"resume_session_id": "allow", "worktree_branch": "allow", "sandbox": "allow",
		"kube_context": "allow",
	},
}

// GUARD: passes on the tree as it stands today. A field added to a create
// payload — a future overlay on a nested request, a new raw argument list —
// fails here until it is classified, and a "refuse" verdict then needs its
// own carriers() check and refused row.
func TestCreatePayloads_EveryFieldAudited(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(ipc.CreatePanePayload{}), reflect.TypeOf(ipc.FirstPaneSpec{}), reflect.TypeOf(ipc.CreatePaneReqPayload{}),
	} {
		audit := standardFieldAudit[typ.Name()]
		seen := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if name == "" || name == "-" {
				t.Errorf("%s.%s has no JSON name: audit it by hand", typ.Name(), typ.Field(i).Name)
				continue
			}
			seen[name] = true
			if _, ok := audit[name]; !ok {
				t.Errorf("%s.%s is not in standardFieldAudit: decide allow or refuse for standard", typ.Name(), name)
			}
		}
		for name := range audit {
			if !seen[name] {
				t.Errorf("standardFieldAudit lists %s.%s, which the struct no longer has", typ.Name(), name)
			}
		}
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
