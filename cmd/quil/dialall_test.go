package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// stubClient is a tui.Client that records what it was sent. cmd/quil is package
// main and cannot import internal/tui's test fakes — Go does not export test
// code across packages — so this is a local one.
type stubClient struct{ sent []*ipc.Message }

func (s *stubClient) Send(m *ipc.Message) error { s.sent = append(s.sent, m); return nil }

// Receive blocks forever. Nothing here reads from a connection, and returning
// an error instead would make a defect look like a healthy conn reporting a
// drop; a test that reaches this hangs and says so.
func (s *stubClient) Receive() (*ipc.Message, error) { select {} }

// An unreachable host must not stop the client starting. Its projects are only
// part of the workspace, and a laptop whose work VM is powered off still has to
// open — with the local daemon connected. The failing destination still reports
// an outcome: an absent entry is what the old code did and is what made a host
// vanish from the sidebar.
func TestDialAllKeepsGoingWhenOneDestFails(t *testing.T) {
	dials := map[string]func() (tui.Client, error){
		"":      func() (tui.Client, error) { return &stubClient{}, nil },
		"gpu01": func() (tui.Client, error) { return nil, errors.New("ssh: connection refused") },
	}

	out := dialAllWith(dials)

	if len(out) != 2 {
		t.Fatalf("outcomes = %d, want 2 — a failed dial still reports", len(out))
	}
	if out[""].client == nil || out[""].err != nil {
		t.Fatalf("local = %+v, want the local daemon still connected with no error", out[""])
	}
	if out["gpu01"].client != nil {
		t.Errorf("gpu01 = %+v, want no client for an unreachable host", out["gpu01"])
	}
	if out["gpu01"].err == nil {
		t.Error("gpu01 err = nil, want the dial failure reported")
	}
}

// A dialer that reports success with no connection must be reported as a
// FAILURE rather than registered as a client. Go's typed-nil trap makes it
// reachable: a nil *ipc.Client returned into the interface is not == nil, and
// the router's pump would dereference it. The entry must still exist — a
// silently dropped entry is indistinguishable from a host nobody tried.
func TestDialAllDropsANilConnection(t *testing.T) {
	out := dialAllWith(map[string]func() (tui.Client, error){
		"gpu01": func() (tui.Client, error) { return nil, nil },
	})
	if out["gpu01"].client != nil {
		t.Errorf("gpu01 client = %v, want nil — a nil connection was registered as live", out["gpu01"].client)
	}
	if out["gpu01"].err == nil {
		t.Error("gpu01 err = nil, want a (nil, nil) dial reported as a failure")
	}
}

// The error is the whole point of the change: without it the caller cannot
// tell a host that is off from one that needs upgrading.
func TestDialAllWith_ReportsTheFailureNotJustTheAbsence(t *testing.T) {
	boom := errors.New("ssh: connect: no route to host")
	out := dialAllWith(map[string]func() (tui.Client, error){
		"up":   func() (tui.Client, error) { return &stubClient{}, nil },
		"down": func() (tui.Client, error) { return nil, boom },
	})

	if len(out) != 2 {
		t.Fatalf("outcomes = %d, want 2 — a failed dial still reports", len(out))
	}
	if out["up"].client == nil || out["up"].err != nil {
		t.Errorf("up = %+v, want a client and no error", out["up"])
	}
	if out["down"].client != nil {
		t.Error("down returned a client")
	}
	if !errors.Is(out["down"].err, boom) {
		t.Errorf("down err = %v, want %v", out["down"].err, boom)
	}
}

// A dialer returning (nil, nil) must not read as success: Receive would panic.
func TestDialAllWith_NilClientIsAFailure(t *testing.T) {
	out := dialAllWith(map[string]func() (tui.Client, error){
		"weird": func() (tui.Client, error) { return nil, nil },
	})
	if out["weird"].err == nil {
		t.Error("a nil client with no error was reported as a success")
	}
}

func TestClassifyOfflineKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want tui.OfflineKind
	}{
		{"missing binary", fmt.Errorf("%w: exit 127", tui.ErrRemoteQuilMissing), tui.OfflineNeedsInstall},
		{"version drift", fmt.Errorf("%w: runs 1.53.0", tui.ErrRemoteVersionMismatch), tui.OfflineNeedsUpgrade},
		{"host off", errors.New("ssh: connect: no route to host"), tui.OfflineRetrying},
		{"no version response", errors.New("no version response from gpu01"), tui.OfflineRetrying},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyOfflineKind(tt.err); got != tt.want {
				t.Errorf("classifyOfflineKind(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// Attach must NOT happen at dial time. AttachPayload carries Cols/Rows and the
// daemon sizes the first PTY from them, but the terminal size is unknown until
// the first WindowSizeMsg — a fresh daemon would spawn its Shell pane at 0×0.
// The Model also already owns attach, twice over, and a third owner would
// attach every connection twice, replaying each ghost buffer twice with it.
func TestDialAllDoesNotAttach(t *testing.T) {
	local := &stubClient{}
	dialAllWith(map[string]func() (tui.Client, error){
		"": func() (tui.Client, error) { return local, nil },
	})

	for _, msg := range local.sent {
		if msg.Type == ipc.MsgAttach {
			t.Fatal("attach must NOT happen at dial time: AttachPayload carries " +
				"Cols/Rows and the terminal size is unknown until WindowSizeMsg, " +
				"and the Model already owns attach")
		}
	}
}

// `quil --remote <host>` means "drive that machine". Attaching the configured
// background destinations to it as well would make one flag mean two things,
// and would open ssh connections the user did not ask for from the session they
// ran precisely to isolate one host.
func TestExtraDestinationsAreSkippedUnderRemoteMode(t *testing.T) {
	cfg := config.Config{Destinations: []config.Destination{{Dest: "prod"}}}

	if got := extraDestinations(cfg, ""); len(got) != 1 {
		t.Fatalf("local session got %d destinations, want 1", len(got))
	}

	withRemote(t, "gpu01")
	if got := extraDestinations(cfg, "gpu01"); got != nil {
		t.Fatalf("--remote session got %v, want none", got)
	}
}

// The primary destination is already in the connection table, so a config entry
// naming it again would install a SECOND connection to the same daemon under
// the same routing key — two pumps racing one socket.
func TestExtraDestinationsSkipsThePrimaryAndDuplicates(t *testing.T) {
	cfg := config.Config{Destinations: []config.Destination{
		{Dest: "prod"},
		{Dest: "prod"},
		{Dest: ""},
	}}

	got := extraDestinations(cfg, "prod")
	if len(got) != 0 {
		t.Fatalf("got %v, want none: the primary, its duplicate and an empty dest are all skipped", got)
	}
}

// Label is what the "unreachable at launch" warning prints, so it must never be
// empty — an ssh destination is often user@10.0.0.4 and a config that names it
// should be able to say something friendlier.
func TestDestinationLabelFallsBackToDest(t *testing.T) {
	if got := (config.Destination{Dest: "user@10.0.0.4"}).Label(); got != "user@10.0.0.4" {
		t.Errorf("Label = %q, want the dest when no name is set", got)
	}
	if got := (config.Destination{Name: "gpu box", Dest: "user@10.0.0.4"}).Label(); got != "gpu box" {
		t.Errorf("Label = %q, want the configured name", got)
	}
}

// asReleaseBuild makes versionHandshake actually negotiate for one test.
//
// It skips outright on a non-release build, which every `go test` run is — so
// without this the interesting arms of gateExtraVersion are unreachable and a
// test aimed at them passes while asserting nothing. That skip is also why a
// dev build admits a background daemon this gate would refuse; see the report's
// note on it.
func asReleaseBuild(t *testing.T, v string) {
	t.Helper()
	prev := versionpkg.Current()
	versionpkg.SetCurrent(v)
	t.Cleanup(func() { versionpkg.SetCurrent(prev) })
}

// TestDialExtra_ReadsLinkErrBeforeClose pins an ordering that this refactor
// SPLIT ACROSS TWO FUNCTIONS, which is what makes it fragile.
//
// gateExtraVersion reads LinkErr; dialExtra closes the connection afterwards.
// The primary path keeps both in one function and pins them with
// TestGateVersionCheck_ReadsExitCodeAfterClose. Here a refactor that moved the
// Close inside gateExtraVersion — or simply closed before returning the error —
// would look tidier and silently lose ssh's own words on every background host:
// Close unblocks the transport's pump, and that return path can complete
// without ever setting pumpErr, so a later read comes back nil.
//
// Close is in the trace deliberately. Recording only the LinkErr read would
// pass unchanged for exactly the regression this guards.
func TestDialExtra_ReadsLinkErrBeforeClose(t *testing.T) {
	asReleaseBuild(t, "1.0.0")

	var order []string
	// A client over an already-closed pipe, so the version handshake fails on
	// its first write and the DaemonUnknown arm is reached with no fake daemon.
	client := clientRecordingClose(t, &order)
	link := recordingLink{
		fakeLink: fakeLink{err: errors.New("ssh: Could not resolve hostname gpu01")},
		order:    &order,
	}
	stubDial(t, func(context.Context, config.Config, string, bool, io.Writer) (*ipc.Client, transport.LinkStatus, error) {
		return client, link, nil
	})

	conn, err := dialExtra(config.Config{}, config.Destination{Dest: "gpu01"})()
	if err == nil {
		t.Fatal("dialExtra admitted a destination whose daemon never answered")
	}
	if conn != nil {
		t.Error("dialExtra returned a connection alongside an error; the router would register a dead conn")
	}
	if !strings.Contains(err.Error(), "Could not resolve hostname") {
		t.Errorf("error = %v, want ssh's own words — LinkErr is the only thing that "+
			"distinguishes an unreachable host from a daemon that answered badly", err)
	}

	// The two reads bracket Close in OPPOSITE directions, and each is wrong on
	// the other side of it: Close can clear LinkErr, so reading it afterwards
	// loses the diagnostic — while Close is what REAPS the ssh child, so the
	// exit status is not final until it has run. Asserted as positions rather
	// than an exact sequence, so adding a read on the correct side of Close
	// does not fail a test that is about ordering.
	closeAt := slices.Index(order, "close")
	if closeAt < 0 {
		t.Fatalf("order = %v — the client was never closed", order)
	}
	if i := slices.Index(order, "linkerr"); i < 0 || i > closeAt {
		t.Errorf("order = %v — LinkErr must be read BEFORE Close, which can clear it", order)
	}
	if i := slices.Index(order, "exitcode"); i >= 0 && i < closeAt {
		t.Errorf("order = %v — ExitCode must be read AFTER Close, which reaps the child "+
			"and makes the status final", order)
	}
}

// A background host whose daemon runs a different version is REFUSED, and
// refused quietly.
//
// gateVersionCheck would open a blocking dialog, offer a remote install,
// restart a daemon or exit the process — every one of which is wrong for a
// machine that is not on screen, and the last would take the whole client down
// over a daemon nobody was looking at. It is also unusable here by
// construction: it reads process-globals that only the primary dial populates.
//
// The refusal has to name the version and the remedy, because it is the only
// thing the user will see about a host that silently vanished from the sidebar.
func TestGateExtraVersion_RefusesAMismatchedDaemonWithoutInteracting(t *testing.T) {
	asReleaseBuild(t, "1.0.0")

	var order []string
	client := clientRecordingClose(t, &order)

	err := gateExtraVersion(config.Destination{Name: "gpu box", Dest: "gpu01"}, client, nil)
	if err == nil {
		t.Fatal("a daemon that never answered was admitted")
	}
	if !strings.Contains(err.Error(), "gpu box") {
		t.Errorf("error = %v, want it to name the destination — it is the only signal the "+
			"user gets about a host missing from the sidebar", err)
	}
	if len(order) != 0 {
		t.Errorf("gateExtraVersion closed or probed the connection itself (%v); dialExtra "+
			"owns the Close, and the split is what the ordering test above pins", order)
	}
}

// The control arm: a matching daemon must be admitted, or the refusal above is
// indistinguishable from a gate that rejects everything.
func TestGateExtraVersion_AdmitsAMatchingDaemon(t *testing.T) {
	asReleaseBuild(t, "1.0.0")

	client, peer := clientOverPipe(t)
	versionResponder(t, peer, "1.0.0")

	if err := gateExtraVersion(config.Destination{Dest: "gpu01"}, client, nil); err != nil {
		t.Fatalf("a version-matched daemon was refused: %v", err)
	}
}

// A daemon OLDER than this client is a mismatch Quil can fix on its own, and
// the sentinel is the only thing that says so.
//
// It cannot be derived the way "not installed" is: quil RAN over there, so the
// link delivered bytes and ClassifyExit's established override answers
// RemedyNone for whatever exit code follows. Without the wrap, connecting a
// host from the New Project dialog printed the mismatch and a command to run
// somewhere else — reported as "I expected it to just install it".
func TestGateExtraVersion_MarksAnOlderDaemonAsUpgradable(t *testing.T) {
	asReleaseBuild(t, "1.47.0")

	client, peer := clientOverPipe(t)
	versionResponder(t, peer, "1.46.3")

	err := gateExtraVersion(config.Destination{Dest: "gpu01"}, client, nil)
	if err == nil {
		t.Fatal("a daemon running a different version was admitted")
	}
	if !errors.Is(err, tui.ErrRemoteVersionMismatch) {
		t.Errorf("error = %v, want it wrapped in ErrRemoteVersionMismatch — without the "+
			"sentinel the dialog can only print the failure and name a CLI command", err)
	}
	if !strings.Contains(err.Error(), "1.46.3") || !strings.Contains(err.Error(), "1.47.0") {
		t.Errorf("error = %v, want both versions named", err)
	}
}

// A daemon NEWER than this client must NOT be marked upgradable.
//
// The install pushes THIS client's build, so acting on the sentinel there
// downgrades a host other clients may share — and does not fix this session
// either. The remedy is on this end, and the message has to say so, because it
// is the only thing the user sees.
func TestGateExtraVersion_NeverOffersToDowngradeANewerDaemon(t *testing.T) {
	asReleaseBuild(t, "1.46.3")

	client, peer := clientOverPipe(t)
	versionResponder(t, peer, "1.47.0")

	err := gateExtraVersion(config.Destination{Dest: "gpu01"}, client, nil)
	if err == nil {
		t.Fatal("a daemon running a different version was admitted")
	}
	if errors.Is(err, tui.ErrRemoteVersionMismatch) {
		t.Error("a NEWER remote daemon was marked upgradable; provisioning would push this " +
			"client's older build over it and respawn every pane there")
	}
	if !strings.Contains(err.Error(), releasesURL) {
		t.Errorf("error = %v, want the client upgrade named — it is the only remedy that works", err)
	}
}

// versionResponder answers ONE version handshake with the given version,
// echoing the request's own ID.
//
// fakeDaemon in remote_verify_test.go cannot stand in: it replies with
// probeRequestID, which is verifyRemoteLink's constant, while versionHandshake
// generates a fresh per-request ID and loops past anything that does not match
// — so that helper would leave this test waiting out the handshake deadline and
// then asserting against DaemonUnknown, i.e. the arm it is the control for.
func versionResponder(t *testing.T, peer net.Conn, version string) {
	t.Helper()
	go func() {
		var length uint32
		if err := binary.Read(peer, binary.BigEndian, &length); err != nil {
			return
		}
		raw := make([]byte, length)
		if _, err := io.ReadFull(peer, raw); err != nil {
			return
		}
		var req ipc.Message
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("versionResponder: decode request: %v", err)
			return
		}

		resp, err := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: version})
		if err != nil {
			t.Errorf("versionResponder: build reply: %v", err)
			return
		}
		resp.ID = req.ID
		out, err := json.Marshal(resp)
		if err != nil {
			t.Errorf("versionResponder: encode reply: %v", err)
			return
		}
		if err := binary.Write(peer, binary.BigEndian, uint32(len(out))); err != nil {
			return
		}
		peer.Write(out)
	}()
}

// The remote installer narrates to a writer so the TUI can keep it off the
// terminal it is drawing on. Line-framing matters: the installer emits partial
// lines and trailing newlines, and passing those straight to the log produces
// records that start mid-sentence — in a file the F1 viewer renders.
func TestSetupLogWriter_FramesLinesAndDropsBlanks(t *testing.T) {
	var lines []string
	orig := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		lines = append(lines, string(p))
		return len(p), nil
	}))
	t.Cleanup(func() { log.SetOutput(orig) })

	w := &setupLogWriter{dest: "gpu01"}
	// Split mid-line across writes, exactly as an ssh stream arrives.
	w.Write([]byte("Checking gpu"))
	w.Write([]byte("01…\n\n"))
	w.Write([]byte("Installing to /home/a/.local/bin…\n"))
	w.Write([]byte("no newline yet"))

	if len(lines) != 2 {
		t.Fatalf("logged %d records, want 2 (the blank is dropped, the unterminated tail waits): %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Checking gpu01") {
		t.Errorf("record 0 = %q, want the line reassembled across writes", lines[0])
	}
	if !strings.Contains(lines[0], "gpu01") || !strings.Contains(lines[0], "\"") {
		t.Errorf("record 0 = %q, want the destination named and the text quoted", lines[0])
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
