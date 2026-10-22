package daemon

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sbx"
)

// copyRunner records sbx calls and, for `sbx cp`, the bytes of the host file
// at the moment it was copied.
type copyRunner struct {
	mu     sync.Mutex
	calls  [][]string
	copied []byte
}

func (r *copyRunner) Run(_ context.Context, _ string, _ io.Writer, argv ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), argv...))
	if len(argv) > 2 && argv[1] == "cp" {
		b, err := os.ReadFile(argv[2])
		if err != nil {
			return err
		}
		r.copied = b
	}
	return nil
}

func pasteDaemon(t *testing.T) (*Daemon, *Pane, *copyRunner) {
	t.Helper()
	r := &copyRunner{}
	prevRunner, prevBin := sbxRunner, sbxBinaryFn
	sbxRunner = r
	sbxBinaryFn = func() (string, error) { return "/fake/sbx", nil }
	t.Cleanup(func() { sbxRunner, sbxBinaryFn = prevRunner, prevBin })

	d := &Daemon{session: NewSessionManager(1024), events: newEventQueue(50), cfg: config.Default()}
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.SandboxBackend = config.SandboxBackendSbx
	pane.PTY = &sbxSession{Session: &fakeSession{}, name: "cf-x-1", stop: func() {}}
	return d, pane, r
}

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), "rest-of-image"...)

// The image lands in the sandbox the pane's agent runs in, readable, and the
// answer is the path inside the VM. The host temp copy is gone afterwards.
func TestPasteImage_CopiesIntoThePanesSandbox(t *testing.T) {
	d, pane, r := pasteDaemon(t)
	resp := d.pasteImageResponse(context.Background(), ipc.PasteImageReqPayload{PaneID: pane.ID, PNG: testPNG})
	if resp.Error != "" {
		t.Fatalf("error = %q", resp.Error)
	}
	if !sbx.ValidPastePath(resp.Path) {
		t.Errorf("path = %q", resp.Path)
	}
	if !bytes.Equal(r.copied, testPNG) {
		t.Errorf("copied %q, want the image", r.copied)
	}
	if len(r.calls) != 2 || r.calls[0][1] != "cp" || r.calls[0][3] != "cf-x-1:"+resp.Path ||
		r.calls[1][1] != "exec" || r.calls[1][4] != "cf-x-1" {
		t.Fatalf("calls = %v", r.calls)
	}
	if _, err := os.Stat(r.calls[0][2]); !os.IsNotExist(err) {
		t.Errorf("the host temp copy %s was left behind (stat err %v)", r.calls[0][2], err)
	}
}

// Refusals run no sbx command and say why.
func TestPasteImage_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Pane)
		req   func(paneID string) ipc.PasteImageReqPayload
		want  string
	}{
		{"not a png", nil, func(id string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: id, PNG: []byte("GIF89a")}
		}, "not a PNG"},
		{"empty", nil, func(id string) ipc.PasteImageReqPayload { return ipc.PasteImageReqPayload{PaneID: id} }, "outside"},
		{"too large", nil, func(id string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: id, PNG: append(testPNG, make([]byte, ipc.MaxPasteImageBytes)...)}
		}, "outside"},
		{"unknown pane", nil, func(string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: "nope", PNG: testPNG}
		}, "no such pane"},
		{"host pane", func(p *Pane) { p.SandboxBackend = "" }, func(id string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: id, PNG: testPNG}
		}, "does not run in an sbx sandbox"},
		{"exited agent", func(p *Pane) { code := 1; p.ExitCode = &code }, func(id string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: id, PNG: testPNG}
		}, "not running"},
		{"no session", func(p *Pane) { p.PTY = nil }, func(id string) ipc.PasteImageReqPayload {
			return ipc.PasteImageReqPayload{PaneID: id, PNG: testPNG}
		}, "not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, pane, r := pasteDaemon(t)
			if tc.setup != nil {
				tc.setup(pane)
			}
			resp := d.pasteImageResponse(context.Background(), tc.req(pane.ID))
			if resp.Path != "" || !strings.Contains(resp.Error, tc.want) {
				t.Errorf("resp = %+v, want an error containing %q", resp, tc.want)
			}
			if len(r.calls) != 0 {
				t.Errorf("sbx was called: %v", r.calls)
			}
		})
	}
}

// The handler answers bad requests at once, holding no slot, and copies a good
// one on a worker that releases the slot when done. A nil conn drops the
// answers, which is all a socket-less test can do with them.
func TestHandlePasteImageReq_SlotAndWorker(t *testing.T) {
	d, pane, r := pasteDaemon(t)
	mk := func(p ipc.PasteImageReqPayload) *ipc.Message {
		m, err := ipc.NewMessage(ipc.MsgPasteImageReq, p)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = "1"
		return m
	}

	d.handlePasteImageReq(nil, &ipc.Message{Type: ipc.MsgPasteImageReq, ID: "1", Payload: []byte(`{"png":7}`)})
	d.handlePasteImageReq(nil, mk(ipc.PasteImageReqPayload{PaneID: pane.ID, PNG: []byte("GIF89a")}))
	if d.pasteCopying.Load() {
		t.Fatal("a refused request holds the slot")
	}

	d.pasteCopying.Store(true)
	d.handlePasteImageReq(nil, mk(ipc.PasteImageReqPayload{PaneID: pane.ID, PNG: testPNG}))
	r.mu.Lock()
	busyCalls := len(r.calls)
	r.mu.Unlock()
	if busyCalls != 0 {
		t.Fatal("a request ran while another held the slot")
	}
	d.pasteCopying.Store(false)

	d.handlePasteImageReq(nil, mk(ipc.PasteImageReqPayload{PaneID: pane.ID, PNG: testPNG}))
	deadline := time.Now().Add(5 * time.Second)
	for d.pasteCopying.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.pasteCopying.Load() || len(r.calls) != 2 {
		t.Errorf("slot held=%v, sbx calls=%d, want released and 2", d.pasteCopying.Load(), len(r.calls))
	}
}
