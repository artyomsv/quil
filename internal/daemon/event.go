package daemon

import (
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// PaneEvent represents a notification event from a pane.
type PaneEvent struct {
	ID        string
	PaneID    string
	TabID     string
	PaneName  string
	Type      string            // "process_exit", "output_match"
	Title     string
	Message   string
	Severity  string            // "info", "warning", "error"
	Timestamp time.Time
	Data      map[string]string // e.g., {"exit_code": "1"}
}

// connWatcher blocks an MCP connection until a matching event fires.
type connWatcher struct {
	conn    *ipc.Conn
	paneIDs map[string]bool // filter set (empty = all panes)
	ch      chan *PaneEvent // buffered(1), woken on match
}

// eventQueue is a bounded, mutex-protected event store with watcher support.
type eventQueue struct {
	events   []PaneEvent
	max      int
	watchers []*connWatcher
	mu       sync.Mutex
}

func newEventQueue(max int) *eventQueue {
	if max <= 0 {
		max = 50
	}
	return &eventQueue{
		max:    max,
		events: make([]PaneEvent, 0, max),
	}
}

// Push adds an event (newest first) and wakes any matching watchers.
func (q *eventQueue) Push(e PaneEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.events = append([]PaneEvent{e}, q.events...)
	if len(q.events) > q.max {
		q.events = q.events[:q.max]
	}

	// Wake matching watchers (one-shot: remove after waking)
	var remaining []*connWatcher
	for _, w := range q.watchers {
		if len(w.paneIDs) == 0 || w.paneIDs[e.PaneID] {
			select {
			case w.ch <- &e:
			default:
			}
			// Don't add to remaining — watcher is consumed
		} else {
			remaining = append(remaining, w)
		}
	}
	q.watchers = remaining
}

// Dismiss removes an event by ID.
func (q *eventQueue) Dismiss(eventID string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, e := range q.events {
		if e.ID == eventID {
			q.events = append(q.events[:i], q.events[i+1:]...)
			return
		}
	}
}

// DismissAll removes all events.
func (q *eventQueue) DismissAll() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = nil
}

// Events returns a snapshot copy of all events.
func (q *eventQueue) Events() []PaneEvent {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]PaneEvent, len(q.events))
	copy(out, q.events)
	return out
}

// Count returns the number of pending events.
func (q *eventQueue) Count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}

// AddWatcher registers a connection to be woken when a matching event fires.
func (q *eventQueue) AddWatcher(w *connWatcher) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.watchers = append(q.watchers, w)
}

// RemoveWatcher removes a specific watcher (used on timeout).
func (q *eventQueue) RemoveWatcher(w *connWatcher) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, existing := range q.watchers {
		if existing == w {
			q.watchers = append(q.watchers[:i], q.watchers[i+1:]...)
			return
		}
	}
}

// RemoveWatchersByConn removes all watchers for a specific connection
// and closes their channels to unblock any waiting goroutines.
func (q *eventQueue) RemoveWatchersByConn(conn *ipc.Conn) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var remaining []*connWatcher
	for _, w := range q.watchers {
		if w.conn == conn {
			close(w.ch)
		} else {
			remaining = append(remaining, w)
		}
	}
	q.watchers = remaining
}

// Per-event wire-size caps. The earlier wedge incident happened with a
// > 1 KiB box-drawing excerpt from an opencode splash screen flooding the
// IPC fan-out. 4 KiB per Message and 128 bytes per Data value give comfortable
// headroom for legitimate content (multi-line excerpts, command previews,
// error stacks) while keeping a runaway event source from bloating the wire.
//
// Truncation is from the *front* so the most recent (and usually most
// relevant) content survives — for excerpts that's the last visible line a
// terminal would actually display.
const (
	maxEventMessageBytes  = 4 * 1024
	maxEventDataValueBytes = 128
	truncationMarker      = "…[truncated]"
)

// toPaneEventPayload converts a PaneEvent to an IPC payload, enforcing the
// per-event wire-size caps. Caps are applied at the IPC boundary so all
// emitters (idle checker, bell, process_exit, future hook events) share the
// same protection.
func toPaneEventPayload(e PaneEvent) ipc.PaneEventPayload {
	message := e.Message
	truncated := false

	if len(message) > maxEventMessageBytes {
		// Keep the tail — for excerpts, the bottom-most lines are the
		// terminal-visible ones; for error blobs the trailing lines often
		// carry the actual cause.
		message = truncationMarker + message[len(message)-(maxEventMessageBytes-len(truncationMarker)):]
		truncated = true
	}

	var data map[string]string
	if len(e.Data) > 0 {
		data = make(map[string]string, len(e.Data))
		for k, v := range e.Data {
			if len(v) > maxEventDataValueBytes {
				data[k] = v[:maxEventDataValueBytes-len(truncationMarker)] + truncationMarker
				truncated = true
			} else {
				data[k] = v
			}
		}
	}
	if truncated {
		if data == nil {
			data = make(map[string]string, 1)
		}
		data["truncated"] = "1"
	}

	return ipc.PaneEventPayload{
		ID:        e.ID,
		PaneID:    e.PaneID,
		TabID:     e.TabID,
		PaneName:  e.PaneName,
		Type:      e.Type,
		Title:     e.Title,
		Message:   message,
		Severity:  e.Severity,
		Timestamp: e.Timestamp.UnixMilli(),
		Data:      data,
	}
}
