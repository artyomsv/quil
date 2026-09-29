package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/persist"
)

func TestHandleMessage_SharedImport_AppliesEveryKindOnceOnAnEmptyDaemon(t *testing.T) {
	d, client, _, paneID := notesTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	// Built from the code point (U+202E, RIGHT-TO-LEFT OVERRIDE) rather than an
	// escape literal in source: this file must never carry the raw rune.
	bidiOverride := "a" + string(rune(0x202e)) + "b"
	req := ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindGroups, ipc.ImportKindRecent, ipc.ImportKindNotes},
		Groups: []ipc.SharedImportGroup{{Name: "Infra", ProjectIDs: []string{p.ID, "proj-unknown"}}, {Name: "Empty"}, {Name: bidiOverride}},
		Recent: []string{"/one", "/two", "", "/one"},
		Notes:  []ipc.SharedImportNote{{PaneID: paneID, Text: "old\n"}, {PaneID: "pane-00000000", Text: "x"}},
	}
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, req))
	if len(resp.Answered) != 3 || !resp.GroupsApplied || !resp.RecentApplied || resp.NotesApplied != 1 || resp.NotesSkipped != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	groups, recent := d.session.SharedSnapshot()
	if len(groups) != 2 || groups[0] != "Infra" || groups[1] != "Empty" {
		t.Errorf("groups = %v (the invalid name must be skipped)", groups)
	}
	if len(recent) != 2 || recent[0] != "/one" {
		t.Errorf("recent = %v", recent)
	}
	for _, ps := range d.buildWorkspaceState().Projects {
		if ps.ID == p.ID && ps.Group != "Infra" {
			t.Errorf("project group = %q", ps.Group)
		}
	}
	if noteRevOf(d, paneID) != 1 {
		t.Errorf("note rev = %d, want 1", noteRevOf(d, paneID))
	}
	if text, _ := persist.LoadNotes(config.NotesDir(), paneID); text != "old\n" {
		t.Errorf("note text = %q", text)
	}
	// Second import: every kind is skipped, and still answered.
	again := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, req))
	if len(again.Answered) != 3 || again.GroupsApplied || again.RecentApplied || again.NotesApplied != 0 {
		t.Errorf("second import = %+v, want all answered, nothing applied", again)
	}
}

func TestHandleMessage_SharedImport_SkipsAKindTheDaemonAlreadyHolds(t *testing.T) {
	d, client, _, _ := notesTestDaemon(t)
	p := d.session.CreateProject("api", t.TempDir())
	if err := d.session.SetProjectGroup(p.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindGroups},
		Groups: []ipc.SharedImportGroup{{Name: "Theirs", ProjectIDs: []string{p.ID}}},
	}))
	if resp.GroupsApplied || len(resp.Answered) != 1 {
		t.Errorf("resp = %+v", resp)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 1 || groups[0] != "Mine" {
		t.Errorf("groups = %v", groups)
	}
}

func TestHandleMessage_SharedImport_KindNotListedIsNeitherAppliedNorAnswered(t *testing.T) {
	d, client, _, _ := notesTestDaemon(t)
	resp := decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, client, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
		Kinds:  []string{ipc.ImportKindRecent},
		Groups: []ipc.SharedImportGroup{{Name: "Stray"}},
		Recent: []string{"/r"},
	}))
	if len(resp.Answered) != 1 || resp.Answered[0] != ipc.ImportKindRecent || resp.GroupsApplied {
		t.Errorf("resp = %+v", resp)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 0 {
		t.Errorf("an unlisted kind was applied: %v", groups)
	}
}

// TC-4: two clients importing at once — exactly one applies.
func TestHandleMessage_SharedImport_ConcurrentImports_ExactlyOneApplies(t *testing.T) {
	d, a, sock, _ := notesTestDaemon(t)
	b, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	sendNoID(t, b, ipc.MsgClientHello, ipc.ClientHelloPayload{Role: "bridge", PID: os.Getpid()})
	var wg sync.WaitGroup
	results := make([]ipc.SharedImportRespPayload, 2)
	for i, c := range []*ipc.Client{a, b} {
		wg.Add(1)
		go func(i int, c *ipc.Client) {
			defer wg.Done()
			results[i] = decodeInto[ipc.SharedImportRespPayload](t, roundTrip(t, c, ipc.MsgSharedImport, ipc.MsgSharedImportResp, ipc.SharedImportPayload{
				Kinds:  []string{ipc.ImportKindGroups, ipc.ImportKindRecent},
				Groups: []ipc.SharedImportGroup{{Name: "G" + string(rune('a'+i))}},
				Recent: []string{"/r" + string(rune('a'+i))},
			}))
		}(i, c)
	}
	wg.Wait()
	applied := 0
	for _, r := range results {
		if r.GroupsApplied {
			applied++
		}
		if r.GroupsApplied != r.RecentApplied {
			t.Errorf("one client's kinds split: %+v", r)
		}
	}
	if applied != 1 {
		t.Fatalf("%d imports applied, want 1: %+v", applied, results)
	}
	if groups, _ := d.session.SharedSnapshot(); len(groups) != 1 {
		t.Errorf("groups = %v", groups)
	}
}

func TestHandleMessage_SharedImport_OversizeAndBadPayloadAnswered(t *testing.T) {
	_, client, _, _ := notesTestDaemon(t)
	helloAsScript(t, client)
	big, _ := json.Marshal(ipc.SharedImportPayload{Kinds: []string{ipc.ImportKindNotes},
		Notes: []ipc.SharedImportNote{{PaneID: "pane-00000000", Text: strings.Repeat("x", ipc.MaxSharedImportBytes+1)}}})
	msg := &ipc.Message{Type: ipc.MsgSharedImport, ID: "big-1", Payload: big}
	if err := client.Send(msg); err != nil {
		t.Fatal(err)
	}
	if got := waitFrameWithID(t, client, "big-1", 10*time.Second); got.Type != ipc.MsgError {
		t.Errorf("oversize import answered with %s, want error", got.Type)
	}
	bad := &ipc.Message{Type: ipc.MsgSharedImport, ID: "bad-1", Payload: []byte(`"not an object"`)}
	if err := client.Send(bad); err != nil {
		t.Fatal(err)
	}
	waitFrameWithID(t, client, "bad-1", 5*time.Second)
}
