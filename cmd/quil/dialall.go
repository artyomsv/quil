package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/remoteinstall"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// extraDialTimeout bounds one background destination's whole dial: ssh's own
// connect, the daemon's readiness, and the version handshake.
//
// It exists because a configured host that is simply switched off must not
// become a tax on every launch. The transport's ConnectTimeout already bounds
// the TCP half at 15 s, but a host that accepts the connection and then never
// answers is bounded by nothing.
const extraDialTimeout = 25 * time.Second

// dialAllWith dials every destination and keeps whatever succeeds. A
// destination unreachable at launch is left out rather than being allowed to
// stop the client starting: its projects are only part of the workspace, and a
// laptop whose work VM is powered off must still open.
//
// It does NOT attach. Attach is the Model's job, on the first WindowSizeMsg,
// for two independent reasons: AttachPayload carries Cols/Rows and the daemon
// sizes the first PTY from them, while at dial time Bubble Tea has not reported
// a terminal size yet — a fresh daemon would spawn its Shell pane at 0×0 — and
// attach already has two owners, so a third would attach every conn twice and
// each attach replays the whole ghost buffer.
//
// Dials run CONCURRENTLY, which is a decision about latency rather than style:
// serially, three configured hosts that are all down would cost three full
// connect timeouts before the first frame. It is only safe because every dial
// past the primary one runs ssh in batch mode and therefore cannot prompt —
// concurrent interactive dials would interleave host-key prompts on one
// terminal.
func dialAllWith(dials map[string]func() (tui.Client, error)) map[string]tui.Client {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]tui.Client, len(dials))
	)
	for dest, dial := range dials {
		wg.Add(1)
		go func(dest string, dial func() (tui.Client, error)) {
			defer wg.Done()
			c, err := dial()
			if err != nil {
				log.Printf("remote: %s unreachable at launch: %v", dest, err)
				return
			}
			if c == nil {
				log.Printf("remote: %s returned no connection at launch", dest)
				return
			}
			mu.Lock()
			out[dest] = c
			mu.Unlock()
		}(dest, dial)
	}
	wg.Wait()
	return out
}

// extraDestinations lists the [[destinations]] entries to dial beside the
// primary one, in config order and without duplicates.
//
// `quil --remote <host>` gets NONE of them: that mode means "drive that
// machine", and quietly attaching the configured extras to it would make one
// flag mean two different things — including spawning ssh connections the user
// did not ask for from a session they ran to isolate one host.
func extraDestinations(cfg config.Config, primary string) []config.Destination {
	if remoteMode() {
		return nil
	}
	seen := map[string]bool{primary: true}
	var out []config.Destination
	for _, d := range cfg.Destinations {
		if d.Dest == "" {
			log.Printf("config: ignoring a [[destinations]] entry with no dest")
			continue
		}
		if seen[d.Dest] {
			log.Printf("config: ignoring duplicate destination %q", d.Dest)
			continue
		}
		seen[d.Dest] = true
		out = append(out, d)
	}
	return out
}

// dialExtra builds the dial closure for one BACKGROUND destination.
//
// Batch, unlike the primary startup dial, so ssh cannot prompt. That is what
// makes the concurrent dial above safe, and it is also the honest contract for
// a host the user did not name on the command line: a first-time host key or a
// passphrase belongs to `quil remote setup <dest>` or one manual ssh, not to a
// prompt that appears while the client is opening.
func dialExtra(cfg config.Config, d config.Destination) func() (tui.Client, error) {
	return func() (tui.Client, error) {
		ctx, cancel := context.WithTimeout(context.Background(), extraDialTimeout)
		defer cancel()

		// A framer of its own, because sshStderrLogger carries a per-session
		// byte budget: sharing one would let a single flapping host exhaust the
		// log allowance of every other destination.
		sink := &sshStderrLogger{dest: d.Dest}
		client, link, err := dialRemoteTransportFn(ctx, cfg, d.Dest, true, sink)
		if err != nil {
			return nil, err
		}
		if client == nil {
			// A nil *ipc.Client returned into the interface is NOT == nil, so
			// this is the last place it can be caught before Receive panics.
			return nil, errors.New("ssh dial returned no connection")
		}
		if gateErr := gateExtraVersion(d, client, link); gateErr != nil {
			client.Close()
			return nil, classifyDialFailure(link, gateErr)
		}
		return client, nil
	}
}

// classifyDialFailure marks a failure that means "quil is not installed over
// there", so the caller can offer to provision the host instead of only
// reporting it unreachable.
//
// The decision is the ssh child's EXIT CODE, never its message: ssh answers
// 255 for every failure of its own, while the remote shell's codes pass
// through untouched — 127 for a command it could not find. The message is
// locale-dependent AND remote-influenced, since ssh multiplexes the remote
// command's stderr onto its own, so an ordinary ~/.bashrc could otherwise
// talk this client into an install offer.
//
// Read AFTER Close, which is what reaps the child and makes the status final
// — the mirror of LinkErr, which Close can clear. dialExtra closes the client
// on the line above.
func classifyDialFailure(link transport.LinkStatus, err error) error {
	if link == nil {
		// A dial that never produced a link failed before ssh ran at all, so
		// there is no remote status to classify.
		return err
	}
	if remoteinstall.ClassifyExit(link.ExitCode(), link.Established()) == remoteinstall.RemedyInstall {
		return fmt.Errorf("%w: %v", tui.ErrRemoteQuilMissing, err)
	}
	return err
}

// gateExtraVersion refuses a background destination whose daemon this client
// cannot talk to — with NONE of gateVersionCheck's interactive remedies.
//
// Those belong to the primary destination alone, because it is the one the user
// asked for: it may offer an install, restart the daemon, or exit the process.
// A background host must do none of that. A version mismatch on a machine that
// is not even on screen has to be a log line and a dropped connection, not a
// blocking dialog, and certainly not an exit that takes the whole client down
// over a daemon nobody was looking at.
//
// A refused destination lands in dialAllWith's "unreachable at launch" branch,
// which is the same outcome as the host being off — deliberately, since from
// the client's point of view an unusable daemon and an absent one are the same
// thing.
func gateExtraVersion(d config.Destination, client *ipc.Client, link transport.LinkStatus) error {
	res := versionHandshake(client)
	switch {
	case res.ClientSkipped, res.Matched:
		return nil

	case res.DaemonUnknown:
		// This is where an unreachable host actually surfaces. The dial proves
		// only that ssh's BINARY started — exec.Cmd.Start succeeds long before
		// ssh resolves the host or authenticates — so a dead link cannot fail
		// the dial and instead shows up as "no version came back".
		//
		// LinkErr is read BEFORE the caller's Close, which is the ordering the
		// version gate documents and pins: Close unblocks the transport pump,
		// and that return path can complete without ever setting pumpErr, so a
		// read afterwards comes back nil and loses ssh's own words.
		if link != nil {
			if le := link.LinkErr(); le != nil {
				return le
			}
		}
		return fmt.Errorf("no version response from %s", d.Label())

	case res.Cmp < 0:
		// The remote daemon is NEWER. Provisioning pushes THIS client's build,
		// so acting on it would DOWNGRADE a machine other clients may share —
		// and would not fix this session either, since the far side would then
		// respawn every pane at a version the user did not ask for. The only
		// remedy that works is on this end, so name it instead of a command
		// that would make things worse.
		return fmt.Errorf("%s runs %s, this client runs %s; upgrade this client from %s",
			d.Label(), res.DaemonVersion, versionpkg.Current(), releasesURL)

	default:
		// This client is newer, so the fix is on the far side and Quil can
		// perform it — the same push `quil remote setup` does, daemon stop
		// included. The sentinel is the only thing that carries "upgradable"
		// across the package boundary: internal/tui cannot name a remedy, and
		// the exit code cannot report one, because quil RAN over there and an
		// established link makes ClassifyExit answer RemedyNone for every code.
		//
		// The command stays in the message for the callers that do not act on
		// the sentinel: a background destination at launch is dropped with a
		// log line, deliberately, since installing software on another machine
		// must not be a side effect of opening the client.
		return fmt.Errorf("%w: %s runs %s, this client runs %s; run `quil remote setup %s`",
			tui.ErrRemoteVersionMismatch, d.Label(), res.DaemonVersion, versionpkg.Current(), d.Dest)
	}
}

// setupLogWriter turns the remote installer's narration into log lines while
// the TUI owns the terminal.
//
// Line-framed rather than a raw pass-through to the log writer: the installer
// writes partial lines and trailing newlines, and handing those straight to
// log.Printf would produce records that start mid-sentence or carry a blank
// line — in a file the F1 viewer renders. Quoting each line also keeps a
// message that came from the REMOTE host (ssh multiplexes its stderr onto its
// own) from forging something that looks like a real slog record.
type setupLogWriter struct {
	dest string
	buf  []byte
}

func (w *setupLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(w.buf[:i]); len(line) > 0 {
			log.Printf("remote setup %s: %q", w.dest, line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
