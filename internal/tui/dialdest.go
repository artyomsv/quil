package tui

import (
	"errors"
	"log"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
)

// Connecting a host WITHOUT relaunching.
//
// Every destination used to come from [[destinations]] and be dialled before
// the Model existed, which made "work on that box too" a config edit plus a
// restart — losing the session you were in the middle of. The pieces to do it
// live were already here: Router.Add installs a connection and starts its pump,
// attachAllDests is idempotent and skips what is already attached, and
// SetRedialFunc gives the new destination the same reconnect ladder every other
// remote has. What was missing is a dialer the Model can reach.

// DialFunc dials a destination that is not connected yet. cmd/quil supplies it
// for the same reason it supplies RedialFunc: the ssh transport lives there and
// this package cannot name it.
//
// It is a FACTORY over dest, not a per-destination closure like RedialFunc,
// because the whole point is dialling a host nobody has named before.
type DialFunc func(dest string) (Client, error)

// SetDialFunc installs the runtime dialer. A Model without one simply cannot
// connect new hosts — every test Model, and any future caller that has no ssh
// transport, keeps working with the destinations it was built with.
func (m *Model) SetDialFunc(f DialFunc) { m.dialDestFn = f }

// SetRedialFactory installs the builder for a newly connected destination's
// reconnect ladder. SetRedialFunc still handles the launch-time destinations
// one at a time; this covers the ones that did not exist yet.
func (m *Model) SetRedialFactory(f func(dest string) RedialFunc) { m.redialDestFn = f }

// destDialedMsg carries a runtime dial's outcome back to Update.
type destDialedMsg struct {
	dest   string
	client Client
	err    error
}

// dialDest connects a destination in the background and reports the result.
//
// Off the Update goroutine deliberately: the dial is an ssh connect, a daemon
// readiness wait and a version handshake, up to tens of seconds against a host
// that is simply switched off. Doing it inline would freeze every pane on
// screen — including the ones on daemons that are perfectly healthy.
func (m *Model) dialDest(dest string) tea.Cmd {
	if dest == "" || m.dialDestFn == nil {
		return nil
	}
	dial := m.dialDestFn
	return func() tea.Msg {
		c, err := dial(dest)
		return destDialedMsg{dest: dest, client: c, err: err}
	}
}

// destConnected reports whether a destination already has a connection, so a
// host that is merely being re-selected is not dialled a second time.
func (m *Model) destConnected(dest string) bool {
	if dest == "" {
		return true // the local daemon is always present
	}
	for _, d := range m.knownDests() {
		if d == dest {
			return true
		}
	}
	return false
}

// adoptDest installs a freshly dialled connection: routing, a reconnect ladder,
// and persistence so the host comes back on the next launch.
//
// Returns the attach command. Attach is NOT done here beyond that, and the
// ledger entry it writes is why: attachAllDests has to run on a Model that
// Update returns, or the entry lands on a copy and the destination is attached
// again on the next resize — replaying its whole ghost buffer a second time.
func (m *Model) adoptDest(dest string, c Client) tea.Cmd {
	r, ok := m.client.(*Router)
	if !ok || c == nil {
		return nil
	}
	r.Add(dest, c)
	// The same ladder every other remote destination has. Installed here rather
	// than at dial time because a dial that failed must not leave a dialer
	// behind: canReconnect is `redialFns[dest] != nil`, so it would make a
	// destination that never connected look reconnectable.
	if m.redialDestFn != nil {
		m.SetRedialFunc(dest, m.redialDestFn(dest))
	}
	m.persistDestination(dest)
	return m.attachAllDests()
}

// persistDestination appends the host to [[destinations]] so a session started
// tomorrow has it too. Best effort: a config that cannot be written is worth a
// log line, not a failed connection — the destination is live either way, and
// telling the user their host did not connect when it did would be worse.
//
// Idempotent by dest, which is also the routing key, so re-adding a host that
// is already configured cannot produce a duplicate entry that later dials
// twice.
func (m *Model) persistDestination(dest string) {
	if dest == "" {
		return
	}
	for _, d := range m.cfg.Destinations {
		if d.Dest == dest {
			return
		}
	}
	m.cfg.Destinations = append(m.cfg.Destinations, config.Destination{Dest: dest})
	if err := config.Save(config.ConfigPath(), m.cfg); err != nil {
		log.Printf("connect %s: could not record it in config: %v", dest, err)
	}
}

// ErrRemoteQuilMissing marks a dial that reached the host but found no quil
// there. cmd/quil classifies it from the ssh child's EXIT CODE — 127 for a
// command the remote shell could not find — never from the message, which is
// locale-dependent and is also a string any shell can emit for its own
// reasons. This package only has to recognise the wrapped sentinel.
var ErrRemoteQuilMissing = errors.New("quil is not installed on that host")

// InstallFunc provisions quil on a host, for the offer raised when a dial
// comes back ErrRemoteQuilMissing. Supplied by cmd/quil, which owns the
// release fetch and the ssh push.
type InstallFunc func(dest string) error

// SetInstallFunc installs the provisioner. A Model without one reports the
// missing binary and names the CLI instead of offering.
func (m *Model) SetInstallFunc(f InstallFunc) { m.installDestFn = f }

// destInstalledMsg carries a remote install's outcome back to Update.
type destInstalledMsg struct {
	dest string
	err  error
}

// installDest provisions a host in the background. Off the Update goroutine
// for the same reason as the dial, and more so: this downloads a release and
// streams it over ssh.
func (m *Model) installDest(dest string) tea.Cmd {
	if dest == "" || m.installDestFn == nil {
		return nil
	}
	install := m.installDestFn
	return func() tea.Msg {
		return destInstalledMsg{dest: dest, err: install(dest)}
	}
}
