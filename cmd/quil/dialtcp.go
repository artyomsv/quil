package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/debugserver"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/tui"
	versionpkg "github.com/artyomsv/quil/internal/version"
)

// tcpDestPrefix keys a --connect destination: "tcp:<addr>". It behaves as a
// remote destination (filesystem dialogs ask the daemon), and remoteDest
// holds it so every --remote guard (daemon, restart, status, mcp, clients)
// also covers --connect.
const tcpDestPrefix = "tcp:"

// tcpLoginStep bounds each wait of the login: the challenge, then the
// daemon's signed answer.
const tcpLoginStep = 5 * time.Second

// connectWhy is --connect's own reason in LoopbackAddr's non-loopback error;
// each caller of LoopbackAddr keeps its own wording.
const connectWhy = "--connect is loopback-only until TLS exists; from another machine, forward the port with ssh -L"

// connectAddr is the --connect address; connectToken the token, held in
// memory only, for the HMAC. Written once in main().
var connectAddr, connectToken string

func connectMode() bool { return connectAddr != "" }

var errNoListener = errors.New("no listener")

// takeTokenEnv reads QUIL_TOKEN once and REMOVES it from this process's
// environment, so no daemon, pane or bridge this process spawns inherits it.
// Called first thing in main, whatever the mode.
func takeTokenEnv() string {
	v := os.Getenv("QUIL_TOKEN")
	if err := unsetenvFn("QUIL_TOKEN"); err != nil {
		// A failure means every child would inherit the token, so it is worth
		// a line — naming the variable, never its value.
		log.Printf("could not remove QUIL_TOKEN from this process's environment: %v", err)
	}
	return v
}

// unsetenvFn is os.Unsetenv, a var so a test can make it fail.
var unsetenvFn = os.Unsetenv

// parseConnectFlags extracts --connect <addr> (or --connect=<addr>) and
// --token-file <path>. The address is loopback-only until TLS exists, which is
// what `ssh -L` gives: "7878" and "localhost:7878" mean 127.0.0.1:7878.
func parseConnectFlags(args []string) (addr, tokenFile string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	raw, seen := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--connect" || a == "--token-file":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", "", nil, fmt.Errorf("%s requires a value", a)
			}
			if a == "--connect" {
				raw, seen = args[i+1], true
			} else {
				tokenFile = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--connect="):
			raw, seen = strings.TrimPrefix(a, "--connect="), true
		case strings.HasPrefix(a, "--token-file="):
			tokenFile = strings.TrimPrefix(a, "--token-file=")
		default:
			rest = append(rest, a)
		}
	}
	if !seen {
		if tokenFile != "" {
			return "", "", nil, errors.New("--token-file needs --connect <addr>")
		}
		return "", "", args, nil
	}
	// LoopbackAddr's error already names the flag and the value given.
	addr, ok, err := debugserver.LoopbackAddr("--connect", connectWhy, raw)
	if err != nil {
		return "", "", nil, err
	}
	if !ok {
		return "", "", nil, errors.New("--connect requires a value")
	}
	return addr, tokenFile, rest, nil
}

// loadConnectToken reads the token from --token-file, else from the
// QUIL_TOKEN value main() took. Never from argv (it would show in ps).
// Surrounding whitespace — the newline `echo` and editors add — is trimmed.
func loadConnectToken(tokenFile, envToken string) (string, error) {
	tok := envToken
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read --token-file: %w", err)
		}
		tok = string(b)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", errors.New("no token: set QUIL_TOKEN or pass --token-file <path>")
	}
	if _, err := clientauth.ParseToken(tok); err != nil {
		return "", err
	}
	return tok, nil
}

// applyConnectFlags arms --connect from argv and returns argv without its
// flags. remoteDest gets "tcp:<addr>", so every guard that refuses a command
// under --remote (daemon, restart, status, mcp, clients) refuses it here too:
// each of them acts on the LOCAL socket, which is not the daemon this session
// talks to. Nothing is armed when it returns an error.
func applyConnectFlags(args []string, envToken string) ([]string, error) {
	addr, tokenFile, rest, err := parseConnectFlags(args)
	if err != nil {
		return nil, err
	}
	if addr == "" {
		return args, nil
	}
	if remoteDest != "" {
		return nil, errors.New("--connect and --remote cannot be used together")
	}
	tok, err := loadConnectToken(tokenFile, envToken)
	if err != nil {
		return nil, fmt.Errorf("--connect: %w", err)
	}
	connectAddr, connectToken = addr, tok
	remoteDest = tcpDestPrefix + addr
	return rest, nil
}

// dialTCP connects, logs in and checks the daemon's signature. On any login
// failure the conn is closed — nothing more is sent, least of all to a
// listener that could not prove it is the daemon.
func dialTCP(ctx context.Context, addr, token string) (*ipc.Client, ipc.HelloRespPayload, error) {
	var none ipc.HelloRespPayload
	client, err := ipc.NewClientWithDialer(ctx, func(c context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(c, "tcp", addr)
	})
	if err != nil {
		if isConnRefused(err) {
			return nil, none, fmt.Errorf("%w at %s", errNoListener, addr)
		}
		return nil, none, err
	}
	// helloPayload (hello.go) is the same self-description sendHello sends;
	// ClientLogin adds the token id and nonce to it.
	resp, err := clientauth.ClientLogin(client, token, helloPayload(helloRoleTUI), tcpLoginStep)
	if err != nil {
		client.Close()
		return nil, none, err
	}
	return client, resp, nil
}

// describeConnectError is the one-line reason printed before exiting. A
// refusal's reason is deliberately not repeated: it is the daemon's text, and
// the line already says everything the user can act on.
func describeConnectError(addr string, err error) string {
	var refused *clientauth.RefusedError
	switch {
	case errors.Is(err, errNoListener):
		return "no listener at " + addr
	case errors.As(err, &refused):
		return "token refused (wrong, expired or revoked)"
	case errors.Is(err, clientauth.ErrServerUnproven):
		return "the listener at " + addr + " could not prove it is your daemon"
	}
	return fmt.Sprintf("cannot connect to %s: %v", addr, err)
}

// gateTCPVersion is the remote branch of the version gate ONLY: it never
// restarts, spawns or installs a daemon — the daemon is on the far side of a
// port, and nothing here can manage it.
func gateTCPVersion(client *ipc.Client, addr string) error {
	res := versionHandshakeWithin(client, remoteGateTimeout)
	if res.ClientSkipped || res.Matched {
		return nil
	}
	reported := res.DaemonVersion
	if reported == "" {
		reported = "unknown"
	}
	// The version string is the daemon's own text and ends up on a terminal.
	return fmt.Errorf("version mismatch: this TUI runs %s, the daemon at %s runs %s — upgrade one of them so both run the same version",
		versionpkg.Current(), addr, transport.SanitizeForTerminalMessage(reported))
}

// connectTUI is the launch-time dial for --connect. It exits the process on
// failure, after one line saying why.
func connectTUI() (*ipc.Client, ipc.HelloRespPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
	defer cancel()
	client, resp, err := dialTCP(ctx, connectAddr, connectToken)
	if err != nil {
		log.Printf("connect %s: %v", connectAddr, err)
		fmt.Fprintln(os.Stderr, describeConnectError(connectAddr, err))
		exitFn(1)
		return nil, resp
	}
	if err := gateTCPVersion(client, connectAddr); err != nil {
		client.Close()
		log.Printf("connect %s: %v", connectAddr, err)
		fmt.Fprintln(os.Stderr, err)
		exitFn(1)
		return nil, resp
	}
	log.Printf("connect %s: logged in as token %q (rights %q)", connectAddr, resp.TokenName, resp.Rights)
	return client, resp
}

// tokenForDest: the only token this process holds is --connect's.
func tokenForDest(dest string) (addr, token string, err error) {
	addr = strings.TrimPrefix(dest, tcpDestPrefix)
	if connectToken == "" || addr != connectAddr {
		return addr, "", fmt.Errorf("no token for %s — start quil with --connect %s", addr, addr)
	}
	return addr, connectToken, nil
}

// dialTCPDest is the runtime dial (New Project dialog) for a tcp: dest.
func dialTCPDest(dest string) (tui.Client, error) {
	addr, token, err := tokenForDest(dest)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
	defer cancel()
	client, resp, err := dialTCP(ctx, addr, token)
	if err != nil {
		// The full error (the daemon's refusal reason included; never the
		// token) goes to quil.log, as at launch: the one-line description
		// deliberately drops it.
		log.Printf("connect %s: %v", addr, err)
		return nil, errors.New(describeConnectError(addr, err))
	}
	if err := gateTCPVersion(client, addr); err != nil {
		client.Close()
		return nil, err
	}
	sendClientHello(client, helloRoleTUI)
	// The rights ride back with the conn; the Model unwraps it before use.
	return &tui.LoggedIn{Client: client, Rights: resp.Rights}, nil
}

// redialTCPDest reconnects a tcp: destination with the same token. A refused
// login or an unproven listener is wrapped in ErrLinkPermanent so the
// destination parks with the reason — retrying a revoked token only fills the
// daemon's log; "connection refused" stays transient (a restarting daemon).
func redialTCPDest(dest string) tui.RedialFunc {
	return func(old tui.Client) (tui.Client, error) {
		if c, ok := old.(*ipc.Client); ok && c != nil {
			c.Close()
		}
		addr, token, err := tokenForDest(dest)
		if err != nil {
			return nil, fmt.Errorf("%v: %w", err, tui.ErrLinkPermanent)
		}
		ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
		defer cancel()
		client, resp, err := dialTCP(ctx, addr, token)
		if err != nil {
			log.Printf("connect %s: re-login: %v", addr, err)
			var refused *clientauth.RefusedError
			if errors.As(err, &refused) || errors.Is(err, clientauth.ErrServerUnproven) {
				return nil, fmt.Errorf("%s: %w", describeConnectError(addr, err), tui.ErrLinkPermanent)
			}
			return nil, err
		}
		sendClientHello(client, helloRoleTUI)
		// Every login answers with the token's CURRENT level — it can have
		// changed while the link was down — so it rides back with the conn
		// and the Model re-applies it before the reattach.
		log.Printf("connect %s: logged in again as token %q (rights %q)", addr, resp.TokenName, resp.Rights)
		return &tui.LoggedIn{Client: client, Rights: resp.Rights}, nil
	}
}

// stagedUpdateFn is maybeApplyStagedUpdate; the seam
// TestStagedUpdate_SkippedUnderConnect stubs.
var stagedUpdateFn = maybeApplyStagedUpdate

// applyStagedAtLaunch applies a staged update before the TUI starts — except
// under --connect. The apply respawns this binary with argv rebuilt by
// respawnArgs, which turns --connect into `--remote tcp:<addr>`, and the
// respawned process would have no token: QUIL_TOKEN was removed from the
// environment at the top of main. A remote host already cannot apply at
// launch either, and the post-exit "Update now" path is already skipped in
// remote mode (main.go: `applyUpdate = ... && !remoteMode()`).
func applyStagedAtLaunch() bool {
	if connectMode() {
		log.Print("connect: a staged update applies on the next local launch")
		return false
	}
	return stagedUpdateFn(false)
}
