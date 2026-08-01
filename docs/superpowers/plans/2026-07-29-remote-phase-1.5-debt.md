# Remote Daemon Phase 1.5 — Debt That Gates Phases 2 and 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Registry:** RD-001, RD-002, RD-003, RD-004 — see `docs/roadmap/remote-daemon.md` § Work registry.

**Goal:** Remove the two latent traps that would make Phase 2 and Phase 3 unsafe to write, and correct one stale project instruction.

**Architecture:** Four independent changes. RD-001 changes who owns the ssh child's lifetime (the connection, not the dial context). RD-002 makes the ssh stderr sink swappable at runtime so it can move to the log once the TUI owns the terminal. RD-003 is a documentation correction. RD-004 threads `context.Context` into three pure discovery packages so Phase 3 can bound them behind RPCs.

**Tech Stack:** Go 1.25, stdlib only. No new dependencies.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25.
- Build and test through Docker only — the host has no Go or make. `./scripts/dev.sh build`, `./scripts/dev.sh test`, `./scripts/dev.sh vet`.
- `./scripts/dev.sh build` refuses to run while any built binary in the project directory is held by a running process. Close dev TUIs/daemons first.
- Production isolation: never touch `~/.quil/`, never run `kill-daemon`/`reset-daemon`, never run bare `./quil`. Dev work uses `./quil-dev.exe` or `QUIL_HOME=<project>/.quil`.
- Commit subjects: imperative, ≤72 chars, reference the RD id. No AI/model/vendor attribution anywhere in commits or PRs.
- `internal/claudesessions` and `internal/panehistory` are stdlib-only by design (imported by the hot-path hook subprocess). Do not add dependencies to them.
- Package-level function vars as test seams are the established pattern in this codebase (`readHookSessionIDFn`, `claudeSessionExistsFn`, `exitFn`, `remoteLinkErrFn`). Use it rather than inventing a new injection style.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `internal/transport/ssh.go` | ssh dialer | RD-001 ctx semantics; RD-002 swappable stderr sink |
| `internal/transport/stderrsink.go` | **new** — runtime-swappable writer + `StderrRedirector` seam | RD-002 |
| `internal/transport/run.go` | one-shot ssh command runner | RD-001: unchanged, but gains a comment saying why it keeps `CommandContext` |
| `internal/ipc/client.go` | `DialFunc` contract | RD-001: document ctx scope |
| `cmd/quil/main.go` | TUI launch | RD-002: redirect before `tea.NewProgram` |
| `.claude/CLAUDE.md` | project instructions | RD-003 |
| `internal/gitdiscover/gitdiscover.go` | repo discovery | RD-004 |
| `internal/kubediscover/kubediscover.go` | kube context discovery | RD-004 |
| `internal/claudesessions/claudesessions.go` | session discovery | RD-004 |
| `internal/daemon/claudesessions.go` | daemon handler | RD-004: pass a bounded ctx |

---

## Task 1 (RD-001): The dial context bounds the dial, not the connection

**Why this is first:** `transport.SSH()` calls `exec.CommandContext(ctx, …)`, which ties the ssh child's life to the *dial* context. It is inert only because `dialRemote` passes `context.Background()`. Phase 2's redial loop naturally reads:

```go
ctx, cancel := context.WithTimeout(parent, 15*time.Second)
defer cancel()
conn, err := dial(ctx)      // cancel() at return kills the session it just made
```

**Files:**
- Modify: `internal/transport/ssh.go` (the `SSH` closure, currently `exec.CommandContext` at the `cmd :=` line)
- Modify: `internal/ipc/client.go` (`DialFunc` doc comment)
- Modify: `internal/transport/run.go` (comment only)
- Test: `internal/transport/ssh_test.go` (append)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `var startCommand = func(name string, args ...string) *exec.Cmd` in package `transport` — unexported test seam. `DialFunc`'s documented contract: *ctx bounds the dial; the returned `net.Conn` owns the child and releases it on `Close`.*

- [ ] **Step 1: Write the failing test**

Append to `internal/transport/ssh_test.go`. The seam is needed because `SSH()` passes ssh's own argument vector to the binary — a test binary cannot be invoked with `-o ServerAliveInterval=15 … dest "quil --stdio"`, so the command construction itself has to be substitutable.

```go
// A dialed connection must outlive the context that dialed it.
//
// The assertion is on the reaped channel, NOT on ExitCode. noExitCode is -1 and
// os/exec also reports -1 for a signalled process — the constant's own comment
// says the two differ "only in meaning" — so an exit-code comparison passes
// against both the bug and the fix. reaped closes only when a status has
// actually been recorded.
func TestSSH_ConnSurvivesDialContextCancel(t *testing.T) {
	orig := startCommand
	t.Cleanup(func() { startCommand = orig })
	used := false
	startCommand = func(_ string, _ ...string) *exec.Cmd {
		used = true
		c := exec.Command(os.Args[0], "-test.run=TestHelperSleep")
		c.Env = append(os.Environ(), "QUIL_HELPER_SLEEP=1")
		return c
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := SSH("helper", SSHOptions{SSHPath: os.Args[0]})(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Without this the test is bypassable: reverting to exec.CommandContext
	// skips the seam entirely, the fake never runs, and any failure that
	// follows is incidental rather than a report about ctx ownership.
	if !used {
		t.Fatal("SSH did not build its child through startCommand; the ctx-ownership " +
			"assertion below is not exercising the production path")
	}

	sc, ok := conn.(*stdioConn)
	if !ok {
		t.Fatalf("conn is %T, want *stdioConn", conn)
	}

	cancel()
	time.Sleep(300 * time.Millisecond) // let a cancel-kill land, if one is coming

	select {
	case <-sc.reaped:
		t.Fatal("the ssh child was reaped after the dial context was cancelled; " +
			"a connection must own its own lifetime, or every reconnect attempt " +
			"kills the session it just established")
	default:
	}
}

// TestHelperSleep is a child process, not a test. It stays alive until killed.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("QUIL_HELPER_SLEEP") == "" {
		t.Skip("helper process")
	}
	time.Sleep(30 * time.Second)
}

// An already-cancelled context must not produce a live child at all.
func TestSSH_RefusesAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := SSH("gpu01", SSHOptions{})(ctx)
	if err == nil {
		conn.Close()
		t.Fatal("dial with a cancelled context succeeded, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}
```

Ensure the file imports `context`, `errors`, `os`, `os/exec`, `time`.

- [ ] **Step 2: Run to verify it fails**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/transport/ -run 'TestSSH_ConnSurvivesDialContextCancel|TestSSH_RefusesAlreadyCancelledContext' -v
```

Expected: compile error `undefined: startCommand`.

- [ ] **Step 3: Add the seam and change the ctx semantics**

In `internal/transport/ssh.go`, add near the top of the file, after the imports:

```go
// startCommand builds the ssh child process.
//
// A package var rather than a direct exec.Command call because SSH() passes
// ssh's own argument vector to the binary, so a test cannot point SSHPath at a
// helper and still control what the helper is asked to do. Mirrors the
// injectable-function-var pattern used across this codebase.
var startCommand = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
```

In the `SSH` closure, replace:

```go
cmd := exec.CommandContext(ctx, resolved, sshArgs(dest, opts)...)
```

with:

```go
// ctx bounds the DIAL, not the connection. exec.CommandContext would bind the
// ssh child's lifetime to it, so a caller doing the ordinary
// `ctx, cancel := context.WithTimeout(...); defer cancel()` would kill the
// session at the moment the dial returned it. The returned conn owns the
// child and releases it in Close (kill + reap).
//
// run.go's RunSSH deliberately keeps CommandContext: a one-shot remote command
// IS a bounded operation, so there the two lifetimes genuinely coincide.
cmd := startCommand(resolved, sshArgs(dest, opts)...)
```

And immediately after the `resolved, err := exec.LookPath(...)` block, add the honest use of ctx:

```go
	// The only thing ctx can still bound: refuse to spawn at all if the caller
	// has already given up. Everything after Start belongs to the connection.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dial %s: %w", dest, err)
	}
```

- [ ] **Step 4: Document the contract at the seam**

In `internal/ipc/client.go`, extend the `DialFunc` comment:

```go
// DialFunc establishes one transport-level connection to a daemon. It is the
// seam that lets a Client run over something other than a Unix socket (an SSH
// channel today, a TLS connection later) without the protocol layer knowing.
//
// CONTRACT: ctx bounds the dial only. Once a DialFunc returns a net.Conn, that
// conn owns any underlying process or socket and releases it on Close —
// cancelling ctx afterwards must not disturb a live connection. Reconnect loops
// depend on this: they dial under a per-attempt timeout and would otherwise
// destroy each session as they created it.
type DialFunc func(ctx context.Context) (net.Conn, error)
```

In `internal/transport/run.go`, above the `exec.CommandContext` line:

```go
	// CommandContext is correct HERE and wrong in ssh.go's dialer: a one-shot
	// remote command is a bounded operation, so killing it when ctx expires is
	// exactly the intent. A dialed session is not bounded by its dial.
```

- [ ] **Step 5: Run to verify it passes**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/transport/ -v
```

Expected: PASS, including the pre-existing exit-code and stdioconn suites.

- [ ] **Step 6: Vet and full suite**

```bash
./scripts/dev.sh vet && ./scripts/dev.sh test
```

- [ ] **Step 7: Commit**

```bash
git add internal/transport/ssh.go internal/transport/run.go \
        internal/transport/ssh_test.go internal/ipc/client.go
git commit -F - <<'EOF'
fix(transport): let a dialed conn outlive its dial context

exec.CommandContext bound the ssh child's lifetime to the dial context.
Inert today because dialRemote passes context.Background(), but the
reconnect loop planned for phase 2 dials under a per-attempt timeout with
a deferred cancel — which would have killed each session at the moment the
dial returned it.

ctx now bounds only the spawn decision; the returned conn owns the child
and releases it in Close. RunSSH keeps CommandContext, where a bounded
one-shot command genuinely is the intent.

RD-001
EOF
```

---

## Task 2 (RD-002): Move ssh stderr to the log once the TUI owns the terminal

**Why:** ssh's stderr stays attached for the whole session, and ssh multiplexes the *remote* command's fd 2 onto it. The security half is already handled — `terminalSanitizer` strips control sequences. What remains is cosmetic: a late `packet_write_wait: Broken pipe` lands mid-render on a terminal the TUI believes it owns. Phase 2 promotes this from rare to routine, because reconnect makes ssh diagnostics a normal occurrence rather than a one-off.

The first dial must keep writing to the terminal — that is where host-key and passphrase prompts are visible — so the sink has to change at runtime, not at construction.

**Files:**
- Create: `internal/transport/stderrsink.go`
- Create: `internal/transport/stderrsink_test.go`
- Modify: `internal/transport/ssh.go` (non-batch stderr construction; `stdioConn` field)
- Modify: `internal/transport/stdioconn.go` (add `RedirectStderr` method)
- Modify: `cmd/quil/main.go` (`launchTUI`, immediately before `tea.NewProgram`)

**Interfaces:**
- Consumes: RD-001's `startCommand` only incidentally (same file).
- Produces:
  - `type switchWriter struct{ … }` with `Write([]byte) (int, error)` and `Set(io.Writer)`.
  - `type StderrRedirector interface { RedirectStderr(w io.Writer) }` — exported, asserted by `cmd/quil` the same way `LinkStatus` is.
  - `func (c *stdioConn) RedirectStderr(w io.Writer)` — satisfies it.

- [ ] **Step 1: Write the failing test**

Create `internal/transport/stderrsink_test.go`:

```go
package transport

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestSwitchWriter_RoutesToCurrentSink(t *testing.T) {
	var first, second bytes.Buffer
	sw := &switchWriter{w: &first}

	if _, err := sw.Write([]byte("before")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sw.Set(&second)
	if _, err := sw.Write([]byte("after")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := first.String(); got != "before" {
		t.Errorf("first sink = %q, want %q", got, "before")
	}
	if got := second.String(); got != "after" {
		t.Errorf("second sink = %q, want %q", got, "after")
	}
}

// A nil sink discards rather than panicking: the exec copier goroutine keeps
// writing after the TUI has torn its log down.
func TestSwitchWriter_NilSinkDiscards(t *testing.T) {
	sw := &switchWriter{}
	n, err := sw.Write([]byte("dropped"))
	if err != nil {
		t.Fatalf("write to nil sink: %v", err)
	}
	if n != len("dropped") {
		t.Errorf("n = %d, want %d", n, len("dropped"))
	}
}

// exec's copier goroutine writes while the TUI swaps the sink.
func TestSwitchWriter_ConcurrentSetAndWrite(t *testing.T) {
	sw := &switchWriter{w: io.Discard}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sw.Write([]byte("x"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sw.Set(io.Discard)
		}
	}()
	wg.Wait()
}

func TestStdioConn_SatisfiesStderrRedirector(t *testing.T) {
	var _ StderrRedirector = (*stdioConn)(nil)
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/transport/ -run 'SwitchWriter|StderrRedirector' -v
```

Expected: `undefined: switchWriter`.

- [ ] **Step 3: Implement the sink**

Create `internal/transport/stderrsink.go`:

```go
package transport

import (
	"io"
	"sync"
)

// switchWriter is an io.Writer whose destination can change while writes are in
// flight. ssh's stderr must reach the terminal during the dial — that is where
// host-key and passphrase prompts appear — and must stop reaching it the moment
// the TUI takes over the screen, because ssh keeps that descriptor for the whole
// session and a late diagnostic would land mid-render.
//
// A mutex rather than atomic.Pointer: writes are rare (ssh diagnostics only) and
// the lock also gives Set a happens-before edge with any in-flight Write, so a
// swap cannot interleave inside one message.
type switchWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write sends p to the current sink. A nil sink discards: exec's copier
// goroutine outlives the TUI's log file, and returning an error there would only
// be logged to the writer that just went away.
func (s *switchWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.w
	s.mu.Unlock()
	if w == nil {
		return len(p), nil
	}
	return w.Write(p)
}

// Set redirects subsequent writes. Pass nil to discard.
func (s *switchWriter) Set(w io.Writer) {
	s.mu.Lock()
	s.w = w
	s.mu.Unlock()
}

// StderrRedirector is implemented by transports that hold a child process whose
// stderr is attached to the operator's terminal. cmd/quil asserts for it after
// dialling, the same way it asserts for LinkStatus.
type StderrRedirector interface {
	// RedirectStderr sends any further child diagnostics to w. Pass nil to
	// discard them entirely.
	RedirectStderr(w io.Writer)
}
```

- [ ] **Step 4: Wire it into the dialer and the conn**

In `internal/transport/stdioconn.go`, add a field to `stdioConn` next to the existing `stderr` field:

```go
	// termErr is the live sink for the non-batch stderr path, or nil when this
	// conn captured stderr into a buffer instead (Batch mode).
	termErr *switchWriter
```

and the method:

```go
// RedirectStderr moves ssh's diagnostics off the terminal. No-op on a batch
// dial, where stderr was captured into a buffer for the error message and never
// reached the screen in the first place.
func (c *stdioConn) RedirectStderr(w io.Writer) {
	if c.termErr != nil {
		c.termErr.Set(w)
	}
}
```

In `internal/transport/ssh.go`, replace the non-batch branch:

```go
		cmd.Stderr = &terminalSanitizer{w: os.Stderr}
```

with:

```go
		// Swappable so cmd/quil can move these diagnostics to quil.log once the
		// TUI owns the screen. Sanitized either way — ssh multiplexes the REMOTE
		// command's fd 2 onto this stream, so it is attacker-influenced.
		termErr = &switchWriter{w: os.Stderr}
		cmd.Stderr = &terminalSanitizer{w: termErr}
```

Declare `var termErr *switchWriter` alongside the existing `var errBuf *lockedBuffer`, and after `conn := newStdioConn(...)` add, next to the existing `conn.stderr` assignment:

```go
		if termErr != nil {
			conn.termErr = termErr
		}
```

- [ ] **Step 5: Redirect at the TUI boundary**

In `cmd/quil/remote.go`, add alongside the other link seams:

```go
// remoteStderrRedirectFn moves ssh's diagnostics off the terminal. Installed by
// dialRemote when the transport supports it; nil in a local session.
var remoteStderrRedirectFn func(io.Writer)

// redirectRemoteStderr sends further ssh diagnostics to w. Safe to call in a
// local session.
func redirectRemoteStderr(w io.Writer) {
	if remoteStderrRedirectFn != nil {
		remoteStderrRedirectFn(w)
	}
}
```

and inside `dialRemote`, in the same block that captures `link`, add:

```go
			if r, ok := conn.(transport.StderrRedirector); ok {
				remoteStderrRedirectFn = r.RedirectStderr
			}
```

In `cmd/quil/main.go`, in `launchTUI`, immediately before `tea.NewProgram` is constructed:

```go
	// ssh keeps its stderr for the whole session and multiplexes the remote
	// command's fd 2 onto it. Once Bubble Tea owns the screen, a late
	// diagnostic ("packet_write_wait: Broken pipe") would land mid-render, so
	// send them to the log instead. Prompts already happened during the dial.
	redirectRemoteStderr(logFileWriter())
```

Where `logFileWriter()` returns the already-open log `io.Writer` used by `logger.Init`. If `launchTUI` does not currently hold that handle, pass it in from `main()` rather than reopening the file — two handles to a rotating log break rotation.

- [ ] **Step 6: Run tests + vet**

```bash
./scripts/dev.sh test && ./scripts/dev.sh vet
```

- [ ] **Step 7: Manual check against the dev VM**

```bash
QUIL_HOME="$(pwd)/.quil" ./quil-dev.exe --remote <test-host>
```

Confirm host-key/passphrase prompts still appear during the dial, then kill the ssh process from the remote side and confirm the diagnostic appears in `.quil/quil.log` and not on screen.

- [ ] **Step 8: Commit**

```bash
git add internal/transport/stderrsink.go internal/transport/stderrsink_test.go \
        internal/transport/ssh.go internal/transport/stdioconn.go \
        cmd/quil/remote.go cmd/quil/main.go
git commit -F - <<'EOF'
fix(remote): send ssh diagnostics to the log once the TUI starts

ssh holds its stderr for the whole session and multiplexes the remote
command's fd 2 onto it, so a late diagnostic lands mid-render on a screen
Bubble Tea believes it owns. The sink is now swappable: the terminal during
the dial, where host-key and passphrase prompts have to be visible, and
quil.log from tea.NewProgram onward.

Sanitization is unchanged and still applies on both paths.

RD-002
EOF
```

---

## Task 3 (RD-003): Correct the stale `runStatus` claim

**Why:** `.claude/CLAUDE.md` states that `runStatus` is unguarded in remote mode. It has been guarded since Phase 1 shipped — `cmd/quil/main.go` refuses `quil status` under `--remote` and points at `ssh <host> quil status`. CLAUDE.md loads into every session, so a false claim there is worse than a missing one: it sends a future session to fix what is already fixed, or teaches it to distrust a guard that works.

**Files:**
- Modify: `.claude/CLAUDE.md` (the Remote mode bullet, line ~123)

- [ ] **Step 1: Replace the sentence**

Find this exact text:

```
Known gap: `runStatus` dials `config.SocketPath()` directly and is not guarded, so `quil status --remote` reports on the local daemon
```

Replace with:

```
`runStatus` resolves `config.SocketPath()`/`daemonPID()`/`config.PidPath()` — all local — so `quil status` is refused under `--remote` at the `main.go` subcommand switch rather than answering about the wrong machine; reading remote status over the transport is RD-026
```

- [ ] **Step 2: Verify no other stale remote claims**

```bash
grep -n 'not guarded\|Known gap' .claude/CLAUDE.md
```

Expected: no remaining hits describing remote-mode guards. If another appears, verify it against `cmd/quil/` before trusting it.

- [ ] **Step 3: Commit**

```bash
git add .claude/CLAUDE.md
git commit -F - <<'EOF'
docs: quil status is guarded in remote mode, not a known gap

The instructions described runStatus as unguarded. It has been refused at
the subcommand switch since remote mode shipped. CLAUDE.md is loaded into
every session, so the stale claim was actively misleading.

RD-003
EOF
```

---

## Task 4 (RD-004): Thread `context.Context` into the discovery packages

**Why:** Phase 3 (RD-020/021/022) moves directory listing, git discovery and kube discovery behind daemon RPCs. All three do unbounded, uncancellable filesystem I/O. Locally that is a slow dialog. Behind an RPC that holds a single-flight slot — the shape `handleClaudeSessionsReq` already uses — an unbounded read becomes a stalled scan that rejects every retry while the TUI reports a timeout, and the user has no way to tell the two apart.

`claudesessions` is the sharpest case: it is already reached from a daemon worker goroutine and already single-flighted behind `d.sessionScanning`.

**Files:**
- Modify: `internal/gitdiscover/gitdiscover.go` (`Candidates`)
- Modify: `internal/kubediscover/kubediscover.go` (`Contexts`)
- Modify: `internal/claudesessions/claudesessions.go` (`List`, `ReadDetail`)
- Modify: `internal/daemon/claudesessions.go` (handler passes a bounded ctx)
- Modify: all call sites in `internal/tui/` and `internal/daemon/`
- Test: one `_test.go` per package (append)
- Delete: `techdebt/3-3-discovery-packages-have-no-io-timeout.md`

**Interfaces:**
- Produces — new signatures every later task and Phase 3 depends on:
  ```go
  func gitdiscover.Candidates(ctx context.Context, dir string) []string
  func kubediscover.Contexts(ctx context.Context) []Context
  func claudesessions.List(ctx context.Context, cwd string) (sessions []Session, truncated bool, err error)
  func claudesessions.ReadDetail(ctx context.Context, cwd, sessionID string) (Detail, error)
  ```
- Cancellation is checked at loop heads only. No goroutines, no `SetDeadline` — these are filesystem reads, and a partial result is the documented degraded behaviour for all three packages.
- `claudesessions` stays stdlib-only; `context` is stdlib.

- [ ] **Step 1: Write the failing tests**

Append to `internal/claudesessions/claudesessions_test.go`:

```go
// A cancelled context stops the scan. Callers get whatever was already found —
// every failure in this package degrades to fewer sessions, never an error that
// blocks pane creation, and cancellation is the same class of event.
func TestList_CancelledContext_StopsEarly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	projects := filepath.Join(dir, ".claude", "projects", EscapeCWD(dir))
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 20; i++ {
		p := filepath.Join(projects, fmt.Sprintf("%02d000000-0000-4000-8000-000000000000.jsonl", i))
		if err := os.WriteFile(p, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sessions, _, err := List(ctx, dir)
	if err != nil {
		t.Fatalf("List returned an error on cancel: %v (want degraded, not failed)", err)
	}
	if len(sessions) == 20 {
		t.Error("cancelled scan returned every session; cancellation was not observed")
	}
}

func TestList_LiveContext_UnchangedBehaviour(t *testing.T) {
	// Same fixture, background context: the pre-existing contract still holds.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	projects := filepath.Join(dir, ".claude", "projects", EscapeCWD(dir))
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(projects, "aaaaaaaa-0000-4000-8000-000000000000.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sessions, _, err := List(context.Background(), dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
}
```

Append the equivalent to `internal/gitdiscover/gitdiscover_test.go`:

```go
func TestCandidates_CancelledContext_ReturnsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Candidates(ctx, t.TempDir()); len(got) != 0 {
		t.Errorf("Candidates on a cancelled context = %v, want empty", got)
	}
}
```

and to `internal/kubediscover/kubediscover_test.go`:

```go
func TestContexts_CancelledContext_ReturnsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Contexts(ctx); len(got) != 0 {
		t.Errorf("Contexts on a cancelled context = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/claudesessions/ ./internal/gitdiscover/ ./internal/kubediscover/ -v
```

Expected: compile errors — `too many arguments in call to List` / `Candidates` / `Contexts`.

- [ ] **Step 3: Change the signatures**

`internal/gitdiscover/gitdiscover.go`:

```go
// Candidates returns the enclosing repository and any one-level sub-repositories
// of dir, canonicalised and capped.
//
// ctx is checked between directory entries. A cancelled scan returns what it had
// rather than an error — every other failure in this package degrades the same
// way, and a pane-creation dialog must never be blocked by discovery.
func Candidates(ctx context.Context, dir string) []string {
	if ctx.Err() != nil {
		return nil
	}
	// … existing body; inside the entry loop, at the top:
	//     if ctx.Err() != nil {
	//         return out
	//     }
}
```

`internal/kubediscover/kubediscover.go`:

```go
// Contexts parses the kubeconfig chain and returns the declared contexts.
//
// ctx is checked before each kubeconfig file is read. Cancellation yields the
// contexts already parsed, matching this package's existing degrade-to-empty
// contract for every other failure.
func Contexts(ctx context.Context) []Context {
	if ctx.Err() != nil {
		return nil
	}
	// … existing body; check ctx.Err() at the top of the per-file loop.
}
```

`internal/claudesessions/claudesessions.go` — two functions:

```go
func List(ctx context.Context, cwd string) (sessions []Session, truncated bool, err error) {
	if ctx.Err() != nil {
		return nil, false, nil
	}
	// … existing body. Check ctx.Err() at the top of the per-file title-scan
	// loop — that loop is the expensive one (up to MaxSessions × titleScanBytes)
	// and is what the daemon's single-flight slot is held across.
}

func ReadDetail(ctx context.Context, cwd, sessionID string) (Detail, error) {
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	// … existing body. ReadDetail streams the WHOLE transcript because the last
	// prompt is only knowable at the end, so check ctx.Err() every N lines
	// (N = 4096) rather than only at entry.
}
```

Note the deliberate asymmetry: `List` and the two discovery functions degrade to a partial result, while `ReadDetail` returns `ctx.Err()`. `ReadDetail` answers about one specific highlighted row — a truncated detail panel would silently misreport a session's prompt count, which is worse than an empty panel.

- [ ] **Step 4: Update every call site**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go build ./... 2>&1 | head -40
```

Fix each reported call site:
- `internal/daemon/claudesessions.go` — in `claudeSessionsResponse` and the detail handler, wrap with a bound. Use `context.WithTimeout(context.Background(), discoveryTimeout)` where `discoveryTimeout = 10 * time.Second`, declared next to the existing single-flight fields. Add `defer cancel()`.
- `internal/tui/` call sites — pass `context.Background()` for now. The TUI's own 3 s timeout tick already covers the user-visible case, and Phase 3 replaces these calls with RPCs.

- [ ] **Step 5: Run to verify they pass**

```bash
./scripts/dev.sh test
```

Expected: PASS. Confirm `internal/daemon` and `internal/tui` suites are green — the call-site edits touch both.

- [ ] **Step 6: Retire the techdebt entry**

```bash
git rm techdebt/3-3-discovery-packages-have-no-io-timeout.md
```

- [ ] **Step 7: Vet**

```bash
./scripts/dev.sh vet
```

- [ ] **Step 8: Commit**

```bash
git add internal/gitdiscover internal/kubediscover internal/claudesessions internal/daemon internal/tui
git commit -F - <<'EOF'
feat(discovery): bound repo, kube and session scans with a context

The three pure-discovery packages read the filesystem with uncancellable
calls. Locally that costs a slow dialog. Phase 3 moves all three behind
daemon RPCs holding a single-flight slot, where an unbounded read becomes a
stalled scan that rejects every retry while the client reports a timeout.

Cancellation degrades to a partial result for List/Candidates/Contexts,
matching how those functions already treat every other failure. ReadDetail
returns the error instead: it answers about one highlighted row, and a
truncated answer there would misreport a prompt count rather than show
nothing.

RD-004
EOF
```

---

## Verification (whole phase)

- [ ] `./scripts/dev.sh test` — full suite green.
- [ ] `./scripts/dev.sh vet` — clean.
- [ ] `./scripts/dev.sh test-race` — clean (RD-002 adds a mutex on a path exec writes to from its copier goroutine).
- [ ] `./scripts/dev.sh build` — all six binaries.
- [ ] Windows native run of the transport suite per the repo workflow: build the test binary in Docker with `go test -c ./internal/transport/`, run the `.exe` on the host. `GOOS=windows` vet only proves it compiles, and RD-001 and RD-002 both touch process lifetime, which is where the platforms differ.
- [ ] Manual: `--remote` against the test VM still attaches; host-key prompt still visible; killing ssh remotely puts the diagnostic in the log, not on screen.
- [ ] `docs/roadmap/remote-daemon.md` — flip RD-001..RD-004 to `done`.

## Self-review notes

Checked against `docs/superpowers/specs/2026-07-27-remote-daemon-design.md` and the Phase 2 section of the roadmap:

- **Spec coverage.** The spec's Phase 2 preconditions are the ctx contract (RD-001) and the stderr diversion (RD-002); both are tasks here. RD-004 comes from the techdebt file rather than the spec — the spec assumes the discovery functions are movable, which is true only once they are cancellable.
- **Type consistency.** `startCommand` (Task 1) is referenced by Task 2's file but not its logic. The four new discovery signatures in Task 4 are the ones Phase 3's RPC tasks consume; they are restated in that plan's Interfaces block.
- **Known gap in this plan.** Task 2 Step 5 assumes `launchTUI` can reach the log writer. If it cannot, the fix is to thread it from `main()`, which is stated in the step rather than left to the implementer to discover — but the exact plumbing depends on the current shape of `logger.Init`'s caller and was not verified line-by-line while writing this.
