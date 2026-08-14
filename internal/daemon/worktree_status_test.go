package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// stubWorktreeStatus swaps the change-counting seam and records the context
// each call was handed, which is how the shared-deadline property is observed.
func stubWorktreeStatus(t *testing.T, fn func(path string) (int, error)) *[]context.Context {
	t.Helper()
	var ctxs []context.Context
	prev := worktreeStatusFn
	worktreeStatusFn = func(ctx context.Context, path string) (int, error) {
		ctxs = append(ctxs, ctx)
		return fn(path)
	}
	t.Cleanup(func() { worktreeStatusFn = prev })
	return &ctxs
}

// The echo contract every request-response pair in this daemon keeps: Paths
// comes back byte-identical, because it is the client's staleness key rather
// than a statement about what was read. The confirm dialog holds one request in
// flight and matches the answer against the paths it asked about; a
// daemon-side normalisation makes a live request look permanently stale and
// leaves the dialog on "checking…" until it times out.
func TestWorktreeStatusResponse_EchoesThePathsVerbatim(t *testing.T) {
	stubWorktreeStatus(t, func(string) (int, error) { return 0, errors.New("boom") })
	raw := []string{"  /w/./feat-a/  ", "/w/feat-b"}

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: raw})

	if len(got.Paths) != len(raw) {
		t.Fatalf("Paths = %v, want %v verbatim", got.Paths, raw)
	}
	for i := range raw {
		if got.Paths[i] != raw[i] {
			t.Errorf("Paths[%d] = %q, want %q verbatim", i, got.Paths[i], raw[i])
		}
	}
}

func TestWorktreeStatusResponse_ReportsChangesPerPath(t *testing.T) {
	stubWorktreeStatus(t, func(path string) (int, error) {
		if path == "/w/feat-a" {
			return 3, nil
		}
		return 0, nil
	})

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{
		Paths: []string{"/w/feat-a", "/w/feat-b"},
	})

	if len(got.Statuses) != 2 {
		t.Fatalf("Statuses = %+v, want one per path", got.Statuses)
	}
	if got.Statuses[0].Path != "/w/feat-a" || got.Statuses[0].Changes != 3 {
		t.Errorf("Statuses[0] = %+v, want 3 changes for /w/feat-a", got.Statuses[0])
	}
	if got.Statuses[1].Changes != 0 || got.Statuses[1].Error != "" {
		t.Errorf("Statuses[1] = %+v, want a clean answer", got.Statuses[1])
	}
}

// One unreadable worktree must not take the others' answers with it: a tab can
// hold several, and the dialog renders each row apart. It also must not be
// reported as CLEAN — "0 changes" invites the toggle, and the whole reason the
// count is shown is to say what the removal would destroy.
func TestWorktreeStatusResponse_CarriesAPerPathErrorWithoutClaimingClean(t *testing.T) {
	stubWorktreeStatus(t, func(path string) (int, error) {
		if path == "/w/gone" {
			return 0, errors.New("not a git repository")
		}
		return 2, nil
	})

	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{
		Paths: []string{"/w/gone", "/w/feat-b"},
	})

	if len(got.Statuses) != 2 {
		t.Fatalf("Statuses = %+v, want one per path", got.Statuses)
	}
	if got.Statuses[0].Error == "" {
		t.Errorf("Statuses[0] = %+v, want the failure reported", got.Statuses[0])
	}
	if got.Statuses[1].Changes != 2 {
		t.Errorf("Statuses[1] = %+v, want the other path still answered", got.Statuses[1])
	}
}

// ONE deadline for the whole list, the shape dirsExistResponse uses: every
// entry blocks on the same thing — a repository on an unresponsive mount — and
// a per-path budget lets a tab full of worktrees hold the slot for N times as
// long. Sharing it also makes the timeout self-propagating: a path that spends
// the budget leaves every later one with a non-positive one.
func TestWorktreeStatusResponse_SharesOneDeadlineAcrossThePaths(t *testing.T) {
	ctxs := stubWorktreeStatus(t, func(string) (int, error) { return 0, nil })

	worktreeStatusResponse(ipc.WorktreeStatusReqPayload{
		Paths: []string{"/w/a", "/w/b", "/w/c"},
	})

	if len(*ctxs) != 3 {
		t.Fatalf("%d calls, want one per path", len(*ctxs))
	}
	first, ok := (*ctxs)[0].Deadline()
	if !ok {
		t.Fatal("the status calls run on a context with no deadline")
	}
	for i, ctx := range *ctxs {
		d, ok := ctx.Deadline()
		if !ok || !d.Equal(first) {
			t.Errorf("call %d deadline = %v (%v), want the shared %v", i, d, ok, first)
		}
	}
}

// The request names its own paths, so the count is bounded for the reason
// MaxBrowseEntries and maxDirsExistPaths are: a response must be incapable of
// exceeding the frame, and the work incapable of pinning the slot, BY
// CONSTRUCTION rather than because a tab usually holds few panes.
func TestWorktreeStatusResponse_RefusesAnOversizedRequest(t *testing.T) {
	calls := 0
	stubWorktreeStatus(t, func(string) (int, error) { calls++; return 0, nil })

	paths := make([]string, maxWorktreeStatusPaths+1)
	for i := range paths {
		paths[i] = "/w/x"
	}
	got := worktreeStatusResponse(ipc.WorktreeStatusReqPayload{Paths: paths})

	if got.Error == "" {
		t.Error("an oversized request was accepted")
	}
	if calls != 0 {
		t.Errorf("%d status calls ran for a refused request", calls)
	}
	if len(got.Paths) != len(paths) {
		t.Errorf("the refusal dropped the echo: %d paths, want %d", len(got.Paths), len(paths))
	}
}

// Its OWN single-flight slot, for the reason dirsChecking is not browseScanning:
// the close dialog asks for a status while the setup dialog may be listing
// worktrees, and a shared guard would fail each exactly when it followed the
// other.
func TestBeginWorktreeStatus_IsIndependentOfTheListingSlot(t *testing.T) {
	d := newTestDaemon(t)
	if !d.worktreeScanning.CompareAndSwap(false, true) {
		t.Fatal("the listing slot was already taken")
	}
	defer d.worktreeScanning.Store(false)

	if _, ok := d.beginWorktreeStatus(ipc.WorktreeStatusReqPayload{}); !ok {
		t.Error("a status request was refused while a worktree LISTING held its own slot")
	}
}

func TestBeginWorktreeStatus_RefusesASecondConcurrentRequest(t *testing.T) {
	d := newTestDaemon(t)
	req := ipc.WorktreeStatusReqPayload{Paths: []string{"/w/feat-a"}}
	if _, ok := d.beginWorktreeStatus(req); !ok {
		t.Fatal("the first status request was refused")
	}
	rejection, ok := d.beginWorktreeStatus(req)
	if ok {
		t.Fatal("a second concurrent status request was accepted")
	}
	// The rejection echoes the key like every other response, or the client
	// drops it as stale and waits out its whole timeout for an answer it holds.
	if len(rejection.Paths) != 1 || rejection.Paths[0] != "/w/feat-a" {
		t.Errorf("rejection Paths = %v, want the request's echoed", rejection.Paths)
	}
	if rejection.Error == "" {
		t.Error("the rejection carries no reason")
	}
}

// The handler has to be ON THE WIRE, and that is not derivable from any test of
// the pure half: a message type handleMessage has no case for is DROPPED, so
// the client sits on "checking…" until its own timeout and nothing anywhere
// reports a failure. This repo has already paid an evening for exactly that
// shape (a daemon that accepted project messages and did nothing), so the
// round trip is driven over a real socket rather than by calling the handler.
func TestWorktreeStatusReq_IsDispatchedAndAnswered(t *testing.T) {
	d, sock := overlayServerDaemon(t)
	stubWorktreeStatus(t, func(string) (int, error) { return 4, nil })
	_ = d

	c, err := ipc.NewClient(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	req, err := ipc.NewMessage(ipc.MsgWorktreeStatusReq, ipc.WorktreeStatusReqPayload{
		Paths: []string{"/w/feat-a"},
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	req.ID = "req-1"
	if err := c.Send(req); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Read on a goroutine and WAIT on a select. Receive blocks, so a loop with
	// its own deadline check hangs forever in the exact case this test exists to
	// catch — an undispatched type — and a hang is a ten-minute timeout rather
	// than a failure that names what is wrong.
	answers := make(chan *ipc.Message, 1)
	go func() {
		for {
			msg, err := c.Receive()
			if err != nil {
				return
			}
			if msg.Type == ipc.MsgWorktreeStatusResp {
				answers <- msg
				return
			}
		}
	}()

	select {
	case msg := <-answers:
		var resp ipc.WorktreeStatusRespPayload
		if err := msg.DecodePayload(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if msg.ID != "req-1" {
			t.Errorf("response ID = %q, want the request's", msg.ID)
		}
		if len(resp.Statuses) != 1 || resp.Statuses[0].Changes != 4 {
			t.Errorf("Statuses = %+v, want one entry with 4 changes", resp.Statuses)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no worktree_status_resp arrived — is the type dispatched?")
	}
}
