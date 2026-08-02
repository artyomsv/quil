package tui

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/artyomsv/quil/internal/ipc"
)

// Router multiplexes several daemon connections behind the single tuiClient
// the Model consumes — a third implementation of that interface beside
// *ipc.Client and the test fakes, which is why the Model needs no transport
// change to gain multi-daemon support.
type Router struct {
	mu    sync.RWMutex
	conns map[string]Client
	stop  map[string]chan struct{}
	in    chan *ipc.Message

	// activeDest is an atomic rather than a func() string closure. A closure
	// built in main would capture the Model VALUE that tea.NewProgram copies,
	// and the program mutates only its own copy — so it would report zero
	// projects forever and route every unstamped send, keystrokes included, to
	// the local daemon. The running program pushes here instead, via
	// SetActiveDest.
	activeDest atomic.Value // string
}

// NewRouter builds a router over the given connections, keyed by destination.
// The empty key is the local daemon.
func NewRouter(conns map[string]Client) *Router {
	r := &Router{
		conns: make(map[string]Client, len(conns)),
		stop:  make(map[string]chan struct{}),
		in:    make(chan *ipc.Message, 64),
	}
	r.activeDest.Store("")
	for dest, c := range conns {
		r.Add(dest, c)
	}
	return r
}

// SetActiveDest is called by the running program whenever the active project
// changes, so the router's default routing target tracks what the user is
// looking at. Safe from any goroutine.
func (r *Router) SetActiveDest(dest string) { r.activeDest.Store(dest) }

func (r *Router) currentDest() string {
	d, _ := r.activeDest.Load().(string)
	return d
}

// Add installs a connection for dest and starts its pump.
//
// A dest that is already connected LIVE is left alone: a second pump on the
// same conn would race two readers over one socket. A dest whose pump has died
// is not "already connected" — pump retires its own registration on failure
// precisely so that this is a replace rather than a silent no-op, which is what
// makes the documented reconnect path (a fresh conn handed to Add) work.
func (r *Router) Add(dest string, c Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.conns[dest]; exists {
		return
	}
	stop := make(chan struct{})
	r.conns[dest] = c
	r.stop[dest] = stop
	go r.pump(dest, c, stop)
}

// Conn returns the connection installed for dest, or nil when none is.
//
// The reconnect loop needs it to hand the DEAD connection to the dialer, which
// is the layer that can close it. Nil is a real answer rather than a defect: a
// pump retires its own registration BEFORE publishing the loss, so a destination
// whose link died is already gone from the table by the time a redial for it
// runs. Callers must tolerate it — see Model.connFor.
func (r *Router) Conn(dest string) Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.conns[dest]
}

// Remove drops a connection from the routing table and signals its pump.
//
// It cannot interrupt a pump parked inside Receive — Client is deliberately
// only Send/Receive, so this package cannot close the socket underneath one;
// that is what SetClientCloser exists for. What the stop channel guarantees is
// narrower and is checked FIRST at every publish point: a Remove that completes
// before the parked read returns can never deliver another message or a link
// loss for a dest the caller deliberately disconnected. (Without that ordering
// the guarantee is a coin flip — select chooses uniformly among ready cases, and
// a 64-buffered r.in is almost always ready.)
func (r *Router) Remove(dest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.stop[dest]; ok {
		close(stop)
		delete(r.stop, dest)
	}
	delete(r.conns, dest)
}

// retire drops a dead pump's OWN registration, so Add can install a replacement.
//
// Identity of the stop channel is the guard: a pump whose dest has since been
// replaced by a newer conn would otherwise unregister its successor. Comparing
// the channel is exact where comparing the dest alone is not.
func (r *Router) retire(dest string, stop chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop[dest] != stop {
		return
	}
	delete(r.stop, dest)
	delete(r.conns, dest)
}

// pump reads one connection, stamping every message with its origin so
// applyWorkspaceState knows whose state it is replacing.
//
// On error it emits ONE MsgLinkLost and RETURNS. It must not loop: a dead conn
// fails Receive instantly, so looping floods the channel at CPU speed, pegs a
// core and drowns the Model. This mirrors listenForMessages, which returns
// linkLostMsg once and stops. Reconnect installs a fresh conn via Add, which
// starts a new pump — reachable because retire runs BEFORE the loss is
// published, so the registration is already gone by the time anyone can react
// to it.
//
// Every publish is preceded by a non-blocking stop check rather than relying on
// a two-case select: both cases are normally ready and Go picks uniformly, so a
// removed conn would still deliver roughly half its messages — including a
// MsgLinkLost for a dest the user disconnected on purpose, which quits the TUI
// in local mode, and a stale workspace_state that rebuilds a project after a
// reconnect has already replaced the conn.
func (r *Router) pump(dest string, c Client, stop chan struct{}) {
	for {
		msg, err := c.Receive()
		// A nil message with a nil error breaks Receive's contract; *ipc.Client
		// never does it, but the interface does not forbid it and the stamp
		// below would dereference nil. Treated as a failure rather than skipped,
		// because a client that does it once will do it every iteration and the
		// skip would be the busy loop this pump exists to avoid.
		if err != nil || msg == nil {
			if stopped(stop) {
				return
			}
			r.retire(dest, stop)
			select {
			case <-stop:
			case r.in <- &ipc.Message{Type: ipc.MsgLinkLost, Origin: dest}:
			}
			return
		}
		msg.Origin = dest
		if stopped(stop) {
			return
		}
		select {
		case r.in <- msg:
		case <-stop:
			return
		}
	}
}

// stopped reports whether stop is already closed, without blocking.
func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// destLocal is the local daemon named EXPLICITLY.
//
// Everywhere else in the client the local daemon's destination is "" —
// ProjectModel.Dest, TabModel.Dest, the conns key, the Origin the local pump
// stamps. An unstamped ipc.Message also carries "", because that is the zero
// value of the field. Send therefore could not tell "this is for the local
// daemon" from "nobody stamped this", and resolved both to the active project's
// dest: with a remote project focused, every LOCAL pane's resize, every local
// tab's layout and every local lazygit overlay went to the remote daemon, which
// drops them as unknown ids — silently stopping local PTYs from tracking the
// window size and local layouts from persisting.
//
// The sendFor* helpers therefore never emit "": stampDest translates it to this
// sentinel and routeDest translates it back, so "" keeps meaning UNSTAMPED on
// the one path that has to tell the two apart. A sentinel rather than a second
// "was it set" bool for the reason ErrLinkPermanent gives in reconnect.go — a
// field a call site can forget to copy is a field that will be forgotten.
const destLocal = "\x00local"

// stampDest records the destination a message is aimed at.
func stampDest(m *ipc.Message, dest string) {
	if dest == "" {
		dest = destLocal
	}
	m.Origin = dest
}

// routeDest reads a stamp back, reporting whether the sender set one at all.
func routeDest(origin string) (dest string, stamped bool) {
	switch origin {
	case "":
		return "", false
	case destLocal:
		return "", true
	default:
		return origin, true
	}
}

// Send routes on the stamp. An UNSTAMPED message resolves to the active
// project's dest — NOT to local — so a missed stamp fails toward the daemon the
// user is looking at. During startup there are no projects yet, so a
// single-connection client falls back to its sole conn; that keeps remote-only
// mode, where no "" conn exists, from dropping its own first sends. Both the
// active-dest resolution and that fallback are restricted to unstamped
// messages: a send that named a destination named it for a reason, and
// re-aiming it at whatever conn happens to be the only one is how a remote
// pane's keystrokes end up on the local daemon — or a local pane's on a remote
// host.
func (r *Router) Send(m *ipc.Message) error {
	dest, stamped := routeDest(m.Origin)
	if !stamped {
		dest = r.currentDest()
	}
	// The sentinel is internal to the send path: the conn sees the plain dest,
	// which is what every other Origin reader in the client expects.
	m.Origin = dest

	r.mu.RLock()
	c, ok := r.conns[dest]
	if !ok && !stamped && len(r.conns) == 1 {
		for _, only := range r.conns {
			c, ok = only, true
		}
	}
	r.mu.RUnlock()

	if !ok {
		// Drop with a log. Returning an error would break resizeAllPanes and
		// sendAllLayouts mid-iteration and leave other daemons unsynced.
		log.Printf("router: dropping %s for unreachable dest %q", m.Type, dest)
		return nil
	}
	return c.Send(m)
}

// Receive hands the Model the next message from any connection. It never
// returns an error: a dead connection arrives as MsgLinkLost carrying the dest
// that died, so one daemon's loss cannot be read as the session's.
func (r *Router) Receive() (*ipc.Message, error) { return <-r.in, nil }

// destRouter is the optional capability a multi-daemon client has. Kept as a
// narrow interface so *ipc.Client and the test fakes stay two-method.
type destRouter interface{ SetActiveDest(string) }

// activeDest is the destination of the project the user is looking at. Empty
// means the local daemon — which is also the answer for a Model with no
// projects yet, and for every single-daemon session.
func (m *Model) activeDest() string {
	if p := m.cur(); p != nil {
		return p.Dest
	}
	return ""
}

// syncActiveDest pushes the active project's dest into the router. Call it
// after ANY change to m.activeProject or m.projects — the router cannot read
// the Model, because tea.NewProgram holds its own copy.
func (m *Model) syncActiveDest() {
	if r, ok := m.client.(destRouter); ok {
		r.SetActiveDest(m.activeDest())
	}
}

// destOfPane resolves which daemon owns a pane. Falls back to the active dest
// for a pane the Model has not seen yet.
//
// The overlay pane is checked alongside the layout tree because it is a real
// daemon pane that deliberately lives OUTSIDE that tree: Alt+G's destroy and
// resize sends name it by ID, and a FindLeaf-only lookup would answer for the
// wrong machine on every one of them.
func (m *Model) destOfPane(paneID string) string {
	for _, proj := range m.projects {
		for _, tab := range proj.tabs {
			if tab.Root != nil && tab.Root.FindLeaf(paneID) != nil {
				return proj.Dest
			}
			if tab.overlayPane != nil && tab.overlayPane.ID == paneID {
				return proj.Dest
			}
		}
	}
	return m.activeDest()
}

// sendForPane stamps a pane-scoped message with its OWNING daemon. Every send
// that names a PaneID must go through here: the active project is the wrong
// answer whenever the pane lives in a different one, and for MsgPaneInput that
// wrong answer means keystrokes on the wrong machine.
func (m *Model) sendForPane(paneID string, msg *ipc.Message) error {
	return m.sendForDest(m.destOfPane(paneID), msg)
}

// sendForDest stamps an already-known destination onto a message — the ONE
// place a send-side stamp is written. The tab carries its project's dest, so a
// call site holding a *TabModel needs no lookup, but it must not skip the stamp
// either: a tab in a background project is exactly as remote as a pane in one,
// and a LOCAL tab while a remote project is active needs the stamp just as
// much (see destLocal).
func (m *Model) sendForDest(dest string, msg *ipc.Message) error {
	stampDest(msg, dest)
	return m.client.Send(msg)
}

// destOfTab resolves which daemon owns a tab, for the call sites that hold
// only its ID. Falls back to the active dest, like destOfPane.
func (m *Model) destOfTab(tabID string) string {
	if p := m.projectOf(tabID); p != nil {
		return p.Dest
	}
	return m.activeDest()
}

// destOfProject resolves which daemon owns a project, for call sites that
// hold only its ID — the rename/destroy confirm dialogs resolve back to a
// *ProjectModel at SEND time rather than closing over a pointer that a
// workspace reconciliation could have invalidated while the dialog was open.
// Falls back to the active dest, like destOfPane and destOfTab.
func (m *Model) destOfProject(id string) string {
	if p := m.projectByID(id); p != nil {
		return p.Dest
	}
	return m.activeDest()
}
