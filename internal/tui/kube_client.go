package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/kubediscover"
)

// kubeCtxMsg carries one kube-context response. Gen echoes the requesting
// message's ID — see kubeScanState.gen.
type kubeCtxMsg struct {
	Resp ipc.KubeCtxRespPayload
	Gen  string
}

// kubeScanTimeoutMsg fires when a kube-context request went unanswered.
type kubeScanTimeoutMsg struct{ gen string }

// kubeScanTimeout bounds one round trip from the client side. Sized like
// gitScanTimeout and for the same reason: this request can cross ssh, where a
// first round trip after an idle period pays a TCP handshake and, on Windows, a
// full authentication. Still inside the daemon's own 10 s bound, so a
// slow-but-working scan reports its result rather than being pre-empted here.
//
// A var purely so the test binary can shorten it — same TestMain override as
// browseTimeout and gitScanTimeout.
var kubeScanTimeout = 8 * time.Second

// kubeScanPhase distinguishes the three things an empty context list can mean.
// Without it the field renders "(no kube contexts found)" while a scan is still
// in flight and again when one failed — a wrong answer stated confidently in
// both cases, which is the failure this phase exists to remove.
type kubeScanPhase int

const (
	kubeScanIdle kubeScanPhase = iota
	kubeScanning
	kubeScanReady
	kubeScanFailed
)

// kubeScanState tracks an in-flight kube-context request.
//
// The zero value means "nothing in flight". Unlike browseState this needs no
// separate pending flag: the request carries no content key at all, so gen is
// the whole identity and nextReqGen never returns "".
type kubeScanState struct {
	gen   string
	phase kubeScanPhase
}

// requestKubeContexts asks the daemon which kube contexts its kubeconfig names.
//
// The daemon is the only side that can answer honestly: kubediscover run here
// parses THIS machine's kubeconfig, so against a remote host it offered the
// laptop's clusters and then launched k9s with a --context the server may not
// have — with nothing in the list hinting the wrong kubeconfig had been read.
//
// Used in local mode too, deliberately. The answer is identical when the daemon
// is local, and a path exercised only by remote sessions is one that rots.
func (m *Model) requestKubeContexts() tea.Cmd {
	gen := m.nextReqGen()
	m.kubeScan = kubeScanState{gen: gen, phase: kubeScanning}
	m.kubeContexts = nil
	m.kubeCursor = 0
	m.kubeTruncated = false
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgKubeCtxReq, ipc.KubeCtxReqPayload{})
			if err != nil {
				log.Printf("kube discovery: encode: %v", err)
				return nil
			}
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		kubeScanTimeoutCmd(gen),
	)
}

func kubeScanTimeoutCmd(gen string) tea.Cmd {
	return tea.Tick(kubeScanTimeout, func(time.Time) tea.Msg {
		return kubeScanTimeoutMsg{gen: gen}
	})
}

// applyKubeContexts installs a discovery answer, dropping one whose generation
// no longer matches: the dialog can be reopened on another plugin before the
// first answer lands, and applying a superseded one would offer clusters chosen
// for a pane that no longer exists.
//
// There is deliberately NO fallback to the local kubeconfig on failure — that
// is precisely the bug this replaces. Locally the daemon is this machine, so
// nothing is lost.
func (m *Model) applyKubeContexts(resp ipc.KubeCtxRespPayload, gen string) tea.Cmd {
	if gen == "" || gen != m.kubeScan.gen {
		return nil
	}
	// Same dialog-closed guard as applyGitReposPickList. A late apply is inert
	// today — nothing renders kubeContexts outside this dialog and
	// enterSetupOrSplit nils it on the next open — but "inert today" is what
	// rots, and the surrounding code has this guard everywhere else.
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}
	if resp.Error != "" {
		log.Printf("kube discovery: %s", resp.Error)
		m.kubeScan.phase = kubeScanFailed
		return nil
	}

	// Set from BOTH signals, not just the daemon's own flag: a daemon that
	// over-sends without setting Truncated must still be caught, the same
	// reason the length is capped again below rather than trusted outright.
	m.kubeTruncated = resp.Truncated || len(resp.Contexts) > maxKubeContexts

	// Cap again on receipt. The daemon caps too, but a cap enforced only by the
	// sender is not a cap once the sender is a host the user may not control.
	ctxs := resp.Contexts
	if len(ctxs) > maxKubeContexts {
		ctxs = ctxs[:maxKubeContexts]
	}
	m.kubeContexts = make([]kubediscover.Context, 0, len(ctxs))
	for _, c := range ctxs {
		m.kubeContexts = append(m.kubeContexts, kubediscover.Context{
			Name: c.Name, Namespace: c.Namespace, Current: c.Current,
		})
	}
	m.kubeCursor = 0 // Default context row
	m.kubeScan.phase = kubeScanReady
	return nil
}

// applyKubeScanTimeout turns a never-answered request into something
// diagnosable rather than a list that stays empty forever.
//
// Local timer: it must NOT re-arm listenForMessages, unlike the response
// branch. Matched on gen so a late tick from a superseded request cannot clear
// a live one.
func (m *Model) applyKubeScanTimeout(gen string) tea.Cmd {
	if gen == "" || gen != m.kubeScan.gen || m.kubeScan.phase != kubeScanning {
		return nil
	}
	// Same dialog-closed guard its response sibling carries, and that both
	// recent_client.go functions carry. Inert today — this only sets a phase
	// nothing renders outside the dialog — but an unstated divergence between
	// the three client files is exactly what rots.
	if m.dialog != dialogCreatePaneSetup {
		return nil
	}
	m.kubeScan.phase = kubeScanFailed
	return nil
}
