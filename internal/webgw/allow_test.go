package webgw

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func msg(t *testing.T, typ, id string, payload any) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

func helloFor(t *testing.T, id, kind string) *ipc.Message {
	return msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{Kind: kind, Proto: ipc.ProtocolVersion, ClientID: id})
}

func TestCheckForward_FirstMustBeWebHelloWithLeasedID(t *testing.T) {
	for name, m := range map[string]*ipc.Message{
		"not hello":   msg(t, ipc.MsgAttach, "a", ipc.AttachPayload{ClientID: "web-p-1"}),
		"kind tui":    helloFor(t, "web-p-1", "tui"),
		"other id":    helloFor(t, "web-p-2", "web"),
		"hello no id": msg(t, ipc.MsgHello, "", ipc.HelloPayload{Kind: "web", ClientID: "web-p-1"}),
	} {
		g := &forwardGate{leasedID: "web-p-1"}
		if _, _, fatal := g.check(m); fatal == nil {
			t.Errorf("%s: accepted as the first message", name)
		}
	}
	g := &forwardGate{leasedID: "web-p-1"}
	fwd, refuse, fatal := g.check(helloFor(t, "web-p-1", "web"))
	if fatal != nil || refuse != nil || fwd == nil {
		t.Fatalf("valid hello: %v %v %v", fwd, refuse, fatal)
	}
}

func TestCheckForward_RefusesUnlistedTypes(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for _, typ := range []string{"token_create_req", ipc.MsgShutdown, "create_pane", "destroy_pane", "create_pane_req", "create_tab", "reload_plugins", "kill_process_req", "subscribe", "overlay_policy", MsgWebWelcome, "invented_type"} {
		fwd, refuse, fatal := g.check(msg(t, typ, "r1", struct{}{}))
		if fwd != nil || fatal != nil || refuse == nil {
			t.Fatalf("%s: fwd=%v refuse=%v fatal=%v", typ, fwd, refuse, fatal)
		}
		var p ipc.ErrorPayload
		if err := json.Unmarshal(refuse.Payload, &p); err != nil || p.Code != ipc.ErrCodeRefused || refuse.ID != "r1" {
			t.Fatalf("%s: refusal %+v id %q", typ, p, refuse.ID)
		}
	}
}

func TestCheckForward_5cPaletteSearch(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	fwd, refuse, fatal := g.check(msg(t, ipc.MsgPaneSearchReq, "s1", ipc.PaneSearchReqPayload{Query: "x"}))
	if fwd == nil || refuse != nil || fatal != nil {
		t.Fatalf("pane_search_req: fwd=%v refuse=%v fatal=%v", fwd, refuse, fatal)
	}
	if fwd.ID != "s1" {
		t.Fatalf("pane_search_req lost its id: %q", fwd.ID)
	}
}

func TestCheckForward_5cNotes(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	if fwd, refuse, _ := g.check(msg(t, ipc.MsgNoteGet, "n1", ipc.NoteGetPayload{PaneID: "p"})); fwd == nil || refuse != nil {
		t.Fatalf("note_get refused: %v", refuse)
	}
	in := msg(t, ipc.MsgNoteSet, "n2", map[string]any{"pane_id": "p", "text": "x\n", "base_rev": 3, "extra": true})
	fwd, refuse, _ := g.check(in)
	if fwd == nil || refuse != nil {
		t.Fatalf("note_set refused: %v", refuse)
	}
	var out map[string]any
	if err := json.Unmarshal(fwd.Payload, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["extra"]; ok || out["text"] != "x\n" || out["base_rev"] != float64(3) || out["pane_id"] != "p" || fwd.ID != "n2" {
		t.Fatalf("note_set re-encode: %v id %q", out, fwd.ID)
	}
	// A negative base cannot decode into the daemon's uint64: refused here.
	bad := msg(t, ipc.MsgNoteSet, "n3", map[string]any{"pane_id": "p", "text": "x", "base_rev": -1})
	if fwd, refuse, _ := g.check(bad); fwd != nil || refuse == nil {
		t.Fatalf("negative base_rev forwarded: %v", fwd)
	}
}

func TestCheckForward_5cHistoryAndSession(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for _, m := range []*ipc.Message{
		msg(t, ipc.MsgPaneHistoryReq, "h1", ipc.PaneHistoryReqPayload{PaneID: "p"}),
		msg(t, ipc.MsgPaneHistoryEntryReq, "h2", ipc.PaneHistoryEntryReqPayload{PaneID: "p", TsMs: 1}),
		msg(t, ipc.MsgClaudeSessionDetailReq, "h3", ipc.ClaudeSessionDetailReqPayload{CWD: "/", SessionID: "s"}),
	} {
		if fwd, refuse, _ := g.check(m); fwd == nil || refuse != nil {
			t.Fatalf("%s refused: %v", m.Type, refuse)
		}
	}
}

func TestCheckForward_AttachMustCarryTheLeasedID(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	if _, refuse, _ := g.check(msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{ClientID: "web-p-9"})); refuse == nil {
		t.Fatal("attach with another tab's id was forwarded")
	}
	if fwd, _, _ := g.check(msg(t, ipc.MsgAttach, "a2", ipc.AttachPayload{ClientID: "web-p-1"})); fwd == nil {
		t.Fatal("attach with the leased id refused")
	}
}

// Size messages, and keystrokes, go id-less: an id-bearing pane_input is
// answered on the daemon's 64-slot must-deliver queue. A paste chunk keeps
// its id (spec 5b §4.3) so the page gets delivered:true/false.
func TestCheckForward_StripsIDsFromSizeKeepsPasteIDs(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for _, typ := range []string{ipc.MsgResizePanes, ipc.MsgClientGeometry} {
		fwd, _, _ := g.check(msg(t, typ, "x", struct{}{}))
		if fwd == nil || fwd.ID != "" {
			t.Fatalf("%s forwarded as %+v", typ, fwd)
		}
	}
	fwd, _, _ := g.check(msg(t, ipc.MsgPaneInput, "", ipc.PaneInputPayload{PaneID: "p", Data: []byte("a")}))
	if fwd == nil || fwd.ID != "" {
		t.Fatal("an id-less keystroke changed")
	}
	fwd, _, _ = g.check(msg(t, ipc.MsgPaneInput, "paste-1", ipc.PaneInputPayload{PaneID: "p", Data: []byte("a")}))
	if fwd == nil || fwd.ID != "paste-1" {
		t.Fatalf("a paste chunk lost its id: %+v", fwd)
	}
}

// At most two unanswered paste chunks per socket; the third is answered
// busy with its own id, and an answer frees a place.
func TestCheckForward_PasteCap(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	chunk := func(id string) (*ipc.Message, *ipc.Message) {
		fwd, refuse, _ := g.check(msg(t, ipc.MsgPaneInput, id, ipc.PaneInputPayload{PaneID: "p", Data: []byte("x")}))
		return fwd, refuse
	}
	for _, id := range []string{"c1", "c2"} {
		if fwd, refuse := chunk(id); fwd == nil || refuse != nil {
			t.Fatalf("%s refused", id)
		}
	}
	fwd, refuse := chunk("c3")
	if fwd != nil || refuse == nil || refuse.ID != "c3" {
		t.Fatalf("third chunk: fwd=%v refuse=%v", fwd, refuse)
	}
	var p ipc.ErrorPayload
	if err := json.Unmarshal(refuse.Payload, &p); err != nil || p.Code != ErrCodeBusy {
		t.Fatalf("refusal %+v", p)
	}
	ans, _ := ipc.NewMessage(ipc.MsgPaneInputResp, ipc.PaneInputRespPayload{PaneID: "p", Delivered: true})
	ans.ID = "c1"
	g.answered(ans)
	if fwd, refuse := chunk("c4"); fwd == nil || refuse != nil {
		t.Fatal("an answered chunk did not free a place")
	}
	errAns, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Type: ipc.MsgPaneInput})
	errAns.ID = "c2"
	g.answered(errAns)
	if fwd, refuse := chunk("c5"); fwd == nil || refuse != nil {
		t.Fatal("an error answer did not free a place")
	}
}

// A pending id cannot be sent again to take a second place, and only an
// answer about a pane_input frees a place: an error answering some other
// request that reused the id does not.
func TestCheckForward_PasteCapCannotBeBypassed(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	chunk := func(id string) (*ipc.Message, *ipc.Message) {
		fwd, refuse, _ := g.check(msg(t, ipc.MsgPaneInput, id, ipc.PaneInputPayload{PaneID: "p", Data: []byte("x")}))
		return fwd, refuse
	}
	if fwd, refuse := chunk("c1"); fwd == nil || refuse != nil {
		t.Fatal("c1 refused")
	}
	fwd, refuse := chunk("c1")
	if fwd != nil || refuse == nil || refuse.ID != "c1" {
		t.Fatalf("a repeated pending id: fwd=%v refuse=%v", fwd, refuse)
	}
	var p ipc.ErrorPayload
	if err := json.Unmarshal(refuse.Payload, &p); err != nil || p.Code != ErrCodeBusy {
		t.Fatalf("refusal %+v", p)
	}
	if fwd, refuse := chunk("c2"); fwd == nil || refuse != nil {
		t.Fatal("c2 refused")
	}

	other, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: "stale", Type: ipc.MsgUpdateLayout})
	other.ID = "c1"
	g.answered(other)
	if fwd, _ := chunk("c3"); fwd != nil {
		t.Fatal("an update_layout error with a paste's id freed its place")
	}
	untyped, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused})
	untyped.ID = "c1"
	g.answered(untyped)
	if fwd, _ := chunk("c3"); fwd != nil {
		t.Fatal("an error naming no type freed a paste place")
	}

	mine, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Type: ipc.MsgPaneInput})
	mine.ID = "c1"
	g.answered(mine)
	if fwd, refuse := chunk("c3"); fwd == nil || refuse != nil {
		t.Fatal("a pane_input error did not free its place")
	}
}

// One test per 5b allow-list row (spec §4.1).
func TestCheckForward_5bRows(t *testing.T) {
	base := uint64(4)
	pass := map[string]*ipc.Message{
		"destroy_tab":           msg(t, ipc.MsgDestroyTab, "d1", ipc.DestroyTabPayload{TabID: "t"}),
		"update_tab":            msg(t, ipc.MsgUpdateTab, "u1", ipc.UpdateTabPayload{TabID: "t", Name: "n"}),
		"destroy_pane_req":      msg(t, ipc.MsgDestroyPaneReq, "d2", ipc.DestroyPaneReqPayload{PaneID: "p", RemoveWorktree: true}),
		"update_pane":           msg(t, ipc.MsgUpdatePane, "u2", map[string]any{"pane_id": "p", "name": "n", "muted": true, "overlay_visible": false, "unseen": false}),
		"update_layout":         msg(t, ipc.MsgUpdateLayout, "l1", ipc.UpdateLayoutPayload{TabID: "t", Layout: json.RawMessage(`{"pane_id":"p"}`), BaseRev: &base}),
		"move_pane":             msg(t, ipc.MsgMovePane, "m1", ipc.MovePanePayload{PaneID: "p", TabID: "t"}),
		"restart_pane_req":      msg(t, ipc.MsgRestartPaneReq, "r1", ipc.RestartPaneReqPayload{PaneID: "p"}),
		"dismiss_event":         msg(t, ipc.MsgDismissEvent, "e1", ipc.DismissEventPayload{}),
		"get_notifications_req": msg(t, ipc.MsgGetNotificationsReq, "n1", struct{}{}),
		"plugin_list_req":       msg(t, ipc.MsgPluginListReq, "q1", struct{}{}),
		"browse_dir_req":        msg(t, ipc.MsgBrowseDirReq, "q2", struct{}{}),
		"git_repos_req":         msg(t, ipc.MsgGitReposReq, "q3", struct{}{}),
		"kube_ctx_req":          msg(t, ipc.MsgKubeCtxReq, "q4", struct{}{}),
		"claude_sessions_req":   msg(t, ipc.MsgClaudeSessionsReq, "q5", struct{}{}),
		"worktree_list_req":     msg(t, ipc.MsgWorktreeListReq, "q6", struct{}{}),
		"sandbox_cap_req":       msg(t, ipc.MsgSandboxCapReq, "q7", struct{}{}),
		"dirs_exist_req":        msg(t, ipc.MsgDirsExistReq, "q8", struct{}{}),
		"split_pane_req":        msg(t, ipc.MsgSplitPaneReq, "s1", ipc.SplitPaneReqPayload{Placement: ipc.PlacementRight}),
	}
	for name, m := range pass {
		g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
		if fwd, refuse, fatal := g.check(m); fwd == nil || refuse != nil || fatal != nil {
			t.Errorf("%s: fwd=%v refuse=%v fatal=%v", name, fwd, refuse, fatal)
		} else if fwd.ID != m.ID {
			t.Errorf("%s: id %q became %q", name, m.ID, fwd.ID)
		}
	}
	refuse := map[string]*ipc.Message{
		"destroy_tab without id":     msg(t, ipc.MsgDestroyTab, "", ipc.DestroyTabPayload{TabID: "t"}),
		"update_tab without id":      msg(t, ipc.MsgUpdateTab, "", ipc.UpdateTabPayload{TabID: "t"}),
		"update_pane cwd":            msg(t, ipc.MsgUpdatePane, "u3", map[string]any{"pane_id": "p", "cwd": "/etc"}),
		"update_pane eager":          msg(t, ipc.MsgUpdatePane, "u4", map[string]any{"pane_id": "p", "eager": true}),
		"update_pane pin":            msg(t, ipc.MsgUpdatePane, "u5", map[string]any{"pane_id": "p", "pinned_attention": true}),
		"update_pane delete mark":    msg(t, ipc.MsgUpdatePane, "u6", map[string]any{"pane_id": "p", "marked_for_deletion": true}),
		"update_pane not an object":  &ipc.Message{Type: ipc.MsgUpdatePane, ID: "u7", Payload: json.RawMessage(`"x"`)},
		"update_layout without base": msg(t, ipc.MsgUpdateLayout, "l2", ipc.UpdateLayoutPayload{TabID: "t", Layout: json.RawMessage(`{}`)}),
	}
	for name, m := range refuse {
		g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
		if fwd, r, fatal := g.check(m); fwd != nil || r == nil || fatal != nil {
			t.Errorf("%s: fwd=%v refuse=%v fatal=%v", name, fwd, r, fatal)
		}
	}
}

// The page never supplies instance_args: the gateway drops them and fills
// them from the saved instance named by the gateway-only instance_id, on its
// own disk (spec 5b E7, §3.3, Ruling R-B). instance_id never reaches the daemon.
func TestCheckForward_SplitPaneReqArgsComeFromTheGateway(t *testing.T) {
	var asked [2]string
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true, expand: func(p, id string) (string, []string, error) {
		asked = [2]string{p, id}
		return "prod", []string{"u@h"}, nil
	}}
	paneOf := func(fwd *ipc.Message) map[string]any {
		var top map[string]any
		if err := json.Unmarshal(fwd.Payload, &top); err != nil {
			t.Fatal(err)
		}
		p, _ := top["pane"].(map[string]any)
		return p
	}
	in := msg(t, ipc.MsgSplitPaneReq, "s1", map[string]any{"placement": "right",
		"pane": map[string]any{"type": "ssh", "instance_id": "i1", "instance_args": []string{"-oProxyCommand=evil"}}})
	fwd, refuse, _ := g.check(in)
	if fwd == nil || refuse != nil {
		t.Fatalf("refused: %v", refuse)
	}
	p := paneOf(fwd)
	if got, _ := json.Marshal(p["instance_args"]); string(got) != `["u@h"]` || p["instance_name"] != "prod" || asked != [2]string{"ssh", "i1"} {
		t.Fatalf("forwarded pane %v (asked %v)", p, asked)
	}
	if _, has := p["instance_id"]; has {
		t.Fatal("instance_id reached the daemon")
	}

	noID := msg(t, ipc.MsgSplitPaneReq, "s2", map[string]any{"placement": "right",
		"pane": map[string]any{"type": "terminal", "instance_args": []string{"-c", "id"}}})
	fwd, _, _ = g.check(noID)
	if p := paneOf(fwd); p["instance_args"] != nil {
		t.Fatalf("page args survived without an instance id: %v", p["instance_args"])
	}

	g.expand = func(string, string) (string, []string, error) {
		return "", nil, errors.New("no saved instance i1 for ssh")
	}
	if fwd, refuse, _ := g.check(in); fwd != nil || refuse == nil {
		t.Fatal("an unknown saved instance was forwarded")
	}
	g.expand = nil
	if fwd, refuse, _ := g.check(in); fwd != nil || refuse == nil {
		t.Fatal("an instance id was forwarded with no expander")
	}
}

func TestCheckForward_LargePasteFitsTheLimit(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	big := strings.Repeat("x", 200<<10)
	fwd, refuse, fatal := g.check(msg(t, ipc.MsgPaneInput, "", ipc.PaneInputPayload{PaneID: "p", Data: []byte(big)}))
	if fwd == nil || refuse != nil || fatal != nil {
		t.Fatal("a 200 KB paste was not forwarded")
	}
}

func TestWebOpenPayload_DecodesHintAndKey(t *testing.T) {
	var p WebOpenPayload
	if err := json.Unmarshal([]byte(`{"client_id_hint":"web-ab-1","key":"k-1"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ClientIDHint != "web-ab-1" || p.Key != "k-1" {
		t.Fatalf("decoded %+v", p)
	}
}

func TestCheckForward_HelloCarriesTheGatewaysOwnProcessFields(t *testing.T) {
	g := newForwardGate("web-p-1", "9.9.9", nil)
	forged := msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{
		Kind: "web", Proto: ipc.ProtocolVersion, ClientID: "web-p-1",
		PID: 1, ExeName: "quil.exe", Version: "0.0.1",
	})
	for i, m := range []*ipc.Message{forged, forged} { // first hello, then a repeat
		fwd, refuse, fatal := g.check(m)
		if fwd == nil || refuse != nil || fatal != nil {
			t.Fatalf("hello %d: %v %v %v", i, fwd, refuse, fatal)
		}
		var h ipc.HelloPayload
		if err := json.Unmarshal(fwd.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if h.PID != os.Getpid() || h.ExeName != g.exeName || h.ExeName == "quil.exe" || h.Version != "9.9.9" {
			t.Fatalf("hello %d forwarded with page-supplied fields: %+v", i, h)
		}
		if h.Kind != "web" || h.ClientID != "web-p-1" || fwd.ID != "h1" {
			t.Fatalf("hello %d lost its pinned fields: %+v id %q", i, h, fwd.ID)
		}
	}
}

func TestCheckForward_SecondHelloIsPinnedToo(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	for name, m := range map[string]*ipc.Message{
		"other id":   helloFor(t, "web-p-2", "web"),
		"other kind": helloFor(t, "web-p-1", "tui"),
		"no id":      msg(t, ipc.MsgHello, "", ipc.HelloPayload{Kind: "web", ClientID: "web-p-1"}),
	} {
		fwd, refuse, fatal := g.check(m)
		if fwd != nil || refuse == nil || fatal != nil {
			t.Errorf("%s: fwd=%v refuse=%v fatal=%v", name, fwd, refuse, fatal)
		}
	}
}

func TestCheckForward_AttachWithoutClientIDIsRefused(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	if fwd, refuse, _ := g.check(msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{})); fwd != nil || refuse == nil {
		t.Fatal("attach without a client id was forwarded")
	}
}

// A login's token id and nonce, and a working directory for new panes, are
// never the page's to send: the forwarded hello and attach drop them and keep
// everything else.
func TestCheckForward_DropsLoginFieldsAndAttachCWD(t *testing.T) {
	g := newForwardGate("web-p-1", "9.9.9", nil)
	hello := msg(t, ipc.MsgHello, "h1", ipc.HelloPayload{
		Kind: "web", Proto: ipc.ProtocolVersion, ClientID: "web-p-1", TokenID: "tok-1", Nonce: "n-1",
	})
	fwd, _, fatal := g.check(hello)
	if fwd == nil || fatal != nil {
		t.Fatalf("hello: %v %v", fwd, fatal)
	}
	var h ipc.HelloPayload
	if err := json.Unmarshal(fwd.Payload, &h); err != nil || h.TokenID != "" || h.Nonce != "" || h.ClientID != "web-p-1" {
		t.Fatalf("forwarded hello %+v (%v)", h, err)
	}

	attach := msg(t, ipc.MsgAttach, "a1", ipc.AttachPayload{
		ClientID: "web-p-1", Cols: 80, Rows: 24, WinCols: 100, WinRows: 30, Reattach: true, CWD: "/home/someone",
	})
	fwd, refuse, fatal := g.check(attach)
	if fwd == nil || refuse != nil || fatal != nil {
		t.Fatalf("attach: %v %v %v", fwd, refuse, fatal)
	}
	var a ipc.AttachPayload
	if err := json.Unmarshal(fwd.Payload, &a); err != nil {
		t.Fatal(err)
	}
	want := ipc.AttachPayload{ClientID: "web-p-1", Cols: 80, Rows: 24, WinCols: 100, WinRows: 30, Reattach: true}
	if a != want || fwd.ID != "a1" {
		t.Fatalf("forwarded attach %+v id %q, want %+v", a, fwd.ID, want)
	}
}

// A malformed paste chunk is refused before it holds a place: an older daemon
// drops it unanswered, and the place would be held until resync.
func TestCheckForward_MalformedPasteChunkHoldsNoPlace(t *testing.T) {
	g := &forwardGate{leasedID: "web-p-1", helloSeen: true}
	fwd, refuse, fatal := g.check(msg(t, ipc.MsgPaneInput, "bad-1", map[string]any{"pane_id": 5}))
	if fwd != nil || refuse == nil || refuse.ID != "bad-1" || fatal != nil {
		t.Fatalf("malformed chunk: fwd=%v refuse=%v fatal=%v", fwd, refuse, fatal)
	}
	if len(g.pastes) != 0 {
		t.Fatalf("a refused chunk holds a place: %v", g.pastes)
	}
}
