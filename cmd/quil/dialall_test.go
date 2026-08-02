package main

import (
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
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
// open — with the local daemon connected.
func TestDialAllKeepsGoingWhenOneDestFails(t *testing.T) {
	dials := map[string]func() (tui.Client, error){
		"":      func() (tui.Client, error) { return &stubClient{}, nil },
		"gpu01": func() (tui.Client, error) { return nil, errors.New("ssh: connection refused") },
	}

	conns := dialAllWith(dials)

	if len(conns) != 1 {
		t.Fatalf("conns = %d, want 1 — an unreachable host must not block startup", len(conns))
	}
	if _, ok := conns[""]; !ok {
		t.Fatal("the local daemon should still be connected")
	}
}

// A dialer that reports success with no connection must be dropped rather than
// registered. Go's typed-nil trap makes it reachable: a nil *ipc.Client returned
// into the interface is not == nil, and the router's pump would dereference it.
func TestDialAllDropsANilConnection(t *testing.T) {
	conns := dialAllWith(map[string]func() (tui.Client, error){
		"gpu01": func() (tui.Client, error) { return nil, nil },
	})
	if len(conns) != 0 {
		t.Fatalf("conns = %v, want none — a nil connection was registered", conns)
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
