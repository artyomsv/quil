package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/webgw"
)

func TestParseWebFlags(t *testing.T) {
	f, err := parseWebFlags([]string{"--port", "7880", "--no-open"})
	if err != nil || f.Listen != "127.0.0.1:7880" || !f.NoOpen {
		t.Fatalf("%+v %v", f, err)
	}
	f, err = parseWebFlags(nil)
	if err != nil || f.Listen != "127.0.0.1:0" {
		t.Fatalf("default: %+v %v", f, err)
	}
	f, err = parseWebFlags([]string{"--listen", "localhost:7881"})
	if err != nil || f.Listen != "127.0.0.1:7881" {
		t.Fatalf("loopback --listen: %+v %v", f, err)
	}
	if _, err := parseWebFlags([]string{"--listen", "0.0.0.0:7880"}); err == nil {
		t.Fatal("a non-loopback --listen was accepted")
	}
	if _, err := parseWebFlags([]string{"--port"}); err == nil {
		t.Fatal("--port without a value was accepted")
	}
	if _, err := parseWebFlags([]string{"--port", "abc"}); err == nil {
		t.Fatal("a non-numeric --port was accepted")
	}
	if _, err := parseWebFlags([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Fatalf("unknown flag: %v", err)
	}
}

// The code never reaches the browser-opening command: that command's
// arguments are readable by other local accounts while it runs.
func TestWebStartURLCarriesNoSecret(t *testing.T) {
	u := webStartURL("127.0.0.1:7880")
	if u != "http://127.0.0.1:7880/" {
		t.Fatalf("start URL = %q", u)
	}
	if strings.ContainsAny(u, "#?") {
		t.Fatal("start URL carries a fragment or query")
	}
}

func TestWebVersionMismatch(t *testing.T) {
	cases := []struct {
		name string
		res  handshakeResult
		want bool
	}{
		{"matched", handshakeResult{Matched: true, DaemonVersion: "1.2.3"}, false},
		{"older daemon", handshakeResult{DaemonVersion: "1.0.0", Cmp: 1}, true},
		{"daemon did not answer", handshakeResult{DaemonUnknown: true}, false},
		{"dev client", handshakeResult{ClientSkipped: true}, false},
	}
	for _, c := range cases {
		if got := webVersionMismatch(c.res); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOpenWebLog_WritesUnderQuilHome(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	prev := log.Writer()
	closeLog := openWebLog(config.Default())
	log.Print("web log probe")
	closeLog()
	log.SetOutput(prev)
	data, err := os.ReadFile(filepath.Join(config.QuilDir(), "web.log"))
	if err != nil || !strings.Contains(string(data), "web log probe") {
		t.Fatalf("web.log = %q, %v", data, err)
	}
}

func TestEnsureLocalDaemon_AutoStartOffNamesTheCommand(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Daemon.AutoStart = false
	err := ensureLocalDaemon(config.SocketPath(), cfg)
	if err == nil || !strings.Contains(err.Error(), "quil daemon start") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebDialers_FailWithoutADaemon(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if _, _, err := localWebDialer(config.SocketPath())(context.Background(), "c1"); err == nil {
		t.Fatal("local dial succeeded with no daemon")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, _, err = tokenWebDialer(addr, "qtk_unused")(context.Background(), "c1")
	if err == nil || errors.Is(err, webgw.ErrTokenRefused) {
		t.Fatalf("a missing listener must fail as a plain dial error, got %v", err)
	}
}
