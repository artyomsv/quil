package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHelloPayload_TokenFieldsOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(HelloPayload{Kind: "tui", Proto: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"token_id"`, `"nonce"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("unix-socket hello carries %s: %s", key, b)
		}
	}
	b, _ = json.Marshal(HelloPayload{Kind: "tui", Proto: 1, TokenID: "0a1b2c3d", Nonce: "n"})
	if !strings.Contains(string(b), `"token_id":"0a1b2c3d"`) || !strings.Contains(string(b), `"nonce":"n"`) {
		t.Errorf("login hello lost its fields: %s", b)
	}
}

func TestHelloResp_RightsOnlyWhenSet(t *testing.T) {
	b, _ := json.Marshal(HelloRespPayload{Version: "1"})
	for _, key := range []string{`"rights"`, `"token_name"`, `"server_sig"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("local hello_resp carries %s: %s", key, b)
		}
	}
	b, _ = json.Marshal(HelloRespPayload{Rights: RightsReadOnly, TokenName: "laptop", ServerSig: "s"})
	for _, want := range []string{`"rights":"read-only"`, `"token_name":"laptop"`, `"server_sig":"s"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

func TestCreatePayloads_CarryNamedSelections(t *testing.T) {
	b, _ := json.Marshal(CreatePanePayload{Toggles: []string{"readonly"}, KubeContext: "prod"})
	if !strings.Contains(string(b), `"toggles":["readonly"]`) || !strings.Contains(string(b), `"kube_context":"prod"`) {
		t.Errorf("create_pane: %s", b)
	}
	b, _ = json.Marshal(FirstPaneSpec{Toggles: []string{"chrome"}, KubeContext: "k"})
	if !strings.Contains(string(b), `"toggles":["chrome"]`) || !strings.Contains(string(b), `"kube_context":"k"`) {
		t.Errorf("first_pane: %s", b)
	}
	b, _ = json.Marshal(CreatePanePayload{})
	if strings.Contains(string(b), "toggles") || strings.Contains(string(b), "kube_context") {
		t.Errorf("empty selections reached the wire: %s", b)
	}
}

// failReader fails the test if anything reads past the length prefix.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("ReadMessageLimit read the payload of a frame over its limit")
	return 0, io.EOF
}

func TestReadMessageLimit_RefusesBeforeReading(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10<<20)
	_, err := ReadMessageLimit(io.MultiReader(bytes.NewReader(hdr[:]), failReader{t}), 4<<10)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadMessageLimit_AcceptsWithinLimit(t *testing.T) {
	frame, err := EncodeFrame(&Message{Type: MsgHello, ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ReadMessageLimit(bytes.NewReader(frame), 4<<10)
	if err != nil || msg.Type != MsgHello {
		t.Fatalf("msg=%v err=%v", msg, err)
	}
}

func pipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	c, err := NewClientWithDialer(context.Background(), func(context.Context) (net.Conn, error) { return clientSide, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); serverSide.Close() })
	return c, serverSide
}

func TestReceiveByID_SkipsOtherIDs(t *testing.T) {
	c, srv := pipeClient(t)
	go func() {
		_ = WriteMessage(srv, &Message{Type: MsgError, ID: "other"})
		_ = WriteMessage(srv, &Message{Type: MsgAuthChallenge, ID: "want"})
	}()
	msg, err := c.ReceiveByID("want", 2*time.Second)
	if err != nil || msg.Type != MsgAuthChallenge {
		t.Fatalf("msg=%v err=%v", msg, err)
	}
}

func TestReceiveByID_TimesOut(t *testing.T) {
	c, _ := pipeClient(t)
	start := time.Now()
	if _, err := c.ReceiveByID("never", 150*time.Millisecond); err == nil {
		t.Fatal("ReceiveByID returned no error with nothing written")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ReceiveByID ignored its timeout")
	}
}
