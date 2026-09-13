package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestCreateFromTemplate_ProjectHost_ForwardsRequestAndRemembersIDs(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.74.0")
	remote := newFakeIPCDaemonVersion(t, "pane-remote", "1.74.0")
	session, router := toolHarness(t, local, remote)
	router.remember("gpu", "project-remote")
	want := ipc.CreateFromTemplateReqPayload{Template: "pair", Task: "task text", CWD: "/chosen path", Branch: "feat/new", ProjectID: "project-remote"}
	text, err := callTool(t, session, "create_from_template", map[string]any{
		"template": want.Template, "task": want.Task, "cwd": want.CWD, "branch": want.Branch, "project_id": want.ProjectID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ipc.CreateFromTemplateRespPayload
		Host string `json:"host"`
	}
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Host != "gpu" || response.PreparingWorktree != want.Branch || len(response.PaneIDs) != 1 {
		t.Fatal(response)
	}
	remote.mu.Lock()
	var got ipc.CreateFromTemplateReqPayload
	for _, msg := range remote.received {
		if msg.Type == ipc.MsgCreateFromTemplateReq {
			if err := msg.DecodePayload(&got); err != nil {
				t.Error(err)
			}
		}
	}
	remote.mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, id := range append(response.PaneIDs, response.TabID) {
		_, host, err := router.bridgeFor("", id)
		if err != nil || host != "gpu" {
			t.Fatalf("id %s routed to %s: %v", id, host, err)
		}
	}
	_, err = callTool(t, session, "create_from_template", map[string]any{"template": "unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown template") {
		t.Fatalf("refusal: %v", err)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "create_from_template" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"template", "task", "cwd", "branch", "project_id", "host"} {
			if !strings.Contains(string(schema), `"`+field+`"`) {
				t.Errorf("schema missing %s: %s", field, schema)
			}
		}
		return
	}
	t.Fatal("tool not registered")
}
