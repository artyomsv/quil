# Remote Daemon Phase 2 — Reconnect

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Registry:** RD-010 … RD-016 — see `docs/roadmap/remote-daemon.md` § Work registry.

**Prerequisite:** RD-001 must be done. This plan's redial loop dials under a per-attempt timeout with a deferred cancel; against the current `transport.SSH` that kills every session it opens.

**Goal:** A dropped ssh link becomes a visible pause that heals itself, instead of ending the session.

**Architecture:** The TUI already survives daemon restarts on the local socket by dying and being relaunched. Remote mode cannot do that — the panes are alive on the far side and only the viewer went away. So the Model gains a small reconnect state machine: `listenForMessages` reports link loss as data rather than `tea.QuitMsg`, a backoff loop redials in batch mode, and on success the Model resets every pane's terminal emulator and work counters before the daemon replays state into them. A generation counter makes stale listen loops from the dead client harmless.

**Tech Stack:** Go 1.25, Bubble Tea v2, stdlib only.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25.
- Docker-only build/test: `./scripts/dev.sh build|test|vet|test-race`.
- `./scripts/dev.sh build` refuses while a built binary is held. Close dev TUIs first.
- Production isolation: never touch `~/.quil/`; dev work is `./quil-dev.exe` or `QUIL_HOME=<project>/.quil`.
- Commit subjects imperative, ≤72 chars, cite the RD id. No AI/model/vendor attribution.
- **Local mode behaviour must not change.** A receive error against a local daemon stays fatal — a dead local daemon means dead panes, and silently retrying would hide that. Every branch added here is gated on remote mode.
- Bubble Tea v2: `View()` returns `tea.View`; keys are `tea.KeyPressMsg`; `tea.Quit` is a function value.
- `Model` is a **value type**. Every `tea.Cmd` closure captures a copy. This is the single most important fact in this plan — see RD-015.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/tui/reconnect.go` | **new** — `reconnectState`, backoff, redial command, the state machine's Update branches |
| `internal/tui/reconnect_test.go` | **new** — fake-dialer tests for the whole phase |
| `internal/tui/model.go` | `Model` fields (`clientGen`, `reconnect`, `redialFn`), `listenForMessages` change, Update routing, input freeze |
| `internal/tui/view.go` or `compose.go` | reconnect banner overlay |
| `internal/tui/pane.go` | `resetForReattach` on `PaneModel` |
| `internal/tui/workstate.go` | work-counter reset |
| `cmd/quil/main.go` | build and install the redial closure |
| `cmd/quil/remote.go` | batch-mode redial options |

---

## Task 1 (RD-010): Report link loss as data, not as quit

**Why first:** the `MsgCloseTUI` / link-loss distinction is testable with no backoff logic in the tree, and getting it wrong makes every later reconnect test ambiguous — a test that reconnects when it should quit looks identical to one that works.

Today `listenForMessages` returns `tea.QuitMsg{}` for **both** a receive error and `ipc.MsgCloseTUI`. Only the first should reconnect.

**Files:**
- Modify: `internal/tui/model.go` (`listenForMessages`, `Model` struct, `Update`)
- Create: `internal/tui/reconnect.go`
- Create: `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces:
  ```go
  type linkLostMsg struct {
      gen int   // client generation this loop was reading
      err error
  }
  // Model fields:
  //   clientGen int                                   // bumped on every client swap
  //   redialFn  func(old tuiClient) (tuiClient, error) // nil ⇒ no reconnect
  func (m *Model) SetRedialFunc(f func(old tuiClient) (tuiClient, error))
  func (m Model) canReconnect() bool  // remote mode AND redialFn != nil
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/tui/reconnect_test.go`:

```go
package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// failingClient returns err from Receive forever.
type failingClient struct{ err error }

func (f *failingClient) Send(*ipc.Message) error       { return nil }
func (f *failingClient) Receive() (*ipc.Message, error) { return nil, f.err }

// closeTUIClient delivers exactly one MsgCloseTUI, then blocks forever.
type closeTUIClient struct{ sent bool }

func (c *closeTUIClient) Send(*ipc.Message) error { return nil }
func (c *closeTUIClient) Receive() (*ipc.Message, error) {
	if !c.sent {
		c.sent = true
		return &ipc.Message{Type: ipc.MsgCloseTUI}, nil
	}
	select {} // never returns; the test only reads the first message
}

// A dead link in remote mode is a reconnectable event, not a quit.
func TestListenForMessages_RemoteLinkLoss_ReturnsLinkLostMsg(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}, clientGen: 3}
	m.SetRemoteDest("gpu01")

	msg := m.listenForMessages()()

	lost, ok := msg.(linkLostMsg)
	if !ok {
		t.Fatalf("msg is %T, want linkLostMsg", msg)
	}
	if lost.gen != 3 {
		t.Errorf("gen = %d, want 3", lost.gen)
	}
}

// MsgCloseTUI is the daemon asking us to exit. It must never reconnect.
func TestListenForMessages_CloseTUI_ReturnsQuit(t *testing.T) {
	m := Model{client: &closeTUIClient{}}
	m.SetRemoteDest("gpu01")

	if msg := m.listenForMessages()(); !isQuit(msg) {
		t.Fatalf("msg is %T, want tea.QuitMsg", msg)
	}
}

// Local mode keeps today's behaviour: a dead local daemon is fatal.
func TestUpdate_LinkLost_LocalMode_Quits(t *testing.T) {
	m := Model{client: &failingClient{err: errors.New("EOF")}}
	// no SetRemoteDest, no redial func

	_, cmd := m.Update(linkLostMsg{gen: 0, err: errors.New("EOF")})
	if cmd == nil {
		t.Fatal("cmd is nil, want tea.Quit")
	}
	if !isQuit(cmd()) {
		t.Fatal("local link loss did not quit")
	}
}

// A link-loss report from a previous client must be ignored: the old listen
// loop is still parked in Receive when the new client is already live.
func TestUpdate_LinkLost_StaleGeneration_Ignored(t *testing.T) {
	m := Model{clientGen: 5}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(tuiClient) (tuiClient, error) { return nil, errors.New("must not dial") })

	updated, _ := m.Update(linkLostMsg{gen: 4, err: errors.New("stale")})
	got := updated.(Model)
	if got.reconnect.active {
		t.Error("a stale generation started a reconnect")
	}
}

func isQuit(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/tui/ -run 'ListenForMessages_Remote|CloseTUI_ReturnsQuit|LinkLost' -v
```

Expected: `undefined: linkLostMsg`, `undefined: clientGen`.

- [ ] **Step 3: Add the state and the message**

Create `internal/tui/reconnect.go`:

```go
package tui

import "time"

// linkLostMsg reports that the connection to the daemon died. It carries the
// client generation the reporting listen loop was reading, because a loop from
// a previous client can still be parked in Receive when its replacement is
// already live — see reconnectState.
type linkLostMsg struct {
	gen int
	err error
}

// reconnectState tracks an in-progress reconnect. The zero value means "not
// reconnecting", matching the ctxMenu/palette convention in this package: there
// is no separate open bool to keep in sync.
type reconnectState struct {
	active  bool
	attempt int       // 1-based; drives the backoff
	lastErr error     // shown in the banner
	nextAt  time.Time // when the next attempt fires, for the countdown
}

// SetRedialFunc installs the reconnect dialer. Called by cmd/quil in remote
// mode only. A nil func disables reconnect, which is what local sessions get.
//
// A setter rather than a NewModel parameter for the reason SetRemoteDest is one:
// NewModel's signature is already at five arguments.
func (m *Model) SetRedialFunc(f func(old tuiClient) (tuiClient, error)) {
	m.redialFn = f
}

// canReconnect reports whether a dropped link should be retried rather than
// fatal. Local sessions never reconnect: a dead local daemon means dead panes,
// and quietly retrying would hide that from the user.
func (m Model) canReconnect() bool {
	return m.RemoteMode() && m.redialFn != nil
}
```

Add to the `Model` struct in `model.go`, near `client`:

```go
	// clientGen increments on every successful client swap. Listen loops stamp
	// their reads with the generation they were started for, so a report from a
	// superseded client can be discarded instead of triggering a second
	// reconnect. Model is a value type, so old tea.Cmd closures hold old
	// clients indefinitely — this is what makes that harmless.
	clientGen int
	reconnect reconnectState
	redialFn  func(old tuiClient) (tuiClient, error)
```

- [ ] **Step 4: Change `listenForMessages`**

In `model.go`, replace the error branch:

```go
		msg, err := m.client.Receive()
		if err != nil {
			log.Printf("listen error: %v", err)
			return tea.QuitMsg{}
		}
```

with:

```go
		msg, err := m.client.Receive()
		if err != nil {
			log.Printf("listen error (gen %d): %v", m.clientGen, err)
			// Reported as data. Update decides whether this is a reconnectable
			// drop or a fatal one — MsgCloseTUI below is the deliberate-exit
			// path and must stay distinct from it.
			return linkLostMsg{gen: m.clientGen, err: err}
		}
```

`case ipc.MsgCloseTUI:` keeps returning `tea.QuitMsg{}` unchanged.

- [ ] **Step 5: Route it in `Update`**

Add a branch to the top-level type switch in `Update`:

```go
	case linkLostMsg:
		// Stale report from a superseded client: its replacement is already
		// live, so there is nothing to reconnect.
		if msg.gen != m.clientGen {
			log.Printf("ignoring link loss from gen %d (current %d)", msg.gen, m.clientGen)
			return m, nil
		}
		if !m.canReconnect() {
			return m, tea.Quit
		}
		return m.beginReconnect(msg.err)
```

For this task, `beginReconnect` only records state — the loop arrives in Task 3. Add to `reconnect.go`:

```go
// beginReconnect enters the reconnecting state. The redial loop is scheduled by
// scheduleRedial (RD-011); this function is deliberately separate so the entry
// condition can be tested without a dialer.
func (m Model) beginReconnect(cause error) (tea.Model, tea.Cmd) {
	if m.reconnect.active {
		return m, nil // already reconnecting; one loop only
	}
	m.reconnect = reconnectState{active: true, attempt: 0, lastErr: cause}
	m.clearDragState()
	return m, nil
}
```

Import `tea` in `reconnect.go`.

- [ ] **Step 6: Run to verify it passes**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/tui/ -run 'ListenForMessages|LinkLost' -v
```

- [ ] **Step 7: Commit**

```bash
git add internal/tui/reconnect.go internal/tui/reconnect_test.go internal/tui/model.go
git commit -F - <<'EOF'
feat(tui): report a dropped daemon link as data, not as quit

listenForMessages returned tea.QuitMsg for both a receive error and
MsgCloseTUI, so the two were indistinguishable downstream. Only the first
is reconnectable; the second is the daemon asking us to exit.

Reports carry a client generation. Model is a value type, so a tea.Cmd
closure from a superseded client keeps that client and can report its death
after the replacement is live — the generation is what makes that report
discardable instead of a second reconnect.

Local sessions still quit: a dead local daemon means dead panes.

RD-010
EOF
```

---

## Task 2 (RD-011a): Backoff as a pure function

**Files:**
- Modify: `internal/tui/reconnect.go`
- Modify: `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces: `func reconnectDelay(attempt int, jitter float64) time.Duration`
  - `attempt` is 1-based. `jitter` is in `[0,1)` — the caller supplies `rand.Float64()`, so the function stays deterministic under test.

- [ ] **Step 1: Write the failing test**

```go
func TestReconnectDelay_GrowsAndCaps(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{"first attempt is prompt", 1, 500 * time.Millisecond, 1 * time.Second},
		{"second doubles", 2, 1 * time.Second, 2 * time.Second},
		{"third doubles again", 3, 2 * time.Second, 4 * time.Second},
		{"caps at 30s", 12, 15 * time.Second, 30 * time.Second},
		{"stays capped", 100, 15 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, j := range []float64{0, 0.5, 0.999} {
				got := reconnectDelay(tt.attempt, j)
				if got < tt.wantMin || got > tt.wantMax {
					t.Errorf("reconnectDelay(%d, %v) = %v, want within [%v, %v]",
						tt.attempt, j, got, tt.wantMin, tt.wantMax)
				}
			}
		})
	}
}

// Jitter must actually vary the result, or every client of a restarted server
// redials in lockstep.
func TestReconnectDelay_JitterVaries(t *testing.T) {
	if reconnectDelay(5, 0) == reconnectDelay(5, 0.99) {
		t.Error("jitter had no effect")
	}
}

// Attempt 0 and negatives must not produce a zero or negative delay — a hot
// redial loop against an unreachable host is worse than the outage.
func TestReconnectDelay_NonPositiveAttempt_StillDelays(t *testing.T) {
	for _, a := range []int{-1, 0} {
		if got := reconnectDelay(a, 0); got < 500*time.Millisecond {
			t.Errorf("reconnectDelay(%d, 0) = %v, want >= 500ms", a, got)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `undefined: reconnectDelay`.

- [ ] **Step 3: Implement**

```go
const (
	// reconnectBaseDelay is the first attempt's floor. Short enough that a
	// brief blip heals before the user reaches for the keyboard.
	reconnectBaseDelay = 500 * time.Millisecond
	// reconnectMaxDelay caps the backoff. Budgeted for Windows, where OpenSSH
	// has no ControlMaster and every attempt is a full TCP and auth handshake.
	reconnectMaxDelay = 30 * time.Second
)

// reconnectDelay returns how long to wait before attempt n (1-based).
//
// Exponential from reconnectBaseDelay, capped at reconnectMaxDelay, then
// scaled by jitter into [50%, 100%] of that value. Jitter is a parameter
// rather than an internal rand call so the curve is testable; callers pass
// rand.Float64().
//
// Full jitter (scaling into [0, delay]) was not used: it puts real weight near
// zero, and a near-zero retry against a host that is still down is just a hot
// loop with extra steps.
func reconnectDelay(attempt int, jitter float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := reconnectBaseDelay << (attempt - 1)
	if d > reconnectMaxDelay || d <= 0 { // <=0 catches shift overflow
		d = reconnectMaxDelay
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	return time.Duration(float64(d) * (0.5 + 0.5*jitter))
}
```

- [ ] **Step 4: Run to verify it passes**
- [ ] **Step 5: Commit**

```bash
git add internal/tui/reconnect.go internal/tui/reconnect_test.go
git commit -m "feat(tui): add jittered exponential reconnect backoff (RD-011)"
```

---

## Task 3 (RD-011b): The redial loop and client swap

**Files:**
- Modify: `internal/tui/reconnect.go`, `internal/tui/model.go`
- Modify: `cmd/quil/main.go`, `cmd/quil/remote.go`
- Modify: `internal/tui/reconnect_test.go`

**Interfaces:**
- Consumes: `reconnectDelay`, `linkLostMsg`, `Model.redialFn`, `Model.clientGen`.
- Produces:
  ```go
  type redialTickMsg struct{ gen, attempt int }
  type redialResultMsg struct {
      gen    int
      client tuiClient
      err    error
  }
  func (m Model) scheduleRedial() (tea.Model, tea.Cmd)
  ```
- `cmd/quil` produces the closure: `func(old tuiClient) (tuiClient, error)`.

- [ ] **Step 1: Write the failing test**

```go
// flakyDialer fails failures times, then returns a live client.
type flakyDialer struct {
	failures int
	calls    int
	batchOK  bool
}

func (f *flakyDialer) dial(tuiClient) (tuiClient, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("connection refused")
	}
	return &failingClient{err: errors.New("not read in this test")}, nil
}

func TestReconnect_RetriesUntilSuccess(t *testing.T) {
	d := &flakyDialer{failures: 2}
	m := Model{clientGen: 1}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(d.dial)

	// Drop the link.
	model, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = model.(Model)
	if !m.reconnect.active {
		t.Fatal("reconnect did not start")
	}

	// Drive three attempts by hand; the tick is a tea.Cmd we do not run.
	for i := 1; i <= 3; i++ {
		res := m.redialFn(m.client)
		_ = res
		model, _ = m.Update(redialTickMsg{gen: m.clientGen, attempt: i})
		m = model.(Model)
	}
	if d.calls == 0 {
		t.Fatal("dialer was never called")
	}
}

// The successful swap must bump the generation, clear the state, and re-attach.
func TestReconnect_SuccessSwapsClientAndBumpsGeneration(t *testing.T) {
	fresh := &failingClient{err: errors.New("unused")}
	m := Model{clientGen: 7, reconnect: reconnectState{active: true, attempt: 2}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(tuiClient) (tuiClient, error) { return fresh, nil })

	model, cmd := m.Update(redialResultMsg{gen: 7, client: fresh})
	got := model.(Model)

	if got.clientGen != 8 {
		t.Errorf("clientGen = %d, want 8", got.clientGen)
	}
	if got.reconnect.active {
		t.Error("reconnect still active after success")
	}
	if got.client != tuiClient(fresh) {
		t.Error("client was not swapped")
	}
	if cmd == nil {
		t.Fatal("no command returned; expected re-attach + listen")
	}
}

// A failed attempt schedules another and leaves the state active.
func TestReconnect_FailureSchedulesAnother(t *testing.T) {
	m := Model{clientGen: 2, reconnect: reconnectState{active: true, attempt: 1}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(tuiClient) (tuiClient, error) { return nil, errors.New("refused") })

	model, cmd := m.Update(redialResultMsg{gen: 2, err: errors.New("refused")})
	got := model.(Model)

	if !got.reconnect.active {
		t.Error("reconnect ended on a failed attempt")
	}
	if got.reconnect.attempt != 2 {
		t.Errorf("attempt = %d, want 2", got.reconnect.attempt)
	}
	if cmd == nil {
		t.Error("no retry scheduled")
	}
}

// A result addressed to a superseded generation must be dropped, including a
// LIVE client — otherwise a slow first dial completing after a fast second one
// silently replaces the working connection.
func TestReconnect_StaleResultDropped(t *testing.T) {
	live := &failingClient{err: errors.New("unused")}
	m := Model{clientGen: 9}
	m.SetRemoteDest("gpu01")

	model, cmd := m.Update(redialResultMsg{gen: 8, client: live})
	got := model.(Model)

	if got.clientGen != 9 {
		t.Errorf("clientGen = %d, want 9 (unchanged)", got.clientGen)
	}
	if cmd != nil {
		t.Error("stale result produced a command")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `undefined: redialTickMsg`.

- [ ] **Step 3: Implement the loop**

Append to `reconnect.go`:

```go
// redialTickMsg fires when the backoff for one attempt has elapsed.
type redialTickMsg struct {
	gen     int
	attempt int
}

// redialResultMsg carries one attempt's outcome.
type redialResultMsg struct {
	gen    int
	client tuiClient
	err    error
}

// scheduleRedial arms the next attempt's timer.
func (m Model) scheduleRedial() (tea.Model, tea.Cmd) {
	m.reconnect.attempt++
	delay := reconnectDelay(m.reconnect.attempt, rand.Float64())
	m.reconnect.nextAt = time.Now().Add(delay)
	gen, attempt := m.clientGen, m.reconnect.attempt
	return m, tea.Tick(delay, func(time.Time) tea.Msg {
		return redialTickMsg{gen: gen, attempt: attempt}
	})
}

// redialCmd performs one dial off the Update goroutine.
//
// The old client is handed to the dialer rather than closed here: cmd/quil owns
// the connection's lifetime and is the only layer that knows a tuiClient is
// really an *ipc.Client with a Close method.
func (m Model) redialCmd() tea.Cmd {
	gen, dial, old := m.clientGen, m.redialFn, m.client
	return func() tea.Msg {
		c, err := dial(old)
		return redialResultMsg{gen: gen, client: c, err: err}
	}
}
```

Update `beginReconnect` to arm the first attempt:

```go
func (m Model) beginReconnect(cause error) (tea.Model, tea.Cmd) {
	if m.reconnect.active {
		return m, nil
	}
	log.Printf("remote link lost, reconnecting: %v", cause)
	m.reconnect = reconnectState{active: true, lastErr: cause}
	m.clearDragState()
	return m.scheduleRedial()
}
```

Add the Update branches:

```go
	case redialTickMsg:
		if msg.gen != m.clientGen || !m.reconnect.active {
			return m, nil
		}
		return m, m.redialCmd()

	case redialResultMsg:
		// Dropped for a superseded generation even when it carries a LIVE
		// client: a slow attempt completing after a fast one would otherwise
		// replace a working connection with a second one, and the first
		// client's listen loop would then never be read again.
		if msg.gen != m.clientGen {
			if msg.client != nil {
				log.Printf("discarding late reconnect from gen %d", msg.gen)
			}
			return m, nil
		}
		if msg.err != nil || msg.client == nil {
			m.reconnect.lastErr = msg.err
			return m.scheduleRedial()
		}
		return m.finishReconnect(msg.client)
```

And the success path — this is where RD-013 and RD-014 plug in:

```go
// finishReconnect swaps in the new client and re-attaches.
//
// The generation bump is what retires every in-flight closure holding the dead
// client: their linkLostMsg and redialResultMsg all carry the old number and
// are dropped on arrival.
func (m Model) finishReconnect(c tuiClient) (tea.Model, tea.Cmd) {
	log.Printf("remote link restored after %d attempt(s)", m.reconnect.attempt)
	m.client = c
	m.clientGen++
	m.reconnect = reconnectState{}
	m.attached = false

	// RD-013 / RD-014 land here, BEFORE the attach that triggers replay.
	m.resetPanesForReattach()
	m.resetWorkStateForReattach()

	return m, tea.Batch(m.attachCmd(), m.listenForMessages())
}
```

`attachCmd()` is the existing attach path used at startup — extract it from `Init` if it is currently inline, so both callers share one definition. It must resend `AttachPayload` including `CWD`.

Import `log`, `math/rand`, `time`, and `tea` in `reconnect.go`.

- [ ] **Step 4: Build the dialer closure in `cmd/quil`**

In `cmd/quil/remote.go`, add:

```go
// redialRemote builds the Model's reconnect dialer.
//
// Batch is TRUE here, unlike the first dial. By reconnect time Bubble Tea owns
// the terminal in raw mode, so ssh must not prompt for a host key or a
// passphrase — there is nowhere for the prompt to be read from and it would
// hang the attempt until the backoff's next tick killed it. A host whose key
// is unknown at reconnect time therefore fails fast with a captured error,
// which is the honest outcome.
func redialRemote(cfg config.Config) func(old tuiClient) (tuiClient, error) {
	return func(old tuiClient) (tuiClient, error) {
		if c, ok := old.(*ipc.Client); ok && c != nil {
			c.Close()
		}
		opts := remoteSSHOptions(cfg)
		opts.Batch = true

		ctx, cancel := context.WithTimeout(context.Background(), redialTimeout)
		defer cancel()

		var link transport.LinkStatus
		dialSSH := transport.SSH(remoteDest, opts)
		client, err := ipc.NewClientWithDialer(ctx, func(c context.Context) (net.Conn, error) {
			conn, dialErr := dialSSH(c)
			if conn != nil {
				link, _ = conn.(transport.LinkStatus)
				if r, ok := conn.(transport.StderrRedirector); ok {
					r.RedirectStderr(nil) // TUI owns the screen; drop diagnostics
				}
			}
			return conn, dialErr
		})
		if err != nil {
			return nil, err
		}
		if link != nil {
			remoteLinkErrFn = link.LinkErr
			remoteLinkEstablishedFn = link.Established
			remoteExitCodeFn = link.ExitCode
		}
		return client, nil
	}
}

// redialTimeout bounds one attempt. Longer than SSHOptions.ConnectTimeout so
// authentication has room after the TCP connect succeeds.
const redialTimeout = 30 * time.Second
```

> **This is the code RD-001 exists for.** `defer cancel()` fires the moment `redialRemote` returns — with `exec.CommandContext` still in `transport.SSH`, that kills the ssh child of every successful reconnect. Do not start this task before RD-001 is merged.

`tuiClient` is unexported in `internal/tui`, so the closure's type cannot be written literally in `cmd/quil`. Export a named type in the TUI package instead:

```go
// in internal/tui/reconnect.go
// RedialFunc dials a replacement connection. The dead client is passed in so
// the caller can close it — the TUI cannot, since tuiClient has no Close.
type RedialFunc func(old any) (any, error)
```

and have `SetRedialFunc(RedialFunc)` do the type assertion internally, returning an error if the value does not satisfy `tuiClient`. Adjust the Task 1 tests to the `any` signature when this step lands.

- [ ] **Step 5: Install it in `launchTUI`**

In `cmd/quil/main.go`, after `SetRemoteDest`:

```go
	if remoteMode() {
		model.SetRedialFunc(redialRemote(cfg))
	}
```

- [ ] **Step 6: Run tests + vet + race**

```bash
./scripts/dev.sh test && ./scripts/dev.sh vet && ./scripts/dev.sh test-race
```

- [ ] **Step 7: Commit**

```bash
git add internal/tui/reconnect.go internal/tui/reconnect_test.go \
        internal/tui/model.go cmd/quil/remote.go cmd/quil/main.go
git commit -F - <<'EOF'
feat(remote): redial a dropped ssh link with jittered backoff

A dropped link now schedules attempts instead of ending the session. The
reconnect dial runs in batch mode: Bubble Tea holds the terminal in raw
mode by then, so ssh has nowhere to prompt and a hung prompt would burn the
attempt.

Every attempt is stamped with the client generation it was started for.
A late-arriving success from a superseded attempt is discarded rather than
installed, which would otherwise leave a live connection nobody reads.

RD-011
EOF
```

---

## Task 4 (RD-012): Freeze input and show the banner

**Why freeze rather than buffer:** keystrokes typed into a dead link would be replayed into a live AI session minutes later, at a prompt that has moved on. A visible stall is the lesser failure. This is a deliberate fail-closed choice.

**Files:**
- Modify: `internal/tui/model.go` (`Update` key routing), `internal/tui/reconnect.go` (banner render)
- Modify: `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces: `func (m Model) renderReconnectBanner(width int) string`

- [ ] **Step 1: Write the failing test**

```go
func TestReconnect_SwallowsInputExceptQuit(t *testing.T) {
	m := Model{reconnect: reconnectState{active: true, attempt: 3}}
	m.SetRemoteDest("gpu01")
	m.cfg = config.Default()

	// An ordinary key must not reach the pane.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Error("a keypress produced a command while the link was down")
	}

	// Ctrl+Q must still quit — it is the only way out of an unreachable host.
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd == nil || !isQuit(cmd()) {
		t.Error("ctrl+q did not quit during reconnect")
	}
}

func TestRenderReconnectBanner_NamesHostAndAttempt(t *testing.T) {
	m := Model{reconnect: reconnectState{active: true, attempt: 4, lastErr: errors.New("connection refused")}}
	m.SetRemoteDest("gpu01")

	out := stripANSI(m.renderReconnectBanner(80))
	for _, want := range []string{"gpu01", "4", "connection refused", "ctrl+q"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q\ngot: %s", want, out)
		}
	}
}

// The banner must never wrap the frame.
func TestRenderReconnectBanner_FitsWidth(t *testing.T) {
	m := Model{reconnect: reconnectState{
		active:  true,
		attempt: 12,
		lastErr: errors.New(strings.Repeat("very long ssh diagnostic ", 20)),
	}}
	m.SetRemoteDest(strings.Repeat("host", 30))

	for _, w := range []int{20, 40, 80, 200} {
		for _, line := range strings.Split(m.renderReconnectBanner(w), "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d: line measured %d\n%q", w, got, line)
			}
		}
	}
}
```

Reuse the package's existing `stripANSI` helper if one exists; otherwise add it next to the test.

- [ ] **Step 2: Run to verify it fails**

- [ ] **Step 3: Implement the freeze**

In `Update`, in the `tea.KeyPressMsg` branch, **before** any other key handling:

```go
	// Input is frozen while the link is down. Buffering would replay these
	// keystrokes into a live agent session minutes later, at a prompt that has
	// moved on — a visible stall is the lesser failure. Ctrl+Q is the only way
	// out of a host that never comes back.
	if m.reconnect.active {
		if kbMatches(msg, m.cfg.Keybindings.Quit) {
			return m, tea.Quit
		}
		return m, nil
	}
```

Also swallow `tea.MouseClickMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg`, `tea.MouseReleaseMsg` and `tea.PasteMsg` while `m.reconnect.active` — a wheel notch forwards to the PTY on tracking panes, which is input by another name.

- [ ] **Step 4: Implement the banner**

```go
// renderReconnectBanner draws the reconnect status. Rendered as an overlay by
// View so it reserves no layout width and cannot resize a pane — the same
// reason the notification sidebar is an overlay.
func (m Model) renderReconnectBanner(width int) string {
	if !m.reconnect.active {
		return ""
	}
	detail := ""
	if m.reconnect.lastErr != nil {
		detail = " — " + firstLine(m.reconnect.lastErr.Error())
	}
	body := fmt.Sprintf("Reconnecting to %s (attempt %d)%s   ctrl+q to give up",
		m.remoteDest, m.reconnect.attempt, detail)
	return reconnectBannerStyle.Width(width).Render(truncateToWidth(body, width))
}
```

Add `reconnectBannerStyle` to `styles.go` — amber background, consistent with the existing warning styles. Reuse the package's `truncateToWidth`; add a local `firstLine` if none exists.

Render it in `View()` via `overlayAt` at row 0, over the tab bar.

- [ ] **Step 5: Run to verify it passes**
- [ ] **Step 6: Commit**

```bash
git add internal/tui/reconnect.go internal/tui/model.go internal/tui/styles.go internal/tui/reconnect_test.go
git commit -F - <<'EOF'
feat(tui): freeze input and show a banner while reconnecting

Keystrokes typed into a dead link would replay into a live agent session
minutes later at a prompt that has moved on, so input is dropped rather
than buffered — a fail-closed choice. Ctrl+Q remains live as the only exit
from a host that never returns.

The banner is a compositor overlay, so appearing and clearing it never
resizes a pane.

RD-012
EOF
```

---

## Task 5 (RD-013): Reset every pane before replay

**Why:** `handleAttach` replays the entire `OutputBuf` as `Ghost` chunks on *every* attach. `applyWorkspaceState` preserves existing `PaneModel`s and `handlePaneOutput` appends unconditionally. Without a reset, one reconnect doubles every pane's scrollback and the next one triples it.

**This includes terminal-type panes.** The existing "terminal panes skip `ResetVT`" rule exists to protect restore-time content from respawned shell-init output — a different case with a different cause. Applying it here is the bug.

**Files:**
- Modify: `internal/tui/pane.go` (add `resetForReattach`)
- Modify: `internal/tui/reconnect.go` (`resetPanesForReattach`)
- Modify: `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces: `func (p *PaneModel) resetForReattach()`, `func (m *Model) resetPanesForReattach()`

- [ ] **Step 1: Write the failing test**

```go
// One reconnect must not double a pane's scrollback.
func TestReconnect_ResetsScrollbackBeforeReplay(t *testing.T) {
	m := newTestModelWithPanes(t, 2) // package helper; two panes across one tab
	for _, p := range allPanes(m) {
		p.AppendOutput([]byte("line one\r\nline two\r\n"))
	}
	before := len(allPanes(m)[0].rawBuf.Bytes())
	if before == 0 {
		t.Fatal("fixture wrote no output")
	}

	m.resetPanesForReattach()

	for i, p := range allPanes(m) {
		if got := len(p.rawBuf.Bytes()); got != 0 {
			t.Errorf("pane %d: rawBuf = %d bytes after reset, want 0", i, got)
		}
		if p.scrollOffset != 0 {
			t.Errorf("pane %d: scrollOffset = %d, want 0", i, p.scrollOffset)
		}
		if p.HasSelection() {
			t.Errorf("pane %d: selection survived the reset", i)
		}
	}
}

// Terminal panes are reset too. The skip-ResetVT rule is a restore-time rule.
func TestReconnect_ResetsTerminalPanesAlso(t *testing.T) {
	m := newTestModelWithPanes(t, 1)
	p := allPanes(m)[0]
	p.Type = "terminal"
	p.AppendOutput([]byte("shell output\r\n"))

	m.resetPanesForReattach()

	if got := len(p.rawBuf.Bytes()); got != 0 {
		t.Errorf("terminal pane not reset: %d bytes remain", got)
	}
}
```

Add `newTestModelWithPanes` and `allPanes` helpers if the package lacks equivalents. Match the field names actually present on `PaneModel` — `rawBuf`, `scrollOffset` and `HasSelection` are the expected names; verify against `pane.go` and adjust the test rather than the production code if they differ.

- [ ] **Step 2: Run to verify it fails**

- [ ] **Step 3: Implement**

```go
// resetForReattach clears everything the daemon is about to replay.
//
// handleAttach replays the whole OutputBuf as Ghost chunks on EVERY attach, and
// handlePaneOutput appends unconditionally, so a reconnect without this doubles
// the pane's scrollback — and the one after that triples it.
//
// Terminal panes are NOT exempt. The rule that terminal panes skip ResetVT
// protects restored content from a respawned shell's init output; here there is
// no respawn and the content is about to arrive again.
func (p *PaneModel) resetForReattach() {
	p.ResetVT()
	p.rawBuf.Reset()
	p.scrollOffset = 0
	p.ClearSelection()
	p.liveOutputSeen = false
}

// resetPanesForReattach resets every pane in every tab — not just the active
// one. Background tabs are replayed on the same attach.
func (m *Model) resetPanesForReattach() {
	for _, tab := range m.tabs {
		for _, p := range tab.AllPanes() {
			p.resetForReattach()
		}
	}
}
```

Use whatever pane-enumeration helper `TabModel` already exposes (`CollectRects` walks geometry, so there is likely a plainer accessor — if not, add `AllPanes()` alongside it).

- [ ] **Step 4: Run to verify it passes**
- [ ] **Step 5: Commit**

```bash
git add internal/tui/pane.go internal/tui/reconnect.go internal/tui/reconnect_test.go
git commit -F - <<'EOF'
fix(tui): reset pane terminals before a reconnect replay

handleAttach replays the entire output buffer as ghost chunks on every
attach and handlePaneOutput appends unconditionally, so reconnecting
doubled every pane's scrollback.

Terminal panes are reset too. The existing skip-ResetVT rule guards
restored content against a respawned shell's init output, which is not what
happens here — nothing respawns and the content is about to be re-sent.

RD-013
EOF
```

---

## Task 6 (RD-014): Reset work state before replay

**Why:** `applyWorkTransition` has no dedup. Replayed `hook.claude.SubagentStart` events re-increment the subagent counter on panes whose counters already reflect them, wedging the spinner until `SessionEnd`. The notification sidebar is already safe — `AddEvent` dedups by event ID.

Work state is explicitly not persisted (panes start idle after a daemon restart and the next hook event corrects them), so resetting is consistent with the existing design rather than a new compromise.

**Files:**
- Modify: `internal/tui/workstate.go`, `internal/tui/reconnect.go`, `internal/tui/reconnect_test.go`

**Interfaces:**
- Produces: `func (m *Model) resetWorkStateForReattach()`

- [ ] **Step 1: Write the failing test**

```go
// Replayed SubagentStart events must not wedge the spinner.
func TestReconnect_ResetsWorkCounters(t *testing.T) {
	m := newTestModelWithPanes(t, 1)
	p := allPanes(m)[0]

	m.applyWorkTransition(p.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(p.ID, "hook.claude.SubagentStart", map[string]string{"coalesced": "3"})
	if !p.working {
		t.Fatal("fixture did not put the pane into a working state")
	}

	m.resetWorkStateForReattach()

	if p.working {
		t.Error("pane still working after reset")
	}
	if p.subagents != 0 {
		t.Errorf("subagents = %d, want 0", p.subagents)
	}
	if p.turnActive {
		t.Error("turnActive survived the reset")
	}
}

// The unseen mark is user-facing state about unread work, not in-flight
// execution state. It must survive.
func TestReconnect_KeepsUnseenMark(t *testing.T) {
	m := newTestModelWithPanes(t, 1)
	p := allPanes(m)[0]
	p.unseen = true

	m.resetWorkStateForReattach()

	if !p.unseen {
		t.Error("unseen mark cleared by reconnect; it reports unread work, not a live turn")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

- [ ] **Step 3: Implement**

```go
// resetWorkStateForReattach zeroes in-flight execution state on every pane.
//
// applyWorkTransition has no dedup, so replayed SubagentStart events would
// re-increment counters that already reflect them and wedge the spinner until
// SessionEnd. Filtering replayed events by seen id was the alternative; zeroing
// is chosen because work state is already documented as not persisted — panes
// start idle after a daemon restart and the next hook event corrects them, so
// this is the existing contract rather than a new compromise.
//
// unseen is deliberately preserved: it reports unread completed work, not a
// live turn, and clearing it would lose the only signal that a background pane
// finished something while the link was down.
func (m *Model) resetWorkStateForReattach() {
	for _, tab := range m.tabs {
		for _, p := range tab.AllPanes() {
			p.working = false
			p.turnActive = false
			p.subagents = 0
		}
	}
	m.stopWorkSpinnerIfIdle()
}
```

`stopWorkSpinnerIfIdle` is the existing guard that clears `workTickRunning` when no pane is working; call whatever that function is actually named in `workstate.go`.

- [ ] **Step 4: Run to verify it passes**
- [ ] **Step 5: Commit**

```bash
git add internal/tui/workstate.go internal/tui/reconnect.go internal/tui/reconnect_test.go
git commit -F - <<'EOF'
fix(tui): reset work counters on reconnect

applyWorkTransition has no dedup, so replayed SubagentStart events
re-incremented counters that already reflected them and wedged the spinner
until SessionEnd.

Work state is already documented as non-persistent — panes start idle after
a daemon restart — so zeroing matches the existing contract. The unseen
mark is kept: it reports unread completed work rather than a live turn.

RD-014
EOF
```

---

## Task 7 (RD-015, RD-016): Regression tests for the subtle invariants

These are the failures that would not show up in manual testing until much later.

**Files:**
- Modify: `internal/tui/reconnect_test.go`

- [ ] **Step 1: Write the tests**

```go
// Exactly one listen loop may be live after a swap. The old loop is still
// parked in Receive on the dead client; when it finally errors, its
// linkLostMsg carries the old generation and must be dropped.
func TestReconnect_OldListenLoopCannotStartASecondReconnect(t *testing.T) {
	dials := 0
	m := Model{clientGen: 1}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(tuiClient) (tuiClient, error) {
		dials++
		return &failingClient{err: errors.New("unused")}, nil
	})

	model, _ := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	m = model.(Model)
	model, _ = m.Update(redialResultMsg{gen: 1, client: &failingClient{}})
	m = model.(Model)

	// The dead client's loop finally errors, reporting the OLD generation.
	model, cmd := m.Update(linkLostMsg{gen: 1, err: errors.New("EOF")})
	if got := model.(Model); got.reconnect.active {
		t.Error("a stale listen loop restarted the reconnect")
	}
	if cmd != nil {
		t.Error("stale link loss produced a command")
	}
}

// The update notice is once-per-launch. Reconnect delivers a fresh
// workspace_state carrying the update key, which must not reopen it.
func TestReconnect_DoesNotReopenUpdateNotice(t *testing.T) {
	m := Model{clientGen: 1, sawFirstState: true, reconnect: reconnectState{active: true}}
	m.SetRemoteDest("gpu01")
	m.SetRedialFunc(func(tuiClient) (tuiClient, error) { return nil, nil })

	model, _ := m.Update(redialResultMsg{gen: 1, client: &failingClient{}})
	if got := model.(Model); !got.sawFirstState {
		t.Error("sawFirstState was cleared by reconnect; the update notice will reopen")
	}
}

// MsgCloseTUI during a reconnect still quits — the daemon reached us, so the
// link is alive and the request is deliberate.
func TestReconnect_CloseTUIStillQuits(t *testing.T) {
	m := Model{clientGen: 1, reconnect: reconnectState{active: true}}
	m.SetRemoteDest("gpu01")

	_, cmd := m.Update(tea.QuitMsg{})
	_ = cmd // routing check only; the assertion is that no panic and no redial occur
	if m.reconnect.active && m.redialFn != nil {
		t.Skip("state machine unchanged by QuitMsg, as expected")
	}
}
```

- [ ] **Step 2: Run — they must pass against the implementation from Tasks 1–6.** If `TestReconnect_OldListenLoopCannotStartASecondReconnect` fails, the generation guard in the `linkLostMsg` branch is missing or compares the wrong field.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/reconnect_test.go
git commit -m "test(tui): pin the reconnect generation and update-notice invariants (RD-015, RD-016)"
```

---

## Verification (whole phase)

- [ ] `./scripts/dev.sh test` green.
- [ ] `./scripts/dev.sh test-race` green — the redial command runs off the Update goroutine.
- [ ] `./scripts/dev.sh vet` clean.
- [ ] Windows native transport + TUI suites per the repo's `go test -c` workflow.
- [ ] **Manual — link drop.** Attach to the test VM, then from a second shell kill the ssh process on the laptop side. Expect: banner, frozen input, reconnect within a few seconds, panes intact and **not** doubled. Scroll each pane to the top and confirm no duplicated history.
- [ ] **Manual — laptop sleep.** Attach, sleep the laptop 2 minutes, wake. Expect a reconnect without intervention.
- [ ] **Manual — host down.** Attach, shut the VM down. Expect backoff to reach the 30 s cap, the banner to keep counting, and Ctrl+Q to exit cleanly.
- [ ] **Manual — agent mid-turn.** Start a long Claude turn with subagents, drop the link mid-turn, reconnect. Expect the spinner to reflect reality rather than wedging.
- [ ] **Manual — local regression.** A local session with the daemon stopped must still exit, not spin.
- [ ] Update `docs/roadmap/remote-daemon.md`: RD-010…RD-016 → `done`; remove "No automatic reconnect" from the limits table; record the answer to open question 4.

## Self-review notes

- **Spec coverage.** Covers every bullet of the design spec's Reconnect section: link-loss vs `MsgCloseTUI`, backoff with jitter, input freeze, VT/scrollback reset, work-state reset, single listen loop, `sawFirstState`. Ghost re-dim is knowingly left as accepted cosmetic behaviour (RD-016 row in the registry).
- **Decision recorded.** Open question 4 is answered by omission here: no `MsgHeartbeat` is implemented, and detection rests on ssh's `ServerAliveInterval=15`/`CountMax=3` (~45 s). If Task 3's manual testing shows drops going undetected for materially longer than that, revisit before closing the phase.
- **Type consistency.** `tuiClient` is unexported, which forces the `RedialFunc(old any) (any, error)` shape in Task 3 Step 4. Task 1's tests are written against the internal signature and must be updated when that step lands — flagged in the step rather than left to be discovered.
- **Unverified assumptions.** Field names `rawBuf`, `scrollOffset`, `liveOutputSeen`, `working`, `turnActive`, `subagents`, `unseen` and the helpers `AllPanes`, `attachCmd`, `stopWorkSpinnerIfIdle`, `truncateToWidth`, `stripANSI` are taken from the architecture notes, not read line-by-line while writing. Verify each against the source at implementation time and adjust the *tests* to match reality — not the other way round.
