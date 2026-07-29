package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// failingClient returns err from Receive forever.
type failingClient struct{ err error }

func (f *failingClient) Send(*ipc.Message) error        { return nil }
func (f *failingClient) Receive() (*ipc.Message, error) { return nil, f.err }

// closeTUIClient delivers exactly one MsgCloseTUI.
//
// A later call returns an error rather than blocking: listenForMessages reads
// once per invocation, so a second call only happens if the implementation is
// wrong, and an error fails the test where a block would hang it.
type closeTUIClient struct{ sent bool }

func (c *closeTUIClient) Send(*ipc.Message) error { return nil }
func (c *closeTUIClient) Receive() (*ipc.Message, error) {
	if !c.sent {
		c.sent = true
		return &ipc.Message{Type: ipc.MsgCloseTUI}, nil
	}
	return nil, errors.New("closeTUIClient read twice")
}

func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}

// A dead link in remote mode is a reconnectable event, not a quit.
func TestListenForMessages_RemoteLinkLoss_ReturnsLinkLostMsg(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}, clientGen: 3}
	m.SetRemoteDest("gpu01")

	msg := m.listenForMessages()()

	lost, ok := msg.(linkLostMsg)
	if !ok {
		t.Fatalf("msg is %T, want linkLostMsg", msg)
	}
	if lost.gen != 3 {
		t.Errorf("gen = %d, want 3", lost.gen)
	}
	if lost.err == nil {
		t.Error("linkLostMsg carries no cause; the banner has nothing to show")
	}
}

// MsgCloseTUI is the daemon asking us to exit. It must never reconnect.
func TestListenForMessages_CloseTUI_ReturnsQuit(t *testing.T) {
	m := Model{client: &closeTUIClient{}}
	m.SetRemoteDest("gpu01")

	if msg := m.listenForMessages()(); !isQuit(msg) {
		t.Fatalf("msg is %T, want tea.QuitMsg", msg)
	}
}

// Local mode keeps today's behaviour: a dead local daemon means dead panes, and
// quietly retrying would hide that.
func TestUpdate_LinkLost_LocalMode_Quits(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}}
	// no SetRemoteDest, no redial func

	_, cmd := m.Update(linkLostMsg{gen: 0, err: errors.New("EOF")})
	if cmd == nil {
		t.Fatal("cmd is nil, want tea.Quit")
	}
	if !isQuit(cmd()) {
		t.Fatal("local link loss did not quit")
	}
}

// Remote mode with a dialer installed enters the reconnecting state instead of
// quitting. This is the behaviour the whole phase is built on.
func TestUpdate_LinkLost_RemoteMode_BeginsReconnect(t *testing.T) {
	m := Model{clientGen: 1}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("unused") })

	updated, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	got := updated.(Model)

	if !got.reconnect.active {
		t.Fatal("remote link loss did not begin a reconnect")
	}
	if got.reconnect.lastErr == nil {
		t.Error("reconnect state kept no cause")
	}
}

// Remote mode with no dialer installed is still fatal — there is nothing to
// retry with, and pretending otherwise would hang the session forever.
func TestUpdate_LinkLost_RemoteWithoutDialer_Quits(t *testing.T) {
	m := Model{}
	m.SetRemoteDest("gpu01")

	_, cmd := m.Update(linkLostMsg{gen: 0, err: errors.New("EOF")})
	if cmd == nil || !isQuit(cmd()) {
		t.Fatal("remote link loss without a dialer did not quit")
	}
}

// A link-loss report from a previous client must be ignored: the old listen
// loop is still parked in Receive when the new client is already live.
func TestUpdate_LinkLost_StaleGeneration_Ignored(t *testing.T) {
	m := Model{clientGen: 5}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(Client) (Client, error) { return nil, errors.New("must not dial") })

	updated, cmd := m.Update(linkLostMsg{gen: 4, err: errors.New("stale")})
	got := updated.(Model)

	if got.reconnect.active {
		t.Error("a stale generation started a reconnect")
	}
	if cmd != nil {
		t.Error("a stale generation produced a command")
	}
}
