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

// Add installs a connection for dest and starts its pump. A dest that is
// already connected is left alone: a second pump on the same conn would race
// two readers over one socket.
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

// Remove drops a connection from the routing table and signals its pump.
//
// It cannot interrupt a pump parked inside Receive — Client is deliberately
// only Send/Receive, so this package cannot close the socket underneath one;
// that is what SetClientCloser exists for. The stop channel is what keeps the
// pump from publishing after the removal once its read does return.
func (r *Router) Remove(dest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stop, ok := r.stop[dest]; ok {
		close(stop)
		delete(r.stop, dest)
	}
	delete(r.conns, dest)
}

// pump reads one connection, stamping every message with its origin so
// applyWorkspaceState knows whose state it is replacing.
//
// On error it emits ONE MsgLinkLost and RETURNS. It must not loop: a dead conn
// fails Receive instantly, so looping floods the channel at CPU speed, pegs a
// core and drowns the Model. This mirrors listenForMessages, which returns
// linkLostMsg once and stops. Reconnect installs a fresh conn via Add, which
// starts a new pump.
func (r *Router) pump(dest string, c Client, stop <-chan struct{}) {
	for {
		msg, err := c.Receive()
		if err != nil {
			select {
			case <-stop:
			case r.in <- &ipc.Message{Type: ipc.MsgLinkLost, Origin: dest}:
			}
			return
		}
		msg.Origin = dest
		select {
		case r.in <- msg:
		case <-stop:
			return
		}
	}
}

// Send routes on Origin. An empty Origin resolves to the active project's
// dest — NOT to local — so a missed stamp fails toward the daemon the user is
// looking at. During startup there are no projects yet, so a single-connection
// client falls back to its sole conn; that keeps remote-only mode, where no ""
// conn exists, from dropping its own first sends. The fallback is deliberately
// restricted to an UNSTAMPED message: a send that named a dest named it for a
// reason, and re-aiming it at whatever conn happens to be the only one is how a
// remote pane's keystrokes end up on the local daemon.
func (r *Router) Send(m *ipc.Message) error {
	dest := m.Origin
	if dest == "" {
		dest = r.currentDest()
	}

	r.mu.RLock()
	c, ok := r.conns[dest]
	if !ok && m.Origin == "" && len(r.conns) == 1 {
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
	msg.Origin = m.destOfPane(paneID)
	return m.client.Send(msg)
}

// sendForDest stamps an already-known destination onto a message. The tab
// carries its project's dest, so a call site holding a *TabModel needs no
// lookup — but it must not skip the stamp either, since a tab in a background
// project is exactly as remote as a pane in one.
func (m *Model) sendForDest(dest string, msg *ipc.Message) error {
	msg.Origin = dest
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

// sendForTab stamps a tab-scoped message with its owning daemon.
func (m *Model) sendForTab(tabID string, msg *ipc.Message) error {
	return m.sendForDest(m.destOfTab(tabID), msg)
}
