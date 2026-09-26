package ipc_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestWriteReadMessage(t *testing.T) {
	var buf bytes.Buffer

	msg := &ipc.Message{
		Type:    "create_pane",
		Payload: []byte(`{"cwd":"/home/user"}`),
	}

	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	got, err := ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if got.Type != msg.Type {
		t.Errorf("Type: got %q, want %q", got.Type, msg.Type)
	}
	if string(got.Payload) != string(msg.Payload) {
		t.Errorf("Payload: got %q, want %q", got.Payload, msg.Payload)
	}
}

func TestWriteReadMultipleMessages(t *testing.T) {
	var buf bytes.Buffer

	messages := []*ipc.Message{
		{Type: "attach", Payload: []byte(`{}`)},
		{Type: "pane_input", Payload: []byte(`{"pane_id":"p1","data":"ls\n"}`)},
		{Type: "pane_output", Payload: []byte(`{"pane_id":"p1","data":"file1 file2"}`)},
	}

	for _, m := range messages {
		if err := ipc.WriteMessage(&buf, m); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	for i, want := range messages {
		got, err := ipc.ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage %d failed: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("msg %d Type: got %q, want %q", i, got.Type, want.Type)
		}
	}
}

func TestMessageTypes(t *testing.T) {
	types := []string{
		ipc.MsgAttach,
		ipc.MsgDetach,
		ipc.MsgShutdown,
		ipc.MsgHeartbeat,
		ipc.MsgCreatePane,
		ipc.MsgDestroyPane,
		ipc.MsgResizePane,
		ipc.MsgPaneInput,
		ipc.MsgPaneOutput,
		ipc.MsgCreateTab,
		ipc.MsgDestroyTab,
		ipc.MsgSwitchTab,
		ipc.MsgUpdateTab,
		ipc.MsgReorderTab,
		ipc.MsgWorkspaceState,
		ipc.MsgStateUpdate,
		ipc.MsgListPanesReq,
		ipc.MsgListPanesResp,
		ipc.MsgReadPaneOutputReq,
		ipc.MsgReadPaneOutputResp,
		ipc.MsgPaneStatusReq,
		ipc.MsgPaneStatusResp,
		ipc.MsgCreatePaneReq,
		ipc.MsgCreatePaneResp,
		ipc.MsgRestartPaneReq,
		ipc.MsgRestartPaneResp,
		ipc.MsgScreenshotPaneReq,
		ipc.MsgScreenshotPaneResp,
		ipc.MsgSwitchTabReq,
		ipc.MsgSwitchTabResp,
		ipc.MsgListTabsReq,
		ipc.MsgListTabsResp,
		ipc.MsgDestroyPaneReq,
		ipc.MsgDestroyPaneResp,
		ipc.MsgSetActivePane,
		ipc.MsgCloseTUI,
		ipc.MsgHighlightPane,
		ipc.MsgPaneEvent,
		ipc.MsgDismissEvent,
		ipc.MsgGetNotificationsReq,
		ipc.MsgGetNotificationsResp,
		ipc.MsgWatchNotificationsReq,
		ipc.MsgWatchNotificationsResp,
		ipc.MsgPaneHistoryReq,
		ipc.MsgPaneHistoryResp,
		ipc.MsgPaneHistoryEntryReq,
		ipc.MsgPaneHistoryEntryResp,
		ipc.MsgPaneSearchReq,
		ipc.MsgPaneSearchResp,
		ipc.MsgPaneSizes,
		ipc.MsgResizePanes,
		ipc.MsgClientGeometry,
		ipc.MsgTakeControl,
		ipc.MsgListClientsReq,
		ipc.MsgListClientsResp,
		ipc.MsgEventDismissed,
		ipc.MsgPaneSeen,
	}
	for _, typ := range types {
		if typ == "" {
			t.Error("found empty message type constant")
		}
	}
}

func TestMessageIDBackwardCompat(t *testing.T) {
	// Messages without ID should round-trip with empty ID (omitempty)
	var buf bytes.Buffer
	msg := &ipc.Message{Type: "attach", Payload: []byte(`{}`)}
	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	// Verify the wire format does NOT contain "id" when empty
	if bytes.Contains(buf.Bytes()[4:], []byte(`"id"`)) {
		t.Error("empty ID should be omitted from wire format")
	}
	got, err := ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.ID != "" {
		t.Errorf("ID should be empty, got %q", got.ID)
	}

	// Messages with ID should preserve it
	buf.Reset()
	msg = &ipc.Message{Type: "list_panes_req", ID: "req-123"}
	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	got, err = ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.ID != "req-123" {
		t.Errorf("ID: got %q, want %q", got.ID, "req-123")
	}
}

func TestMCPPayloadRoundTrip(t *testing.T) {
	// ListPanesResp
	resp, err := ipc.NewMessage(ipc.MsgListPanesResp, ipc.ListPanesRespPayload{
		Panes: []ipc.PaneInfo{
			{ID: "pane-1", TabID: "tab-1", TabName: "Shell", Type: "terminal", CWD: "/home", Running: true},
		},
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var payload ipc.ListPanesRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(payload.Panes) != 1 {
		t.Fatalf("Panes: got %d, want 1", len(payload.Panes))
	}
	if payload.Panes[0].ID != "pane-1" {
		t.Errorf("Pane ID: got %q, want %q", payload.Panes[0].ID, "pane-1")
	}
	if !payload.Panes[0].Running {
		t.Error("Pane should be running")
	}
}

func TestNewMessageAndDecode(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
		TabID: "tab-1",
		CWD:   "/home/user",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if msg.Type != ipc.MsgCreatePane {
		t.Errorf("Type: got %q, want %q", msg.Type, ipc.MsgCreatePane)
	}

	var payload ipc.CreatePanePayload
	if err := msg.DecodePayload(&payload); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.TabID != "tab-1" {
		t.Errorf("TabID: got %q, want %q", payload.TabID, "tab-1")
	}
	if payload.CWD != "/home/user" {
		t.Errorf("CWD: got %q, want %q", payload.CWD, "/home/user")
	}
}

func TestEncodeFrame_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: "pane-x", Data: []byte("hello"), Ghost: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := ipc.EncodeFrame(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ipc.ReadMessage(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ReadMessage on encoded frame: %v", err)
	}
	if got.Type != msg.Type || !bytes.Equal(got.Payload, msg.Payload) {
		t.Errorf("round trip mismatch: got %+v want %+v", got, msg)
	}
}

// TestAttachPayload_CWDRoundTrip locks in the wire-format contract for the
// optional CWD field on MsgAttach. New clients send CWD; old clients omit
// it and the daemon must see an empty string (no JSON decode error).
func TestAttachPayload_CWDRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   ipc.AttachPayload
		want string
	}{
		{"new client with CWD", ipc.AttachPayload{Cols: 80, Rows: 24, CWD: "/work"}, "/work"},
		{"old client omits CWD", ipc.AttachPayload{Cols: 80, Rows: 24}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgAttach, tc.in)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			var got ipc.AttachPayload
			if err := msg.DecodePayload(&got); err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			if got.CWD != tc.want {
				t.Errorf("CWD: got %q, want %q", got.CWD, tc.want)
			}
			if got.Cols != tc.in.Cols || got.Rows != tc.in.Rows {
				t.Errorf("dims: got %dx%d, want %dx%d", got.Cols, got.Rows, tc.in.Cols, tc.in.Rows)
			}
		})
	}
}

func TestPaneHistoryPayloads_RoundTrip(t *testing.T) {
	req := ipc.PaneHistoryReqPayload{PaneID: "pane-1"}
	msg, err := ipc.NewMessage(ipc.MsgPaneHistoryReq, req)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var gotReq ipc.PaneHistoryReqPayload
	if err := msg.DecodePayload(&gotReq); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if gotReq.PaneID != "pane-1" {
		t.Fatalf("req round-trip mismatch: %+v", gotReq)
	}

	resp := ipc.PaneHistoryRespPayload{
		PaneID:  "pane-1",
		Entries: []ipc.HistoryEntryMeta{{TsMs: 9, Preview: "a b"}},
	}
	rmsg, _ := ipc.NewMessage(ipc.MsgPaneHistoryResp, resp)
	var gotResp ipc.PaneHistoryRespPayload
	if err := rmsg.DecodePayload(&gotResp); err != nil {
		t.Fatalf("DecodePayload resp: %v", err)
	}
	if len(gotResp.Entries) != 1 || gotResp.Entries[0].TsMs != 9 || gotResp.Entries[0].Preview != "a b" {
		t.Fatalf("resp round-trip mismatch: %+v", gotResp)
	}

	er := ipc.PaneHistoryEntryRespPayload{PaneID: "p", TsMs: 9, Text: "full", Found: true}
	emsg, _ := ipc.NewMessage(ipc.MsgPaneHistoryEntryResp, er)
	var gotER ipc.PaneHistoryEntryRespPayload
	if err := emsg.DecodePayload(&gotER); err != nil {
		t.Fatalf("DecodePayload entry resp: %v", err)
	}
	if !gotER.Found || gotER.Text != "full" {
		t.Fatalf("entry resp round-trip mismatch: %+v", gotER)
	}
}

// TestEncodeFrame_RejectsOversizedFrame: the read side caps frames at 10 MB —
// without a matching write-side guard, an oversized producer poisons the
// stream and surfaces as an opaque "message too large" disconnect on the
// PEER, attributing the failure to the wrong side. The guard also bounds the
// size arithmetic in EncodeFrame's allocation (CodeQL finding on PR #51).
func TestEncodeFrame_RejectsOversizedFrame(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
		PaneID: "pane-x",
		Data:   make([]byte, 11*1024*1024), // base64 in JSON ≈ 14.7 MB > cap
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.EncodeFrame(msg); err == nil {
		t.Fatal("EncodeFrame produced a frame larger than the wire maximum")
	}
}

func TestPaneSearchPayload_RoundTrip(t *testing.T) {
	req := ipc.PaneSearchReqPayload{Query: "connection refused"}
	msg, err := ipc.NewMessage(ipc.MsgPaneSearchReq, req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var gotReq ipc.PaneSearchReqPayload
	if err := msg.DecodePayload(&gotReq); err != nil {
		t.Fatalf("decode req: %v", err)
	}
	if gotReq.Query != req.Query {
		t.Errorf("query = %q, want %q", gotReq.Query, req.Query)
	}

	resp := ipc.PaneSearchRespPayload{
		Query:     "refused",
		Hits:      []ipc.PaneSearchHit{{PaneID: "p1", Matches: 3, Excerpt: "connection refused"}},
		Truncated: true,
	}
	msg2, err := ipc.NewMessage(ipc.MsgPaneSearchResp, resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var gotResp ipc.PaneSearchRespPayload
	if err := msg2.DecodePayload(&gotResp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if len(gotResp.Hits) != 1 {
		t.Errorf("Hits length = %d, want 1", len(gotResp.Hits))
	}
	if gotResp.Query != resp.Query {
		t.Errorf("query = %q, want %q", gotResp.Query, resp.Query)
	}
	if len(gotResp.Hits) >= 1 {
		if gotResp.Hits[0].PaneID != resp.Hits[0].PaneID {
			t.Errorf("PaneID = %q, want %q", gotResp.Hits[0].PaneID, resp.Hits[0].PaneID)
		}
		if gotResp.Hits[0].Matches != resp.Hits[0].Matches {
			t.Errorf("Matches = %d, want %d", gotResp.Hits[0].Matches, resp.Hits[0].Matches)
		}
		if gotResp.Hits[0].Excerpt != resp.Hits[0].Excerpt {
			t.Errorf("Excerpt = %q, want %q", gotResp.Hits[0].Excerpt, resp.Hits[0].Excerpt)
		}
	}
	if gotResp.Truncated != resp.Truncated {
		t.Errorf("Truncated = %v, want %v", gotResp.Truncated, resp.Truncated)
	}
}

func TestMessageOriginIsNeverSerialized(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgResizePane, ipc.ResizePanePayload{PaneID: "pane-abc", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	msg.Origin = "gpu01"

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(data, []byte("gpu01")) {
		t.Fatalf("Origin leaked onto the wire: %s", data)
	}

	var back ipc.Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Origin != "" {
		t.Fatalf("Origin survived a round trip: %q", back.Origin)
	}
}

// A create with no worktree spec must stay wire-identical to today's, or every
// existing client — MCP create_pane, the plugin dialog, restore — silently
// changes shape.
func TestCreatePanePayload_NoWorktreeIsWireIdentical(t *testing.T) {
	got, err := json.Marshal(ipc.CreatePanePayload{TabID: "t1", Type: "terminal", CWD: "/x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(got, []byte("worktree")) {
		t.Errorf("payload carries a worktree key with no spec set: %s", got)
	}
}

// The response echoes the spec VERBATIM on the ERROR path too: it is the
// client's staleness key, and the client holds a layout placeholder armed
// against it that nothing else will unwind.
func TestCreatePaneResp_EchoesTheSpecOnFailure(t *testing.T) {
	spec := &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"}
	raw, err := json.Marshal(ipc.CreatePaneRespPayload{Error: "already exists", Worktree: spec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ipc.CreatePaneRespPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Worktree == nil || back.Worktree.Branch != "feat/x" || back.Worktree.RepoRoot != "/repo" {
		t.Errorf("spec not echoed verbatim: %+v", back.Worktree)
	}
	if back.PaneID != "" {
		t.Error("a failed create must carry no pane id")
	}
}

// A create WITH a spec round-trips it unchanged — the daemon derives the
// worktree path from these two fields, so a dropped one lands a pane
// somewhere else entirely.
func TestCreatePanePayload_WorktreeRoundTrips(t *testing.T) {
	raw, err := json.Marshal(ipc.CreatePanePayload{
		TabID:    "t1",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ipc.CreatePanePayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Worktree == nil {
		t.Fatal("worktree spec lost in the round trip")
	}
	if back.Worktree.RepoRoot != "/repo" || back.Worktree.Branch != "feat/x" {
		t.Errorf("spec = %+v, want {/repo feat/x}", back.Worktree)
	}
}

// The tri-state matters for the same reason it does for Muted: handleUpdatePane
// is a PARTIAL update handler, so a plain bool would mark an overlay hidden on
// every rename and every OSC 7 CWD change.
func TestUpdatePanePayload_OverlayVisibleOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(ipc.UpdatePanePayload{PaneID: "pane-1", Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("overlay_visible")) {
		t.Errorf("nil OverlayVisible must not appear on the wire: %s", b)
	}
}

func TestUpdatePanePayload_OverlayVisibleRoundTripsFalse(t *testing.T) {
	no := false
	b, err := json.Marshal(ipc.UpdatePanePayload{PaneID: "pane-1", OverlayVisible: &no})
	if err != nil {
		t.Fatal(err)
	}
	var out ipc.UpdatePanePayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.OverlayVisible == nil || *out.OverlayVisible {
		t.Errorf("OverlayVisible = %v, want an explicit false", out.OverlayVisible)
	}
}

func TestOverlayPolicyPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgOverlayPolicy, ipc.OverlayPolicyPayload{IdleTimeoutMinutes: 7, MaxLive: 3})
	if err != nil {
		t.Fatal(err)
	}
	var out ipc.OverlayPolicyPayload
	if err := msg.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if out.IdleTimeoutMinutes != 7 || out.MaxLive != 3 {
		t.Errorf("payload = %+v, want {7 3}", out)
	}
}

// Multi-client sync payload round trips.

// TestAttachPayload_ClientIDRoundTrip locks in the wire-format contract for
// the client id multi-client sync needs for master election and per-client
// state. An older client that never sets it must decode to an empty string,
// not an error.
func TestAttachPayload_ClientIDRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   ipc.AttachPayload
		want string
	}{
		{"new client with id", ipc.AttachPayload{Cols: 80, Rows: 24, ClientID: "client-abc"}, "client-abc"},
		{"old client omits id", ipc.AttachPayload{Cols: 80, Rows: 24}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgAttach, tc.in)
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			var got ipc.AttachPayload
			if err := msg.DecodePayload(&got); err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			if got.ClientID != tc.want {
				t.Errorf("ClientID: got %q, want %q", got.ClientID, tc.want)
			}
		})
	}
}

func TestResizePanesPayload_RoundTrip(t *testing.T) {
	req := ipc.ResizePanesPayload{Panes: []ipc.ResizePanePayload{
		{PaneID: "p1", Cols: 80, Rows: 24},
		{PaneID: "p2", Cols: 40, Rows: 12},
	}}
	msg, err := ipc.NewMessage(ipc.MsgResizePanes, req)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var got ipc.ResizePanesPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(got.Panes) != 2 || got.Panes[0].PaneID != "p1" || got.Panes[1].Cols != 40 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestPaneSizesPayload_RoundTrip(t *testing.T) {
	resp := ipc.PaneSizesPayload{Panes: []ipc.ResizePanePayload{{PaneID: "p1", Cols: 100, Rows: 30}}}
	msg, err := ipc.NewMessage(ipc.MsgPaneSizes, resp)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	var got ipc.PaneSizesPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if len(got.Panes) != 1 || got.Panes[0].PaneID != "p1" || got.Panes[0].Rows != 30 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestClientGeometryPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgClientGeometry, ipc.ClientGeometryPayload{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.ClientGeometryPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cols != 120 || got.Rows != 40 {
		t.Errorf("payload = %+v, want {120 40}", got)
	}
}

// TestUpdateLayoutPayload_BaseRevNilVsZero is load-bearing for Task 5: an
// absent base must stay nil (no base known) and an explicit revision 0 must
// round-trip as a non-nil zero (based on the daemon's very first revision).
// Collapsing the two would make the daemon unable to tell them apart.
func TestUpdateLayoutPayload_BaseRevNilVsZero(t *testing.T) {
	t.Run("absent stays nil", func(t *testing.T) {
		b, err := json.Marshal(ipc.UpdateLayoutPayload{TabID: "t1", Layout: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("base_rev")) {
			t.Errorf("nil BaseRev must not appear on the wire: %s", b)
		}
		var got ipc.UpdateLayoutPayload
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.BaseRev != nil {
			t.Errorf("BaseRev = %v, want nil", got.BaseRev)
		}
	})
	t.Run("explicit zero round-trips non-nil", func(t *testing.T) {
		var zero uint64
		b, err := json.Marshal(ipc.UpdateLayoutPayload{TabID: "t1", Layout: json.RawMessage(`{}`), BaseRev: &zero})
		if err != nil {
			t.Fatal(err)
		}
		var got ipc.UpdateLayoutPayload
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.BaseRev == nil {
			t.Fatal("BaseRev = nil, want a non-nil zero")
		}
		if *got.BaseRev != 0 {
			t.Errorf("BaseRev = %d, want 0", *got.BaseRev)
		}
	})
}

func TestSetActivePanePayload_ClientRoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgSetActivePane, ipc.SetActivePanePayload{PaneID: "p1", Client: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.SetActivePanePayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "p1" || got.Client != "client-a" {
		t.Errorf("payload = %+v, want {p1 client-a}", got)
	}
}

func TestSetActivePanePayload_ClientOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(ipc.SetActivePanePayload{PaneID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("client")) {
		t.Errorf("empty Client must not appear on the wire: %s", b)
	}
}

func TestCloseTUIPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgCloseTUI, ipc.CloseTUIPayload{Client: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.CloseTUIPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if got.Client != "client-a" {
		t.Errorf("Client = %q, want %q", got.Client, "client-a")
	}
}

func TestListClientsRespPayload_RoundTrip(t *testing.T) {
	resp := ipc.ListClientsRespPayload{Clients: []ipc.ClientInfo{
		{Client: "c1", AttachedAt: "2026-09-25T00:00:00Z", Cols: 80, Rows: 24, Master: true, Role: "tui", PID: 123, Exe: "quil"},
	}}
	msg, err := ipc.NewMessage(ipc.MsgListClientsResp, resp)
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.ListClientsRespPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 || !got.Clients[0].Master || got.Clients[0].Client != "c1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestEventDismissedPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgEventDismissed, ipc.EventDismissedPayload{EventID: "evt-1"})
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.EventDismissedPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != "evt-1" {
		t.Errorf("EventID = %q, want %q", got.EventID, "evt-1")
	}
}

func TestPaneSeenPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgPaneSeen, ipc.PaneSeenPayload{PaneID: "pane-1"})
	if err != nil {
		t.Fatal(err)
	}
	var got ipc.PaneSeenPayload
	if err := msg.DecodePayload(&got); err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "pane-1" {
		t.Errorf("PaneID = %q, want %q", got.PaneID, "pane-1")
	}
}
