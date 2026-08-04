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
	// Read-modify-write, NOT a save of m.cfg. m.cfg is the launch-time
	// snapshot, and the install that usually precedes this recorded the remote
	// binary path into the same file moments ago — saving the snapshot whole
	// would erase it and put the host back to offering an install it has
	// already done. The in-memory append above still happens, because the
	// idempotency check above reads it.
	if err := config.Mutate(config.ConfigPath(), func(c *config.Config) {
		for _, d := range c.Destinations {
			if d.Dest == dest {
				return
			}
		}
		c.Destinations = append(c.Destinations, config.Destination{Dest: dest})
	}); err != nil {
		log.Printf("connect %s: could not record it in config: %v", dest, err)
	}
}

// ErrRemoteQuilMissing marks a dial that reached the host but found no quil
// there. cmd/quil classifies it from the ssh child's EXIT CODE — 127 for a
// command the remote shell could not find — never from the message, which is
// locale-dependent and is also a string any shell can emit for its own
// reasons. This package only has to recognise the wrapped sentinel.
var ErrRemoteQuilMissing = errors.New("quil is not installed on that host")

// ErrRemoteVersionMismatch marks a dial that found a WORKING quil on the host
// running a version this client refuses to attach to.
//
// It cannot be derived from the exit code the way ErrRemoteQuilMissing is, and
// that is the whole reason it exists: quil ran over there, so the link
// delivered bytes, so ClassifyExit's established override answers RemedyNone
// for every code that follows. cmd/quil raises it from the version handshake
// instead — and ONLY when this client is the newer of the two, because
// provisioning pushes this client's own build and would otherwise downgrade a
// daemon other clients may share.
var ErrRemoteVersionMismatch = errors.New("the daemon on that host runs a different version")

// installOffer decides whether a failed dial is something provisioning can fix,
// and returns what to tell the user while it runs. "" means it cannot.
//
// One function rather than a condition per sentinel because the two failures
// share every mechanic — the same InstallFunc, the same once-per-host guard,
// the same retry dial — and differ only in the sentence. Splitting them is how
// the second one ends up with the first one's guard and reinstalls forever.
func installOffer(err error, dest string) string {
	switch {
	case errors.Is(err, ErrRemoteQuilMissing):
		return "quil is not installed on " + sanitizeRemoteText(dest) + " — installing…"
	case errors.Is(err, ErrRemoteVersionMismatch):
		// Names the daemon restart, because an upgrade is not free the way a
		// first install is: panes over there respawn from the saved workspace
		// and whatever was running in their shells is killed. The CLI prompt
		// says the same thing before asking; here the user has already asked
		// for the connection, so it is a statement rather than a question.
		return "upgrading quil on " + sanitizeRemoteText(dest) + " — its daemon restarts…"
	}
	return ""
}

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

// disconnectDest detaches a host: its projects leave the sidebar, its
// connection is released, and it stops being dialled at launch.
//
// Nothing on the far side is destroyed. The remote daemon keeps running with
// every pane alive, which is the whole difference from destroying a project —
// this is "stop showing me that machine", and reconnecting later restores the
// same workspace. That is also why it does not touch the daemon at all: no
// message is sent, because there is nothing for it to do.
//
// Order matters. The router entry goes first so no in-flight receive can
// deliver for a destination the Model has already forgotten; the reconnect
// dialer goes with it, because canReconnect is `redialFns[dest] != nil` and a
// leftover one would have the ladder redial a host the user just dismissed.
func (m *Model) disconnectDest(dest string) {
	if dest == "" {
		return // the local daemon is not disconnectable; its panes died with it
	}
	var conn Client
	if r, ok := m.client.(*Router); ok {
		conn = r.Conn(dest)
		r.Remove(dest)
	}
	delete(m.redialFns, dest)
	delete(m.attached, dest)
	delete(m.links, dest)
	// Every other per-destination table goes with them. These two are read
	// through the ACTIVE dest today, so a leftover entry is unreachable rather
	// than wrong — but they are the same class of key as the three above, and
	// the day something iterates one of these maps instead of indexing it, a
	// disconnected host reappears in whatever it drives.
	delete(m.updateInfos, dest)
	delete(m.installedDests, dest)

	// Drop its projects, and keep the active index inside the survivors —
	// removing a project before the active one shifts every later index down.
	activeID := ""
	if p := m.cur(); p != nil {
		activeID = p.ID
	}
	kept := make([]*ProjectModel, 0, len(m.projects))
	for _, p := range m.projects {
		if p.Dest != dest {
			kept = append(kept, p)
		}
	}
	m.projects = kept
	m.activeProject = 0
	for i, p := range m.projects {
		if p.ID == activeID {
			m.activeProject = i
			break
		}
	}
	m.syncActiveDest()

	// Released last, and through the seam: this package cannot close a Client
	// (Send/Receive only), and an ssh-backed conn holds a child process that
	// leaks for the session if nobody reaps it.
	if conn != nil && m.closeClientFn != nil {
		m.closeClientFn(conn)
	}
	m.forgetDestination(dest)
}

// forgetDestination drops the host from [[destinations]] so it is not dialled
// again at launch. Best effort, like persistDestination: a config that cannot
// be written is a log line, not a failed disconnect — the host is already gone
// from this session either way.
func (m *Model) forgetDestination(dest string) {
	kept := make([]config.Destination, 0, len(m.cfg.Destinations))
	for _, d := range m.cfg.Destinations {
		if d.Dest != dest {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(m.cfg.Destinations) {
		return
	}
	m.cfg.Destinations = kept
	// Read-modify-write for the same reason as persistDestination: a whole-file
	// save from the launch-time snapshot reverts everything written since.
	if err := config.Mutate(config.ConfigPath(), func(c *config.Config) {
		out := make([]config.Destination, 0, len(c.Destinations))
		for _, d := range c.Destinations {
			if d.Dest != dest {
				out = append(out, d)
			}
		}
		c.Destinations = out
	}); err != nil {
		log.Printf("disconnect %s: could not remove it from config: %v", dest, err)
	}
}
