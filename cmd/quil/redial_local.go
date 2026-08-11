package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
)

// localRedialAttempts is how many consecutive failures the local ladder runs
// before parking.
//
// It exists because BOTH extremes are wrong. Never parking traps the user in a
// frozen client when the daemon is genuinely gone — freezeInput drops input for
// as long as the ladder climbs, so a session with nothing to reconnect to
// answers nothing but ctrl+q while retrying forever. Parking on the first
// failure is just as wrong the other way: `quil restart` and an update-restart
// both leave the socket absent for a second or two, and a client that gives up
// inside that window turns a routine restart into a lost session.
//
// Four is the fast rung of the backoff (reconnectDecayAfter), i.e. a few
// seconds of grace — long enough for a daemon that is coming back, short enough
// that one that is not says so promptly.
const localRedialAttempts = 4

// localVerifyTimeout bounds the link probe on a reconnected local socket.
//
// Generous on purpose, and the asymmetry is the point: a daemon that is NOT
// running fails at the dial, instantly, so this budget is only ever spent on a
// daemon that IS listening but slow to answer — which is exactly when patience
// is right. Snapshots on a large workspace have been measured taking seconds
// under load, and parking a healthy daemon for being busy is the failure this
// whole change exists to remove.
const localVerifyTimeout = 10 * time.Second

// redialLocal reconnects the LOCAL daemon over its Unix socket.
//
// A local destination used to get no dialer at all, which made canReconnect
// (literally redialFns[dest] != nil) answer false and a lost local link fatal.
// The rationale — "its panes died with it, so retrying would hide the loss" —
// is true of a dead daemon and false of a dropped socket, and the 2026-08-11
// incident was the second: the daemon was healthy throughout, every pane was
// alive, and the client showed "the local daemon is gone — its panes are lost"
// because a write-side failure had retired the connection underneath it.
//
// There is no version gate here, deliberately, matching redialRemote's
// mid-session behaviour: a mismatch mid-session can only mean the daemon was
// replaced under a running client, and refusing then would end a session whose
// panes are healthy, with Bubble Tea holding the terminal so nothing can even
// be prompted.
func redialLocal() tui.RedialFunc {
	// Consecutive failures, reset by any success. An atomic rather than a plain
	// int because a superseded attempt can still be in flight when the next one
	// starts — the ladder retires stale results by generation, but it does not
	// stop the goroutine that is already running.
	var failures atomic.Int64

	return func(old tui.Client) (tui.Client, error) {
		// Release the dead connection. The TUI cannot: its Client is only
		// Send/Receive, and this is the layer that knows what it really is.
		if c, ok := old.(*ipc.Client); ok && c != nil {
			c.Close()
		}

		client, err := ipc.NewClient(config.SocketPath())
		if err != nil {
			return nil, localRedialFailure(&failures, err)
		}

		// A socket that accepts is not yet a daemon that answers — the wedged
		// daemon this repo already has a watchdog for accepts connections and
		// processes nothing. verifyRemoteLinkGated is transport-agnostic
		// despite its name (it sends MsgVersionReq over an *ipc.Client and
		// waits for the matching ID); gate=false keeps it a liveness probe
		// rather than a version check.
		if verr := verifyRemoteLinkGated(client, localVerifyTimeout, false); verr != nil {
			client.Close()
			return nil, localRedialFailure(&failures, verr)
		}

		failures.Store(0)
		return client, nil
	}
}

// localRedialFailure counts one failure and marks it permanent once the grace
// runs out, which is what parks the ladder.
func localRedialFailure(failures *atomic.Int64, err error) error {
	if n := failures.Add(1); n >= localRedialAttempts {
		log.Printf("local daemon unreachable after %d attempts; parking the reconnect loop: %v", n, err)
		return fmt.Errorf("%w: %v", tui.ErrLinkPermanent, err)
	}
	return err
}

// redialFor picks one destination's dialer. The empty destination is the local
// daemon — see destLocal in internal/tui for why "" carries that meaning.
//
// A single choice point rather than a branch at each of the two install sites:
// the launch loop over connected destinations and the loop over configured ones
// disagreeing about which dialer a destination gets is the kind of split that
// only shows up during an outage.
func redialFor(dest string, cfgOf func() *config.Config) tui.RedialFunc {
	if dest == "" {
		return redialLocal()
	}
	return redialRemote(cfgOf, dest)
}
