package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpRequestTimeout = 10 * time.Second
	maxSendKeys       = 1000 // max keys per send_keys call
	maxScreenshotCols = 500
	maxScreenshotRows = 200
)

// mcpBridge manages the IPC connection to the daemon and provides a
// request-response mechanism for MCP tool calls.
type mcpBridge struct {
	client  *ipc.Client
	mu      sync.Mutex
	pending map[string]chan *ipc.Message
}

func newMCPBridge(client *ipc.Client) *mcpBridge {
	return &mcpBridge{
		client:  client,
		pending: make(map[string]chan *ipc.Message),
	}
}

// readLoop reads messages from the daemon, routing responses to waiting
// callers and discarding broadcast messages. On connection loss, all
// pending requests are woken with a closed channel.
func (b *mcpBridge) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msg, err := b.client.Receive()
		if err != nil {
			// Connection lost — wake all pending requests
			b.mu.Lock()
			for id, ch := range b.pending {
				close(ch)
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
		if msg.ID == "" {
			continue // discard broadcasts
		}
		b.mu.Lock()
		ch, ok := b.pending[msg.ID]
		if ok {
			ch <- msg
			delete(b.pending, msg.ID)
		}
		b.mu.Unlock()
	}
}

// sendRaw sends a message to the daemon (fire-and-forget). Thread-safe.
func (b *mcpBridge) sendRaw(msg *ipc.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client.Send(msg)
}

// request sends an IPC message and waits for the response with the default timeout.
func (b *mcpBridge) request(msgType string, payload any) (*ipc.Message, error) {
	return b.requestWithTimeout(msgType, payload, mcpRequestTimeout)
}

// requestWithTimeout is like request but with a custom timeout.
// Used by watch_notifications which may block for minutes.
func (b *mcpBridge) requestWithTimeout(msgType string, payload any, timeout time.Duration) (*ipc.Message, error) {
	id := uuid.New().String()
	ch := make(chan *ipc.Message, 1)

	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()

	msg, err := ipc.NewMessage(msgType, payload)
	if err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("marshal %s: %w", msgType, err)
	}
	msg.ID = id

	if err := b.client.Send(msg); err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("send %s: %w", msgType, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("connection lost while waiting for %s response", msgType)
		}
		return resp, nil
	case <-timer.C:
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for %s response", msgType)
	}
}

func runMCP() {
	// MCP uses stdout for JSON-RPC — redirect logs to stderr early
	log.SetOutput(os.Stderr)

	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, loadErr := config.Load(cfgPath); loadErr == nil {
			cfg = loaded
		}
	}

	sockPath := config.SocketPath()

	client, err := connectToDaemon(sockPath, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'quil daemon start' first.\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge := newMCPBridge(client)
	go bridge.readLoop(ctx)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "quil", Version: version},
		&mcp.ServerOptions{
			Instructions: "Quil is a terminal multiplexer with multiple panes and tabs.\n\n" +
				"Tool usage guidelines:\n" +
				"- Use list_panes or list_tabs first to discover IDs before calling other tools.\n" +
				"- read_pane_output: best for simple shells and command output (returns scrollback text).\n" +
				"- screenshot_pane: best for interactive TUI apps (vim, htop, Claude Code) — returns the actual screen state.\n" +
				"- send_keys: for navigating interactive menus, send arrow keys ONE AT A TIME with separate calls. " +
				"Do NOT batch escape-sequence keys in a single call — TUI apps may only process the first one.\n" +
				"- send_to_pane: for typing text commands — appends newline by default to execute.\n" +
				"- Destructive tools (restart_pane, destroy_pane, close_tui): always confirm with the user before using.\n" +
				"- watch_notifications: blocks until an event fires on specified panes (replaces polling). Use after starting long-running tasks.\n" +
				"- get_notifications: returns all pending notification events without blocking.\n\n" +
				"Sensitive data handling:\n" +
				"When sending sensitive data (passwords, API keys, tokens, seeds) via send_to_pane or send_keys, " +
				"wrap the value with <<REDACT>>...<</REDACT>> markers.\n" +
				"Example: send_to_pane(input=\"export API_KEY=<<REDACT>>sk-abc123<</REDACT>>\")\n" +
				"The markers are stripped before reaching the terminal. MCP interaction logs show [REDACTED] in their place.",
		},
	)

	mcpLog := newMCPLogger(cfg.MCP)
	registerMCPTools(server, bridge, mcpLog)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server: %v\n", err)
		os.Exit(1)
	}
}

// connectToDaemon connects to the daemon socket, auto-starting it if needed.
func connectToDaemon(sockPath string, cfg config.Config) (*ipc.Client, error) {
	client, err := ipc.NewClient(sockPath)
	if err == nil {
		return client, nil
	}

	if !cfg.Daemon.AutoStart {
		return nil, err
	}

	startDaemon(true)
	for i := 0; i < daemonStartRetries; i++ {
		time.Sleep(daemonRetryInterval)
		client, err = ipc.NewClient(sockPath)
		if err == nil {
			return client, nil
		}
	}
	return nil, err
}
