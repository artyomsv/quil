package daemon

import (
	"context"
	"log"
	"time"

	"github.com/artyomsv/quil/internal/gitdiscover"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/kubediscover"
)

// gitDiscoverTimeout bounds one discovery walk.
//
// SubRepos reads a directory and stats each child, so a home directory on an
// unresponsive network mount can park it. gitdiscover already takes a context
// for exactly this reason (RD-004); this is the deadline that finally supplies
// one — the TUI-side caller it replaces passed context.Background().
var gitDiscoverTimeout = 10 * time.Second

// handleGitReposReq answers "which git repositories are near this directory".
//
// Worker goroutine + single-flight, matching handleBrowseDirReq. Its own slot
// rather than sharing the browser's: the setup dialog resolves a directory and
// then discovers repos in it, so one guard would make each step fail whenever it
// followed the other closely enough.
func (d *Daemon) handleGitReposReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginGitDiscover(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgGitReposResp, rejection)
		return
	}
	fallback := d.defaultCWD()
	go func() {
		defer d.gitDiscovering.Store(false)
		respondTo(conn, msg.ID, ipc.MsgGitReposResp, gitReposResponse(gitReposReq(msg), fallback))
	}()
}

// beginGitDiscover claims the single-flight slot, returning the rejection to
// send when it is already taken. Split from the handler because ipc.Conn cannot
// be built outside its package — same reason as beginBrowseScan.
func (d *Daemon) beginGitDiscover(msg *ipc.Message) (ipc.GitReposRespPayload, bool) {
	if d.gitDiscovering.CompareAndSwap(false, true) {
		return ipc.GitReposRespPayload{}, true
	}
	return ipc.GitReposRespPayload{
		CWD:   gitReposReq(msg).CWD,
		Error: "another repository scan is already running",
	}, false
}

func gitReposReq(msg *ipc.Message) ipc.GitReposReqPayload {
	var req ipc.GitReposReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleGitReposReq: decode: %v", err)
	}
	return req
}

// gitReposResponse is the pure half.
//
// CONTRACT: CWD echoes req.CWD VERBATIM on every path, including the error
// ones. It is the client's staleness key, not a statement about what was read.
//
// An empty result with no Error is a real finding — "no repo here" — and the
// caller must be able to tell it from a failure, because only one of the two
// justifies telling the user there is no repository.
func gitReposResponse(req ipc.GitReposReqPayload, fallback string) ipc.GitReposRespPayload {
	out := ipc.GitReposRespPayload{CWD: req.CWD}

	dir := req.CWD
	if dir == "" {
		dir = fallback
	}
	if dir == "" {
		out.Error = "no directory to scan and no default available"
		return out
	}

	repos, answered := discoverWithin(dir, gitDiscoverTimeout)
	out.Repos = repos
	// Report a timeout as an error rather than as "no repositories". The two
	// render differently and only one of them is a finding — telling the user
	// there is no repo because a slow mount ran out the clock is a wrong answer
	// stated confidently, which is the failure this whole phase is about.
	if !answered && len(out.Repos) == 0 {
		out.Error = "repository scan timed out"
	}
	return out
}

// discoverWithin runs the discovery walk under a wall-clock budget, reporting
// whether it answered in time.
//
// gitdiscover.Candidates already takes a context and checks it between steps,
// and that was believed to be the bound. It is not: a context cannot interrupt
// an os.Stat or os.ReadDir already blocked in the kernel, which is exactly what
// an unresponsive NFS or SMB mount produces — and a home directory on such a
// mount is the case gitDiscoverTimeout was written for. The context bounds the
// WALK; this bounds the CALL, and the call is what holds the single-flight slot.
// Without it, one Alt+G into a dead mount answers every later scan in the
// session with "another repository scan is already running", by something that
// will never finish.
//
// Same abandoned-goroutine trade as readDirWithin, and it shares that function's
// process-wide cap for the same reason: a parked syscall pins an OS thread.
func discoverWithin(dir string, d time.Duration) ([]string, bool) {
	if d <= 0 || !claimBlockingFSCall() {
		return nil, false
	}
	ch := make(chan []string, 1)
	go func() {
		defer releaseBlockingFSCall()
		// The inner context still earns its place: it stops the walk promptly at
		// every point that is NOT blocked in a syscall, so the abandoned
		// goroutine usually exits soon after we stop waiting for it.
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		ch <- discoverCandidates(ctx, dir)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r, true
	case <-timer.C:
		return nil, false
	}
}

// discoverCandidates is the seam the walk goes through, paired with statPath and
// readDirPath in browse.go and existing for the same reason: the branch worth
// pinning is the one where the walk never returns.
var discoverCandidates = gitdiscover.Candidates

// kubeDiscoverTimeout bounds one kubeconfig read.
//
// Contexts() reads files whose paths come from KUBECONFIG, which can name a
// network mount. kubediscover already takes a context (RD-004) and checks it
// between files, but a context cannot interrupt an os.ReadFile already parked
// in the kernel — the same distinction discoverWithin documents.
var kubeDiscoverTimeout = 10 * time.Second

// maxKubeContexts bounds what the daemon will report. The TUI caps again on
// receipt: a cap enforced only by the sender is not a cap.
const maxKubeContexts = 50

// kubeContexts is the seam the read goes through, paired with discoverCandidates
// and existing for the same reason — the branch worth pinning is the one where
// the read never returns.
var kubeContexts = kubediscover.Contexts

// handleKubeCtxReq answers "which kube contexts does this machine's
// kubeconfig name".
func (d *Daemon) handleKubeCtxReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginKubeDiscover()
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgKubeCtxResp, rejection)
		return
	}
	go func() {
		defer d.kubeDiscovering.Store(false)
		respondTo(conn, msg.ID, ipc.MsgKubeCtxResp, kubeCtxResponse())
	}()
}

// beginKubeDiscover claims the single-flight slot, returning the rejection to
// send when it is already taken.
//
// Split from the handler for the same reason beginGitDiscover and
// beginBrowseScan are: ipc.Conn cannot be constructed outside its own
// package, so handleKubeCtxReq itself is untestable — but unlike those two,
// there is no request-derived content key to echo back on rejection
// (KubeCtxReqPayload is empty), so a test manipulating d.kubeDiscovering
// directly would look equivalent to calling through the handler while
// actually exercising a field the handler might not even use. This function
// is that one seam: a test that calls it is guaranteed to be exercising the
// same CompareAndSwap the handler calls, not a same-named field beside it.
func (d *Daemon) beginKubeDiscover() (ipc.KubeCtxRespPayload, bool) {
	if d.kubeDiscovering.CompareAndSwap(false, true) {
		return ipc.KubeCtxRespPayload{}, true
	}
	return ipc.KubeCtxRespPayload{
		Error: "another kube context scan is already running",
	}, false
}

// kubeCtxResponse is the pure half.
//
// An empty result with no Error is a real finding — "no kubeconfig here" — and
// the caller must be able to tell it from a failure, because only one of the
// two justifies telling the user there are no contexts.
func kubeCtxResponse() ipc.KubeCtxRespPayload {
	var out ipc.KubeCtxRespPayload

	ctxs, answered := kubeContextsWithin(kubeDiscoverTimeout)
	if !answered {
		out.Error = "kube context scan timed out"
		return out
	}
	if len(ctxs) > maxKubeContexts {
		ctxs = ctxs[:maxKubeContexts]
		out.Truncated = true
	}
	out.Contexts = make([]ipc.KubeContextInfo, 0, len(ctxs))
	for _, c := range ctxs {
		out.Contexts = append(out.Contexts, ipc.KubeContextInfo{
			Name: c.Name, Namespace: c.Namespace, Current: c.Current,
		})
	}
	return out
}

// kubeContextsWithin runs the read under a wall-clock budget. Same abandoned-
// goroutine trade as discoverWithin, and it shares that function's process-wide
// cap for the same reason: a parked syscall pins an OS thread.
func kubeContextsWithin(d time.Duration) ([]kubediscover.Context, bool) {
	if d <= 0 || !claimBlockingFSCall() {
		return nil, false
	}
	ch := make(chan []kubediscover.Context, 1)
	go func() {
		defer releaseBlockingFSCall()
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		ch <- kubeContexts(ctx)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r, true
	case <-timer.C:
		return nil, false
	}
}
