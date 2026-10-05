package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/artyomsv/quil/internal/clientauth"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/debugserver"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
	"github.com/artyomsv/quil/internal/transport"
	"github.com/artyomsv/quil/internal/webgw"
)

const webListenWhy = "the web gateway serves your workspace with full rights; reach it from another machine through ssh -L"

// webVersionTimeout bounds the version check on each local dial.
const webVersionTimeout = 3 * time.Second

type webFlags struct {
	Listen string
	NoOpen bool
}

func parseWebFlags(args []string) (webFlags, error) {
	f := webFlags{Listen: "127.0.0.1:0"}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--no-open":
			f.NoOpen = true
		case "--port", "--listen":
			if i+1 >= len(args) || args[i+1] == "" {
				return f, fmt.Errorf("%s requires a value", a)
			}
			v := args[i+1]
			i++
			if a == "--port" {
				if _, err := strconv.Atoi(v); err != nil {
					return f, fmt.Errorf("--port: %v", err)
				}
			}
			addr, ok, err := debugserver.LoopbackAddr(a, webListenWhy, v)
			if err != nil {
				return f, err
			}
			if !ok {
				return f, fmt.Errorf("%s requires a value", a)
			}
			f.Listen = addr
		default:
			return f, fmt.Errorf("unknown flag for quil web: %s", a)
		}
	}
	return f, nil
}

// webStartURL is what the browser is opened on. It never carries the login
// code: the page asks for it, and the code is shown only in this terminal.
func webStartURL(hostPort string) string { return "http://" + hostPort + "/" }

// webVersionMismatch reports whether a handshake result names a release
// daemon of another version. A skipped or unanswered handshake is not one.
func webVersionMismatch(res handshakeResult) bool {
	return !res.ClientSkipped && !res.Matched && !res.DaemonUnknown
}

// localWebDialer opens a version-checked connection to the local daemon.
func localWebDialer(sock string) webgw.Dialer {
	return func(ctx context.Context, clientID string) (webgw.DaemonConn, string, error) {
		c, err := ipc.NewClient(sock)
		if err != nil {
			return nil, "", err
		}
		res := versionHandshakeWithin(c, webVersionTimeout)
		if webVersionMismatch(res) {
			c.Close()
			return nil, "", fmt.Errorf("%w: daemon %s", webgw.ErrVersionMismatch, transport.SanitizeForTerminalMessage(res.DaemonVersion))
		}
		return c, "full", nil
	}
}

// tokenWebDialer logs one browser tab in to a --connect daemon with the token.
func tokenWebDialer(addr, token string) webgw.Dialer {
	return func(ctx context.Context, clientID string) (webgw.DaemonConn, string, error) {
		c, resp, err := dialTCPWith(ctx, addr, token, webHelloPayload(clientID))
		if err != nil {
			var refused *clientauth.RefusedError
			if errors.As(err, &refused) || errors.Is(err, clientauth.ErrServerUnproven) {
				return nil, "", fmt.Errorf("%w: %s", webgw.ErrTokenRefused, describeConnectError(addr, err))
			}
			return nil, "", err
		}
		if err := gateTCPVersion(c, addr); err != nil {
			c.Close()
			return nil, "", fmt.Errorf("%w: %v", webgw.ErrVersionMismatch, err)
		}
		return c, resp.Rights, nil
	}
}

// ensureLocalDaemon makes sure a daemon answers on sock, starting one when
// auto_start allows, and refuses a daemon of another release version. It
// never restarts a running daemon.
func ensureLocalDaemon(sock string, cfg config.Config) error {
	probe, err := ipc.NewClient(sock)
	if err != nil {
		if !cfg.Daemon.AutoStart {
			return fmt.Errorf("cannot connect to daemon: %w — auto_start is off; run 'quil daemon start' first", err)
		}
		pid := startDaemon(true)
		if !waitForDaemonReady(sock, pid) {
			return errors.New("daemon did not come up — check the daemon log (see 'quil daemon status')")
		}
		if probe, err = ipc.NewClient(sock); err != nil {
			return fmt.Errorf("cannot connect to daemon: %w", err)
		}
	}
	defer probe.Close()
	if res := versionHandshakeWithin(probe, webVersionTimeout); webVersionMismatch(res) {
		return fmt.Errorf("the daemon is version %s; start quil to upgrade it", transport.SanitizeForTerminalMessage(res.DaemonVersion))
	}
	return nil
}

// openWebLog routes the standard logger to web.log and returns its closer.
// The login code is never logged.
func openWebLog(cfg config.Config) func() {
	dir := config.QuilDir()
	if dir == "" {
		return func() {}
	}
	_ = os.MkdirAll(dir, 0700)
	w, err := logger.NewRotatingWriter(dir, "web.log", int64(cfg.Logging.MaxSizeMB)<<20, cfg.Logging.MaxFiles)
	if err != nil || w == nil {
		return func() {}
	}
	log.SetOutput(w)
	return func() { _ = w.Close() }
}

func runWeb(args []string) {
	flags, err := parseWebFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if remoteMode() && !connectMode() {
		fmt.Fprintln(os.Stderr, "quil web cannot run over --remote yet; run it on the daemon's machine, or use --connect")
		os.Exit(1)
	}
	cfg := config.Default()
	if path := config.ConfigPath(); fileExists(path) {
		if loaded, loadErr := config.Load(path); loadErr == nil {
			cfg = loaded
		}
	}
	closeLog := openWebLog(cfg)
	defer closeLog()

	var dial webgw.Dialer
	if connectMode() {
		dial = tokenWebDialer(connectAddr, connectToken)
	} else {
		sock := config.SocketPath()
		if err := ensureLocalDaemon(sock, cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			closeLog()
			os.Exit(1)
		}
		dial = localWebDialer(sock)
	}

	srv := webgw.New(webgw.Config{
		Dial: dial, Version: version, Logf: log.Printf, Rand: rand.Reader, Now: time.Now, Sleep: time.Sleep,
		// Saved instances are expanded from THIS machine's files, as the TUI
		// expands them from its own (spec 5b E7).
		PluginsDir:    config.PluginsDir(),
		InstancesPath: config.InstancesPath(),
		ClientExtras:  webClientExtras(cfg, connectMode()),
	})
	ln, err := net.Listen("tcp", flags.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		closeLog()
		os.Exit(1)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	url := webStartURL(ln.Addr().String())
	if !webgw.HasUI() {
		fmt.Println("This build has no web UI — install a release build.")
	}
	code, err := srv.NewCode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		stopWeb(srv)
		closeLog()
		os.Exit(1)
	}
	fmt.Printf("Quil web: %s\nLogin code: %s  (Enter: new code, Ctrl+C: stop)\n", url, code)
	log.Printf("web gateway listening on %s", url)
	if !flags.NoOpen {
		if err := openBrowserFn(url); err != nil {
			log.Printf("could not open the browser: %v", err)
		}
	}
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if c, err := srv.NewCode(); err == nil {
				fmt.Printf("Login code: %s\n", c)
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web gateway stopped: %v", err)
			fmt.Fprintln(os.Stderr, err)
		}
	}
	stopWeb(srv)
}

// stopWeb detaches every tab and stops the HTTP server, within 5 s.
func stopWeb(srv *webgw.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

// openBrowserFn is the seam tests replace so none opens a real browser.
var openBrowserFn = openBrowser
