package daemon

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
)

// sendAndProbe sends msg and then a version_req sentinel on the same conn.
// Frames on one conn dispatch in order and a refusal is sent synchronously
// before any handler runs, so a refusal for msg always precedes the
// sentinel's answer. Returns whether msg was refused.
func sendAndProbe(t *testing.T, c *ipc.Client, msg *ipc.Message) bool {
	t.Helper()
	if err := c.Send(msg); err != nil {
		t.Fatal(err)
	}
	sentinel := &ipc.Message{Type: ipc.MsgVersionReq, ID: "sentinel-" + msg.ID}
	if err := c.Send(sentinel); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	refused := false
	for {
		f, err := c.Receive()
		if err != nil {
			t.Fatalf("%s: no sentinel answer: %v", msg.Type, err)
		}
		if f.ID == msg.ID && f.Type == ipc.MsgError {
			var p ipc.ErrorPayload
			_ = f.DecodePayload(&p)
			refused = refused || p.Code == ipc.ErrCodeRefused
		}
		if f.ID == sentinel.ID {
			return refused
		}
	}
}

// Allowed calls with real side effects this suite must not trigger; their
// ALLOWED half is covered by clientauth's table tests.
var skipAllowed = map[string]bool{
	ipc.MsgShutdown: true, ipc.MsgStageUpdateReq: true, ipc.MsgUpdateCheckReq: true,
	ipc.MsgKillProcessReq: true, ipc.MsgReloadPlugins: true,
}

func expectAllowed(class clientauth.Class, level clientauth.Level) bool {
	switch class {
	case clientauth.ClassView:
		return true
	case clientauth.ClassAct:
		return level != clientauth.LevelReadOnly
	case clientauth.ClassAdmin:
		return level == clientauth.LevelFull
	}
	return false // local and never: refused on TCP whatever the level
}

// Every level x every classified client→daemon type, through handleMessage
// on real TCP conns.
func TestRights_EveryLevelEveryType(t *testing.T) {
	h := newAuthHarness(t)
	conns := map[clientauth.Level]*ipc.Client{}
	for _, lvl := range []clientauth.Level{clientauth.LevelReadOnly, clientauth.LevelStandard, clientauth.LevelFull} {
		c, _ := h.login(t, h.mint(t, "t-"+string(lvl), lvl, nil))
		conns[lvl] = c
	}
	table := clientauth.ClassTable()
	types := make([]string, 0, len(table))
	for typ, class := range table {
		if class != clientauth.ClassNever {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	for _, typ := range types {
		for lvl, c := range conns {
			want := expectAllowed(table[typ], lvl)
			if want && skipAllowed[typ] {
				continue
			}
			msg := &ipc.Message{Type: typ, ID: "tc3-" + string(lvl) + "-" + typ, Payload: json.RawMessage(`{}`)}
			if refused := sendAndProbe(t, c, msg); refused == want {
				t.Errorf("%s from %s: refused=%v, want allowed=%v", typ, lvl, refused, want)
			}
		}
	}
}

func TestRights_StandardPayloadCarriers(t *testing.T) {
	h := newAuthHarness(t)
	c, _ := h.login(t, h.mint(t, "std", clientauth.LevelStandard, nil))
	tab := h.d.session.CreateTab("t")
	mk := func(typ string, p any) *ipc.Message {
		m, err := ipc.NewMessage(typ, p)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = "pc-" + typ + time.Now().Format("150405.000000000")
		return m
	}
	for name, msg := range map[string]*ipc.Message{
		"create_pane args":     mk(ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, InstanceArgs: []string{"-c", "id"}}),
		"create_pane replace":  mk(ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, ReplacePaneID: "x", InstanceArgs: []string{"y"}}),
		"create_pane overlay":  mk(ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, Overlay: true}),
		"create_pane_req term": mk(ipc.MsgCreatePaneReq, ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "terminal", InstanceArgs: []string{"-c", "id"}}),
		"create_tab first":     mk(ipc.MsgCreateTab, ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{InstanceArgs: []string{"x"}}}),
		"create_tab_req first": mk(ipc.MsgCreateTabReq, ipc.CreateTabReqPayload{FirstPane: &ipc.CreatePaneReqPayload{InstanceArgs: []string{"x"}}}),
	} {
		before := len(h.d.session.Tabs())
		if !sendAndProbe(t, c, msg) {
			t.Errorf("%s: not refused for standard", name)
		}
		if len(h.d.session.Tabs()) != before {
			t.Errorf("%s: a refused request created a tab", name)
		}
	}
	// Positive control: the same create without args is allowed.
	if sendAndProbe(t, c, mk(ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID})) {
		t.Fatal("a plain create_pane was refused for standard")
	}
}

func TestRights_TokenRequestsLocalOnly(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	msg := &ipc.Message{Type: ipc.MsgTokenListReq, ID: "tl-tcp", Payload: json.RawMessage(`{}`)}
	if !sendAndProbe(t, full, msg) {
		t.Fatal("token_list_req from a FULL tcp conn was not refused")
	}
	local := h.local(t)
	roundTrip(t, local, ipc.MsgHello, ipc.MsgHelloResp, testLoginHello())
	msg = &ipc.Message{Type: ipc.MsgTokenListReq, ID: "tl-local", Payload: json.RawMessage(`{}`)}
	if sendAndProbe(t, local, msg) {
		t.Fatal("token_list_req from the local socket was refused")
	}
}

// authRecordingSession counts PTY writes AND resizes. Not the package's
// existing recordingSession (redraw_kick_test.go): that one records only the
// bytes written and has no resize count, which the read-only tests need — so
// this is a separate fake under its own name.
type authRecordingSession struct {
	fakeSession
	mu      sync.Mutex
	writes  int
	resizes int
}

func (r *authRecordingSession) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes++
	r.mu.Unlock()
	return len(p), nil
}

func (r *authRecordingSession) Resize(rows, cols uint16) error {
	r.mu.Lock()
	r.resizes++
	r.mu.Unlock()
	return nil
}

func (r *authRecordingSession) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes, r.resizes
}

func livePane(t *testing.T, h *authHarness) (*Pane, *authRecordingSession) {
	t.Helper()
	tab := h.d.session.CreateTab("live")
	pane, err := h.d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := &authRecordingSession{}
	pane.PluginMu.Lock()
	pane.Type, pane.PTY, pane.Cols, pane.Rows = "terminal", sess, 80, 24
	pane.PluginMu.Unlock()
	t.Cleanup(pane.StopInput)
	return pane, sess
}

// Read-only pane_input: id-less dropped with no PTY write and one audit line
// per minute; id-bearing refused with no pane_input_resp.
func TestRights_ReadOnlyPaneInput(t *testing.T) {
	h := newAuthHarness(t)
	ro, _ := h.login(t, h.mint(t, "viewer", clientauth.LevelReadOnly, nil))
	pane, sess := livePane(t, h)
	for i := 0; i < 3; i++ {
		sendNoID(t, ro, ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("x")})
	}
	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("y")})
	msg.ID = "ro-input"
	if err := ro.Send(msg); err != nil {
		t.Fatal(err)
	}
	f := waitFrameWithID(t, ro, "ro-input", 3*time.Second)
	if f.Type != ipc.MsgError {
		t.Fatalf("got %s, want error refused instead of pane_input_resp", f.Type)
	}
	if w, _ := sess.counts(); w != 0 {
		t.Fatalf("%d PTY writes from a read-only conn", w)
	}
	n := 0
	for _, e := range h.auditEntries(t) {
		if e.Event == "refused" && e.Type == ipc.MsgPaneInput {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d refused audit lines for pane_input, want 1 per minute", n)
	}
}

// A wait_task on a live task with a long timeout really parks a goroutine,
// and so does ONE watch_notifications — but only one: a second watch on the
// same conn evicts the first, whose goroutine returns at once. So the cap is
// filled with one watch plus waits. An unknown wait_task answers at once and
// must hand its slot straight back.
func TestRights_ParkedRequestsCapped(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	h.d.tasksRegistry().addLive(&task{id: "task-park", to: "pane-park", state: taskSent, done: make(chan struct{})})
	waitReq := func(id, taskID string) *ipc.Message {
		m, err := ipc.NewMessage(ipc.MsgWaitTaskReq, ipc.WaitTaskReqPayload{TaskID: taskID, TimeoutMs: 60000})
		if err != nil {
			t.Fatal(err)
		}
		m.ID = id
		return m
	}
	for i := 0; i < 2*maxParkedPerConn; i++ {
		if sendAndProbe(t, full, waitReq("unknown-"+string(rune('a'+i)), "no-such-task")) {
			t.Fatal("a wait_task that never parks kept its slot")
		}
	}
	refused := 0
	watch, _ := ipc.NewMessage(ipc.MsgWatchNotificationsReq, ipc.WatchNotificationsReqPayload{TimeoutMs: 60000})
	watch.ID = "park-watch"
	if sendAndProbe(t, full, watch) {
		refused++
	}
	for i := 1; i < maxParkedPerConn+1; i++ {
		if sendAndProbe(t, full, waitReq("park-"+string(rune('a'+i)), "task-park")) {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("%d of %d parked requests refused, want exactly the last", refused, maxParkedPerConn+1)
	}
	// The local conn says hello first: one that never did gets silence for a
	// refusal, so the check below could not see a local cap.
	local := h.local(t)
	roundTrip(t, local, ipc.MsgHello, ipc.MsgHelloResp, testLoginHello())
	for i := 0; i < maxParkedPerConn+1; i++ {
		if sendAndProbe(t, local, waitReq("lpark-"+string(rune('a'+i)), "task-park")) {
			t.Fatal("the local socket is capped too")
		}
	}
}

// A revoked conn is refused even for what its level allows, before any
// handler runs: between the silencing and the close, a request must not
// still type into a pane.
func TestRights_RevokedConnRefusedBeforeTheHandler(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "gone", clientauth.LevelFull, nil)
	c, _ := h.login(t, tok)
	pane, sess := livePane(t, h)
	id, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if conns := h.d.auth.markRevoked(id); len(conns) != 1 {
		t.Fatalf("revoked %d conns, want the one logged in", len(conns))
	}
	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("z")})
	msg.ID = "revoked-input"
	if err := c.Send(msg); err != nil {
		t.Fatal(err)
	}
	f := waitFrameWithID(t, c, "revoked-input", 3*time.Second)
	var p ipc.ErrorPayload
	if f.Type != ipc.MsgError || f.DecodePayload(&p) != nil || p.Code != ipc.ErrCodeRefused || p.Message != "token revoked" {
		t.Fatalf("got %s %s, want error refused \"token revoked\"", f.Type, f.Payload)
	}
	if w, _ := sess.counts(); w != 0 {
		t.Fatalf("%d PTY writes from a revoked conn", w)
	}
}

func TestRights_PrivilegedAudited(t *testing.T) {
	h := newAuthHarness(t)
	local := h.local(t)
	sendNoID(t, local, ipc.MsgOverlayPolicy, ipc.OverlayPolicyPayload{IdleTimeoutMinutes: 5, MaxLive: 2})
	tab := h.d.session.CreateTab("t")
	sendNoID(t, local, ipc.MsgCreatePane, ipc.CreatePanePayload{TabID: tab.ID, Type: "ssh", InstanceArgs: []string{"u@h"}})
	h.waitAudit(t, "privileged overlay_policy", func(e auditEntry) bool {
		return e.Event == "privileged" && e.Type == ipc.MsgOverlayPolicy && e.Transport == ipc.TransportLocal
	})
	h.waitAudit(t, "privileged create", func(e auditEntry) bool {
		return e.Event == "privileged" && e.Type == ipc.MsgCreatePane && e.Reason == "raw instance arguments"
	})
}
