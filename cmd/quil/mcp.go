package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
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
	// dead is set when readLoop exits: the connection is gone and every
	// later request would only time out. The host router reads it to
	// decide whether a remote needs re-dialling.
	dead atomic.Bool
	// daemonVersion is what the daemon reported at dial time; "" when it did
	// not answer. Read by requireDaemon so a tool aimed at a daemon too old
	// to know its request type is refused at once instead of timing out.
	daemonVersion string
	// daemonRequests is what the daemon SAYS it can be sent (GatedRequests).
	// Empty when it did not answer, or when it predates the field — see
	// requireRequest, which falls back to the version floor there.
	daemonRequests []string
}

func newMCPBridge(client *ipc.Client) *mcpBridge {
	return &mcpBridge{
		client:  client,
		pending: make(map[string]chan *ipc.Message),
	}
}

// newLocalMCPBridge discovers the local daemon's version before readLoop owns
// the connection. Use the local handshake budget: a pre-versioning daemon
// ignores the probe, and waiting the remote budget would delay MCP startup.
func newLocalMCPBridge(client *ipc.Client) *mcpBridge {
	bridge := newMCPBridge(client)
	bridge.daemonVersion, bridge.daemonRequests = probeDaemonVersion(client, handshakeTimeout)
	return bridge
}

// declinePaneOutput asks the daemon to stop broadcasting the live PTY stream to
// this bridge.
//
// readLoop discards every broadcast it receives (`if msg.ID == "" { continue }`),
// so each pane-output frame cost a socket write on the daemon and a full frame
// decode here purely to be thrown away — multiplied by however many bridges are
// attached, 17 in one measured session. The MCP tools that read pane content
// (read_pane_output, screenshot_pane) use request/response and are unaffected,
// as are workspace state and notifications.
//
// Best-effort by design: an older daemon has no case for this message type,
// ignores it, and keeps sending everything — exactly the previous behaviour. So
// the error is logged, never fatal.
func (b *mcpBridge) declinePaneOutput() error {
	no := false
	msg, err := ipc.NewMessage(ipc.MsgSubscribe, ipc.SubscribePayload{PaneOutput: &no})
	if err != nil {
		return fmt.Errorf("build subscribe: %w", err)
	}
	if err := b.sendRaw(msg); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}
	return nil
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
			b.dead.Store(true)
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
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: quil mcp")
		exitFn(1)
		return
	}

	if refuseRemoteMCP() {
		return
	}

	// Tie this bridge's lifetime to the AI client that spawned it. Stdin
	// EOF alone is not a reliable termination signal on Windows (sibling
	// processes inherit the pipe's write handle and keep it open past the
	// parent's death), which leaked orphaned bridges for days. See
	// parentwatch_windows.go for the full mechanism.
	watchParentExit()

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

	bridge := newLocalMCPBridge(client)
	if err := bridge.declinePaneOutput(); err != nil {
		log.Printf("mcp: decline pane output: %v", err)
	}
	go bridge.readLoop(ctx)

	// Remote hosts ride the same [[destinations]] the TUI attaches to, dialled
	// in the background so the local daemon is served at once.
	router := newMCPRouter(bridge, cfg, dialMCPHost)
	router.connectAll()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "quil", Version: version},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
		},
	)

	mcpLog := newMCPLogger(cfg.MCP)
	registerMCPTools(server, router, mcpLog)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server: %v\n", err)
		os.Exit(1)
	}
}

// mcpInstructions is the server-level guidance every MCP client shows its
// model. Kept as one constant so the tool descriptions and this text are
// edited side by side.
const mcpInstructions = "Quil is a terminal multiplexer with projects, tabs and panes, possibly across several hosts.\n\n" +
	"Tool usage guidelines:\n" +
	"- Use list_panes, list_tabs or list_projects first to discover IDs before calling other tools. " +
	"list_panes marks the pane you are running inside with self=true and reports each AI pane's agent_state " +
	"(working / blocked / idle; empty means unknown, not idle).\n" +
	"- read_pane_output: best for simple shells and command output (returns scrollback text).\n" +
	"- screenshot_pane: best for interactive TUI apps (vim, htop, Claude Code) — returns the actual screen state.\n" +
	"- send_keys: for navigating interactive menus, send arrow keys ONE AT A TIME with separate calls. " +
	"Do NOT batch escape-sequence keys in a single call — TUI apps may only process the first one.\n" +
	"- send_to_pane: for typing text commands — appends newline by default to execute. Use paste=true for multi-line text to an AI pane.\n" +
	"- delegate_task: to ask ANOTHER AI pane (or a terminal) to do work. It pastes the prompt, returns a task id, and the daemon " +
	"reports done / failed / timeout from the target's own state. Then either wait_task (blocking), watch_notifications for task_done, " +
	"or carry on — with notify (default) a '[quil task ...]' line is typed into your pane when the task ends. get_task returns the last output.\n" +
	"- create_pane / create_tab: call list_plugins first for the toggle names an AI plugin accepts (permission mode, chrome, search); " +
	"list_sessions for a Claude session to resume. worktree_branch opens the pane in a fresh git worktree; sandbox_image runs it in Docker.\n" +
	"- Projects: list_projects, create_project, update_project, switch_project, destroy_project; tabs: create_tab, rename_tab, destroy_tab.\n" +
	"- Hosts: list_hosts shows the remote daemons this bridge reaches. Ids you discovered route to their host automatically; " +
	"pass host explicitly to create things on a remote. delegate_task with notify only works when requester and target share a host.\n" +
	"- Several TUIs can share one daemon: list_clients shows them, and its client id targets set_active_pane or close_tui at " +
	"one of them instead of the one that typed most recently.\n" +
	"- Destructive tools (restart_pane, destroy_pane, destroy_tab, destroy_project, close_tui): always confirm with the user before using.\n" +
	"- watch_notifications: blocks until an event fires on specified panes (replaces polling). Use after starting long-running tasks.\n" +
	"- get_notifications: returns all pending notification events without blocking.\n" +
	"- dismiss_notifications: ack events you've handled so they don't show up again. Pass an event_id (and its host), or omit to clear all.\n\n" +
	"Sensitive data handling:\n" +
	"When sending sensitive data (passwords, API keys, tokens, seeds) via send_to_pane, send_keys or delegate_task, " +
	"wrap the value with <<REDACT>>...<</REDACT>> markers.\n" +
	"Example: send_to_pane(input=\"export API_KEY=<<REDACT>>sk-abc123<</REDACT>>\")\n" +
	"The markers are stripped before reaching the terminal. MCP interaction logs show [REDACTED] in their place."

// refuseRemoteMCP reports whether this session is attached to a remote
// daemon over --remote and, if so, prints a refusal to stderr and calls
// exitFn(1). Extracted from runMCP as a testable seam: runMCP's very next
// steps are watchParentExit (real OpenProcess/Getppid calls) and
// connectToDaemon (a real socket dial), neither of which a test may trigger.
//
// Without this guard, connectToDaemon's ipc.NewClient(sockPath) succeeds
// silently whenever a local daemon is already listening — the common case on
// a developer machine that runs quil as its daily driver — so
// startDaemon's own remote-mode refusal is never reached. The AI client then
// believes it is driving --remote's host while every tool call
// (create_pane, send_to_pane, destroy_pane, …) acts on the user's live local
// session instead.
//
// The explicit bool return (checked by the caller) mirrors startDaemon's
// remote guard in main.go: exitFn is swappable for tests, and a double that
// records the call and returns (a reasonable double to write, unlike the
// real os.Exit which never returns) must not let runMCP fall through into
// connectToDaemon.
func refuseRemoteMCP() bool {
	if !remoteMode() {
		return false
	}
	fmt.Fprintf(os.Stderr,
		"quil mcp: refusing to start — this session is attached to --remote %s.\n"+
			"The MCP bridge must run on the same host as the daemon it drives: run "+
			"'quil mcp' on %s itself (e.g. inside a pane there), not by bridging to "+
			"it from this machine.\n",
		remoteDest, remoteDest)
	exitFn(1)
	return true
}

// connectToDaemon connects to the daemon socket, auto-starting it if needed.
func connectToDaemon(sockPath string, cfg config.Config) (*ipc.Client, error) {
	client, err := ipc.NewClient(sockPath)
	if err == nil {
		sendClientHello(client, helloRoleBridge)
		return client, nil
	}

	if !cfg.Daemon.AutoStart {
		return nil, err
	}

	pid := startDaemon(true)
	if !waitForDaemonReady(sockPath, pid) {
		// Spawned, but the socket never opened — surface that instead of the
		// stale pre-spawn dial error so the MCP client's log points at the
		// daemon rather than looking like a plain "not running".
		return nil, fmt.Errorf("daemon spawned but did not open socket %s within %s: %w", sockPath, daemonReadyTimeout, err)
	}
	if client, err = ipc.NewClient(sockPath); err != nil {
		return nil, fmt.Errorf("reconnect after auto-start: %w", err)
	}
	sendClientHello(client, helloRoleBridge)
	return client, nil
}
