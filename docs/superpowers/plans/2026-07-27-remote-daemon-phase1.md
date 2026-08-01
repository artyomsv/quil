# Remote Daemon Phase 1 — Transport and Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `quil --remote <dest>` attach a local TUI to a `quild` running on another machine over SSH, without opening any network port on that machine and without any local-daemon lifecycle function ever acting on the wrong host.

**Architecture:** A `DialFunc` seam in `internal/ipc` lets a `Client` be built over any `net.Conn`. A new `internal/transport` package supplies two backends: `Local` (the existing Unix socket) and `SSH` (spawns the system `ssh` binary running `quil --stdio` on the far side, wrapping the child's pipes in a `net.Conn`). `quil --stdio` is a server-side proxy that ensures the daemon is up and copies bytes between its own stdio and the daemon's Unix socket. The daemon and `internal/ipc/server.go` are not modified at all.

**Tech Stack:** Go 1.25, stdlib only (`os/exec`, `net`, `os`). No new module dependencies.

## Global Constraints

- Go 1.25.0, module `github.com/artyomsv/quil`. Tabs for indentation in Go files; `gofmt` is mandatory.
- **No new dependencies.** Phase 1 is stdlib-only. Do not add `golang.org/x/crypto`.
- **`internal/daemon` and `internal/ipc/server.go` must not be modified.** If a task appears to require it, stop and escalate.
- Error wrapping follows the existing `internal/ipc` rule: wrap with
  `fmt.Errorf("doing X: %w", err)` when a function has **more than one fallible
  step**, so the message says which step failed (`ReadMessage` wraps three ways
  at `protocol.go:626,636,641`). A **single-operation pass-through returns the
  error bare**, because the underlying error already names itself — matching
  `NewClient` (`client.go:17`) and `NewMessage` (`protocol.go:578`). Do not add
  a comment justifying a bare return; it is the norm here, not an exception.
- Never compare errors with `==`; use `errors.Is`/`errors.As`.
- Test names follow `TestFunctionName_Scenario_Expected`. Table-driven where there are multiple input/output cases. Stdlib assertions only (`t.Errorf`/`t.Fatalf`) — no assertion libraries.
- Swappable package-level `var fooFn = realFoo` is the established seam for testability in `cmd/quil` (see `stopDaemonForUpgradeFn`, `spawnDaemonForUpgradeFn` at `cmd/quil/version_gate.go:168-171`). Follow it rather than inventing interfaces.
- Go and make are NOT installed on the host. Build and test via `./scripts/dev.sh test` and `./scripts/dev.sh vet` (Docker-based). Windows-specific tests require the documented `go test -c` cross-compile-then-run-natively workflow.
- **Never run `./scripts/kill-daemon.sh`, `./scripts/reset-daemon.sh`, or bare `./quil`** — they act on the developer's production `~/.quil/`. See `.claude/rules/dev-environment.md`.
- Commit messages: imperative mood, max 72 chars on the subject line. No AI/agent attribution of any kind.

## File Structure

| File | Responsibility |
|---|---|
| `internal/ipc/client.go` (modify) | Add `DialFunc` type and `NewClientWithDialer`. `NewClient` unchanged. |
| `internal/transport/stdioconn.go` (create) | `net.Conn` over an `exec.Cmd`'s pipes, with working read deadlines. |
| `internal/transport/stdioconn_test.go` (create) | Contract tests for the adapter. |
| `internal/transport/local.go` (create) | `Local(socketPath)` dialer. |
| `internal/transport/ssh.go` (create) | `SSH(dest, opts)` dialer and the pure `sshArgs` builder. |
| `internal/transport/ssh_test.go` (create) | Table tests for `sshArgs`. |
| `cmd/quil/stdio.go` (create) | `runStdio()` — the server-side proxy subcommand. |
| `cmd/quil/remote.go` (create) | `remoteDest` state, `remoteMode()`, `errRemoteMode`, `exitFn`. |
| `cmd/quil/remote_test.go` (create) | The regression canary for the guards. |
| `cmd/quil/main.go` (modify) | `--remote` parsing, `--stdio` case, guard in `startDaemon`, remote branch in `launchTUI`. |
| `cmd/quil/daemonctl.go` (modify) | Guard in `stopDaemonEscalating`. |
| `cmd/quil/version_gate.go` (modify) | Guard in `restartDaemonForUpgrade`; remote-specific mismatch message. |

---

### Task 1: Dialer seam in `internal/ipc`

**Files:**
- Modify: `internal/ipc/client.go:1-19`
- Test: `internal/ipc/client_dialer_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `ipc.DialFunc` = `func(context.Context) (net.Conn, error)`; `ipc.NewClientWithDialer(ctx context.Context, dial DialFunc) (*Client, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/ipc/client_dialer_test.go`:

```go
package ipc

import (
	"context"
	"errors"
	"net"
	"testing"
)

// TestNewClientWithDialer_UsesDialer_ReturnsWorkingClient proves the seam
// carries an arbitrary net.Conn, not just a Unix socket.
func TestNewClientWithDialer_UsesDialer_ReturnsWorkingClient(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	t.Cleanup(func() { clientSide.Close(); serverSide.Close() })

	called := false
	dial := func(ctx context.Context) (net.Conn, error) {
		called = true
		return clientSide, nil
	}

	c, err := NewClientWithDialer(context.Background(), dial)
	if err != nil {
		t.Fatalf("NewClientWithDialer: %v", err)
	}
	if !called {
		t.Fatal("dialer was not called")
	}

	// Server reads what the client sends.
	go func() {
		msg, _ := NewMessage(MsgHeartbeat, nil)
		_ = c.Send(msg)
	}()

	got, err := ReadMessage(serverSide)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.Type != MsgHeartbeat {
		t.Errorf("Type = %q, want %q", got.Type, MsgHeartbeat)
	}
}

// TestNewClientWithDialer_DialError_Propagates checks the error is returned
// unwrapped enough for errors.Is to work at the call site.
func TestNewClientWithDialer_DialError_Propagates(t *testing.T) {
	sentinel := errors.New("boom")
	dial := func(ctx context.Context) (net.Conn, error) { return nil, sentinel }

	_, err := NewClientWithDialer(context.Background(), dial)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want errors.Is(err, sentinel)", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: NewClientWithDialer`

- [ ] **Step 3: Write minimal implementation**

In `internal/ipc/client.go`, add `"context"` to the imports and append:

```go
// DialFunc establishes one transport-level connection to a daemon. It is the
// seam that lets a Client run over something other than a Unix socket (an SSH
// channel today, a TLS connection later) without the protocol layer knowing.
type DialFunc func(ctx context.Context) (net.Conn, error)

// NewClientWithDialer builds a Client over whatever connection dial returns.
// NewClient remains the Unix-socket convenience wrapper used by every local
// call site.
func NewClientWithDialer(ctx context.Context, dial DialFunc) (*Client, error) {
	raw, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{conn: newConn(raw)}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS. Then `./scripts/dev.sh vet` — expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/client.go internal/ipc/client_dialer_test.go
git commit -m "feat(ipc): add DialFunc seam for non-socket transports"
```

---

### Task 2: stdio `net.Conn` adapter

**Files:**
- Create: `internal/transport/stdioconn.go`
- Test: `internal/transport/stdioconn_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `newStdioConn(cmd *exec.Cmd, r, w *os.File, desc string) *stdioConn` — unexported, satisfies `net.Conn`. Used only by `ssh.go` in Task 4.

**Why a read pump rather than delegating to the pipe:** on Linux and macOS `os.Pipe` is pollable and `SetReadDeadline` works, but on Windows the handles are non-overlapped and it returns `os.ErrNoDeadline`. Three call sites depend on read deadlines — `cmd/quil/handshake.go:82` and `statusRoundTrip` at `cmd/quil/status.go:405,420`. A uniform pump avoids platform branching and makes the behaviour identical everywhere, which is what the tests then pin. Write deadlines return `os.ErrNoDeadline` honestly: the sole caller (`internal/ipc/server.go:260`) already discards that error, and OpenSSH's `ServerAliveInterval` supplies the liveness backstop the deadline would have given.

- [ ] **Step 1: Write the failing test**

Create `internal/transport/stdioconn_test.go`:

```go
package transport

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// pipePair builds a stdioConn wired to two os.Pipes with no child process,
// so the adapter can be tested without spawning anything.
func pipePair(t *testing.T) (c *stdioConn, feed *os.File, drain *os.File) {
	t.Helper()
	// Data the conn will READ arrives on inR; tests write to inW.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// Data the conn WRITES lands on outW; tests read from outR.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	c = newStdioConn(exec.Command("nonexistent-by-design"), inR, outW, "test")
	t.Cleanup(func() { c.Close(); inW.Close(); outR.Close() })
	return c, inW, outR
}

func TestStdioConn_Read_ReturnsWrittenBytes(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("hello")) }()

	buf := make([]byte, 5)
	n, err := io.ReadFull(c, buf)
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Errorf("read %q, want %q", got, "hello")
	}
}

// TestStdioConn_Read_SplitsLargePayloadAcrossCalls pins that a held-over
// remainder is returned on the next Read rather than dropped — bufio.Reader
// calls Read with whatever capacity it has left.
func TestStdioConn_Read_SplitsLargePayloadAcrossCalls(t *testing.T) {
	c, feed, _ := pipePair(t)

	go func() { feed.Write([]byte("abcdef")) }()

	first := make([]byte, 2)
	if _, err := io.ReadFull(c, first); err != nil {
		t.Fatalf("first ReadFull: %v", err)
	}
	if string(first) != "ab" {
		t.Fatalf("first read %q, want %q", first, "ab")
	}

	rest := make([]byte, 4)
	if _, err := io.ReadFull(c, rest); err != nil {
		t.Fatalf("second ReadFull: %v", err)
	}
	if string(rest) != "cdef" {
		t.Errorf("second read %q, want %q", rest, "cdef")
	}
}

// TestStdioConn_ReadDeadline_TimesOutAsNetError is the load-bearing one:
// cmd/quil/handshake.go does errors.As(err, &netErr) && netErr.Timeout().
func TestStdioConn_ReadDeadline_TimesOutAsNetError(t *testing.T) {
	c, _, _ := pipePair(t)

	if err := c.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	buf := make([]byte, 4)
	_, err := c.Read(buf)
	if err == nil {
		t.Fatal("Read succeeded, want timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("err = %v, want a net.Error with Timeout() == true", err)
	}
}

// TestStdioConn_ReadDeadline_ZeroClearsIt pins that handshake.go's
// `defer client.SetReadDeadline(time.Time{})` actually re-enables blocking.
func TestStdioConn_ReadDeadline_ZeroClearsIt(t *testing.T) {
	c, feed, _ := pipePair(t)

	if err := c.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // deadline is now in the past
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear SetReadDeadline: %v", err)
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		feed.Write([]byte("late"))
	}()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("ReadFull after clearing deadline: %v", err)
	}
	if string(buf) != "late" {
		t.Errorf("read %q, want %q", buf, "late")
	}
}

func TestStdioConn_Write_ReachesTheChild(t *testing.T) {
	c, _, drain := pipePair(t)

	go func() { c.Write([]byte("ping")) }()

	buf := make([]byte, 4)
	if _, err := io.ReadFull(drain, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("child got %q, want %q", buf, "ping")
	}
}

func TestStdioConn_SetWriteDeadline_ReportsUnsupported(t *testing.T) {
	c, _, _ := pipePair(t)
	if err := c.SetWriteDeadline(time.Now().Add(time.Second)); !errors.Is(err, os.ErrNoDeadline) {
		t.Fatalf("err = %v, want os.ErrNoDeadline", err)
	}
}

func TestStdioConn_ReadAfterClose_ReturnsError(t *testing.T) {
	c, _, _ := pipePair(t)
	c.Close()

	if _, err := c.Read(make([]byte, 4)); err == nil {
		t.Fatal("Read after Close succeeded, want error")
	}
}

func TestStdioConn_Close_IsIdempotent(t *testing.T) {
	c, _, _ := pipePair(t)
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStdioConn_Addrs_AreDescriptive(t *testing.T) {
	c, _, _ := pipePair(t)
	if got := c.RemoteAddr().Network(); got != "stdio" {
		t.Errorf("Network() = %q, want %q", got, "stdio")
	}
	if got := c.RemoteAddr().String(); got != "test" {
		t.Errorf("String() = %q, want %q", got, "test")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: newStdioConn`

- [ ] **Step 3: Write minimal implementation**

Create `internal/transport/stdioconn.go`:

```go
// Package transport supplies the connection backends a quil client can use to
// reach a daemon: the local Unix socket, or an SSH channel to another host.
package transport

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// readChunk is the pump's buffer size. IPC frames are usually far smaller;
// this only bounds how much one Read syscall can pull at once.
const readChunk = 32 * 1024

// stdioAddr reports a stdio-backed connection's endpoint for net.Conn's
// LocalAddr/RemoteAddr. Purely descriptive — nothing routes on it.
type stdioAddr string

func (a stdioAddr) Network() string { return "stdio" }
func (a stdioAddr) String() string  { return string(a) }

// stdioConn adapts a child process's stdin/stdout pipes to net.Conn.
//
// Reads go through a pump goroutine rather than straight to the pipe so that
// read deadlines work identically on every platform. On Unix os.Pipe is
// pollable and SetReadDeadline would work directly, but on Windows the handles
// are non-overlapped and it fails with os.ErrNoDeadline — and three call sites
// depend on read deadlines (cmd/quil/handshake.go and cmd/quil/status.go).
// One uniform implementation is simpler than a platform split and is what the
// tests pin.
//
// Write deadlines are honestly unsupported: the only caller
// (internal/ipc/server.go:260) discards the error, and OpenSSH's
// ServerAliveInterval provides the wedged-peer detection the deadline would.
type stdioConn struct {
	r    *os.File // parent's read end of the child's stdout
	w    *os.File // parent's write end of the child's stdin
	cmd  *exec.Cmd
	desc string

	readCh chan []byte
	done   chan struct{}

	mu       sync.Mutex // guards held, deadline, pumpErr
	held     []byte     // remainder of the last chunk not yet returned
	deadline time.Time
	pumpErr  error

	closeOnce sync.Once
}

// newStdioConn wires an already-started command's pipes into a net.Conn and
// starts the read pump. r and w are the PARENT's ends; the caller must have
// already closed the child's ends.
func newStdioConn(cmd *exec.Cmd, r, w *os.File, desc string) *stdioConn {
	c := &stdioConn{
		r:      r,
		w:      w,
		cmd:    cmd,
		desc:   desc,
		readCh: make(chan []byte, 1),
		done:   make(chan struct{}),
	}
	go c.pump()
	return c
}

// pump drains the pipe into readCh until the pipe errors or the conn closes.
// Closing readCh is what turns a dead child into a read error rather than a
// permanent block.
func (c *stdioConn) pump() {
	defer close(c.readCh)
	buf := make([]byte, readChunk)
	for {
		n, err := c.r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case c.readCh <- chunk:
			case <-c.done:
				return
			}
		}
		if err != nil {
			c.mu.Lock()
			c.pumpErr = err
			c.mu.Unlock()
			return
		}
	}
}

func (c *stdioConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.held) > 0 {
		n := copy(p, c.held)
		c.held = c.held[n:]
		c.mu.Unlock()
		return n, nil
	}
	deadline := c.deadline
	c.mu.Unlock()

	var timeout <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}

	select {
	case chunk, ok := <-c.readCh:
		if !ok {
			c.mu.Lock()
			err := c.pumpErr
			c.mu.Unlock()
			if err == nil {
				err = net.ErrClosed
			}
			return 0, err
		}
		n := copy(p, chunk)
		if n < len(chunk) {
			c.mu.Lock()
			c.held = chunk[n:]
			c.mu.Unlock()
		}
		return n, nil
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *stdioConn) Write(p []byte) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	return c.w.Write(p)
}

// Close tears down the pipes and reaps the child. Idempotent.
func (c *stdioConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.w.Close()
		_ = c.r.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
		}
	})
	return nil
}

func (c *stdioConn) LocalAddr() net.Addr  { return stdioAddr("local") }
func (c *stdioConn) RemoteAddr() net.Addr { return stdioAddr(c.desc) }

func (c *stdioConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *stdioConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline is not supported. See the type comment for why that is
// safe here rather than papered over with a lie that returns nil.
func (c *stdioConn) SetWriteDeadline(t time.Time) error {
	return os.ErrNoDeadline
}

// compile-time proof the adapter satisfies the interface ipc.DialFunc returns.
var _ net.Conn = (*stdioConn)(nil)
```

The import block above must NOT include `"errors"` — nothing in this file uses
it. Imports are exactly: `net`, `os`, `os/exec`, `sync`, `time`.

Note on `SetDeadline`: it returns `os.ErrNoDeadline` because the write half is
unsupported. That is correct and honest — no current caller uses `SetDeadline`
on the client side.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS, all ten tests.
Run: `./scripts/dev.sh test-race`
Expected: PASS with no data races (the pump and Read share `held`/`deadline`/`pumpErr`).
Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/stdioconn.go internal/transport/stdioconn_test.go
git commit -m "feat(transport): add stdio net.Conn adapter with read deadlines"
```

---

### Task 3: `Local` dialer

**Files:**
- Create: `internal/transport/local.go`
- Test: `internal/transport/local_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `transport.Local(socketPath string) func(context.Context) (net.Conn, error)`.

The return type is the bare function signature rather than `ipc.DialFunc` so `internal/transport` imports nothing from `internal/ipc`. Go's structural typing makes it assignable to `ipc.DialFunc` at the call site.

- [ ] **Step 1: Write the failing test**

Create `internal/transport/local_test.go`:

```go
package transport

import (
	"context"
	"net"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocal_DialsListeningSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket path length and semantics differ; covered on CI Linux")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()

	conn, err := Local(sock)(context.Background())
	if err != nil {
		t.Fatalf("Local dial: %v", err)
	}
	conn.Close()
}

func TestLocal_MissingSocket_ReturnsError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := Local(sock)(context.Background()); err == nil {
		t.Fatal("dial to a missing socket succeeded, want error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: Local`

- [ ] **Step 3: Write minimal implementation**

Create `internal/transport/local.go`:

```go
package transport

import (
	"context"
	"net"
)

// Local returns a dialer for a daemon listening on a Unix socket on this
// machine — the transport quil has always used.
func Local(socketPath string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/local.go internal/transport/local_test.go
git commit -m "feat(transport): add local unix socket dialer"
```

---

### Task 4: `SSH` dialer

**Files:**
- Create: `internal/transport/ssh.go`
- Test: `internal/transport/ssh_test.go`

**Interfaces:**
- Consumes: `newStdioConn` (Task 2).
- Produces:
  - `type SSHOptions struct { SSHPath string; RemoteCommand string; Batch bool }`
  - `transport.SSH(dest string, opts SSHOptions) func(context.Context) (net.Conn, error)`
  - `DefaultRemoteCommand` = `"quil --stdio"`

- [ ] **Step 1: Write the failing test**

Create `internal/transport/ssh_test.go`:

```go
package transport

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSSHArgs_PassesDestinationVerbatim(t *testing.T) {
	// A ~/.ssh/config Host alias must survive untouched — parsing it into
	// user/host/port and reassembling would discard HostName, Port, User and
	// ProxyJump, and a bastion hop is the common case for a cluster.
	for _, dest := range []string{"gpu01", "user@gpu01", "user@gpu01.example.com"} {
		args := sshArgs(dest, SSHOptions{})
		found := false
		for i, a := range args {
			if a == dest {
				found = true
				// The destination must be immediately followed by the remote
				// command and be the last option-free token before it.
				if i != len(args)-2 {
					t.Errorf("dest %q at index %d, want second-to-last of %v", dest, i, args)
				}
			}
		}
		if !found {
			t.Errorf("dest %q not present verbatim in %v", dest, args)
		}
	}
}

func TestSSHArgs_ForcesSecurityOptions(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"ForwardAgent=no",
		"ForwardX11=no",
		"ForwardX11Trusted=no",
		"PermitLocalCommand=no",
		"ClearAllForwardings=yes",
		"RequestTTY=no",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing forced option %q in %v", want, args)
		}
	}
}

// TestSSHArgs_DoesNotForceHostKeyChecking pins a deliberate decision:
// StrictHostKeyChecking=accept-new is WEAKER than the default prompt, so the
// user's own policy is left alone.
func TestSSHArgs_DoesNotForceHostKeyChecking(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	if strings.Contains(joined, "StrictHostKeyChecking") {
		t.Errorf("StrictHostKeyChecking must not be forced, got %q", joined)
	}
}

// TestSSHArgs_DoesNotForceProxyOptions pins that bastion support survives.
func TestSSHArgs_DoesNotForceProxyOptions(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	for _, forbidden := range []string{"ProxyCommand", "ProxyJump", "ControlMaster", "IdentityFile"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("must not force %q, got %q", forbidden, joined)
		}
	}
}

func TestSSHArgs_NoTTY(t *testing.T) {
	// -T is mandatory: a PTY would apply CRLF translation and corrupt the
	// 4-byte big-endian length prefix on every frame.
	args := sshArgs("gpu01", SSHOptions{})
	hasT := false
	for _, a := range args {
		if a == "-T" {
			hasT = true
		}
	}
	if !hasT {
		t.Errorf("missing -T in %v", args)
	}
}

func TestSSHArgs_BatchMode(t *testing.T) {
	tests := []struct {
		name  string
		batch bool
		want  bool
	}{
		{"first dial is interactive so host-key and passphrase prompts work", false, false},
		{"reconnect is batch because no terminal is available", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joined := strings.Join(sshArgs("gpu01", SSHOptions{Batch: tt.batch}), " ")
			got := strings.Contains(joined, "BatchMode=yes")
			if got != tt.want {
				t.Errorf("BatchMode present = %v, want %v (args: %q)", got, tt.want, joined)
			}
		})
	}
}

func TestSSHArgs_DefaultRemoteCommand(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	if got := args[len(args)-1]; got != DefaultRemoteCommand {
		t.Errorf("remote command = %q, want %q", got, DefaultRemoteCommand)
	}
}

func TestSSHArgs_CustomRemoteCommand(t *testing.T) {
	// A non-interactive login shell often lacks ~/.local/bin, so an absolute
	// path must be usable.
	args := sshArgs("gpu01", SSHOptions{RemoteCommand: "/opt/bin/quil --stdio"})
	if got := args[len(args)-1]; got != "/opt/bin/quil --stdio" {
		t.Errorf("remote command = %q, want the override", got)
	}
}

func TestSSHArgs_KeepsOptionOrderStable(t *testing.T) {
	// Regression guard: the args builder is pure, so its output must be
	// deterministic for a given input.
	a := sshArgs("gpu01", SSHOptions{Batch: true})
	b := sshArgs("gpu01", SSHOptions{Batch: true})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("sshArgs is not deterministic:\n%v\n%v", a, b)
	}
}

func TestSSH_MissingBinary_ReturnsError(t *testing.T) {
	dial := SSH("gpu01", SSHOptions{SSHPath: "definitely-not-a-real-ssh-binary"})
	if _, err := dial(context.Background()); err == nil {
		t.Fatal("dial with a bogus ssh path succeeded, want error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: sshArgs`, `undefined: SSH`, `undefined: SSHOptions`, `undefined: DefaultRemoteCommand`

- [ ] **Step 3: Write minimal implementation**

Create `internal/transport/ssh.go`:

```go
package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
)

// DefaultRemoteCommand is what ssh runs on the far side. It is `quil`, not
// `quild`: the daemon-ensure logic (startDaemon, waitForDaemonReady,
// findDaemonBinary) lives in the TUI binary, and release archives ship both
// binaries together so quil is present wherever quild is.
const DefaultRemoteCommand = "quil --stdio"

// forcedSSHOptions are passed as -o flags ahead of the user's ssh_config.
// OpenSSH uses the FIRST obtained value for each parameter and processes
// command-line -o before any configuration file, so these win over a user's
// global settings.
//
// ClearAllForwardings=yes neutralises LocalForward, RemoteForward and
// DynamicForward in one option rather than enumerating each.
//
// Deliberately NOT forced: ProxyCommand and ProxyJump (bastion hops are a core
// requirement), IdentityFile, IdentityAgent, User, HostName, Port, Ciphers and
// KexAlgorithms (the user's crypto policy), ControlMaster, and
// StrictHostKeyChecking — accept-new is weaker than the default prompt, so the
// user's own host-key policy is left intact.
var forcedSSHOptions = []string{
	"ForwardAgent=no",
	"ForwardX11=no",
	"ForwardX11Trusted=no",
	"PermitLocalCommand=no",
	"ClearAllForwardings=yes",
	"RequestTTY=no",
}

// keepaliveSSHOptions make ssh itself notice a dead link and exit, which EOFs
// our pipes. There is no application-layer heartbeat: ipc.MsgHeartbeat is
// declared but never sent anywhere, so this is the only liveness check.
var keepaliveSSHOptions = []string{
	"ServerAliveInterval=15",
	"ServerAliveCountMax=3",
}

// SSHOptions tunes the ssh invocation.
type SSHOptions struct {
	// SSHPath overrides the ssh binary. Empty means "ssh" from PATH.
	SSHPath string

	// RemoteCommand overrides what runs on the far side. Empty means
	// DefaultRemoteCommand. A non-interactive login shell often lacks
	// ~/.local/bin, which is where scripts/install.sh puts quil, so an
	// absolute path is sometimes required.
	RemoteCommand string

	// Batch suppresses every interactive prompt. False for the first dial,
	// which happens before the TUI takes the terminal and must be able to
	// prompt for a host-key fingerprint or a key passphrase. True for
	// reconnects, which happen under raw mode where a prompt would garble or
	// deadlock the display — on Windows especially, since ssh reads CONIN$
	// directly rather than stdin.
	Batch bool
}

// sshArgs builds the ssh argument vector. Pure, so it is unit-testable without
// spawning anything.
func sshArgs(dest string, opts SSHOptions) []string {
	args := []string{"-T"}
	for _, o := range forcedSSHOptions {
		args = append(args, "-o", o)
	}
	for _, o := range keepaliveSSHOptions {
		args = append(args, "-o", o)
	}
	if opts.Batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	cmd := opts.RemoteCommand
	if cmd == "" {
		cmd = DefaultRemoteCommand
	}
	// dest is passed VERBATIM — never parsed into user/host/port and
	// reassembled, or a ~/.ssh/config Host alias loses its HostName, Port,
	// User and ProxyJump.
	return append(args, dest, cmd)
}

// SSH returns a dialer that reaches a daemon on another host by running the
// remote command over ssh and speaking the IPC protocol across its stdio.
//
// No network port is opened on the remote host: the daemon keeps listening
// only on its local Unix socket, and the remote command proxies to it.
func SSH(dest string, opts SSHOptions) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		sshPath := opts.SSHPath
		if sshPath == "" {
			sshPath = "ssh"
		}
		resolved, err := exec.LookPath(sshPath)
		if err != nil {
			return nil, fmt.Errorf("locate ssh binary %q: %w", sshPath, err)
		}

		// Own the pipes rather than using cmd.StdinPipe/StdoutPipe so both
		// ends are *os.File, which is what stdioConn needs.
		childIn, parentWrite, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}
		parentRead, childOut, err := os.Pipe()
		if err != nil {
			childIn.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", err)
		}

		cmd := exec.CommandContext(ctx, resolved, sshArgs(dest, opts)...)
		cmd.Stdin = childIn
		cmd.Stdout = childOut

		// ssh's own diagnostics ("Permission denied (publickey)", "Host key
		// verification failed") are better than anything we would write, so
		// keep them. On the first dial stderr goes to the terminal so prompts
		// are visible; on reconnect it is captured for the error message.
		var errBuf *lockedBuffer
		if opts.Batch {
			errBuf = &lockedBuffer{}
			cmd.Stderr = errBuf
		} else {
			cmd.Stderr = os.Stderr
		}

		if err := cmd.Start(); err != nil {
			childIn.Close()
			childOut.Close()
			parentRead.Close()
			parentWrite.Close()
			return nil, fmt.Errorf("start ssh to %s: %w", dest, err)
		}
		// The child holds its own descriptors now; drop the parent's copies or
		// EOF will never propagate.
		childIn.Close()
		childOut.Close()

		conn := newStdioConn(cmd, parentRead, parentWrite, dest)
		if errBuf != nil {
			conn.stderr = errBuf
		}
		return conn, nil
	}
}

// lockedBuffer is a bytes.Buffer safe for the exec package's writer goroutine
// to fill while the dial path reads it on error.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
```

- [ ] **Step 4: Add the `stderr` field to `stdioConn`**

In `internal/transport/stdioconn.go`, add to the struct:

```go
	// stderr holds ssh's captured diagnostics when the dial ran in batch mode.
	// Nil on interactive dials, where stderr went straight to the terminal.
	stderr *lockedBuffer
```

and add this method so callers can surface the real reason a link died:

```go
// Stderr returns whatever the child wrote to stderr, or "" when stderr was not
// captured. Used to turn "read: EOF" into ssh's own explanation.
func (c *stdioConn) Stderr() string {
	if c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.
Run: `./scripts/dev.sh test-race`
Expected: PASS.
Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/transport/ssh.go internal/transport/ssh_test.go internal/transport/stdioconn.go
git commit -m "feat(transport): add ssh dialer running quil --stdio remotely"
```

---

### Task 5: `quil --stdio` proxy subcommand

**Files:**
- Create: `cmd/quil/stdio.go`
- Modify: `cmd/quil/main.go:90-111` (the subcommand switch)
- Test: `cmd/quil/stdio_test.go`

**Interfaces:**
- Consumes: `startDaemon(quiet bool) int` (`cmd/quil/main.go:173`), `waitForDaemonReady(sockPath string, pid int) bool` (`cmd/quil/daemonctl.go:224`).
- Produces: `runStdio()` — never returns; exits the process.

**Critical:** `startDaemon` must be called with `quiet=true`. Its non-quiet branch prints "daemon already running" to **stdout** (`main.go:180`), and stdout is the IPC channel — that line would corrupt the first frame.

- [ ] **Step 1: Write the failing test**

Create `cmd/quil/stdio_test.go`:

```go
package main

import (
	"io"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestProxyStdio_CopiesBothDirections covers the pure copy loop without
// touching the daemon-ensure path or os.Stdin/os.Stdout.
func TestProxyStdio_CopiesBothDirections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket semantics; covered on CI Linux")
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// Echo server standing in for the daemon.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()

	daemon, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	inR, inW := net.Pipe()   // stands in for os.Stdin
	outR, outW := net.Pipe() // stands in for os.Stdout
	t.Cleanup(func() { inW.Close(); outR.Close() })

	go proxyStdio(daemon, inR, outW)

	go func() { inW.Write([]byte("round-trip")) }()

	buf := make([]byte, 10)
	outR.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(outR, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "round-trip" {
		t.Errorf("got %q, want %q", buf, "round-trip")
	}
}

// TestProxyStdio_ReturnsWhenDaemonCloses proves the proxy exits rather than
// hanging when the far end goes away, so ssh tears the session down.
func TestProxyStdio_ReturnsWhenDaemonCloses(t *testing.T) {
	daemonA, daemonB := net.Pipe()
	inR, inW := net.Pipe()
	outR, outW := net.Pipe()
	t.Cleanup(func() { inW.Close(); outR.Close(); daemonA.Close() })

	done := make(chan struct{})
	go func() { proxyStdio(daemonA, inR, outW); close(done) }()

	daemonB.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxyStdio did not return after the daemon closed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: proxyStdio`

- [ ] **Step 3: Write minimal implementation**

Create `cmd/quil/stdio.go`:

```go
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
```

- [ ] **Step 4: Wire the subcommand**

In `cmd/quil/main.go`, inside the `switch os.Args[1]` block at line 91, add a case before `case "status":`:

```go
		case "--stdio":
			runStdio()
			return
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS.
Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/quil/stdio.go cmd/quil/stdio_test.go cmd/quil/main.go
git commit -m "feat(cli): add quil --stdio daemon proxy for remote attach"
```

---

### Task 6: Remote-mode state and the three lifecycle guards

**Files:**
- Create: `cmd/quil/remote.go`
- Create: `cmd/quil/remote_test.go`
- Modify: `cmd/quil/main.go:173` (`startDaemon`)
- Modify: `cmd/quil/daemonctl.go:67` (`stopDaemonEscalating`)
- Modify: `cmd/quil/version_gate.go:191` (`restartDaemonForUpgrade`)

**Interfaces:**
- Consumes: nothing.
- Produces: `remoteDest string` (package var), `remoteMode() bool`, `errRemoteMode` (sentinel), `exitFn` (swappable `os.Exit`).

**Why the guard goes inside each function rather than at the call sites:** all three read `config.SocketPath()` and `config.PidPath()` **internally**. Nothing at a call site looks remote-unsafe — a reviewer reading `gateVersionCheck` sees `restartDaemonForUpgrade()` with no arguments and no hint that it will SIGKILL a daemon on another machine's behalf. Putting the guard where the hidden global read happens keeps the invariant local to the hazard.

- [ ] **Step 1: Write the failing test**

Create `cmd/quil/remote_test.go`:

```go
package main

import (
	"errors"
	"testing"
)

// withRemote sets remote mode for one test and restores it afterwards.
func withRemote(t *testing.T, dest string) {
	t.Helper()
	prev := remoteDest
	remoteDest = dest
	t.Cleanup(func() { remoteDest = prev })
}

func TestRemoteMode_ReflectsDest(t *testing.T) {
	if remoteMode() {
		t.Fatal("remoteMode() is true by default, want false")
	}
	withRemote(t, "gpu01")
	if !remoteMode() {
		t.Error("remoteMode() = false after setting remoteDest, want true")
	}
}

// TestStopDaemonEscalating_RemoteMode_Refuses is a regression canary.
//
// Without it: over a deadline-less transport the version handshake returns
// DaemonUnknown, gateVersionCheck falls to its default branch and calls
// restartDaemonForUpgrade, which reads config.SocketPath()/config.PidPath() —
// the LAPTOP's — and SIGKILLs the user's local production daemon while the
// remote one sits untouched. Do not delete this test.
func TestStopDaemonEscalating_RemoteMode_Refuses(t *testing.T) {
	withRemote(t, "gpu01")

	wasRunning, err := stopDaemonEscalating(false)
	if !errors.Is(err, errRemoteMode) {
		t.Fatalf("err = %v, want errRemoteMode", err)
	}
	if wasRunning {
		t.Error("wasRunning = true, want false — nothing local was touched")
	}
}

func TestRestartDaemonForUpgrade_RemoteMode_Refuses(t *testing.T) {
	withRemote(t, "gpu01")

	client, err := restartDaemonForUpgrade()
	if !errors.Is(err, errRemoteMode) {
		t.Fatalf("err = %v, want errRemoteMode", err)
	}
	if client != nil {
		t.Error("client is non-nil, want nil")
	}
}

func TestStartDaemon_RemoteMode_Exits(t *testing.T) {
	withRemote(t, "gpu01")

	var code int
	called := false
	prev := exitFn
	exitFn = func(c int) { called = true; code = c; panic(errTestExit) }
	t.Cleanup(func() { exitFn = prev })

	defer func() {
		if r := recover(); r != errTestExit {
			t.Fatalf("recover() = %v, want errTestExit — startDaemon did not exit", r)
		}
		if !called {
			t.Error("exitFn was not called")
		}
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero")
		}
	}()

	startDaemon(true)
	t.Fatal("startDaemon returned in remote mode, want exit")
}

var errTestExit = errors.New("test exit")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: remoteDest`, `undefined: remoteMode`, `undefined: errRemoteMode`, `undefined: exitFn`

- [ ] **Step 3: Write the remote-mode state**

Create `cmd/quil/remote.go`:

```go
package main

import (
	"errors"
	"os"
)

// remoteDest is empty for a local session and holds the --remote destination
// otherwise. Written once in main() before any daemon-lifecycle function can
// run, then read-only for the life of the process.
var remoteDest string

// remoteMode reports whether this TUI is attached to a daemon on another host.
func remoteMode() bool { return remoteDest != "" }

// errRemoteMode is returned by the local daemon-lifecycle functions when the
// session is attached to a remote daemon. They all resolve
// config.SocketPath()/config.PidPath() internally, so running them here would
// act on THIS machine's daemon — which is not the one the user is looking at.
var errRemoteMode = errors.New("not available while attached to a remote daemon")

// exitFn is os.Exit, swappable so the guards can be tested. Mirrors the
// existing seam pattern (stopDaemonForUpgradeFn, spawnDaemonForUpgradeFn).
var exitFn = os.Exit
```

- [ ] **Step 4: Add the guard to `stopDaemonEscalating`**

In `cmd/quil/daemonctl.go`, immediately after the `func stopDaemonEscalating(verbose bool) (wasRunning bool, err error) {` line at :67, insert:

```go
	if remoteMode() {
		return false, errRemoteMode
	}
```

- [ ] **Step 5: Add the guard to `restartDaemonForUpgrade`**

In `cmd/quil/version_gate.go`, immediately after `func restartDaemonForUpgrade() (*ipc.Client, error) {` at :191, insert:

```go
	if remoteMode() {
		return nil, errRemoteMode
	}
```

- [ ] **Step 6: Add the guard to `startDaemon`**

In `cmd/quil/main.go`, immediately after `func startDaemon(quiet bool) int {` at :173, insert:

```go
	if remoteMode() {
		// Defense in depth: launchTUI never reaches here in remote mode, but
		// startDaemon spawns against config.SocketPath() and a future caller
		// that forgets would start a daemon on the wrong machine.
		fmt.Fprintln(os.Stderr, "internal error: startDaemon called while attached to a remote daemon")
		exitFn(1)
	}
```

- [ ] **Step 7: Replace the remaining `os.Exit` calls in `startDaemon`**

Still in `startDaemon`, change the two existing `os.Exit(1)` calls (the `MkdirAll` failure at :197 and the spawn failure below it) to `exitFn(1)`, so the whole function is testable through one seam.

- [ ] **Step 8: Run tests to verify they pass**

Run: `./scripts/dev.sh test`
Expected: PASS, all four tests in `remote_test.go`.
Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add cmd/quil/remote.go cmd/quil/remote_test.go cmd/quil/main.go cmd/quil/daemonctl.go cmd/quil/version_gate.go
git commit -m "feat(cli): guard local daemon lifecycle against remote mode"
```

---

### Task 7: `--remote` flag, launch wiring, and the remote version-gate message

**Files:**
- Modify: `cmd/quil/main.go:73-88` (flag parsing), `:287-302` (connection), `:129` and `:206` (daemon subcommand refusals)
- Modify: `cmd/quil/version_gate.go:51-61` (mismatch branch)
- Test: `cmd/quil/remote_flag_test.go`

**Interfaces:**
- Consumes: `remoteDest`, `remoteMode()` (Task 6); `transport.SSH` (Task 4); `ipc.NewClientWithDialer` (Task 1).
- Produces: `parseRemoteFlag(args []string) (dest string, rest []string, err error)`.

- [ ] **Step 1: Write the failing test**

Create `cmd/quil/remote_flag_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestParseRemoteFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDest string
		wantRest []string
		wantErr  bool
	}{
		{
			name:     "absent leaves args untouched",
			args:     []string{"quil"},
			wantDest: "",
			wantRest: []string{"quil"},
		},
		{
			name:     "separate value",
			args:     []string{"quil", "--remote", "gpu01"},
			wantDest: "gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "equals form",
			args:     []string{"quil", "--remote=gpu01"},
			wantDest: "gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "user@host is passed through verbatim",
			args:     []string{"quil", "--remote", "user@gpu01"},
			wantDest: "user@gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "other args survive",
			args:     []string{"quil", "--remote", "gpu01", "status"},
			wantDest: "gpu01",
			wantRest: []string{"quil", "status"},
		},
		{
			name:    "missing value is an error",
			args:    []string{"quil", "--remote"},
			wantErr: true,
		},
		{
			name:    "empty value is an error",
			args:    []string{"quil", "--remote="},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest, rest, err := parseRemoteFlag(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if dest != tt.wantDest {
				t.Errorf("dest = %q, want %q", dest, tt.wantDest)
			}
			if !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `undefined: parseRemoteFlag`

- [ ] **Step 3: Write the parser**

Append to `cmd/quil/remote.go`:

```go
// parseRemoteFlag extracts --remote <dest> or --remote=<dest> from args,
// returning the destination and args with the flag removed. args includes
// argv[0]; it is returned unchanged when the flag is absent.
//
// The destination is never interpreted here — it goes to ssh verbatim so a
// ~/.ssh/config Host alias keeps its HostName, Port, User and ProxyJump.
func parseRemoteFlag(args []string) (dest string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--remote":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
			dest = args[i+1]
			i++
		case strings.HasPrefix(arg, "--remote="):
			dest = strings.TrimPrefix(arg, "--remote=")
			if dest == "" {
				return "", nil, errors.New("--remote requires a destination, e.g. --remote gpu01")
			}
		default:
			rest = append(rest, arg)
		}
	}
	return dest, rest, nil
}
```

Add `"strings"` to `cmd/quil/remote.go`'s imports.

- [ ] **Step 4: Run the parser test**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Wire the flag in `main()`**

In `cmd/quil/main.go`, immediately after the `--dev` loop ends (after line 88, before the `if len(os.Args) > 1` switch), insert:

```go
	// --remote binds this TUI to a daemon on another host. Parsed before the
	// subcommand switch so the lifecycle guards are armed for everything below.
	if dest, rest, err := parseRemoteFlag(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	} else if dest != "" {
		remoteDest = dest
		os.Args = rest
	}
```

- [ ] **Step 6: Refuse the daemon lifecycle subcommands**

In `cmd/quil/main.go`, at the top of `handleDaemon()` (after line 116's opening brace), insert:

```go
	if remoteMode() {
		fmt.Fprintf(os.Stderr, "quil daemon: not available with --remote (target: %s)\n"+
			"Manage the remote daemon over ssh, or drop --remote to manage the local one.\n", remoteDest)
		os.Exit(1)
	}
```

And in the `case "restart":` branch of the main switch (line 101), insert the same refusal before `restartDaemonCmd()`:

```go
		case "restart":
			if remoteMode() {
				fmt.Fprintf(os.Stderr, "quil restart: not available with --remote (target: %s)\n", remoteDest)
				os.Exit(1)
			}
			restartDaemonCmd()
			launchTUI()
			return
```

- [ ] **Step 7: Branch the connection in `launchTUI`**

In `cmd/quil/main.go`, replace lines 287-302 (from `sockPath := config.SocketPath()` through the closing brace of the auto-start `if`) with:

```go
	sockPath := config.SocketPath()
	log.Printf("config loaded, AutoStart=%v", cfg.Daemon.AutoStart)

	var client *ipc.Client
	var err error
	spawnedButNotReady := false

	if remoteMode() {
		// No local daemon is involved: `quil --stdio` on the far side ensures
		// the remote one. Batch=false so this first dial can prompt for a
		// host-key fingerprint or key passphrase — it runs before tea.NewProgram
		// takes the terminal.
		log.Printf("remote mode: dialing %s over ssh", remoteDest)
		client, err = ipc.NewClientWithDialer(
			context.Background(),
			transport.SSH(remoteDest, transport.SSHOptions{}),
		)
		if err != nil {
			log.Printf("cannot connect to remote daemon %s: %v", remoteDest, err)
			fmt.Fprintf(os.Stderr, "cannot connect to %s: %v\n"+
				"Check that you can run: ssh %s quil --stdio\n", remoteDest, err, remoteDest)
			os.Exit(1)
		}
	} else {
		client, err = ipc.NewClient(sockPath)
		if err != nil && cfg.Daemon.AutoStart {
			log.Printf("daemon not reachable, auto-starting...")
			pid := startDaemon(true) // quiet — no stdout during TUI launch
			if waitForDaemonReady(sockPath, pid) {
				client, err = ipc.NewClient(sockPath)
			} else {
				spawnedButNotReady = true
			}
		}
	}
```

Add `"context"` and `"github.com/artyomsv/quil/internal/transport"` to the imports. The existing `if err != nil { ... os.Exit(1) }` block at lines 303-316 stays as-is — it now only fires on the local path, since the remote path exits on its own with a better message.

- [ ] **Step 8: Add the remote version-gate message**

In `cmd/quil/version_gate.go`, at the start of the `default:` branch (line 51, before the `promptRestartDaemon` call), insert:

```go
		if remoteMode() {
			// The restart path below manages the LOCAL daemon and is guarded
			// against remote mode, so there is nothing to offer here — say what
			// is wrong and how to fix it instead of prompting for an action
			// that cannot run.
			reported := res.DaemonVersion
			if reported == "" {
				reported = "unknown"
			}
			client.Close()
			fmt.Fprintf(os.Stderr,
				"\n"+
					"  Version mismatch with the remote daemon.\n"+
					"\n"+
					"    this TUI:            %s\n"+
					"    daemon at %s: %s\n"+
					"\n"+
					"  Upgrade one of them so both run the same version, then try again.\n"+
					"  To upgrade the remote daemon:\n"+
					"    ssh %s 'quil daemon restart'\n"+
					"\n",
				versionpkg.Current(), remoteDest, reported, remoteDest)
			os.Exit(1)
		}
```

- [ ] **Step 9: Verify the whole build and suite**

Run: `./scripts/dev.sh test`
Expected: PASS.
Run: `./scripts/dev.sh test-race`
Expected: PASS.
Run: `./scripts/dev.sh vet`
Expected: clean.
Run: `./scripts/dev.sh build`
Expected: six binaries produced.

- [ ] **Step 10: Manual end-to-end check without ssh**

This exercises the whole proxy path with no SSH server. Use a short `QUIL_HOME` — a long path breaks the AF_UNIX 108-char limit.

```bash
export QUIL_HOME=/tmp/quiltest
mkdir -p "$QUIL_HOME"
./quil-dev --stdio < /dev/null
```

Expected: it starts a daemon under `/tmp/quiltest`, then exits immediately on stdin EOF. Confirm `/tmp/quiltest/quild.sock` exists and `/tmp/quiltest/quild.log` shows a startup line. Then stop it by PID from `/tmp/quiltest/quild.pid` — **never** via `kill-daemon.sh`.

- [ ] **Step 11: Manual end-to-end check over real ssh (optional but recommended)**

If an SSH-reachable host with quil installed is available:

```bash
./quil-dev --remote <dest>
```

Expected: the TUI attaches and shows the remote workspace. Verify `[dev]` behaviour is unaffected and that `quil daemon stop --remote <dest>` refuses.

- [ ] **Step 12: Commit**

```bash
git add cmd/quil/main.go cmd/quil/remote.go cmd/quil/remote_flag_test.go cmd/quil/version_gate.go
git commit -m "feat(cli): add --remote to attach a TUI over ssh"
```

---

## Self-Review

**Spec coverage.** Phase 1 of the spec lists: dialer seam (Task 1), `internal/transport` with Local and SSH backends (Tasks 3, 4), stdio adapter with working deadlines (Task 2), `quil --stdio` (Task 5), the three remote-mode guards (Task 6), and the remote-specific version-gate message (Task 7). All covered.

Deliberately **not** in Phase 1, per the spec's phasing: reconnect (Phase 2); the four RPCs, async dialog refactor, per-target recent CWDs, empty `AttachPayload.CWD`, and the disabled surfaces (Phase 3). The `ssh -G` `StrictHostKeyChecking` warning is a Phase 1 candidate that was dropped to keep the phase minimal — it is listed below as follow-up rather than silently omitted.

**Known gaps carried forward:**
- No `ssh -G` warning when the user's config has `StrictHostKeyChecking no`.
- No Windows job object for deterministic `ssh.exe` cleanup — `stdioConn.Close` kills and reaps the child, which covers ordinary exit but not a hard TUI crash. Add with Phase 2's reconnect work, where a killed-and-respawned transport makes it matter.
- `stdioConn.Stderr()` is populated but not yet surfaced anywhere; Phase 2's reconnect banner is its consumer.
- Documentation (`docs/` and `README.md`) is not updated in Phase 1 — remote mode is not user-facing until Phase 3 makes pane creation usable.

**Type consistency.** `newStdioConn(cmd, r, w, desc)` is defined in Task 2 and called in Task 4 with that exact signature. `sshArgs(dest, opts)` and `SSHOptions{SSHPath, RemoteCommand, Batch}` are defined and used consistently in Task 4. `remoteDest`, `remoteMode()`, `errRemoteMode`, `exitFn` are defined in Task 6 and used in Tasks 6 and 7. `parseRemoteFlag(args) (dest, rest, err)` is defined and tested in Task 7. `proxyStdio(daemon, in, out)` is defined in Task 5 and tested there.

---

## Execution Handoff

Plan complete. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task with review between tasks.

**2. Inline Execution** — tasks executed in this session with checkpoints.
