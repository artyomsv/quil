package main

import (
	"fmt"
	"io"
	"net"
	"os"

	"github.com/artyomsv/quil/internal/config"
)

// runStdio is the server-side half of the SSH transport. `quil --remote host`
// runs `quil --stdio` over ssh on the far side; this ensures a daemon is up and
// then splices its Unix socket onto this process's stdin/stdout.
//
// It exists in the quil binary rather than quild because the daemon-ensure
// logic (startDaemon, waitForDaemonReady, findDaemonBinary) lives here.
//
// Nothing may write to stdout except the proxy: stdout IS the IPC channel.
// Diagnostics go to stderr, which ssh relays back to the client.
func runStdio() {
	sockPath := config.SocketPath()

	daemon, err := net.Dial("unix", sockPath)
	if err != nil {
		// quiet=true is mandatory — startDaemon's verbose branch prints
		// "daemon already running" to stdout, which would corrupt the first
		// protocol frame.
		pid := startDaemon(true)
		if !waitForDaemonReady(sockPath, pid) {
			fmt.Fprintf(os.Stderr, "quil --stdio: daemon did not come up within %s\n", daemonReadyTimeout)
			os.Exit(1)
		}
		daemon, err = net.Dial("unix", sockPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "quil --stdio: connect to daemon: %v\n", err)
			os.Exit(1)
		}
	}
	defer daemon.Close()

	proxyStdio(daemon, os.Stdin, os.Stdout)
}

// proxyStdio copies bytes both ways until either direction ends. Split out
// from runStdio so it can be tested without real stdio or a real daemon.
func proxyStdio(daemon net.Conn, in io.Reader, out io.Writer) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(daemon, in); done <- struct{}{} }()
	go func() { io.Copy(out, daemon); done <- struct{}{} }()
	// One direction ending means the session is over; the deferred Close in
	// runStdio unblocks the other copy.
	<-done
}
