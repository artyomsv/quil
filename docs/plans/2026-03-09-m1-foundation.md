# M1: Foundation — Daemon + Shell + TUI

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** `quil` launches a daemon, opens a Bubble Tea TUI with one tab and one pane running a shell. Users can create tabs, split panes, and run shell commands on all three platforms.

**Architecture:** Client-daemon model over IPC. `quild` daemon manages PTY sessions and routes I/O. `quil` client renders a Bubble Tea TUI and forwards keystrokes. IPC uses length-prefixed JSON over Unix domain sockets (Linux/macOS) or Named Pipes (Windows).

**Tech Stack:** Go 1.23+, Bubble Tea v2, charmbracelet/lipgloss, creack/pty (Unix), charmbracelet/x/conpty (Windows), BurntSushi/toml

**Reference:** `PRD.md` (full product requirements), `VISION.md` (project vision)

---

## Task 1: Project Scaffolding

**Files:**
- Create: `go.mod`
- Create: `cmd/quil/main.go`
- Create: `cmd/quild/main.go`
- Create: `.gitignore`

**Step 1: Initialize Git repository**

```bash
cd E:/Projects/Stukans/Prototypes/calyx
git init
```

**Step 2: Create `.gitignore`**

```gitignore
# Binaries
quil
quild
quil.exe
quild.exe
*.exe
*.dll
*.so
*.dylib

# Go
/vendor/

# IDE
.idea/
.vscode/
*.swp
*.swo

# OS
.DS_Store
Thumbs.db

# Build
/dist/
/bin/
```

**Step 3: Initialize Go module**

```bash
go mod init github.com/artyomsv/quil
```

**Step 4: Add core dependencies**

```bash
go get github.com/charmbracelet/bubbletea/v2@latest
go get github.com/charmbracelet/lipgloss/v2@latest
go get github.com/charmbracelet/x/conpty@latest
go get github.com/creack/pty/v2@latest
go get github.com/BurntSushi/toml@latest
go get github.com/google/uuid@latest
```

**Step 5: Create placeholder entry points**

`cmd/quild/main.go`:
```go
package main

import "fmt"

func main() {
	fmt.Println("quild daemon — not yet implemented")
}
```

`cmd/quil/main.go`:
```go
package main

import "fmt"

func main() {
	fmt.Println("quil client — not yet implemented")
}
```

**Step 6: Verify both binaries build**

```bash
go build ./cmd/quil
go build ./cmd/quild
```

Expected: Both compile with zero errors.

**Step 7: Commit**

```bash
git add .
git commit -m "chore: scaffold Go project with dual-binary layout

Initialize go.mod with Bubble Tea v2, creack/pty, lipgloss, toml,
and conpty dependencies. Create cmd/quil and cmd/quild entry points."
```

---

## Task 2: Configuration Package

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Step 1: Write the failing test**

`internal/config/config_test.go`:
```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg := config.Default()

	if cfg.Daemon.SnapshotInterval != "30s" {
		t.Errorf("expected snapshot_interval=30s, got %s", cfg.Daemon.SnapshotInterval)
	}
	if !cfg.Daemon.AutoStart {
		t.Error("expected auto_start=true")
	}
	if cfg.UI.TabDock != "top" {
		t.Errorf("expected tab_dock=top, got %s", cfg.UI.TabDock)
	}
	if cfg.Keybindings.NewTab != "ctrl+t" {
		t.Errorf("expected new_tab=ctrl+t, got %s", cfg.Keybindings.NewTab)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := []byte(`
[daemon]
snapshot_interval = "10s"
auto_start = false

[ui]
tab_dock = "bottom"
`)
	if err := os.WriteFile(cfgPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Daemon.SnapshotInterval != "10s" {
		t.Errorf("expected snapshot_interval=10s, got %s", cfg.Daemon.SnapshotInterval)
	}
	if cfg.Daemon.AutoStart {
		t.Error("expected auto_start=false")
	}
	if cfg.UI.TabDock != "bottom" {
		t.Errorf("expected tab_dock=bottom, got %s", cfg.UI.TabDock)
	}
	// Unset fields keep defaults
	if cfg.Keybindings.NewTab != "ctrl+t" {
		t.Errorf("expected default new_tab=ctrl+t, got %s", cfg.Keybindings.NewTab)
	}
}

func TestQuilDir(t *testing.T) {
	dir := config.QuilDir()
	if dir == "" {
		t.Error("expected non-empty quil dir")
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/config/ -v
```

Expected: FAIL — package doesn't exist yet.

**Step 3: Write implementation**

`internal/config/config.go`:
```go
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Daemon      DaemonConfig      `toml:"daemon"`
	GhostBuffer GhostBufferConfig `toml:"ghost_buffer"`
	Logging     LoggingConfig     `toml:"logging"`
	Security    SecurityConfig    `toml:"security"`
	UI          UIConfig          `toml:"ui"`
	Keybindings KeybindingsConfig `toml:"keybindings"`
}

type DaemonConfig struct {
	SnapshotInterval string `toml:"snapshot_interval"`
	AutoStart        bool   `toml:"auto_start"`
}

type GhostBufferConfig struct {
	MaxLines int  `toml:"max_lines"`
	Dimmed   bool `toml:"dimmed"`
}

type LoggingConfig struct {
	Level     string `toml:"level"`
	MaxSizeMB int    `toml:"max_size_mb"`
	MaxFiles  int    `toml:"max_files"`
}

type SecurityConfig struct {
	EncryptTokens bool `toml:"encrypt_tokens"`
	RedactSecrets bool `toml:"redact_secrets"`
}

type UIConfig struct {
	TabDock string `toml:"tab_dock"`
	Theme   string `toml:"theme"`
}

type KeybindingsConfig struct {
	SplitHorizontal string `toml:"split_horizontal"`
	SplitVertical   string `toml:"split_vertical"`
	NextPane        string `toml:"next_pane"`
	PrevPane        string `toml:"prev_pane"`
	NewTab          string `toml:"new_tab"`
	ClosePane       string `toml:"close_pane"`
	JSONTransform   string `toml:"json_transform"`
	QuickActions    string `toml:"quick_actions"`
}

func Default() Config {
	return Config{
		Daemon: DaemonConfig{
			SnapshotInterval: "30s",
			AutoStart:        true,
		},
		GhostBuffer: GhostBufferConfig{
			MaxLines: 500,
			Dimmed:   true,
		},
		Logging: LoggingConfig{
			Level:     "info",
			MaxSizeMB: 10,
			MaxFiles:  3,
		},
		Security: SecurityConfig{
			EncryptTokens: true,
			RedactSecrets: true,
		},
		UI: UIConfig{
			TabDock: "top",
			Theme:   "default",
		},
		Keybindings: KeybindingsConfig{
			SplitHorizontal: "ctrl+shift+h",
			SplitVertical:   "ctrl+shift+v",
			NextPane:        "ctrl+tab",
			PrevPane:        "ctrl+shift+tab",
			NewTab:          "ctrl+t",
			ClosePane:       "ctrl+w",
			JSONTransform:   "ctrl+j",
			QuickActions:    "ctrl+a",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func QuilDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".quil")
}

func ConfigPath() string {
	return filepath.Join(QuilDir(), "config.toml")
}

func SocketPath() string {
	return filepath.Join(QuilDir(), "quild.sock")
}
```

**Step 4: Run tests**

```bash
go test ./internal/config/ -v
```

Expected: PASS (3 tests).

**Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): add TOML configuration with defaults

Supports loading config from ~/.quil/config.toml with sensible
defaults for daemon, UI, keybindings, logging, and security settings."
```

---

## Task 3: PTY Abstraction Layer

**Files:**
- Create: `internal/pty/session.go`
- Create: `internal/pty/session_unix.go`
- Create: `internal/pty/session_windows.go`
- Create: `internal/pty/session_unix_test.go`

**Step 1: Write the shared interface**

`internal/pty/session.go`:
```go
package pty

// Session represents a pseudo-terminal session wrapping a shell process.
type Session interface {
	// Start launches the given command in a new PTY.
	Start(cmd string, args ...string) error
	// Read reads output from the PTY.
	Read(buf []byte) (int, error)
	// Write sends input to the PTY.
	Write(data []byte) (int, error)
	// Resize changes the PTY window size.
	Resize(rows, cols uint16) error
	// Close terminates the PTY session and cleans up.
	Close() error
	// Pid returns the process ID of the running command.
	Pid() int
}
```

**Step 2: Write Unix implementation**

`internal/pty/session_unix.go`:
```go
//go:build linux || darwin || freebsd

package pty

import (
	"os"
	"os/exec"

	cpty "github.com/creack/pty/v2"
)

type unixSession struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func New() Session {
	return &unixSession{}
}

func (s *unixSession) Start(cmd string, args ...string) error {
	s.cmd = exec.Command(cmd, args...)
	ptmx, err := cpty.Start(s.cmd)
	if err != nil {
		return err
	}
	s.ptmx = ptmx
	return nil
}

func (s *unixSession) Read(buf []byte) (int, error) {
	return s.ptmx.Read(buf)
}

func (s *unixSession) Write(data []byte) (int, error) {
	return s.ptmx.Write(data)
}

func (s *unixSession) Resize(rows, cols uint16) error {
	return cpty.Setsize(s.ptmx, &cpty.Winsize{Rows: rows, Cols: cols})
}

func (s *unixSession) Close() error {
	if s.ptmx != nil {
		s.ptmx.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	return nil
}

func (s *unixSession) Pid() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}
```

**Step 3: Write Windows implementation**

`internal/pty/session_windows.go`:
```go
//go:build windows

package pty

import (
	"io"
	"os/exec"

	"github.com/charmbracelet/x/conpty"
)

type winSession struct {
	cpty *conpty.ConPty
	cmd  *exec.Cmd
}

func New() Session {
	return &winSession{}
}

func (s *winSession) Start(cmd string, args ...string) error {
	cpty, err := conpty.New(80, 24)
	if err != nil {
		return err
	}
	s.cpty = cpty

	s.cmd = exec.Command(cmd, args...)
	s.cmd.Stdin = cpty.InPipe()
	s.cmd.Stdout = cpty.OutPipe()
	s.cmd.Stderr = cpty.OutPipe()

	if err := s.cmd.Start(); err != nil {
		cpty.Close()
		return err
	}
	return nil
}

func (s *winSession) Read(buf []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.EOF
	}
	return s.cpty.OutPipe().Read(buf)
}

func (s *winSession) Write(data []byte) (int, error) {
	if s.cpty == nil {
		return 0, io.ErrClosedPipe
	}
	return s.cpty.InPipe().Write(data)
}

func (s *winSession) Resize(rows, cols uint16) error {
	if s.cpty == nil {
		return nil
	}
	return s.cpty.Resize(int(cols), int(rows))
}

func (s *winSession) Close() error {
	if s.cpty != nil {
		s.cpty.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	return nil
}

func (s *winSession) Pid() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}
```

**Step 4: Write the test (Unix only — Windows tested manually)**

`internal/pty/session_unix_test.go`:
```go
//go:build linux || darwin || freebsd

package pty_test

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/pty"
)

func TestStartAndReadOutput(t *testing.T) {
	s := pty.New()
	err := s.Start("echo", "hello-quil")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.After(3 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for output, got: %q", output.String())
		default:
			n, err := s.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if strings.Contains(output.String(), "hello-quil") {
				return // success
			}
			if err != nil {
				if strings.Contains(output.String(), "hello-quil") {
					return
				}
				t.Fatalf("Read error: %v, output so far: %q", err, output.String())
			}
		}
	}
}

func TestResize(t *testing.T) {
	s := pty.New()
	err := s.Start("sh", "-c", "sleep 1")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	if err := s.Resize(40, 120); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestPid(t *testing.T) {
	s := pty.New()
	err := s.Start("sh", "-c", "sleep 1")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Close()

	pid := s.Pid()
	if pid == 0 {
		t.Error("expected non-zero PID")
	}
}
```

**Step 5: Run tests**

```bash
go test ./internal/pty/ -v
```

Expected: PASS (3 tests on Unix). On Windows, tests are skipped due to build tags.

**Step 6: Verify cross-compilation**

```bash
GOOS=linux go build ./internal/pty/
GOOS=darwin go build ./internal/pty/
GOOS=windows go build ./internal/pty/
```

Expected: All three compile without errors.

**Step 7: Commit**

```bash
git add internal/pty/
git commit -m "feat(pty): add cross-platform PTY session abstraction

Interface with platform-specific implementations:
- Unix (Linux/macOS): creack/pty
- Windows: charmbracelet/x/conpty"
```

---

## Task 4: IPC Protocol

**Files:**
- Create: `internal/ipc/protocol.go`
- Create: `internal/ipc/protocol_test.go`

**Step 1: Write the failing test**

`internal/ipc/protocol_test.go`:
```go
package ipc_test

import (
	"bytes"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestWriteReadMessage(t *testing.T) {
	var buf bytes.Buffer

	msg := &ipc.Message{
		Type: "create_pane",
		Payload: []byte(`{"cwd":"/home/user"}`),
	}

	if err := ipc.WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	got, err := ipc.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if got.Type != msg.Type {
		t.Errorf("Type: got %q, want %q", got.Type, msg.Type)
	}
	if string(got.Payload) != string(msg.Payload) {
		t.Errorf("Payload: got %q, want %q", got.Payload, msg.Payload)
	}
}

func TestWriteReadMultipleMessages(t *testing.T) {
	var buf bytes.Buffer

	messages := []*ipc.Message{
		{Type: "attach", Payload: []byte(`{}`)},
		{Type: "pane_input", Payload: []byte(`{"pane_id":"p1","data":"ls\n"}`)},
		{Type: "pane_output", Payload: []byte(`{"pane_id":"p1","data":"file1 file2"}`)},
	}

	for _, m := range messages {
		if err := ipc.WriteMessage(&buf, m); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}

	for i, want := range messages {
		got, err := ipc.ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage %d failed: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("msg %d Type: got %q, want %q", i, got.Type, want.Type)
		}
	}
}

func TestMessageTypes(t *testing.T) {
	// Verify all message type constants exist
	types := []string{
		ipc.MsgAttach,
		ipc.MsgDetach,
		ipc.MsgShutdown,
		ipc.MsgHeartbeat,
		ipc.MsgCreatePane,
		ipc.MsgDestroyPane,
		ipc.MsgResizePane,
		ipc.MsgPaneInput,
		ipc.MsgPaneOutput,
		ipc.MsgCreateTab,
		ipc.MsgDestroyTab,
		ipc.MsgSwitchTab,
		ipc.MsgWorkspaceState,
		ipc.MsgStateUpdate,
	}
	for _, typ := range types {
		if typ == "" {
			t.Error("found empty message type constant")
		}
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/ipc/ -v
```

Expected: FAIL — package doesn't exist.

**Step 3: Write implementation**

`internal/ipc/protocol.go`:
```go
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message type constants
const (
	// Lifecycle
	MsgAttach    = "attach"
	MsgDetach    = "detach"
	MsgShutdown  = "shutdown"
	MsgHeartbeat = "heartbeat"

	// Session control (Client → Daemon)
	MsgCreatePane  = "create_pane"
	MsgDestroyPane = "destroy_pane"
	MsgResizePane  = "resize_pane"

	// Tab control (Client → Daemon)
	MsgCreateTab  = "create_tab"
	MsgDestroyTab = "destroy_tab"
	MsgSwitchTab  = "switch_tab"

	// I/O (bidirectional)
	MsgPaneInput  = "pane_input"
	MsgPaneOutput = "pane_output"

	// State sync (Daemon → Client)
	MsgWorkspaceState = "workspace_state"
	MsgStateUpdate    = "state_update"
)

// Message is the wire format for IPC communication.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Payload types

type CreatePanePayload struct {
	TabID string `json:"tab_id"`
	CWD   string `json:"cwd"`
}

type DestroyPanePayload struct {
	PaneID string `json:"pane_id"`
}

type ResizePanePayload struct {
	PaneID string `json:"pane_id"`
	Rows   uint16 `json:"rows"`
	Cols   uint16 `json:"cols"`
}

type PaneInputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
}

type PaneOutputPayload struct {
	PaneID string `json:"pane_id"`
	Data   []byte `json:"data"`
}

type CreateTabPayload struct {
	Name string `json:"name"`
}

type DestroyTabPayload struct {
	TabID string `json:"tab_id"`
}

type SwitchTabPayload struct {
	TabID string `json:"tab_id"`
}

// NewMessage creates a Message with a typed payload.
func NewMessage(typ string, payload any) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{Type: typ, Payload: data}, nil
}

// WriteMessage writes a length-prefixed JSON message to w.
// Format: [4 bytes uint32 big-endian length][JSON payload]
func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed JSON message from r.
func ReadMessage(r io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}

	if length > 10*1024*1024 { // 10MB max
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// DecodePayload unmarshals the message payload into the given target.
func (m *Message) DecodePayload(target any) error {
	return json.Unmarshal(m.Payload, target)
}
```

**Step 4: Run tests**

```bash
go test ./internal/ipc/ -v
```

Expected: PASS (3 tests).

**Step 5: Commit**

```bash
git add internal/ipc/
git commit -m "feat(ipc): add length-prefixed JSON protocol

Message types for lifecycle, session control, tab management,
I/O forwarding, and state sync. 4-byte big-endian length framing."
```

---

## Task 5: IPC Transport (Server + Client)

**Files:**
- Create: `internal/ipc/server.go`
- Create: `internal/ipc/client.go`
- Create: `internal/ipc/transport_test.go`

**Step 1: Write the failing test**

`internal/ipc/transport_test.go`:
```go
package ipc_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestServerClientRoundTrip(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	var received *ipc.Message
	var mu sync.Mutex
	done := make(chan struct{})

	handler := func(conn *ipc.Conn, msg *ipc.Message) {
		mu.Lock()
		received = msg
		mu.Unlock()

		resp, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
			PaneID: "p1",
			Data:   []byte("hello back"),
		})
		conn.Send(resp)
		close(done)
	}

	srv := ipc.NewServer(sockPath, handler)
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Stop()

	client, err := ipc.NewClient(sockPath)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer client.Close()

	msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
		PaneID: "p1",
		Data:   []byte("ls\n"),
	})
	if err := client.Send(msg); err != nil {
		t.Fatalf("client send: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server to receive message")
	}

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("server never received message")
	}
	if received.Type != ipc.MsgPaneInput {
		t.Errorf("type: got %q, want %q", received.Type, ipc.MsgPaneInput)
	}

	resp, err := client.Receive()
	if err != nil {
		t.Fatalf("client receive: %v", err)
	}
	if resp.Type != ipc.MsgPaneOutput {
		t.Errorf("response type: got %q, want %q", resp.Type, ipc.MsgPaneOutput)
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/ipc/ -v -run TestServerClient
```

Expected: FAIL — `NewServer`, `NewClient`, `Conn` don't exist.

**Step 3: Write server implementation**

`internal/ipc/server.go`:
```go
package ipc

import (
	"log"
	"net"
	"os"
	"sync"
)

// MessageHandler is called for each incoming message on a connection.
type MessageHandler func(conn *Conn, msg *Message)

// Conn wraps a net.Conn with message framing.
type Conn struct {
	raw net.Conn
	mu  sync.Mutex
}

func newConn(raw net.Conn) *Conn {
	return &Conn{raw: raw}
}

func (c *Conn) Send(msg *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteMessage(c.raw, msg)
}

func (c *Conn) Receive() (*Message, error) {
	return ReadMessage(c.raw)
}

func (c *Conn) Close() error {
	return c.raw.Close()
}

// Server listens for client connections over a Unix socket.
type Server struct {
	path     string
	handler  MessageHandler
	listener net.Listener
	conns    []*Conn
	mu       sync.Mutex
	done     chan struct{}
}

func NewServer(socketPath string, handler MessageHandler) *Server {
	return &Server{
		path:    socketPath,
		handler: handler,
		done:    make(chan struct{}),
	}
}

func (s *Server) Start() error {
	os.Remove(s.path) // Clean up stale socket

	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return err
	}
	s.listener = ln

	go s.acceptLoop()
	return nil
}

func (s *Server) Stop() error {
	close(s.done)
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	return s.listener.Close()
}

// Broadcast sends a message to all connected clients.
func (s *Server) Broadcast(msg *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Send(msg)
	}
}

func (s *Server) acceptLoop() {
	for {
		raw, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}

		conn := newConn(raw)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()

		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn *Conn) {
	defer func() {
		conn.Close()
		s.removeConn(conn)
	}()

	for {
		msg, err := conn.Receive()
		if err != nil {
			return
		}
		s.handler(conn, msg)
	}
}

func (s *Server) removeConn(conn *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.conns {
		if c == conn {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			return
		}
	}
}
```

**Step 4: Write client implementation**

`internal/ipc/client.go`:
```go
package ipc

import "net"

// Client connects to the daemon over a Unix socket.
type Client struct {
	conn *Conn
}

func NewClient(socketPath string) (*Client, error) {
	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: newConn(raw)}, nil
}

func (c *Client) Send(msg *Message) error {
	return c.conn.Send(msg)
}

func (c *Client) Receive() (*Message, error) {
	return c.conn.Receive()
}

func (c *Client) Close() error {
	return c.conn.Close()
}
```

**Step 5: Run tests**

```bash
go test ./internal/ipc/ -v
```

Expected: PASS (4 tests).

**Step 6: Commit**

```bash
git add internal/ipc/
git commit -m "feat(ipc): add Unix socket server and client transport

Server accepts multiple clients, routes messages to handler.
Client connects and sends/receives framed messages.
Server supports broadcast to all connected clients."
```

---

## Task 6: Daemon Session Manager

**Files:**
- Create: `internal/daemon/session.go`
- Create: `internal/daemon/session_test.go`

**Step 1: Write the failing test**

`internal/daemon/session_test.go`:
```go
package daemon_test

import (
	"testing"

	"github.com/artyomsv/quil/internal/daemon"
)

func TestSessionManagerCreateTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")

	if tab.ID == "" {
		t.Error("expected non-empty tab ID")
	}
	if tab.Name != "test-tab" {
		t.Errorf("name: got %q, want %q", tab.Name, "test-tab")
	}

	tabs := sm.Tabs()
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
}

func TestSessionManagerCreatePane(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	pane, err := sm.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	if pane.ID == "" {
		t.Error("expected non-empty pane ID")
	}

	panes := sm.Panes(tab.ID)
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
}

func TestSessionManagerDestroyPane(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	pane, _ := sm.CreatePane(tab.ID, "")

	if err := sm.DestroyPane(pane.ID); err != nil {
		t.Fatalf("DestroyPane: %v", err)
	}

	panes := sm.Panes(tab.ID)
	if len(panes) != 0 {
		t.Fatalf("expected 0 panes, got %d", len(panes))
	}
}

func TestSessionManagerDestroyTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab := sm.CreateTab("test-tab")
	sm.CreatePane(tab.ID, "")

	if err := sm.DestroyTab(tab.ID); err != nil {
		t.Fatalf("DestroyTab: %v", err)
	}

	tabs := sm.Tabs()
	if len(tabs) != 0 {
		t.Fatalf("expected 0 tabs, got %d", len(tabs))
	}
}

func TestSessionManagerActiveTab(t *testing.T) {
	sm := daemon.NewSessionManager()
	tab1 := sm.CreateTab("tab-1")
	tab2 := sm.CreateTab("tab-2")

	if sm.ActiveTabID() != tab1.ID {
		t.Error("expected first tab to be active")
	}

	sm.SwitchTab(tab2.ID)
	if sm.ActiveTabID() != tab2.ID {
		t.Errorf("expected tab2 active, got %s", sm.ActiveTabID())
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/daemon/ -v
```

Expected: FAIL — package doesn't exist.

**Step 3: Write implementation**

`internal/daemon/session.go`:
```go
package daemon

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
	apty "github.com/artyomsv/quil/internal/pty"
)

type Tab struct {
	ID    string
	Name  string
	Panes []string // Pane IDs in order
}

type Pane struct {
	ID    string
	TabID string
	CWD   string
	PTY   apty.Session
}

type SessionManager struct {
	tabs      map[string]*Tab
	tabOrder  []string
	panes     map[string]*Pane
	activeTab string
	mu        sync.RWMutex
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		tabs:  make(map[string]*Tab),
		panes: make(map[string]*Pane),
	}
}

func (sm *SessionManager) CreateTab(name string) *Tab {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := "tab-" + uuid.New().String()[:8]
	tab := &Tab{ID: id, Name: name}
	sm.tabs[id] = tab
	sm.tabOrder = append(sm.tabOrder, id)

	if sm.activeTab == "" {
		sm.activeTab = id
	}
	return tab
}

func (sm *SessionManager) DestroyTab(tabID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return fmt.Errorf("tab not found: %s", tabID)
	}

	// Destroy all panes in the tab
	for _, paneID := range tab.Panes {
		if pane, ok := sm.panes[paneID]; ok {
			if pane.PTY != nil {
				pane.PTY.Close()
			}
			delete(sm.panes, paneID)
		}
	}

	delete(sm.tabs, tabID)
	for i, id := range sm.tabOrder {
		if id == tabID {
			sm.tabOrder = append(sm.tabOrder[:i], sm.tabOrder[i+1:]...)
			break
		}
	}

	if sm.activeTab == tabID {
		if len(sm.tabOrder) > 0 {
			sm.activeTab = sm.tabOrder[0]
		} else {
			sm.activeTab = ""
		}
	}
	return nil
}

func (sm *SessionManager) CreatePane(tabID string, cwd string) (*Pane, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return nil, fmt.Errorf("tab not found: %s", tabID)
	}

	id := "pane-" + uuid.New().String()[:8]
	pane := &Pane{
		ID:    id,
		TabID: tabID,
		CWD:   cwd,
	}

	sm.panes[id] = pane
	tab.Panes = append(tab.Panes, id)
	return pane, nil
}

func (sm *SessionManager) DestroyPane(paneID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	pane, ok := sm.panes[paneID]
	if !ok {
		return fmt.Errorf("pane not found: %s", paneID)
	}

	if pane.PTY != nil {
		pane.PTY.Close()
	}

	// Remove from tab's pane list
	if tab, ok := sm.tabs[pane.TabID]; ok {
		for i, id := range tab.Panes {
			if id == paneID {
				tab.Panes = append(tab.Panes[:i], tab.Panes[i+1:]...)
				break
			}
		}
	}

	delete(sm.panes, paneID)
	return nil
}

func (sm *SessionManager) Tabs() []*Tab {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tabs := make([]*Tab, 0, len(sm.tabOrder))
	for _, id := range sm.tabOrder {
		tabs = append(tabs, sm.tabs[id])
	}
	return tabs
}

func (sm *SessionManager) Tab(id string) *Tab {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.tabs[id]
}

func (sm *SessionManager) Panes(tabID string) []*Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tab, ok := sm.tabs[tabID]
	if !ok {
		return nil
	}

	panes := make([]*Pane, 0, len(tab.Panes))
	for _, id := range tab.Panes {
		if pane, ok := sm.panes[id]; ok {
			panes = append(panes, pane)
		}
	}
	return panes
}

func (sm *SessionManager) Pane(id string) *Pane {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.panes[id]
}

func (sm *SessionManager) ActiveTabID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeTab
}

func (sm *SessionManager) SwitchTab(tabID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.tabs[tabID]; ok {
		sm.activeTab = tabID
	}
}
```

**Step 4: Run tests**

```bash
go test ./internal/daemon/ -v
```

Expected: PASS (5 tests).

**Step 5: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): add session manager for tabs and panes

Manages tab/pane lifecycle with thread-safe operations.
Tracks active tab, pane ordering, and PTY associations."
```

---

## Task 7: Daemon Core

**Files:**
- Create: `internal/daemon/daemon.go`

**Step 1: Write daemon that wires IPC server + session manager**

`internal/daemon/daemon.go`:
```go
package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

type Daemon struct {
	cfg     config.Config
	server  *ipc.Server
	session *SessionManager
}

func New(cfg config.Config) *Daemon {
	return &Daemon{
		cfg:     cfg,
		session: NewSessionManager(),
	}
}

func (d *Daemon) Start() error {
	// Ensure ~/.quil/ exists
	quilDir := config.QuilDir()
	if err := os.MkdirAll(quilDir, 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}

	sockPath := config.SocketPath()
	d.server = ipc.NewServer(sockPath, d.handleMessage)

	if err := d.server.Start(); err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}

	log.Printf("quild started, listening on %s", sockPath)
	return nil
}

func (d *Daemon) Wait() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down...")
	d.Stop()
}

func (d *Daemon) Stop() {
	if d.server != nil {
		d.server.Stop()
	}
	// Clean up all PTY sessions
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			if pane.PTY != nil {
				pane.PTY.Close()
			}
		}
	}
}

func (d *Daemon) handleMessage(conn *ipc.Conn, msg *ipc.Message) {
	switch msg.Type {
	case ipc.MsgAttach:
		d.handleAttach(conn)
	case ipc.MsgCreateTab:
		d.handleCreateTab(conn, msg)
	case ipc.MsgDestroyTab:
		d.handleDestroyTab(conn, msg)
	case ipc.MsgSwitchTab:
		d.handleSwitchTab(conn, msg)
	case ipc.MsgCreatePane:
		d.handleCreatePane(conn, msg)
	case ipc.MsgDestroyPane:
		d.handleDestroyPane(conn, msg)
	case ipc.MsgPaneInput:
		d.handlePaneInput(msg)
	case ipc.MsgResizePane:
		d.handleResizePane(msg)
	case ipc.MsgShutdown:
		d.Stop()
		os.Exit(0)
	}
}

func (d *Daemon) handleAttach(conn *ipc.Conn) {
	// Send full workspace state to the newly attached client
	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	conn.Send(resp)
}

func (d *Daemon) handleCreateTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreateTabPayload
	msg.DecodePayload(&payload)

	tab := d.session.CreateTab(payload.Name)

	update := map[string]any{"action": "tab_created", "tab_id": tab.ID, "name": tab.Name}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleDestroyTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.DestroyTabPayload
	msg.DecodePayload(&payload)
	d.session.DestroyTab(payload.TabID)

	update := map[string]any{"action": "tab_destroyed", "tab_id": payload.TabID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleSwitchTab(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.SwitchTabPayload
	msg.DecodePayload(&payload)
	d.session.SwitchTab(payload.TabID)
}

func (d *Daemon) handleCreatePane(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.CreatePanePayload
	msg.DecodePayload(&payload)

	cwd := payload.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	pane, err := d.session.CreatePane(payload.TabID, cwd)
	if err != nil {
		log.Printf("create pane error: %v", err)
		return
	}

	// Start PTY with default shell
	shell := defaultShell()
	ptySession := apty.New()
	if err := ptySession.Start(shell); err != nil {
		log.Printf("start PTY error: %v", err)
		return
	}
	pane.PTY = ptySession

	// Stream PTY output to all clients
	go d.streamPTYOutput(pane.ID, ptySession)

	update := map[string]any{"action": "pane_created", "pane_id": pane.ID, "tab_id": payload.TabID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handleDestroyPane(conn *ipc.Conn, msg *ipc.Message) {
	var payload ipc.DestroyPanePayload
	msg.DecodePayload(&payload)
	d.session.DestroyPane(payload.PaneID)

	update := map[string]any{"action": "pane_destroyed", "pane_id": payload.PaneID}
	resp, _ := ipc.NewMessage(ipc.MsgStateUpdate, update)
	d.server.Broadcast(resp)
}

func (d *Daemon) handlePaneInput(msg *ipc.Message) {
	var payload ipc.PaneInputPayload
	msg.DecodePayload(&payload)

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Write(payload.Data)
}

func (d *Daemon) handleResizePane(msg *ipc.Message) {
	var payload ipc.ResizePanePayload
	msg.DecodePayload(&payload)

	pane := d.session.Pane(payload.PaneID)
	if pane == nil || pane.PTY == nil {
		return
	}
	pane.PTY.Resize(payload.Rows, payload.Cols)
}

func (d *Daemon) streamPTYOutput(paneID string, pty apty.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			msg, _ := ipc.NewMessage(ipc.MsgPaneOutput, ipc.PaneOutputPayload{
				PaneID: paneID,
				Data:   data,
			})
			d.server.Broadcast(msg)
		}
		if err != nil {
			return
		}
	}
}

func (d *Daemon) buildWorkspaceState() map[string]any {
	tabs := d.session.Tabs()
	tabList := make([]map[string]any, 0, len(tabs))
	paneList := make([]map[string]any, 0)

	for _, tab := range tabs {
		paneIDs := make([]string, len(tab.Panes))
		copy(paneIDs, tab.Panes)
		tabList = append(tabList, map[string]any{
			"id":    tab.ID,
			"name":  tab.Name,
			"panes": paneIDs,
		})

		for _, pane := range d.session.Panes(tab.ID) {
			paneList = append(paneList, map[string]any{
				"id":     pane.ID,
				"tab_id": pane.TabID,
				"cwd":    pane.CWD,
			})
		}
	}

	return map[string]any{
		"active_tab": d.session.ActiveTabID(),
		"tabs":       tabList,
		"panes":      paneList,
	}
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("pwsh.exe"); err == nil {
			return ps
		}
		return "cmd.exe"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}
```

Note: Add `"os/exec"` to imports and `"encoding/json"` can be removed if unused. The `json` import is already present but `encoding/json` in the import block should be kept for `json.RawMessage` usage by `ipc.NewMessage`.

**Step 2: Fix imports — add missing `os/exec`**

Ensure the import block includes:
```go
import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)
```

Remove `"encoding/json"` and `"path/filepath"` if unused — `go vet` will flag them.

**Step 3: Verify it compiles**

```bash
go build ./internal/daemon/
```

Expected: Compiles cleanly.

**Step 4: Commit**

```bash
git add internal/daemon/daemon.go
git commit -m "feat(daemon): add core daemon with IPC message routing

Wires IPC server to session manager. Handles tab/pane lifecycle,
PTY I/O streaming, pane resize, and workspace state sync."
```

---

## Task 8: Daemon Entry Point

**Files:**
- Modify: `cmd/quild/main.go`

**Step 1: Write daemon main**

`cmd/quild/main.go`:
```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
)

func main() {
	cfg := config.Default()

	// Try loading user config
	cfgPath := config.ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			log.Printf("warning: failed to load config: %v", err)
		} else {
			cfg = loaded
		}
	}

	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	d.Wait()
}
```

**Step 2: Verify it builds**

```bash
go build -o bin/quild ./cmd/quild
```

Expected: Binary compiles cleanly.

**Step 3: Commit**

```bash
git add cmd/quild/main.go
git commit -m "feat(quild): add daemon entry point

Loads config, starts daemon, waits for SIGINT/SIGTERM."
```

---

## Task 9: TUI Model — Root + Pane Rendering

**Files:**
- Create: `internal/tui/model.go`
- Create: `internal/tui/pane.go`
- Create: `internal/tui/tab.go`
- Create: `internal/tui/styles.go`

**Step 1: Create styles**

`internal/tui/styles.go`:
```go
package tui

import "github.com/charmbracelet/lipgloss/v2"

var (
	activeTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("230")).
		Background(lipgloss.Color("57")).
		Padding(0, 1)

	inactiveTabStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Background(lipgloss.Color("238")).
		Padding(0, 1)

	activePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("57"))

	inactivePaneBorder = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238"))

	statusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Background(lipgloss.Color("236")).
		Padding(0, 1)
)
```

**Step 2: Create pane model**

`internal/tui/pane.go`:
```go
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss/v2"
)

type PaneModel struct {
	ID     string
	Name   string
	Output strings.Builder
	Width  int
	Height int
	Active bool
}

func NewPaneModel(id string) *PaneModel {
	return &PaneModel{
		ID:   id,
		Name: id,
	}
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.Output.Write(data)
}

func (p *PaneModel) View() string {
	style := inactivePaneBorder
	if p.Active {
		style = activePaneBorder
	}

	// Calculate inner dimensions (subtract border)
	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.visibleContent(innerW, innerH)

	return style.
		Width(innerW).
		Height(innerH).
		Render(content)
}

func (p *PaneModel) visibleContent(width, height int) string {
	raw := p.Output.String()
	lines := strings.Split(raw, "\n")

	// Show only the last `height` lines
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}

	// Truncate long lines
	for i, line := range lines {
		if len(line) > width {
			lines[i] = line[:width]
		}
	}

	// Pad to fill height
	for len(lines) < height {
		lines = append(lines, "")
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
```

**Step 3: Create tab model**

`internal/tui/tab.go`:
```go
package tui

import (
	"github.com/charmbracelet/lipgloss/v2"
)

type TabModel struct {
	ID         string
	Name       string
	Panes      []*PaneModel
	ActivePane int
	Width      int
	Height     int
}

func NewTabModel(id, name string) *TabModel {
	return &TabModel{
		ID:   id,
		Name: name,
	}
}

func (t *TabModel) AddPane(pane *PaneModel) {
	t.Panes = append(t.Panes, pane)
}

func (t *TabModel) ActivePaneModel() *PaneModel {
	if len(t.Panes) == 0 {
		return nil
	}
	if t.ActivePane >= len(t.Panes) {
		t.ActivePane = 0
	}
	return t.Panes[t.ActivePane]
}

func (t *TabModel) NextPane() {
	if len(t.Panes) > 0 {
		t.Panes[t.ActivePane].Active = false
		t.ActivePane = (t.ActivePane + 1) % len(t.Panes)
		t.Panes[t.ActivePane].Active = true
	}
}

func (t *TabModel) PrevPane() {
	if len(t.Panes) > 0 {
		t.Panes[t.ActivePane].Active = false
		t.ActivePane = (t.ActivePane - 1 + len(t.Panes)) % len(t.Panes)
		t.Panes[t.ActivePane].Active = true
	}
}

func (t *TabModel) Resize(w, h int) {
	t.Width = w
	t.Height = h

	if len(t.Panes) == 0 {
		return
	}

	// Simple horizontal split: divide width equally
	paneW := w / len(t.Panes)
	for i, pane := range t.Panes {
		pane.Width = paneW
		pane.Height = h
		// Give remaining pixels to last pane
		if i == len(t.Panes)-1 {
			pane.Width = w - paneW*(len(t.Panes)-1)
		}
	}
}

func (t *TabModel) View() string {
	if len(t.Panes) == 0 {
		return ""
	}

	views := make([]string, len(t.Panes))
	for i, pane := range t.Panes {
		views[i] = pane.View()
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, views...)
}
```

**Step 4: Create root model**

`internal/tui/model.go`:
```go
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// Messages from daemon
type PaneOutputMsg struct {
	PaneID string
	Data   []byte
}

type WorkspaceStateMsg struct {
	ActiveTab string
	Tabs      []TabInfo
	Panes     []PaneInfo
}

type TabInfo struct {
	ID    string
	Name  string
	Panes []string
}

type PaneInfo struct {
	ID    string
	TabID string
	CWD   string
}

type Model struct {
	tabs      []*TabModel
	activeTab int
	width     int
	height    int
	client    *ipc.Client
}

func NewModel(client *ipc.Client) Model {
	return Model{
		client: client,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.attachToDaemon(),
		m.listenForMessages(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeTabs()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case PaneOutputMsg:
		m.handlePaneOutput(msg)
		return m, m.listenForMessages()

	case WorkspaceStateMsg:
		m.applyWorkspaceState(msg)
		m.resizeTabs()
		return m, m.listenForMessages()
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Connecting to quild..."
	}

	var sections []string

	// Tab bar (1 line)
	sections = append(sections, m.renderTabBar())

	// Active tab content
	tabH := m.height - 2 // tab bar + status bar
	if m.activeTab < len(m.tabs) {
		tab := m.tabs[m.activeTab]
		tab.Resize(m.width, tabH)
		sections = append(sections, tab.View())
	}

	// Status bar
	sections = append(sections, m.renderStatusBar())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit

	case "ctrl+t":
		return m, m.createTab()

	case "ctrl+w":
		return m, m.closeActivePane()

	case "ctrl+tab":
		if tab := m.activeTabModel(); tab != nil {
			tab.NextPane()
		}
		return m, nil

	case "ctrl+shift+tab":
		if tab := m.activeTabModel(); tab != nil {
			tab.PrevPane()
		}
		return m, nil

	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5",
		"alt+6", "alt+7", "alt+8", "alt+9":
		idx := int(key[len(key)-1] - '1')
		if idx < len(m.tabs) {
			m.activeTab = idx
		}
		return m, nil

	default:
		// Forward keystrokes to active pane
		return m, m.forwardInput(msg)
	}
}

func (m *Model) handlePaneOutput(msg PaneOutputMsg) {
	for _, tab := range m.tabs {
		for _, pane := range tab.Panes {
			if pane.ID == msg.PaneID {
				pane.AppendOutput(msg.Data)
				return
			}
		}
	}
}

func (m *Model) applyWorkspaceState(state WorkspaceStateMsg) {
	m.tabs = nil
	paneMap := make(map[string]*PaneInfo)
	for i := range state.Panes {
		paneMap[state.Panes[i].ID] = &state.Panes[i]
	}

	for _, tabInfo := range state.Tabs {
		tab := NewTabModel(tabInfo.ID, tabInfo.Name)
		for _, paneID := range tabInfo.Panes {
			pane := NewPaneModel(paneID)
			if info, ok := paneMap[paneID]; ok {
				pane.Name = info.CWD
			}
			tab.AddPane(pane)
		}
		if len(tab.Panes) > 0 {
			tab.Panes[0].Active = true
		}
		m.tabs = append(m.tabs, tab)
	}

	// Set active tab
	for i, tab := range m.tabs {
		if tab.ID == state.ActiveTab {
			m.activeTab = i
			break
		}
	}
}

func (m *Model) resizeTabs() {
	tabH := m.height - 2
	for _, tab := range m.tabs {
		tab.Resize(m.width, tabH)
	}
}

func (m Model) activeTabModel() *TabModel {
	if m.activeTab < len(m.tabs) {
		return m.tabs[m.activeTab]
	}
	return nil
}

func (m Model) renderTabBar() string {
	var tabs []string
	for i, tab := range m.tabs {
		style := inactiveTabStyle
		if i == m.activeTab {
			style = activeTabStyle
		}
		tabs = append(tabs, style.Render(tab.Name))
	}
	bar := strings.Join(tabs, " ")
	return lipgloss.NewStyle().Width(m.width).Render(bar)
}

func (m Model) renderStatusBar() string {
	status := "quil"
	if tab := m.activeTabModel(); tab != nil {
		if pane := tab.ActivePaneModel(); pane != nil {
			status = pane.Name
		}
	}
	return statusBarStyle.Width(m.width).Render(status)
}

// Daemon communication commands

func (m Model) attachToDaemon() tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgAttach, nil)
		m.client.Send(msg)
		return nil
	}
}

func (m Model) listenForMessages() tea.Cmd {
	return func() tea.Msg {
		msg, err := m.client.Receive()
		if err != nil {
			return nil
		}

		switch msg.Type {
		case ipc.MsgPaneOutput:
			var payload ipc.PaneOutputPayload
			msg.DecodePayload(&payload)
			return PaneOutputMsg{PaneID: payload.PaneID, Data: payload.Data}

		case ipc.MsgWorkspaceState:
			var raw map[string]any
			msg.DecodePayload(&raw)
			return m.parseWorkspaceState(raw)
		}

		return nil
	}
}

func (m Model) parseWorkspaceState(raw map[string]any) WorkspaceStateMsg {
	state := WorkspaceStateMsg{}
	if at, ok := raw["active_tab"].(string); ok {
		state.ActiveTab = at
	}
	// Parse tabs and panes from the raw map
	// This is a simplified parser — production code should use proper struct unmarshaling
	if tabs, ok := raw["tabs"].([]any); ok {
		for _, t := range tabs {
			if tm, ok := t.(map[string]any); ok {
				ti := TabInfo{
					ID:   tm["id"].(string),
					Name: tm["name"].(string),
				}
				if panes, ok := tm["panes"].([]any); ok {
					for _, p := range panes {
						ti.Panes = append(ti.Panes, p.(string))
					}
				}
				state.Tabs = append(state.Tabs, ti)
			}
		}
	}
	if panes, ok := raw["panes"].([]any); ok {
		for _, p := range panes {
			if pm, ok := p.(map[string]any); ok {
				pi := PaneInfo{
					ID:    pm["id"].(string),
					TabID: pm["tab_id"].(string),
				}
				if cwd, ok := pm["cwd"].(string); ok {
					pi.CWD = cwd
				}
				state.Panes = append(state.Panes, pi)
			}
		}
	}
	return state
}

func (m Model) createTab() tea.Cmd {
	return func() tea.Msg {
		msg, _ := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{
			Name: "New Tab",
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) closeActivePane() tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}
		msg, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{
			PaneID: pane.ID,
		})
		m.client.Send(msg)
		return nil
	}
}

func (m Model) forwardInput(keyMsg tea.KeyMsg) tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		pane := tab.ActivePaneModel()
		if pane == nil {
			return nil
		}

		// Convert key to bytes
		data := []byte(keyMsg.String())
		if keyMsg.String() == "enter" {
			data = []byte("\n")
		} else if keyMsg.String() == "backspace" {
			data = []byte{0x7f}
		} else if keyMsg.String() == "tab" {
			data = []byte("\t")
		} else if keyMsg.String() == "space" {
			data = []byte(" ")
		} else if len(keyMsg.String()) == 1 {
			data = []byte(keyMsg.String())
		}

		msg, _ := ipc.NewMessage(ipc.MsgPaneInput, ipc.PaneInputPayload{
			PaneID: pane.ID,
			Data:   data,
		})
		m.client.Send(msg)
		return nil
	}
}
```

**Step 2: Verify it compiles**

```bash
go build ./internal/tui/
```

Expected: Compiles cleanly.

**Step 3: Commit**

```bash
git add internal/tui/
git commit -m "feat(tui): add Bubble Tea TUI with tabs, panes, and IPC integration

Root model handles keyboard routing, tab bar, status bar.
Pane model renders PTY output with bordered viewports.
Tab model manages pane layout with horizontal splits."
```

---

## Task 10: Client Entry Point with CLI

**Files:**
- Modify: `cmd/quil/main.go`

**Step 1: Write client main with subcommands**

`cmd/quil/main.go`:
```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			handleDaemon()
			return
		case "version":
			fmt.Println("quil v0.1.0")
			return
		}
	}

	// Default: launch TUI
	launchTUI()
}

func handleDaemon() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil daemon [start|stop]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "start":
		startDaemon()
	case "stop":
		stopDaemon()
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func startDaemon() {
	sockPath := config.SocketPath()

	// Check if daemon is already running
	if client, err := ipc.NewClient(sockPath); err == nil {
		client.Close()
		fmt.Println("daemon already running")
		return
	}

	// Find quild binary
	quild, err := exec.LookPath("quild")
	if err != nil {
		// Try relative to current binary
		quild = "quild"
	}

	cmd := exec.Command(quild)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	// Release the process
	cmd.Process.Release()
	fmt.Printf("daemon started (pid %d)\n", cmd.Process.Pid)
}

func stopDaemon() {
	sockPath := config.SocketPath()
	client, err := ipc.NewClient(sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon not running")
		os.Exit(1)
	}
	defer client.Close()

	msg, _ := ipc.NewMessage(ipc.MsgShutdown, nil)
	client.Send(msg)
	fmt.Println("daemon stopped")
}

func launchTUI() {
	sockPath := config.SocketPath()

	// Auto-start daemon if configured
	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}

	// Try connecting; auto-start if needed
	client, err := ipc.NewClient(sockPath)
	if err != nil && cfg.Daemon.AutoStart {
		startDaemon()
		// Wait for daemon to be ready
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			client, err = ipc.NewClient(sockPath)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'quil daemon start' first.\n", err)
		os.Exit(1)
	}
	defer client.Close()

	model := tui.NewModel(client)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

**Step 2: Verify it builds**

```bash
go build -o bin/quil ./cmd/quil
go build -o bin/quild ./cmd/quild
```

Expected: Both binaries compile cleanly.

**Step 3: Commit**

```bash
git add cmd/
git commit -m "feat(cli): add client entry point with daemon start/stop

Subcommands: daemon start, daemon stop, version.
Default action launches TUI with auto-start daemon support."
```

---

## Task 11: Integration — First End-to-End Run

**Step 1: Run `go vet` and fix any issues**

```bash
go vet ./...
```

Expected: Clean output (or fix any issues flagged).

**Step 2: Run all tests**

```bash
go test ./... -v
```

Expected: All tests pass.

**Step 3: Build both binaries**

```bash
go build -o bin/quild ./cmd/quild
go build -o bin/quil ./cmd/quil
```

**Step 4: Manual smoke test**

```bash
# Terminal 1: Start daemon
./bin/quild

# Terminal 2: Launch client
./bin/quil
```

Expected: TUI opens with tab bar and status bar. No panes yet (daemon creates a default tab+pane on attach — this may need adjustment).

**Step 5: Fix — ensure daemon creates a default workspace on first attach**

If no tabs exist when a client attaches, the daemon should create a default tab with one pane. Add this logic to `handleAttach` in `internal/daemon/daemon.go`:

```go
func (d *Daemon) handleAttach(conn *ipc.Conn) {
	// Create default workspace if empty
	if len(d.session.Tabs()) == 0 {
		tab := d.session.CreateTab("Shell")
		cwd, _ := os.Getwd()
		pane, _ := d.session.CreatePane(tab.ID, cwd)

		shell := defaultShell()
		ptySession := apty.New()
		if err := ptySession.Start(shell); err == nil {
			pane.PTY = ptySession
			go d.streamPTYOutput(pane.ID, ptySession)
		}
	}

	state := d.buildWorkspaceState()
	resp, _ := ipc.NewMessage(ipc.MsgWorkspaceState, state)
	conn.Send(resp)
}
```

**Step 6: Rebuild and re-test**

```bash
go build -o bin/quild ./cmd/quild && go build -o bin/quil ./cmd/quil
```

**Step 7: Commit**

```bash
git add .
git commit -m "feat: wire end-to-end daemon + client with default workspace

Daemon creates a default Shell tab with one pane on first client
attach. All unit tests pass, both binaries build cross-platform."
```

---

## Task 12: Pane Splitting

**Files:**
- Modify: `internal/tui/tab.go` — add split direction support
- Modify: `internal/tui/model.go` — add split keybindings
- Modify: `internal/daemon/daemon.go` — handle split as "create pane in active tab"

**Step 1: Update tab model for split directions**

Add a `SplitDir` field and update `Resize`/`View` in `internal/tui/tab.go`:

```go
type SplitDir int

const (
	SplitHorizontal SplitDir = iota // panes side-by-side
	SplitVertical                   // panes stacked
)
```

Update `Resize` to handle both directions:

```go
func (t *TabModel) Resize(w, h int) {
	t.Width = w
	t.Height = h

	if len(t.Panes) == 0 {
		return
	}

	switch t.Split {
	case SplitHorizontal:
		paneW := w / len(t.Panes)
		for i, pane := range t.Panes {
			pane.Width = paneW
			pane.Height = h
			if i == len(t.Panes)-1 {
				pane.Width = w - paneW*(len(t.Panes)-1)
			}
		}
	case SplitVertical:
		paneH := h / len(t.Panes)
		for i, pane := range t.Panes {
			pane.Width = w
			pane.Height = paneH
			if i == len(t.Panes)-1 {
				pane.Height = h - paneH*(len(t.Panes)-1)
			}
		}
	}
}

func (t *TabModel) View() string {
	if len(t.Panes) == 0 {
		return ""
	}

	views := make([]string, len(t.Panes))
	for i, pane := range t.Panes {
		views[i] = pane.View()
	}

	switch t.Split {
	case SplitVertical:
		return lipgloss.JoinVertical(lipgloss.Left, views...)
	default:
		return lipgloss.JoinHorizontal(lipgloss.Top, views...)
	}
}
```

**Step 2: Add split keybindings to model.go**

In `handleKey`, add:
```go
case "ctrl+shift+h":
	return m, m.splitPane(tui.SplitHorizontal)
case "ctrl+shift+v":
	return m, m.splitPane(tui.SplitVertical)
```

And add the `splitPane` command:
```go
func (m Model) splitPane(dir SplitDir) tea.Cmd {
	return func() tea.Msg {
		tab := m.activeTabModel()
		if tab == nil {
			return nil
		}
		tab.Split = dir
		msg, _ := ipc.NewMessage(ipc.MsgCreatePane, ipc.CreatePanePayload{
			TabID: tab.ID,
		})
		m.client.Send(msg)
		return nil
	}
}
```

**Step 3: Verify build and tests**

```bash
go test ./... -v && go build ./cmd/quil ./cmd/quild
```

**Step 4: Commit**

```bash
git add internal/tui/ internal/daemon/
git commit -m "feat(tui): add horizontal and vertical pane splitting

Ctrl+Shift+H for horizontal split, Ctrl+Shift+V for vertical.
Tab model supports both split directions for pane layout."
```

---

## Task 13: Final Cleanup & M1 Validation

**Step 1: Run full test suite**

```bash
go test ./... -v -race
```

Expected: All tests pass with no race conditions.

**Step 2: Run go vet and staticcheck**

```bash
go vet ./...
```

**Step 3: Verify cross-compilation**

```bash
GOOS=linux GOARCH=amd64 go build ./cmd/quil ./cmd/quild
GOOS=darwin GOARCH=amd64 go build ./cmd/quil ./cmd/quild
GOOS=windows GOARCH=amd64 go build ./cmd/quil ./cmd/quild
```

Expected: All six binaries compile cleanly.

**Step 4: Commit and tag**

```bash
git add .
git commit -m "chore: M1 complete — daemon, TUI, tabs, panes, splits

All M1 deliverables implemented:
- quild daemon with cross-platform PTY management
- quil client with Bubble Tea TUI
- IPC via Unix sockets
- Tab creation/switching (Alt+1-9)
- Horizontal/vertical pane splits
- Keyboard navigation between panes
- Basic config.toml loading
- quil daemon start/stop commands"

git tag -a v0.1.0 -m "M1: Foundation — Daemon + Shell + TUI"
```

---

## Verification Checklist

- [ ] `go test ./... -v` — all tests pass
- [ ] `go vet ./...` — no issues
- [ ] `go build ./cmd/quil` — client binary compiles
- [ ] `go build ./cmd/quild` — daemon binary compiles
- [ ] Cross-compilation for Linux, macOS, Windows succeeds
- [ ] Manual test: start daemon, launch client, type commands in shell pane
- [ ] Manual test: create new tab (Ctrl+T), switch tabs (Alt+1/2)
- [ ] Manual test: split pane (Ctrl+Shift+H), navigate panes (Ctrl+Tab)
- [ ] Manual test: `quil daemon stop` gracefully shuts down
