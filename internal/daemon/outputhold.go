package daemon

import (
	"log"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// Clean attach: a client that attaches while panes are writing must receive
// each pane's history replay and its live output EXACTLY ONCE, in order.
//
// Without a hold, the two streams race. The replay is a snapshot of OutputBuf
// sent frame by frame through the conn's must-deliver queue, while live bytes
// are broadcast through its droppable queue as they arrive. A live frame could
// land in the middle of the replay, and bytes written after the snapshot but
// before the replay finished arrived twice or out of place.
//
// So handleAttach holds the conn off live pane_output (ipc.Conn's
// holdPaneOutput flag) for the whole replay. Every flush during the hold is
// COPIED into the conn's hold here, with its stream position. When the replay
// is done, the held bytes are sent in order, minus whatever the replay already
// covered, and the flag is cleared. The daemon owns the stream, so it owns the
// dedupe: no position goes on the wire and no client repeats this logic.
//
// Two locks, both only ever taken in the order holdGate → holdMu:
//
//   - holdMu is a LEAF guarding the holds map and each hold's contents. It is
//     never held with PluginMu or the client registry mutex, and never across
//     SendBlocking.
//   - holdGate makes "append to the holds, then broadcast" ONE step against
//     setting or clearing a conn's flag. A flush holds it for READ from its
//     hold append through its broadcast (Broadcast only enqueues, so this
//     blocks on nothing); beginOutputHold and the release's final clear hold
//     it for WRITE. Without it, a flush could append its chunk, the release
//     could send that chunk and clear the flag, and then the flush's broadcast
//     would reach the now-unheld conn — the same bytes twice. At the other
//     end, a flush could miss the new hold and then broadcast to a conn whose
//     flag was set in between — the bytes never reach it at all.

// outputHoldLimit bounds one conn's held bytes. A pane whose held bytes would
// pass it loses them all and gets a repaint kick at release instead.
const outputHoldLimit = 4 << 20

// heldChunk is one flush held for one conn. start is the pane's stream
// position (Pane.outPos) of data[0]. data is shared read-only between every
// hold that took the same flush.
type heldChunk struct {
	paneID string
	start  uint64
	data   []byte
	gen    uint64
}

type outputHold struct {
	chunks []heldChunk
	bytes  int
	lost   map[string]bool // panes whose held bytes overflowed
}

// dropPane removes paneID's held chunks and marks the pane lost, so its later
// chunks are skipped too and the gap is whole rather than partial.
func (h *outputHold) dropPane(paneID string) {
	kept := h.chunks[:0]
	for _, ch := range h.chunks {
		if ch.paneID == paneID {
			h.bytes -= len(ch.data)
			continue
		}
		kept = append(kept, ch)
	}
	// Clear the tail so dropped data is not pinned by the backing array.
	for i := len(kept); i < len(h.chunks); i++ {
		h.chunks[i] = heldChunk{}
	}
	h.chunks = kept
	if h.lost == nil {
		h.lost = make(map[string]bool)
	}
	h.lost[paneID] = true
}

// holdDrainTimeout bounds beginOutputHold's wait for live frames queued before
// the hold. Past it the attach proceeds anyway, an ACCEPTED degradation: a
// frame still queued can land behind the replay as a duplicate, which is the
// behaviour before the hold existed, and only on a client too slow to take
// 64 frames in this long. Waiting longer would stall its attach instead.
const holdDrainTimeout = 2 * time.Second

// beginOutputHold starts holding c's live pane output. From the moment it
// returns, every flush either was delivered to c before it, or is held.
//
// "Delivered" includes the wait at the end. A conn receives live output from
// the moment it connects, and frames still in its droppable queue are written
// AFTER any must-deliver frame, since sendLoop drains that queue first. Their
// bytes are already in the replay, so without the wait a busy client got them
// again, behind the state frame and in the middle of its replay. No new frame
// joins that queue once the flag is set, so it only drains.
func (d *Daemon) beginOutputHold(c *ipc.Conn) {
	d.holdGate.Lock()
	d.holdMu.Lock()
	if d.holds == nil {
		d.holds = make(map[*ipc.Conn]*outputHold)
	}
	d.holds[c] = &outputHold{}
	c.SetHoldPaneOutput(true)
	d.holdMu.Unlock()
	d.holdGate.Unlock()

	// Outside both locks: flushes must not wait on a client's socket.
	deadline := time.Now().Add(holdDrainTimeout)
	for c.QueuedOutput() > 0 {
		if time.Now().After(deadline) {
			logger.Debug("attach: %d live frames still queued after %v; proceeding (they may repeat replayed bytes)",
				c.QueuedOutput(), holdDrainTimeout)
			return
		}
		select {
		case <-d.shutdown:
			return
		case <-c.Done():
			return // dead conn: the queue never drains
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// holdOutput appends one flush to every hold. The caller holds holdGate for
// read and must NOT hold the pane's PluginMu. data is copied, because the
// caller's read buffer is reused.
func (d *Daemon) holdOutput(paneID string, start uint64, data []byte, gen uint64) {
	d.holdMu.Lock()
	defer d.holdMu.Unlock()
	var cp []byte
	for _, h := range d.holds {
		if h.lost[paneID] {
			continue
		}
		if h.bytes+len(data) > outputHoldLimit {
			h.dropPane(paneID)
			continue
		}
		if cp == nil {
			cp = append([]byte(nil), data...)
		}
		h.chunks = append(h.chunks, heldChunk{paneID: paneID, start: start, data: cp, gen: gen})
		h.bytes += len(data)
	}
}

// releaseOutputHold delivers c's held output and ends the hold. end maps each
// pane whose replay was OutputBuf's bytes to the stream position that replay
// ended at; held bytes before it are already on the client.
//
// Batches are taken under holdMu and sent outside it. Flushes that happen
// during the drain append to the hold and go out in a later batch, in order.
// Each held frame goes through SendBlocking, i.e. the must-deliver queue that
// sendLoop drains first, so no live frame sent after the flag clears can
// overtake one.
func (d *Daemon) releaseOutputHold(c *ipc.Conn, end map[string]uint64) {
	for {
		d.holdMu.Lock()
		h := d.holds[c]
		if h == nil {
			d.holdMu.Unlock()
			return
		}
		batch, lost := h.chunks, h.lost
		h.chunks, h.bytes, h.lost = nil, 0, nil
		d.holdMu.Unlock()

		if len(batch) == 0 && len(lost) == 0 {
			if d.finishOutputHold(c) {
				return
			}
			continue
		}
		for _, ch := range batch {
			data := ch.data
			if e, ok := end[ch.paneID]; ok {
				if ch.start+uint64(len(data)) <= e {
					continue // already replayed
				}
				if ch.start < e {
					data = data[e-ch.start:] // partial overlap
				}
			}
			msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{PaneID: ch.paneID, Data: data, Generation: ch.gen})
			if err := c.SendBlocking(msg, d.shutdown); err != nil {
				d.dropOutputHold(c)
				return
			}
		}
		for paneID := range lost {
			p := d.session.Pane(paneID)
			if p == nil {
				continue
			}
			log.Printf("attach: held output for pane %s passed %d bytes and was dropped; asking it to repaint",
				paneID, outputHoldLimit)
			d.redrawKickPane(p)
		}
	}
}

// finishOutputHold ends c's hold when nothing more is held, and reports
// whether it did. Taking holdGate for write is what stops a flush sitting
// between its hold append and its broadcast: that flush either finished (its
// chunk is in the hold, so this refuses and the caller drains again) or has
// not appended yet (it will see no hold and an unheld conn).
func (d *Daemon) finishOutputHold(c *ipc.Conn) bool {
	d.holdGate.Lock()
	defer d.holdGate.Unlock()
	d.holdMu.Lock()
	defer d.holdMu.Unlock()
	h := d.holds[c]
	if h == nil {
		return true
	}
	if len(h.chunks) > 0 || len(h.lost) > 0 {
		return false
	}
	delete(d.holds, c)
	c.SetHoldPaneOutput(false)
	return true
}

// dropOutputHold discards c's hold without sending it: the conn is gone, or
// its attach stopped part way. The flag is cleared too, so a conn that is
// still alive is not left off live output forever.
func (d *Daemon) dropOutputHold(c *ipc.Conn) {
	d.holdMu.Lock()
	defer d.holdMu.Unlock()
	if _, ok := d.holds[c]; !ok {
		return
	}
	delete(d.holds, c)
	c.SetHoldPaneOutput(false)
}

// redrawKickPane asks a pane whose held output was dropped to repaint. Type
// and "running" are read in one PluginMu span, as handleAttach does; the kick
// itself runs outside it.
func (d *Daemon) redrawKickPane(p *Pane) {
	p.PluginMu.Lock()
	typ := p.Type
	running := p.PTY != nil && p.ExitCode == nil
	p.PluginMu.Unlock()
	if running {
		d.redrawKick(p, typ)
	}
}
