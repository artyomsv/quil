package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
)

// mustNewToken mints a token for a fixture; a mint failure fails the test
// instead of handing it an empty token.
func mustNewToken(t *testing.T) string {
	t.Helper()
	tok, _, err := clientauth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestParseConnectFlags(t *testing.T) {
	addr, file, rest, err := parseConnectFlags([]string{"quil", "--connect", "7878", "--token-file", "/t", "x"})
	if err != nil || addr != "127.0.0.1:7878" || file != "/t" || len(rest) != 2 || rest[1] != "x" {
		t.Fatalf("got %q %q %v %v", addr, file, rest, err)
	}
	for _, bad := range [][]string{
		{"quil", "--connect"},
		{"quil", "--connect", "0.0.0.0:7878"},
		{"quil", "--token-file", "/t"},
		{"quil", "--connect=:7878"},
		{"quil", "--connect="},
	} {
		if _, _, _, err := parseConnectFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if addr, _, _, err := parseConnectFlags([]string{"quil", "--connect=localhost:7878"}); err != nil || addr != "127.0.0.1:7878" {
		t.Fatalf("--connect= form: %q %v", addr, err)
	}
}

func TestLoadConnectToken_TrimsWhitespace(t *testing.T) {
	tok := mustNewToken(t)
	path := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(path, []byte(tok+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadConnectToken(path, ""); err != nil || got != tok {
		t.Fatalf("got %q %v", got, err)
	}
	// ParseToken refuses a token with a newline in it, so the trim is what
	// keeps the file `echo $TOKEN > file` writes working.
	if _, err := clientauth.ParseToken(tok + "\n"); err == nil {
		t.Fatal("ParseToken accepted a trailing newline: this test no longer proves the trim")
	}
	lf := filepath.Join(t.TempDir(), "tok-lf")
	if err := os.WriteFile(lf, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadConnectToken(lf, ""); err != nil || got != tok {
		t.Fatalf("LF file: got %q %v", got, err)
	}
	if got, err := loadConnectToken("", "  "+tok+"\n"); err != nil || got != tok {
		t.Fatalf("env: got %q %v", got, err)
	}
	if _, err := loadConnectToken("", ""); err == nil {
		t.Fatal("no token accepted")
	}
	if _, err := loadConnectToken("", "qtk_bad"); err == nil {
		t.Fatal("malformed token accepted")
	}
}

// A token that fails to parse is still a secret the user meant to type: the
// error that is printed for it must not echo it back.
func TestLoadConnectToken_ErrorNeverEchoesTheToken(t *testing.T) {
	tok := mustNewToken(t)
	almost := tok + "x" // one character too long: refused, and still secret
	if _, err := loadConnectToken("", almost); err == nil {
		t.Fatal("malformed token accepted")
	} else if strings.Contains(err.Error(), almost) || strings.Contains(err.Error(), tok[len("qtk_")+9:]) {
		t.Fatalf("error carries the token: %v", err)
	}
}

func TestTakeTokenEnv_Unsets(t *testing.T) {
	t.Setenv("QUIL_TOKEN", "qtk_x")
	if got := takeTokenEnv(); got != "qtk_x" {
		t.Fatalf("got %q", got)
	}
	if _, ok := os.LookupEnv("QUIL_TOKEN"); ok {
		t.Fatal("QUIL_TOKEN still in the environment children inherit")
	}
}

// A failed unset is logged — children would inherit the token — naming the
// variable and never its value, and the token is still returned.
func TestTakeTokenEnv_UnsetFailureIsLoggedWithoutTheValue(t *testing.T) {
	logged := captureLog(t)
	const secret = "qtk_0a1b2c3d_secretvalue"
	t.Setenv("QUIL_TOKEN", secret)
	prev := unsetenvFn
	unsetenvFn = func(string) error { return errors.New("environment is read-only") }
	t.Cleanup(func() { unsetenvFn = prev })
	if got := takeTokenEnv(); got != secret {
		t.Fatalf("got %q", got)
	}
	out := logged.String()
	if !strings.Contains(out, "QUIL_TOKEN") || !strings.Contains(out, "environment is read-only") {
		t.Fatalf("the failed unset was not logged: %q", out)
	}
	if strings.Contains(out, "secretvalue") {
		t.Fatalf("the log carries the token: %q", out)
	}
}

// withConnectState restores every package var applyConnectFlags writes.
func withConnectState(t *testing.T) {
	t.Helper()
	prevDest, prevAddr, prevTok, prevExit, prevArgs := remoteDest, connectAddr, connectToken, exitFn, os.Args
	t.Cleanup(func() {
		remoteDest, connectAddr, connectToken, exitFn, os.Args = prevDest, prevAddr, prevTok, prevExit, prevArgs
	})
	remoteDest, connectAddr, connectToken = "", "", ""
}

// The refusals of commands that act on the local daemon name the flag the
// session was started with. Under --connect they never offer `ssh tcp:…`
// advice, which would try to resolve the address as a host; under --remote
// they keep it.
func TestRemoteRefusal_WordedForConnect(t *testing.T) {
	withConnectState(t)
	remoteDest, connectAddr = tcpDestPrefix+"127.0.0.1:7878", "127.0.0.1:7878"
	for name, msg := range map[string]string{
		"status":        remoteRefusal("status", true),
		"mcp":           mcpRemoteRefusal(),
		"sandbox login": sandboxLoginRefusal(),
		"remote setup":  remoteSetupRefusal(),
	} {
		if !strings.Contains(msg, "--connect") || strings.Contains(msg, "--remote") ||
			strings.Contains(msg, "ssh ") || strings.Contains(msg, tcpDestPrefix) {
			t.Errorf("%s under --connect: %q", name, msg)
		}
		if !strings.Contains(msg, "127.0.0.1:7878") {
			t.Errorf("%s under --connect does not name the address: %q", name, msg)
		}
	}

	remoteDest, connectAddr = "gpu01", ""
	if msg := remoteRefusal("status", true); !strings.Contains(msg, "--remote") || !strings.Contains(msg, "ssh gpu01 quil status") {
		t.Errorf("status under --remote lost its ssh advice: %q", msg)
	}
	if msg := mcpRemoteRefusal(); !strings.Contains(msg, "--remote gpu01") {
		t.Errorf("mcp under --remote: %q", msg)
	}
	for name, msg := range map[string]string{
		"sandbox login": sandboxLoginRefusal(),
		"remote setup":  remoteSetupRefusal(),
	} {
		if !strings.Contains(msg, "--remote") || !strings.Contains(msg, "gpu01") || strings.Contains(msg, "--connect") {
			t.Errorf("%s under --remote: %q", name, msg)
		}
	}
}

// --connect must arm every --remote guard: `quil clients` and `quil daemon`
// both act on the LOCAL socket, which under --connect is the wrong daemon.
func TestApplyConnectFlags_ArmsRemoteGuards(t *testing.T) {
	withConnectState(t)
	t.Setenv("QUIL_HOME", t.TempDir())
	tok := mustNewToken(t)

	rest, err := applyConnectFlags([]string{"quil", "--connect", "7878", "clients", "token", "list"}, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !connectMode() || !remoteMode() || remoteDest != tcpDestPrefix+"127.0.0.1:7878" || connectToken != tok {
		t.Fatalf("connect=%v remote=%v dest=%q", connectMode(), remoteMode(), remoteDest)
	}
	if got := strings.Join(rest, " "); got != "quil clients token list" {
		t.Fatalf("rest = %q", got)
	}
	stubTokenRequest(t, func(string, any) (*ipc.Message, error) {
		t.Error("asked a daemon for a token request under --connect")
		return nil, nil
	})

	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"clients", func() { handleClientsArgs(rest) }},
		{"daemon", func() { os.Args = []string{"quil", "daemon", "status"}; handleDaemon() }},
	} {
		code := -1
		exitFn = func(c int) { code = c; panic("exit") }
		func() {
			defer func() { _ = recover() }()
			tc.run()
		}()
		if code != 1 {
			t.Errorf("quil %s under --connect: exit code %d, want 1", tc.name, code)
		}
	}
}

func TestApplyConnectFlags_RefusesBeforeArming(t *testing.T) {
	withConnectState(t)
	tok := mustNewToken(t)

	if _, err := applyConnectFlags([]string{"quil", "--connect", "7878"}, ""); err == nil {
		t.Fatal("--connect with no token accepted")
	}
	if connectMode() || remoteMode() {
		t.Fatal("a refused --connect still armed the session")
	}

	remoteDest = "gpu01"
	if _, err := applyConnectFlags([]string{"quil", "--connect", "7878"}, tok); err == nil {
		t.Fatal("--connect together with --remote accepted")
	}

	remoteDest = ""
	args := []string{"quil", "status"}
	if rest, err := applyConnectFlags(args, tok); err != nil || !reflect.DeepEqual(rest, args) || connectMode() {
		t.Fatalf("no --connect: rest=%v err=%v connect=%v", rest, err, connectMode())
	}
}

func freePortAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestDialTCP_NoListener(t *testing.T) {
	tok := mustNewToken(t)
	_, _, err := dialTCP(context.Background(), freePortAddr(t), tok)
	if !errors.Is(err, errNoListener) {
		t.Fatalf("err = %v, want errNoListener", err)
	}
}

// fakeListener serves ONE conn with serve; done receives serve's verdict.
func fakeListener(t *testing.T, serve func(c net.Conn) error) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		done <- serve(c)
	}()
	return ln.Addr().String(), done
}

func challengeThen(c net.Conn, final func(hello *ipc.Message, hp ipc.HelloPayload, nonceS string) *ipc.Message) error {
	hello, err := ipc.ReadMessage(c)
	if err != nil {
		return err
	}
	var hp ipc.HelloPayload
	_ = hello.DecodePayload(&hp)
	nonceS, _ := clientauth.NewNonce()
	ch, _ := ipc.NewMessage(ipc.MsgAuthChallenge, ipc.AuthChallengePayload{Nonce: nonceS})
	ch.ID = hello.ID
	if err := ipc.WriteMessage(c, ch); err != nil {
		return err
	}
	if _, err := ipc.ReadMessage(c); err != nil { // the proof
		return err
	}
	return ipc.WriteMessage(c, final(hello, hp, nonceS))
}

// expectNothingMore is the listener's half of "the client hangs up and sends
// nothing more": the next read must fail, and not by timing out.
func expectNothingMore(c net.Conn, after string) error {
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	m, err := ipc.ReadMessage(c)
	if err == nil {
		return errors.New("the client sent " + m.Type + " after " + after)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("the client left the conn open after " + after)
	}
	return nil
}

// A listener that cannot sign gets nothing after the proof.
func TestDialTCP_UnprovenGetsNothingMore(t *testing.T) {
	tok := mustNewToken(t)
	squatter := mustNewToken(t)
	addr, done := fakeListener(t, func(c net.Conn) error {
		if err := challengeThen(c, func(h *ipc.Message, hp ipc.HelloPayload, nonceS string) *ipc.Message {
			am := clientauth.AuthMessage(hp.TokenID, hp.Nonce, nonceS)
			m, _ := ipc.NewMessage(ipc.MsgHelloResp, ipc.HelloRespPayload{Rights: "full",
				ServerSig: clientauth.ServerSignature(clientauth.DeriveVerifier(squatter), am)})
			m.ID = h.ID
			return m
		}); err != nil {
			return err
		}
		return expectNothingMore(c, "an unproven hello_resp")
	})
	if _, _, err := dialTCP(context.Background(), addr, tok); !errors.Is(err, clientauth.ErrServerUnproven) {
		t.Fatalf("err = %v, want ErrServerUnproven", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Every way the login can fail closes the conn — not only the unproven one.
func TestDialTCP_ClosesOnEveryLoginFailure(t *testing.T) {
	tok := mustNewToken(t)
	for _, tc := range []struct {
		name  string
		serve func(c net.Conn) error
	}{
		{"refused", func(c net.Conn) error {
			hello, err := ipc.ReadMessage(c)
			if err != nil {
				return err
			}
			m, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: "token refused"})
			m.ID = hello.ID
			if err := ipc.WriteMessage(c, m); err != nil {
				return err
			}
			return expectNothingMore(c, "a refusal")
		}},
		{"malformed challenge", func(c net.Conn) error {
			hello, err := ipc.ReadMessage(c)
			if err != nil {
				return err
			}
			m, _ := ipc.NewMessage(ipc.MsgAuthChallenge, ipc.AuthChallengePayload{Nonce: "short"})
			m.ID = hello.ID
			if err := ipc.WriteMessage(c, m); err != nil {
				return err
			}
			return expectNothingMore(c, "a malformed auth_challenge")
		}},
		{"unexpected type", func(c net.Conn) error {
			hello, err := ipc.ReadMessage(c)
			if err != nil {
				return err
			}
			m, _ := ipc.NewMessage(ipc.MsgHelloResp, ipc.HelloRespPayload{})
			m.ID = hello.ID
			if err := ipc.WriteMessage(c, m); err != nil {
				return err
			}
			return expectNothingMore(c, "a hello_resp with no challenge")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, done := fakeListener(t, tc.serve)
			if _, _, err := dialTCP(context.Background(), addr, tok); err == nil {
				t.Fatal("login succeeded")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDescribeConnectError(t *testing.T) {
	for err, want := range map[error]string{
		errNoListener: "no listener at 127.0.0.1:7878",
		&clientauth.RefusedError{Reason: "token refused"}: "token refused (wrong, expired or revoked)",
		clientauth.ErrServerUnproven:                      "the listener at 127.0.0.1:7878 could not prove it is your daemon",
	} {
		if got := describeConnectError("127.0.0.1:7878", err); got != want {
			t.Errorf("describe(%v) = %q, want %q", err, got, want)
		}
	}
}

// A refused re-login parks the ladder; a refused CONNECTION (daemon
// restarting) stays transient.
func TestRedialTCP_RefusalPermanentConnRefusedNot(t *testing.T) {
	tok := mustNewToken(t)
	prevAddr, prevTok := connectAddr, connectToken
	t.Cleanup(func() { connectAddr, connectToken = prevAddr, prevTok })

	connectAddr, connectToken = freePortAddr(t), tok
	_, err := redialTCPDest(tcpDestPrefix + connectAddr)(nil)
	if err == nil || errors.Is(err, tui.ErrLinkPermanent) {
		t.Fatalf("connection refused: err = %v, want transient", err)
	}

	addr, _ := fakeListener(t, func(c net.Conn) error {
		hello, err := ipc.ReadMessage(c)
		if err != nil {
			return err
		}
		m, _ := ipc.NewMessage(ipc.MsgError, ipc.ErrorPayload{Code: ipc.ErrCodeRefused, Message: "token refused"})
		m.ID = hello.ID
		return ipc.WriteMessage(c, m)
	})
	connectAddr = addr
	if _, err := redialTCPDest(tcpDestPrefix + addr)(nil); !errors.Is(err, tui.ErrLinkPermanent) {
		t.Fatalf("refused login: err = %v, want ErrLinkPermanent", err)
	}
	if _, err := redialTCPDest(tcpDestPrefix + "127.0.0.1:1")(nil); !errors.Is(err, tui.ErrLinkPermanent) {
		t.Fatalf("no token for that address: err = %v, want ErrLinkPermanent", err)
	}
}

// versionedLogin is signedLogin whose daemon then answers the version probe
// with version.
func versionedLogin(t *testing.T, tok, version string) string {
	t.Helper()
	addr, _ := fakeListener(t, func(c net.Conn) error {
		if err := challengeThen(c, func(h *ipc.Message, hp ipc.HelloPayload, nonceS string) *ipc.Message {
			am := clientauth.AuthMessage(hp.TokenID, hp.Nonce, nonceS)
			m, _ := ipc.NewMessage(ipc.MsgHelloResp, ipc.HelloRespPayload{Rights: ipc.RightsFull, TokenName: "t",
				ServerSig: clientauth.ServerSignature(clientauth.DeriveVerifier(tok), am)})
			m.ID = h.ID
			return m
		}); err != nil {
			return err
		}
		req, err := ipc.ReadMessage(c)
		if err != nil {
			return err
		}
		if req.Type != ipc.MsgVersionReq {
			return errors.New("the client sent " + req.Type + ", want version_req")
		}
		resp, _ := ipc.NewMessage(ipc.MsgVersionResp, ipc.VersionRespPayload{Version: version})
		resp.ID = req.ID
		return ipc.WriteMessage(c, resp)
	})
	return addr
}

// A re-login that finds another daemon version: a destination that never
// attached parks with the reason, as the launch gate would refuse it; a
// mid-session reconnect keeps its link (--remote's rule, verifyRemoteLinkGated)
// — and both say so in the log, which they did not.
func TestRedialTCP_VersionMismatch(t *testing.T) {
	asReleaseBuild(t, "1.80.0")
	tok := mustNewToken(t)
	prevAddr, prevTok := connectAddr, connectToken
	t.Cleanup(func() { connectAddr, connectToken = prevAddr, prevTok })
	var logged bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	for _, tc := range []struct {
		name      string
		old       tui.Client
		permanent bool
	}{
		{"never attached", nil, true},
		{"mid-session", &stubClient{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged.Reset()
			connectAddr, connectToken = versionedLogin(t, tok, "1.53.0"), tok
			c, err := redialTCPDest(tcpDestPrefix + connectAddr)(tc.old)
			if tc.permanent {
				if !errors.Is(err, tui.ErrLinkPermanent) || !strings.Contains(err.Error(), "1.53.0") {
					t.Fatalf("err = %v, want a permanent version mismatch naming the daemon's version", err)
				}
			} else {
				if err != nil {
					t.Fatalf("mid-session reconnect refused: %v — that ends a session whose panes are healthy", err)
				}
				if li, ok := c.(*tui.LoggedIn); ok {
					if ic, ok := li.Client.(*ipc.Client); ok {
						ic.Close()
					}
				}
			}
			if !strings.Contains(logged.String(), "1.53.0") {
				t.Errorf("the mismatch was not logged:\n%s", logged.String())
			}
		})
	}
}

// A version reply that never came is not a mismatch: the error says no reply
// arrived, and only a reported version is called a mismatch.
func TestTCPVersionErr_NoReplyIsNotAMismatch(t *testing.T) {
	asReleaseBuild(t, "1.80.0")
	err := tcpVersionErr(handshakeResult{DaemonUnknown: true}, "127.0.0.1:7000")
	if err == nil || strings.Contains(err.Error(), "mismatch") || !strings.Contains(err.Error(), "no version reply") {
		t.Fatalf("err = %v, want \"no version reply\" and no mismatch claim", err)
	}
	err = tcpVersionErr(handshakeResult{DaemonVersion: "1.53.0", Cmp: 1}, "127.0.0.1:7000")
	if err == nil || !strings.Contains(err.Error(), "version mismatch") || !strings.Contains(err.Error(), "1.53.0") {
		t.Fatalf("err = %v, want a version mismatch naming 1.53.0", err)
	}
}

// signedLogin is a listener that completes the login for tok and grants rights.
func signedLogin(t *testing.T, tok, rights string) string {
	t.Helper()
	addr, _ := fakeListener(t, func(c net.Conn) error {
		return challengeThen(c, func(h *ipc.Message, hp ipc.HelloPayload, nonceS string) *ipc.Message {
			am := clientauth.AuthMessage(hp.TokenID, hp.Nonce, nonceS)
			m, _ := ipc.NewMessage(ipc.MsgHelloResp, ipc.HelloRespPayload{Rights: rights, TokenName: "t",
				ServerSig: clientauth.ServerSignature(clientauth.DeriveVerifier(tok), am)})
			m.ID = h.ID
			return m
		})
	})
	return addr
}

// A re-login hands the daemon's CURRENT rights to the Model with the conn, so
// a token whose level changed while the link was down changes the mode; the
// runtime dial (New Project dialog) does the same.
func TestDialTCPDest_HandsTheLoginRightsToTheModel(t *testing.T) {
	tok := mustNewToken(t)
	prevAddr, prevTok := connectAddr, connectToken
	t.Cleanup(func() { connectAddr, connectToken = prevAddr, prevTok })
	for _, rights := range []string{ipc.RightsReadOnly, ipc.RightsFull} {
		for name, dial := range map[string]func(dest string) (tui.Client, error){
			"redial": func(dest string) (tui.Client, error) { return redialTCPDest(dest)(nil) },
			"dial":   dialTCPDest,
		} {
			t.Run(name+"/"+rights, func(t *testing.T) {
				connectAddr, connectToken = signedLogin(t, tok, rights), tok
				c, err := dial(tcpDestPrefix + connectAddr)
				if err != nil {
					t.Fatalf("login failed: %v", err)
				}
				li, ok := c.(*tui.LoggedIn)
				if !ok {
					t.Fatalf("dial returned %T, want *tui.LoggedIn carrying the rights", c)
				}
				ic, ok := li.Client.(*ipc.Client)
				if !ok {
					t.Fatalf("wrapped conn is %T, want *ipc.Client (the closer seam asserts it)", li.Client)
				}
				defer ic.Close()
				if li.Rights != rights {
					t.Fatalf("rights = %q, want %q", li.Rights, rights)
				}
			})
		}
	}
}

// The login hello and the ordinary hello are ONE builder, so the
// self-description a TCP daemon registers cannot drift from the unix one.
func TestDialTCP_LoginHelloIsSendHellos(t *testing.T) {
	tok := mustNewToken(t)
	seen := make(chan ipc.HelloPayload, 1)
	addr, _ := fakeListener(t, func(c net.Conn) error {
		hello, err := ipc.ReadMessage(c)
		if err != nil {
			return err
		}
		var hp ipc.HelloPayload
		_ = hello.DecodePayload(&hp)
		seen <- hp
		return nil // closing makes the login fail; only the hello matters here
	})
	_, _, _ = dialTCP(context.Background(), addr, tok)
	var got ipc.HelloPayload
	select {
	case got = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never received a login hello")
	}
	want := helloPayload(helloRoleTUI)
	got.TokenID, got.Nonce, got.UptimeMS, want.UptimeMS = "", "", 0, 0
	if !reflect.DeepEqual(got, want) { // HelloPayload holds a slice (Caps)
		t.Fatalf("login hello = %+v\nwant helloPayload(tui) = %+v", got, want)
	}
}

// Under --connect a staged update is not applied at launch: its respawn would
// rebuild argv as --remote tcp:<addr> and lose the token.
func TestStagedUpdate_SkippedUnderConnect(t *testing.T) {
	prevFn, prevAddr := stagedUpdateFn, connectAddr
	t.Cleanup(func() { stagedUpdateFn, connectAddr = prevFn, prevAddr })
	calls := 0
	stagedUpdateFn = func(bool) bool { calls++; return true }

	connectAddr = "127.0.0.1:7878"
	if applied := applyStagedAtLaunch(); applied || calls != 0 {
		t.Fatalf("--connect: applied=%v calls=%d, want the apply skipped", applied, calls)
	}
	// Control: a local launch still applies.
	connectAddr = ""
	if applied := applyStagedAtLaunch(); !applied || calls != 1 {
		t.Fatalf("local launch: applied=%v calls=%d, want the staged update applied", applied, calls)
	}
}
