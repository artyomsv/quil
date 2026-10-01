package daemon

import (
	"encoding/json"
	"sort"
	"strconv"
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
			if err := f.DecodePayload(&p); err != nil {
				t.Fatalf("%s: undecodable error frame: %v", msg.Type, err)
			}
			refused = refused || p.Code == ipc.ErrCodeRefused
		}
		if f.ID == sentinel.ID {
			return refused
		}
	}
}

// mustMessage builds a request with the given ID.
func mustMessage(t *testing.T, typ, id string, payload any) *ipc.Message {
	t.Helper()
	m, err := ipc.NewMessage(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

// waitReq is a wait_task_req with the given ID.
func waitReq(t *testing.T, id, taskID string, timeoutMs int) *ipc.Message {
	t.Helper()
	return mustMessage(t, ipc.MsgWaitTaskReq, id, ipc.WaitTaskReqPayload{TaskID: taskID, TimeoutMs: timeoutMs})
}

// liveTask registers a task nothing will finish, so a wait on it parks.
func liveTask(h *authHarness, id string) *task {
	tk := &task{id: id, to: "pane-" + id, state: taskSent, done: make(chan struct{})}
	h.d.tasksRegistry().addLive(tk)
	return tk
}

// pollUntil waits up to 3 s for cond.
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
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
		before, beforePanes := len(h.d.session.Tabs()), len(h.d.session.Panes(tab.ID))
		if !sendAndProbe(t, c, msg) {
			t.Errorf("%s: not refused for standard", name)
		}
		if len(h.d.session.Tabs()) != before {
			t.Errorf("%s: a refused request created a tab", name)
		}
		if n := len(h.d.session.Panes(tab.ID)); n != beforePanes {
			t.Errorf("%s: a refused request changed the tab's panes (%d -> %d)", name, beforePanes, n)
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
	tok := h.mint(t, "viewer", clientauth.LevelReadOnly, nil)
	tokenID, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	ro, _ := h.login(t, tok)
	pane, sess := livePane(t, h)
	for i := 0; i < 3; i++ {
		sendNoID(t, ro, ipc.MsgPaneInput, ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("x")})
	}
	msg := mustMessage(t, ipc.MsgPaneInput, "ro-input", ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("y")})
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
			// The line names who was refused, not just what.
			if e.Transport != ipc.TransportTCP || e.TokenID != tokenID || e.TokenName != "viewer" || e.Rights != ipc.RightsReadOnly {
				t.Errorf("refused line lacks the token: %+v", e)
			}
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
	liveTask(h, "task-park")
	for i := 0; i < 2*maxParkedPerConn; i++ {
		if sendAndProbe(t, full, waitReq(t, "unknown-"+string(rune('a'+i)), "no-such-task", 60000)) {
			t.Fatal("a wait_task that never parks kept its slot")
		}
	}
	refused := 0
	watch := mustMessage(t, ipc.MsgWatchNotificationsReq, "park-watch", ipc.WatchNotificationsReqPayload{TimeoutMs: 60000})
	if sendAndProbe(t, full, watch) {
		refused++
	}
	for i := 1; i < maxParkedPerConn+1; i++ {
		if sendAndProbe(t, full, waitReq(t, "park-"+string(rune('a'+i)), "task-park", 60000)) {
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
		if sendAndProbe(t, local, waitReq(t, "lpark-"+string(rune('a'+i)), "task-park", 60000)) {
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
	msg := mustMessage(t, ipc.MsgPaneInput, "revoked-input", ipc.PaneInputPayload{PaneID: pane.ID, Data: []byte("z")})
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
	// The audit line is written BEFORE dispatch, so the create may still be
	// running here — and its spawn reads newSessionFn, which the harness's
	// cleanup restores. One conn's frames dispatch in order, so an answered
	// request sent after it means the create has returned.
	finishDispatch(t, local)
	if n := len(h.d.session.Panes(tab.ID)); n != 1 {
		t.Fatalf("the audited create left %d pane(s) in the tab, want 1", n)
	}
}

// finishDispatch returns once every frame c sent before it has been handled:
// one conn's frames dispatch in order, so the answer to a request sent now
// arrives only after the earlier handlers returned. A test that sent an
// id-less request whose handler spawns calls it before ending, or the
// handler can still be reading the package's spawn seam when the harness's
// cleanup restores it.
func finishDispatch(t *testing.T, c *ipc.Client) {
	t.Helper()
	roundTrip(t, c, ipc.MsgListTabsReq, ipc.MsgListTabsResp, struct{}{})
}

// A full TCP conn's admin action is audited with the token that made it.
func TestRights_PrivilegedAuditedFromTCP(t *testing.T) {
	h := newAuthHarness(t)
	tok := h.mint(t, "admin", clientauth.LevelFull, nil)
	tokenID, err := clientauth.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := h.login(t, tok)
	sendNoID(t, full, ipc.MsgReloadPlugins, nil)
	h.waitAudit(t, "privileged reload_plugins from tcp", func(e auditEntry) bool {
		return e.Event == "privileged" && e.Type == ipc.MsgReloadPlugins && e.Transport == ipc.TransportTCP &&
			e.TokenID == tokenID && e.TokenName == "admin" && e.Rights == ipc.RightsFull
	})
	finishDispatch(t, full) // the reload may still be running past its audit line
}

// A type in no class is admin at runtime, so it is refused below full. Its
// refusals share one audit key: the type is the sender's own string, and
// keying on it would let one conn write a line per invented name.
func TestRights_UnclassifiedTypesRefusedAndAuditedOnce(t *testing.T) {
	h := newAuthHarness(t)
	for _, lvl := range []clientauth.Level{clientauth.LevelReadOnly, clientauth.LevelStandard} {
		c, _ := h.login(t, h.mint(t, "u-"+string(lvl), lvl, nil))
		for i := 0; i < 50; i++ {
			typ := "zz_invented_" + string(lvl) + "_" + strconv.Itoa(i)
			msg := &ipc.Message{Type: typ, ID: "uc-" + typ, Payload: json.RawMessage(`{}`)}
			if !sendAndProbe(t, c, msg) {
				t.Fatalf("unclassified %s from %s was not refused", typ, lvl)
			}
		}
	}
	lines := map[string]int{}
	for _, e := range h.auditEntries(t) {
		if e.Event == "refused" && !clientauth.Classified(e.Type) {
			lines[e.TokenName]++
		}
	}
	for _, name := range []string{"u-" + string(clientauth.LevelReadOnly), "u-" + string(clientauth.LevelStandard)} {
		if lines[name] != 1 {
			t.Errorf("%s: %d refused lines for 50 unclassified types, want 1 per minute", name, lines[name])
		}
	}
}

// A parked wait gives its slot back when it ends — by completion or by
// timeout — and BEFORE its answer is sent, so a client at the cap may send
// its next wait the moment a reply lands. No poll: the slots must already be
// free when the last answer arrives.
func TestRights_ParkedWaitReturnsItsSlotWhenItEnds(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	parkAll := func(prefix, taskID string, timeoutMs int) []string {
		t.Helper()
		var ids []string
		for i := 0; i < maxParkedPerConn; i++ {
			id := prefix + string(rune('a'+i))
			if sendAndProbe(t, full, waitReq(t, id, taskID, timeoutMs)) {
				t.Fatalf("%s refused with %d slots taken before it", id, i)
			}
			ids = append(ids, id)
		}
		return ids
	}
	// The answers come from separate goroutines in any order.
	answered := func(ids []string) {
		t.Helper()
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		if err := full.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		defer full.SetReadDeadline(time.Time{})
		for len(want) > 0 {
			f, err := full.Receive()
			if err != nil {
				t.Fatalf("still waiting for %v: %v", want, err)
			}
			if want[f.ID] {
				if f.Type != ipc.MsgWaitTaskResp {
					t.Fatalf("%s: got %s, want wait_task_resp", f.ID, f.Type)
				}
				delete(want, f.ID)
			}
		}
	}

	done := liveTask(h, "task-done")
	ids := parkAll("done-", "task-done", 60000)
	close(done.done)
	answered(ids)

	// Completion returned all four; the timeout arm must too.
	liveTask(h, "task-slow")
	ids = parkAll("timeout-", "task-slow", 1000)
	answered(ids)

	parkAll("again-", "task-slow", 60000)
}

// A watch that only replaces the conn's own watch adds no goroutine, so it
// is admitted at the cap; the watch slot comes back when the watch answers.
func TestRights_WatchReplacementAdmittedAtTheCap(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	liveTask(h, "task-w")
	for i := 0; i < maxParkedPerConn-1; i++ {
		if sendAndProbe(t, full, waitReq(t, "w-"+string(rune('a'+i)), "task-w", 60000)) {
			t.Fatalf("wait %d refused below the cap", i)
		}
	}
	watch := func(id string, timeoutMs int) *ipc.Message {
		return mustMessage(t, ipc.MsgWatchNotificationsReq, id, ipc.WatchNotificationsReqPayload{TimeoutMs: timeoutMs})
	}
	if sendAndProbe(t, full, watch("watch-1", 60000)) {
		t.Fatal("the first watch was refused below the cap")
	}
	if !sendAndProbe(t, full, waitReq(t, "over-1", "task-w", 60000)) {
		t.Fatal("a wait past the cap was admitted")
	}
	if sendAndProbe(t, full, watch("watch-2", 1000)) {
		t.Fatal("a watch that only replaces the conn's own was refused at the cap")
	}
	// watch-2 holds the slot watch-1 had: still at the cap.
	if !sendAndProbe(t, full, waitReq(t, "over-2", "task-w", 60000)) {
		t.Fatal("replacing a watch freed a slot")
	}
	f := waitFrameWithID(t, full, "watch-2", 3*time.Second)
	var p ipc.WatchNotificationsRespPayload
	if f.Type != ipc.MsgWatchNotificationsResp || f.DecodePayload(&p) != nil || !p.Timeout {
		t.Fatalf("watch-2: got %s %s, want a timeout answer", f.Type, f.Payload)
	}
	if sendAndProbe(t, full, waitReq(t, "after", "task-w", 60000)) {
		t.Fatal("the watch's slot was not back when its answer arrived")
	}
}

// Waits alone can fill the cap, and then a watch — which would add a parked
// goroutine, not replace one — is refused.
func TestRights_WatchRefusedWhenWaitsFillTheCap(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	liveTask(h, "task-full")
	for i := 0; i < maxParkedPerConn; i++ {
		if sendAndProbe(t, full, waitReq(t, "f-"+string(rune('a'+i)), "task-full", 60000)) {
			t.Fatalf("wait %d refused below the cap", i)
		}
	}
	watch := mustMessage(t, ipc.MsgWatchNotificationsReq, "watch-over", ipc.WatchNotificationsReqPayload{TimeoutMs: 60000})
	if !sendAndProbe(t, full, watch) {
		t.Fatal("a watch past a cap the waits filled was admitted")
	}
}

// Parked waits end with their conn: nobody is left to answer, and a token
// holder could otherwise park, disconnect and log in again to keep
// goroutines and timers alive for minutes each.
func TestRights_ParkedWaitsEndWithTheirConn(t *testing.T) {
	h := newAuthHarness(t)
	full, _ := h.login(t, h.mint(t, "full", clientauth.LevelFull, nil))
	liveTask(h, "task-gone")
	for i := 0; i < maxParkedPerConn; i++ {
		if sendAndProbe(t, full, waitReq(t, "g-"+string(rune('a'+i)), "task-gone", 300000)) {
			t.Fatalf("wait %d refused below the cap", i)
		}
	}
	pollUntil(t, "the waits to park", func() bool { return h.d.parkedWaits.Load() == maxParkedPerConn })
	if err := full.Close(); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, "the parked waits to end with their conn", func() bool { return h.d.parkedWaits.Load() == 0 })
}
