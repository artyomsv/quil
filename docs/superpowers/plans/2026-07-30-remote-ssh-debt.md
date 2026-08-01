# Remote SSH Transport — Tech Debt Remediation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clear the three Medium tech-debt items the SSH remote transport
accumulated through Phase 1.5 and Phase 2, in the order their dependencies
require.

**Architecture:** Three independent defects in two packages. `internal/transport`
owns two of them (a Windows close/read race, and an unbounded remote-influenced
stderr buffer); `internal/tui` + `cmd/quil` own the third (a reconnect loop that
cannot tell a permanent authentication failure from a transient network blip).
Each task ends by deleting its own `techdebt/` file and flipping its row in the
work registry, so the tracked debt and the code cannot drift apart.

**Tech Stack:** Go 1.25, stdlib only for all three. No new dependencies.

## Why this order

```
RD-017 (Windows Close race)  ──►  un-flakes `internal/transport` on Windows
                                  │
                                  ▼
RD-018 (cap + log batch stderr) ──►  makes ssh's own words bounded and visible
                                  │
                                  ▼
RD-019 (classify permanent failure)  ──► reads exactly those words
```

**RD-017 goes first because it is the gate on verifying the other two.**
`internal/transport`'s test binary currently hangs on 1-in-2 to 3-in-4 Windows
runs. Windows is the primary development platform for this repo, and RD-018
changes `internal/transport` — landing it against a suite that hangs at random
means no one can tell a real regression from the known flake.

**RD-019 depends on RD-018** for a non-obvious reason: a classifier needs ssh's
stderr as input, and today that buffer is both unbounded and invisible. Building
the classifier first would mean building it against a source that a hostile
remote can grow without limit.

## Global Constraints

- **Go 1.25, stdlib only.** No new module dependencies in any task.
- **`gofmt` is mandatory.** The working tree is CRLF while git stores LF, so
  `gofmt -l` reports ~275 false positives and is useless here. Verify formatting
  against the **staged blob** instead — see the recipe in Task 1, Step 6.
- **Test naming:** `TestFunctionName_Scenario_Expected`; table-driven where the
  input space has more than two cases.
- **Never touch `~/.quil/`.** All manual verification runs against a dev build
  (`./scripts/dev.sh build`, then `quil-dev.exe`). See
  `.claude/rules/dev-environment.md`.
- **Commit messages:** imperative mood, ≤72 chars on the first line, body for
  anything non-trivial, cite the `RD-###`. No AI/model/vendor attribution of any
  kind, no `Co-Authored-By` trailer.
- **Do not commit this plan file.** `docs/superpowers/plans/` stays untracked in
  this repo by convention.
- Build/test only through Docker: `./scripts/dev.sh test`, `test-race`, `vet`.
  There is no local Go toolchain.
- **Imports each task adds** (compile-caught, listed so they are not a surprise):
  `internal/transport/ssh.go` → `io`; `internal/transport/ssh_test.go` →
  `fmt`, `strings`, `time`; `internal/tui/reconnect.go` → `errors`;
  `internal/tui/reconnect_test.go` → `errors`, `fmt`, `strings`.

## File Structure

| File | Change | Task |
|---|---|---|
| `internal/transport/stdioconn.go` | `pump` owns `r`'s lifetime; `Close` stops closing it | 1 |
| `internal/transport/stdioconn_test.go` | Close-during-parked-read regression test | 1 |
| `internal/transport/ssh.go` | `lockedBuffer` gains a tail cap; batch arm gains a sanitized tee | 2 |
| `internal/transport/ssh_test.go` | Cap + tee + sanitize coverage | 2 |
| `internal/transport/linkfailure.go` | **new** — pure `ClassifyLinkFailure` | 3 |
| `internal/transport/linkfailure_test.go` | **new** — table test over real OpenSSH strings | 3 |
| `internal/tui/reconnect.go` | Parked state, `ErrLinkPermanent`, resume key | 3 |
| `internal/tui/reconnect_test.go` | Park / resume / banner coverage | 3 |
| `cmd/quil/remote.go` | `redialRemote` gains a log sink; wraps the sentinel | 2, 3 |
| `cmd/quil/main.go` | Pass `logSink` into `redialRemote` | 2 |
| `docs/roadmap/remote-daemon.md` | Registry rows RD-017…RD-019 | 1, 2, 3 |
| `.claude/CLAUDE.md` | Transport bullet updates | 1, 2, 3 |

---

### Task 1: RD-017 — stop `Close` from racing the pump's read on Windows

**Files:**
- Modify: `internal/transport/stdioconn.go` (`pump` ~line 167, `Close` ~line 266)
- Test: `internal/transport/stdioconn_test.go`
- Modify: `docs/roadmap/remote-daemon.md` (registry)
- Delete: `techdebt/3-3-stdioconn-close-races-pump-read-on-windows.md`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: no signature change. `Close() error` and `pump()` keep their
  contracts; only the descriptor's owner moves.

**Background you need before touching this.** `Close` currently runs
`c.w.Close()`, then `c.r.Close()`, and only *then* kills the child. On Windows
the pipe handle is non-overlapped, so a parked `ReadFile` cannot be cancelled;
`internal/poll.FD.Close` waits on a semaphore until that read's reference drops,
and the kill that would end the read is three lines further down and never
reached. Linux is unaffected because `os.Pipe` descriptors go through netpoll,
where `Close` unblocks pending reads.

The fix is **not** to reorder the kill above `c.r.Close()`. `Close` deliberately
waits for a natural exit before killing (`pumpFailed()` + `exitGrace`), because
on Windows `Kill` is `TerminateProcess(handle, 1)` and would overwrite the real
exit status — and `remoteinstall` detection reads exactly those codes (127 =
remote command not found, 126 = found but not executable). Hoisting the kill
would break auto-install detection to fix a test hang.

Instead **give the read descriptor to the goroutine that reads it.** `pump` is
the only reader, always runs (`newStdioConn` starts it unconditionally), and
always exits when its read errors — so it can close `r` on its way out and no
cross-goroutine close-during-read exists to race.

- [ ] **Step 1: Write the failing test**

Add to `internal/transport/stdioconn_test.go`:

```go
// TestStdioConn_Close_ReturnsWhilePumpIsParkedInRead pins the descriptor
// ownership that keeps Close off the pump's in-flight read.
//
// On Windows the pipe handle is non-overlapped, so a parked ReadFile cannot be
// cancelled and internal/poll.FD.Close blocks until its reference drops. If
// Close ever closes c.r itself while the pump is inside Read, this test hangs
// and the package times out — which is the failure it exists to convert into a
// named, bounded one.
func TestStdioConn_Close_ReturnsWhilePumpIsParkedInRead(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	// Hold the write end open for the whole test so the pump's read genuinely
	// parks instead of taking an immediate EOF. Closed via t.Cleanup rather
	// than defer so it outlives the Close under test.
	t.Cleanup(func() {
		outW.Close()
		inR.Close()
		inW.Close()
	})

	c := newStdioConn(nil, outR, inW, "parked-read")

	// Let the pump reach its read. Without this the race is not set up and the
	// test passes for the wrong reason.
	time.Sleep(300 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Close()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked while the pump was parked in Read — " +
			"the read descriptor is being closed from the wrong goroutine")
	}
}
```

A `nil` cmd is deliberate: it isolates the descriptor race from the child's
lifecycle, and `exitcode_test.go:109` already establishes that a nil-cmd conn is
a supported shape.

- [ ] **Step 2: Run it and watch it fail on Windows**

```bash
./scripts/dev.sh test
```

Linux will PASS — this defect does not exist there. To see it fail you need a
native Windows run; cross-compile a test binary in Docker and execute it on the
host (per the `run-windows-tests-natively` note):

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 \
  golang:1.25 go test -c ./internal/transport -o /src/transport_win_test.exe
./transport_win_test.exe -test.run TestStdioConn_Close_ReturnsWhilePumpIsParkedInRead -test.v -test.timeout 30s
```

Expected on Windows: FAIL with `Close blocked while the pump was parked in Read`.

> If it passes on Windows before the fix, stop and re-check that the sleep is
> long enough and that `outW` is still open — a test that cannot fail proves
> nothing, and this repo has shipped two of those already.

- [ ] **Step 3: Move the descriptor's ownership into the pump**

In `internal/transport/stdioconn.go`, change `pump`'s defer stack:

```go
func (c *stdioConn) pump() {
	// Deferred LIFO: close(readCh) runs FIRST, unparking any blocked reader,
	// and only then does reap() park this goroutine in Wait. Reversed, a child
	// that closed stdout without exiting would hold every reader blocked until
	// Close killed it.
	//
	// c.r is closed HERE, by its only reader, and never by Close. On Windows the
	// handle is non-overlapped, so a parked ReadFile cannot be cancelled and
	// internal/poll.FD.Close blocks until that read's reference drops — closing
	// from Close deadlocks against this goroutine, and the kill that would end
	// the read sits below the close that never returns. Owning it here removes
	// the cross-goroutine close entirely rather than trying to time it.
	//
	// The trade: Close no longer force-unparks this read. On a real conn the
	// kill in Close EOFs it, and a cmd-less conn is EOF'd by Close's c.w.Close()
	// when both ends are the same pipe — but a cmd-less conn whose stdout write
	// end is held open elsewhere now keeps this goroutine and its descriptor
	// alive until that holder lets go. A test-only shape, and the deadlock it
	// replaces was reachable from the TUI exit path and every reconnect attempt.
	defer c.reap()
	defer c.r.Close()
	defer close(c.readCh)
	buf := make([]byte, readChunk)
	// ... loop unchanged
}
```

Ordering inside that stack matters: `close(readCh)` still runs first (unparking
readers), then `c.r.Close()`, then `reap()`.

- [ ] **Step 4: Stop `Close` from closing the read handle**

In `Close`, replace the `_ = c.r.Close()` line:

```go
		_ = c.w.Close()
		// c.r is NOT closed here — pump owns it. Closing ssh's stdin above and
		// killing the child below is what ends the pump's read; it then closes
		// the descriptor on its way out. See pump's defer stack for why this
		// goroutine must not do it.
		if c.cmd != nil && c.cmd.Process != nil {
```

Everything from `if c.pumpFailed()` down is unchanged.

- [ ] **Step 5: Run the tests**

```bash
./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet
```

Then the native Windows run again — **the whole package, repeatedly**, because
the original symptom was a load-dependent hang, not a single-test failure:

```bash
docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 \
  golang:1.25 go test -c ./internal/transport -o /src/transport_win_test.exe
for i in 1 2 3 4 5 6 7 8 9 10; do ./transport_win_test.exe -test.timeout 60s || echo "FAILED run $i"; done
```

Expected: 10/10 clean. The pre-fix baseline is 1-in-2 to 3-in-4 hangs, so ten
consecutive passes is a meaningful signal rather than a lucky run.

- [ ] **Step 6: Verify formatting against the staged blob**

`gofmt -l` on the working tree is useless here (CRLF vs LF). Check what git will
actually store:

```bash
git add internal/transport/stdioconn.go internal/transport/stdioconn_test.go
git show :internal/transport/stdioconn.go | docker run --rm -i golang:1.25 gofmt -l /dev/stdin
```

Expected: no output.

- [ ] **Step 7: Retire the debt and update the registry**

```bash
git rm techdebt/3-3-stdioconn-close-races-pump-read-on-windows.md
```

In `docs/roadmap/remote-daemon.md`, add to the Phase 2 registry table, after the
RD-016 row:

```markdown
| RD-017 | `stdioConn.Close` no longer closes the read handle the pump is parked on (Windows) | — | done |
```

In `.claude/CLAUDE.md`, in the `internal/transport/` bullet, after the
single-reader sentence, add:

> `pump` owns `r` and closes it on its way out; `Close` deliberately does not
> touch it. Windows pipe handles are non-overlapped, so a parked `ReadFile`
> cannot be cancelled and `internal/poll.FD.Close` blocks on its reference —
> closing from `Close` deadlocks against the pump, and the kill that would end
> the read sits *below* the close that never returns. Reordering the kill above
> it is not the fix: `Close` waits for a natural exit first on purpose, because
> `Kill` is `TerminateProcess(handle, 1)` on Windows and would overwrite the
> 127/126 statuses `remoteinstall` detection reads.

- [ ] **Step 8: Commit**

```bash
git add internal/transport/stdioconn.go internal/transport/stdioconn_test.go \
        docs/roadmap/remote-daemon.md .claude/CLAUDE.md
git commit -F - <<'EOF'
fix(transport): let the pump own the read descriptor (RD-017)

stdioConn.Close closed the read handle while the pump goroutine was parked
in ReadFile. Windows pipe handles are non-overlapped, so the read cannot be
cancelled and internal/poll.FD.Close waits on its reference forever; the kill
that would end the read sits below the close that never returns. The package
test binary hung on 1-in-2 to 3-in-4 Windows runs, and the same Close runs on
the TUI exit path and on every reconnect attempt.

Reordering the kill above the close was rejected: Close waits for a natural
exit first on purpose, because Kill is TerminateProcess(handle, 1) on Windows
and would overwrite the 127/126 statuses remote-install detection reads.

The pump is the only reader and always exits when its read errors, so it now
closes the descriptor itself and Close never touches it.
EOF
```

---

### Task 2: RD-018 — bound the batch ssh stderr buffer and send it to the log

**Files:**
- Modify: `internal/transport/ssh.go` (`lockedBuffer` ~line 289, batch arm ~line 232, `SSHOptions` ~line 105)
- Test: `internal/transport/ssh_test.go`
- Modify: `cmd/quil/remote.go` (`redialRemote` ~line 424, `dialRemoteTransport` ~line 268)
- Modify: `cmd/quil/main.go` (line ~474)
- Modify: `docs/roadmap/remote-daemon.md`, `.claude/CLAUDE.md`
- Delete: `techdebt/3-3-batch-ssh-stderr-unbounded-and-unlogged.md`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `transport.SSHOptions.StderrSink io.Writer` — batch dials tee sanitized
    stderr here. Nil keeps today's behaviour.
  - `redialRemote(cfg config.Config, logW io.Writer) tui.RedialFunc` — the
    second parameter is new. Task 3 extends this function further.

**Two defects, one seam.** `lockedBuffer` is a mutex around a `bytes.Buffer`
with `Write` and `String` and nothing else — nothing drains, caps or resets it.
That was harmless while batch dials were seconds long (the `remote setup` probe,
`RunSSH` one-shots). Phase 2 made a *successful* reconnect keep its batch conn
for the rest of the session, so the buffer is now session-length with a
remote-influenced writer: ssh multiplexes the remote command's fd 2 onto its own
stderr. Separately, those post-reconnect diagnostics stop reaching `quil.log`,
because `RedirectStderr` is a no-op on the batch path (`termErr` is nil) — so a
mid-session `Timeout, server not responding` is lost exactly when a flapping
link is being diagnosed.

**Read this before writing the tee.** The batch arm assigns `cmd.Stderr = errBuf`
**raw**; only the non-batch arm wraps in `terminalSanitizer`. The techdebt file
claims "the sanitizer still sits in front" — it does not, on this path. Teeing
unsanitized bytes into `quil.log` would give a hostile remote an escape-sequence
channel into a file the F1 log viewer renders. The sanitizer must be *added*
here.

- [ ] **Step 1: Write the failing tests**

Add to `internal/transport/ssh_test.go`:

```go
func TestLockedBuffer_Write_KeepsOnlyTheTailOnceCapped(t *testing.T) {
	var b lockedBuffer
	// Three full caps of distinguishable content. LinkErr wants the most recent
	// diagnostic, so the tail is what must survive.
	for i := 0; i < 3; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i)}, stderrBufCap)
		n, err := b.Write(chunk)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("Write returned %d, want %d — io.Writer requires the full count", n, len(chunk))
		}
	}

	got := b.String()
	if len(got) > stderrBufCap {
		t.Errorf("buffer grew to %d bytes, want at most %d", len(got), stderrBufCap)
	}
	if strings.Contains(got, "a") {
		t.Error("oldest content survived; the cap must drop from the front")
	}
	if !strings.Contains(got, "c") {
		t.Error("newest content was dropped; the cap must keep the tail")
	}
}

// TestSSH_BatchStderrSink_ReceivesSanitizedOutput pins that a batch dial's
// diagnostics reach the log sink with control sequences already filtered.
//
// A re-exec helper rather than `sh -c`: internal/transport's test binary is run
// NATIVELY on Windows (see Task 1), where there is no sh. Mirrors the existing
// TestHelperSleep pattern, including SSHPath: os.Args[0] so exec.LookPath is
// deterministic rather than finding a real ssh on PATH.
func TestSSH_BatchStderrSink_ReceivesSanitizedOutput(t *testing.T) {
	orig := startCommand
	t.Cleanup(func() { startCommand = orig })
	used := false
	startCommand = func(_ string, _ ...string) *exec.Cmd {
		used = true
		c := exec.Command(os.Args[0], "-test.run=TestHelperStderr")
		c.Env = append(os.Environ(), "QUIL_HELPER_STDERR=1")
		return c
	}

	var sink lockedBuffer // the sink is written from exec's copier goroutine
	conn, err := SSH("helper", SSHOptions{
		SSHPath:    os.Args[0],
		Batch:      true,
		StderrSink: &sink,
	})(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if !used {
		t.Fatal("SSH did not build its child through startCommand; the assertion below is not exercising the production path")
	}

	// Poll rather than sleep: the copier goroutine is asynchronous, and a fixed
	// sleep is either flaky or slow.
	var got string
	for i := 0; i < 100; i++ {
		if got = sink.String(); strings.Contains(got, "warned") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(got, "warned") {
		t.Fatalf("plain diagnostic text never reached the sink: %q", got)
	}
	if strings.Contains(got, "\033]52") {
		t.Errorf("OSC 52 reached the sink unsanitized: %q", got)
	}
}

// TestHelperStderr is a child process, not a test. It writes one hostile-looking
// diagnostic to stderr and exits.
func TestHelperStderr(t *testing.T) {
	if os.Getenv("QUIL_HELPER_STDERR") == "" {
		t.Skip("helper process")
	}
	// OSC 52 is a clipboard write — the concrete capability a compromised remote
	// gains if this stream reaches a renderer unfiltered.
	fmt.Fprint(os.Stderr, "\033]52;c;cGF5bG9hZA==\007warned\n")
}
```

The sink is a `lockedBuffer`, not a bare `bytes.Buffer`: exec's copier goroutine
writes it while the test polls, and `-race` would flag the unsynchronised read.

- [ ] **Step 2: Run them to verify they fail**

```bash
./scripts/dev.sh test
```

Expected: FAIL — `stderrBufCap` undefined, `SSHOptions.StderrSink` undefined.

- [ ] **Step 3: Cap the buffer**

In `internal/transport/ssh.go`:

```go
// stderrBufCap bounds what a batch dial retains from ssh's stderr.
//
// ssh multiplexes the REMOTE command's fd 2 onto its own stderr, and Phase 2
// gave a successful batch dial the lifetime of the whole session — so without a
// cap a noisy or hostile remote grows this process without bound. LinkErr
// already truncates the MESSAGE to 2000 bytes and wants the most recent output,
// so keeping the tail loses nothing a caller reads.
const stderrBufCap = 64 << 10

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.buf.Len() > stderrBufCap {
		// Copy before Reset: the slice Bytes() returns aliases the buffer's
		// own storage, which Reset rewinds and the next Write overwrites.
		keep := b.buf.Bytes()
		tail := make([]byte, stderrBufCap)
		copy(tail, keep[len(keep)-stderrBufCap:])
		b.buf.Reset()
		b.buf.Write(tail)
	}
	return n, err
}
```

The returned `n` stays the count from the inner `Write` — trimming afterwards
must not change what the caller is told it wrote, or `io.Copy` treats it as a
short write and retries forever.

- [ ] **Step 4: Add the sink option and the sanitized tee**

In `SSHOptions`, after `Batch`:

```go
	// StderrSink receives a sanitized copy of the child's stderr on a batch
	// dial. Nil discards it, which is the pre-Phase-2 behaviour.
	//
	// Only meaningful with Batch: a non-batch dial's stderr goes to the
	// terminal and is redirected later through RedirectStderr instead.
	StderrSink io.Writer
```

And the error-swallowing wrapper, beside `lockedBuffer`:

```go
// bestEffort reports every write as a full success.
//
// It sits under the stderr tee because io.MultiWriter stops at the first error:
// without it, one failed log write silences the diagnostic buffer LinkErr reads.
// A dropped log line is the acceptable loss; a lost error message is not.
type bestEffort struct{ w io.Writer }

func (b bestEffort) Write(p []byte) (int, error) {
	if b.w != nil {
		_, _ = b.w.Write(p)
	}
	return len(p), nil
}
```

In the batch arm of the dialer:

```go
		if opts.Batch {
			errBuf = &lockedBuffer{}
			cmd.Stderr = errBuf
			if opts.StderrSink != nil {
				// Sanitized into the sink, raw into the buffer. LinkErr
				// sanitizes the buffer when it reads it, but the sink is
				// quil.log — rendered by the F1 viewer and read with cat — and
				// this stream carries the remote command's fd 2, so it is
				// attacker-influenced. Without the filter a compromised remote
				// gets an escape-sequence channel into the operator's log.
				//
				// bestEffort wraps the sink because io.MultiWriter ABORTS on the
				// first writer error: a failed log write (disk full, rotation
				// holding the file on Windows) would stop exec's copier
				// entirely, and errBuf would go silent too — losing LinkErr's
				// diagnostics to a logging failure. Same reasoning as
				// switchWriter's nil-sink branch in stderrsink.go.
				cmd.Stderr = io.MultiWriter(errBuf, &terminalSanitizer{w: bestEffort{opts.StderrSink}})
			}
		} else {
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
./scripts/dev.sh test && ./scripts/dev.sh test-race
```

Expected: PASS.

- [ ] **Step 6: Feed the log writer into reconnect dials**

In `cmd/quil/remote.go`, thread the sink through both functions:

```go
func dialRemoteTransport(ctx context.Context, cfg config.Config, batch bool, stderrSink io.Writer) (*ipc.Client, transport.LinkStatus, error) {
	opts := remoteSSHOptions(cfg)
	opts.Batch = batch
	opts.StderrSink = stderrSink
```

and

```go
// redialRemote builds the Model's reconnect dialer.
//
// logW receives ssh's diagnostics for every reconnect. The startup dial routes
// them through RedirectStderr instead, but that seam is a no-op on a batch dial
// — stderr was captured into a buffer and never reached a terminal — so without
// this a flapping link goes undiagnosable after its first reconnect.
func redialRemote(cfg config.Config, logW io.Writer) tui.RedialFunc {
	return func(old tui.Client) (tui.Client, error) {
		// ... existing old-client release and ctx setup unchanged ...
		client, link, err := dialRemoteTransport(ctx, cfg, true, logW)
```

`RedialFunc` is `func(old Client) (Client, error)` — it takes the dead client to
release, not a context; the context is built inside. Do not change that shape.

Update the startup dial's call to pass `nil` — its stderr goes to the terminal:

```go
	client, link, err := dialRemoteTransport(context.Background(), cfg, false, nil)
```

In `cmd/quil/main.go` line ~474:

```go
		model.SetRedialFunc(redialRemote(cfg, logSink))
```

`logSink` is already in scope there — it is the same rotating writer
`redirectRemoteStderr(logSink)` uses at line 497, which is deliberate: opening
the file a second time would put two writers on one rotating log and break
rotation.

- [ ] **Step 7: Full suite and format check**

```bash
./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet
git add internal/transport/ssh.go internal/transport/ssh_test.go cmd/quil/remote.go cmd/quil/main.go
for f in internal/transport/ssh.go cmd/quil/remote.go cmd/quil/main.go; do
  git show ":$f" | docker run --rm -i golang:1.25 gofmt -l /dev/stdin
done
```

Expected: tests pass, no gofmt output.

- [ ] **Step 8: Retire the debt and update the registry**

```bash
git rm techdebt/3-3-batch-ssh-stderr-unbounded-and-unlogged.md
```

Registry row:

```markdown
| RD-018 | Cap the batch-dial stderr buffer; tee it sanitized into `quil.log` | — | done |
```

`.claude/CLAUDE.md`, `internal/transport/` bullet — extend the `SSHOptions.Batch`
sentence:

> `SSHOptions.Batch` adds `BatchMode=yes` and captures stderr into a
> `lockedBuffer` capped at `stderrBufCap` (64 KiB, tail-kept — a successful
> reconnect holds its batch conn for the whole session, and ssh multiplexes the
> remote command's fd 2 onto that stream). `SSHOptions.StderrSink` tees the same
> stream, **sanitized**, into `quil.log`; the buffer keeps raw bytes because
> `LinkErr` sanitizes on read, but the log is rendered by the F1 viewer so the
> filter goes in front of the sink.

- [ ] **Step 9: Commit**

```bash
git add internal/transport/ssh.go internal/transport/ssh_test.go \
        cmd/quil/remote.go cmd/quil/main.go \
        docs/roadmap/remote-daemon.md .claude/CLAUDE.md
git commit -F - <<'EOF'
fix(transport): bound and log batch ssh stderr (RD-018)

A batch dial captured ssh's stderr into a buffer nothing drained, capped or
reset. That was bounded in practice while batch dials were one-shots, but a
successful reconnect now holds its conn for the whole session, and ssh
multiplexes the remote command's fd 2 onto that stream — so a noisy or hostile
remote could grow the buffer without limit.

The same change fixes an observability regression: RedirectStderr is a no-op on
a batch dial, so post-reconnect ssh diagnostics stopped reaching quil.log and a
flapping link became undiagnosable after its first reconnect.

The tee is sanitized. Only the non-batch path wrapped stderr in the terminal
sanitizer; the log is rendered by the F1 viewer, so the filter had to be added
rather than assumed.
EOF
```

---

### Task 3: RD-019 — park the reconnect loop on a permanent ssh failure

**Files:**
- Create: `internal/transport/linkfailure.go`, `internal/transport/linkfailure_test.go`
- Modify: `internal/tui/reconnect.go`, `internal/tui/reconnect_test.go`
- Modify: `cmd/quil/remote.go` (`redialRemote`)
- Modify: `docs/roadmap/remote-daemon.md`, `.claude/CLAUDE.md`
- Delete: `techdebt/3-3-reconnect-cannot-classify-permanent-ssh-failure.md`

**Interfaces:**
- Consumes: `SSHOptions.StderrSink` from Task 2 (the classifier reads the same
  stream, now bounded); `redialRemote(cfg, logW)` signature from Task 2.
- Produces:
  - `transport.ClassifyLinkFailure(stderr string) LinkFailure` — pure.
  - `transport.LinkFailure` — `LinkFailureTransient` | `LinkFailurePermanent`.
  - `tui.ErrLinkPermanent` — sentinel; `redialRemote` wraps it, the reconnect
    loop tests with `errors.Is`.

**The problem.** Every reconnect is a full authentication with `BatchMode=yes`.
There is a realistic case — not an attack, a consequence of the design's own
asymmetry — where authentication can *never* succeed while the link is healthy:
the startup dial is non-batch so ssh can prompt for a passphrase or host key,
every reconnect is batch because Bubble Tea owns the terminal by then. An
operator with a passphrase-protected key and no agent authenticates once at
launch, then every reconnect fails `publickey` permanently. A dead agent socket
and a changed host key behave identically. The loop produces a steady stream of
failed authentications from the operator's own address; a default fail2ban
`sshd` jail (5 failures / 10 min) bans them, and `recidive` escalates that
across every service on the host.

`reconnectSlowMaxDelay` already decayed this from ~120/hour to ~33/hour, which
**reduces but does not remove** it: the early fast attempts still put roughly ten
into the first ten minutes.

**Why the exit code cannot do this.** ssh returns 255 for all of its own
failures — a permanent `Permission denied` and a transient `Connection timed
out` are both 255. Discriminating means matching ssh's prose, which is the
pattern `internal/remoteinstall` explicitly rejected ("Detection is the ssh EXIT
CODE, never the string 'command not found' — locale-dependent"). The exception
is defensible here and must be *documented as an exception*: OpenSSH does not
localise these messages, whereas a shell's "command not found" is localised by
every major shell. Match a short, explicit list and default to transient —
mis-classifying a transient failure as permanent parks a session that would have
healed, which is the worse error.

- [ ] **Step 1: Write the failing classifier test**

Create `internal/transport/linkfailure_test.go`:

```go
package transport

import "testing"

func TestClassifyLinkFailure(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   LinkFailure
	}{
		{"empty", "", LinkFailureTransient},
		{"publickey denied", "user@host: Permission denied (publickey).", LinkFailurePermanent},
		{"host key changed", "@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@\nHost key verification failed.", LinkFailurePermanent},
		{"batch refused a prompt", "Host key verification failed.", LinkFailurePermanent},
		{"no matching host key type", "Unable to negotiate with 10.0.0.1 port 22: no matching host key type found.", LinkFailurePermanent},
		{"connect timeout", "ssh: connect to host gpu01 port 22: Connection timed out", LinkFailureTransient},
		{"refused", "ssh: connect to host gpu01 port 22: Connection refused", LinkFailureTransient},
		{"dns", "ssh: Could not resolve hostname gpu01: Name or service not known", LinkFailureTransient},
		{"keepalive drop", "Timeout, server 10.0.0.1 not responding.", LinkFailureTransient},
		{"broken pipe", "packet_write_wait: Connection to 10.0.0.1 port 22: Broken pipe", LinkFailureTransient},
		{"remote reboot mid-session", "Connection to gpu01 closed by remote host.", LinkFailureTransient},
		// Case and surrounding noise must not defeat it: ssh interleaves
		// banner text and debug lines with the diagnostic.
		{"denied among noise", "debug1: Offering public key\nPermission denied (publickey,password).\n", LinkFailurePermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLinkFailure(tt.stderr); got != tt.want {
				t.Errorf("ClassifyLinkFailure(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}
```

Every string above is real OpenSSH output. **Before implementing, confirm the
permanent list against the OpenSSH on the machine you are running** — the whole
mechanism rests on these being current:

```bash
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes nonexistent-user@127.0.0.1 true 2>&1 | head -5
ssh -V
```

- [ ] **Step 2: Run it to verify it fails**

```bash
./scripts/dev.sh test
```

Expected: FAIL — `ClassifyLinkFailure` and `LinkFailure` undefined.

- [ ] **Step 3: Implement the classifier**

Create `internal/transport/linkfailure.go`:

```go
package transport

import "strings"

// LinkFailure says whether a failed dial is worth retrying.
type LinkFailure int

const (
	// LinkFailureTransient is the default and the safe answer: retry.
	LinkFailureTransient LinkFailure = iota
	// LinkFailurePermanent means further identical attempts cannot succeed.
	LinkFailurePermanent
)

func (f LinkFailure) String() string {
	if f == LinkFailurePermanent {
		return "permanent"
	}
	return "transient"
}

// permanentMarkers are OpenSSH diagnostics that mean an identical retry cannot
// succeed. Kept short and explicit on purpose.
//
// This project's rule is to detect on the ssh EXIT CODE, never on its prose
// (internal/remoteinstall says so, about "command not found"). This is a
// deliberate, documented exception, for two reasons. ssh answers 255 for every
// failure of its own, so a permanent "Permission denied" and a transient
// "Connection timed out" are the same code and the rule's preferred signal
// carries no information here. And the reason the rule exists — locale
// dependence — does not apply: OpenSSH ships no translations for these strings,
// whereas "command not found" is localised by every major shell.
//
// The asymmetry is deliberate. An unmatched string is TRANSIENT, so a message
// we have not seen keeps retrying; mis-parking a session that would have healed
// is worse than retrying one that will not.
var permanentMarkers = []string{
	"permission denied",             // publickey/password exhausted
	"host key verification failed",  // known_hosts mismatch, or BatchMode refusing the prompt
	"no matching host key type",     // negotiation failure — config, not weather
	"no matching cipher found",      //
	"no matching mac found",         //
	"too many authentication failures",
}

// ClassifyLinkFailure reports whether ssh's stderr describes a failure that
// retrying cannot fix.
//
// Lower-cased before matching: ssh's own casing is stable, but the stream also
// carries the remote command's fd 2 and a server banner, and a case-sensitive
// match is a silent no-op the tests would not obviously catch.
func ClassifyLinkFailure(stderr string) LinkFailure {
	s := strings.ToLower(stderr)
	for _, m := range permanentMarkers {
		if strings.Contains(s, m) {
			return LinkFailurePermanent
		}
	}
	return LinkFailureTransient
}
```

- [ ] **Step 4: Run it to verify it passes**

```bash
./scripts/dev.sh test
```

Expected: PASS, all 12 subtests.

- [ ] **Step 5: Write the failing TUI park test**

Add to `internal/tui/reconnect_test.go`:

```go
func TestReconnect_PermanentFailureParksInsteadOfRetrying(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.reconnect.active = true
	m.reconnect.attempt = 3

	updated, cmd := m.Update(redialResultMsg{
		gen: m.clientGen,
		err: fmt.Errorf("dial gpu01: %w", ErrLinkPermanent),
	})
	got := updated.(Model)

	if !got.reconnect.parked {
		t.Fatal("a permanent failure must park the loop")
	}
	if cmd != nil {
		t.Error("parking must not schedule another redial")
	}
	if !got.reconnect.active {
		t.Error("the banner must stay up while parked — the session is not over")
	}
}

func TestReconnect_ResumeKeyRestartsAParkedLoop(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.reconnect.active = true
	m.reconnect.parked = true

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	got := updated.(Model)

	if got.reconnect.parked {
		t.Error("the resume key must clear the parked state")
	}
	if cmd == nil {
		t.Error("resuming must schedule a redial")
	}
}

func TestReconnect_ParkedBannerNamesTheCauseAndTheResumeKey(t *testing.T) {
	m := newReconnectTestModel(t, 1)
	m.reconnect.active = true
	m.reconnect.parked = true
	m.reconnect.lastErr = errors.New("Permission denied (publickey)")

	banner := stripANSI(m.renderReconnectBanner(80))
	// bannerResumeHint, not the bare letter "r": every existing rung already
	// contains an r ("ctrl+q", "unreachable", "Connecting"), so asserting on
	// the letter passes with no resume hint rendered at all.
	for _, want := range []string{"Permission denied", bannerResumeHint, "ctrl+q"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner %q is missing %q", banner, want)
		}
	}
}
```

`lastErr` is an `error` — the literal must be `errors.New(...)`.

**The resume assertion must not be the bare letter `r`.** `"ctrl+q"` contains an
`r`, as do `"unreachable"`, `"retry"` and `"Connecting"` — so
`strings.Contains(banner, "r")` is true whether or not a resume hint is
rendered. Introduce a named constant in `reconnect.go` and assert on it:

```go
// bannerResumeHint is the parked banner's resume affordance. A named constant
// because the banner test and the bannerCandidates invariant test must assert
// the same literal — asserting the bare key letter cannot fail, since every
// rung already contains an "r".
const bannerResumeHint = "r retries"
```

Signatures to match exactly, all confirmed against the current source:
`newReconnectTestModel(t *testing.T, n int) *Model` (two args — `n` is the pane
count); `redialResultMsg{gen int, client Client, err error}` — the field is
`gen`, **not** `attempt` (`attempt` lives on `redialTickMsg`). `stripANSI` is in
`notes_test.go`, same package. Reuse both helpers rather than adding parallels.

- [ ] **Step 6: Run to verify they fail**

```bash
./scripts/dev.sh test
```

Expected: FAIL — `ErrLinkPermanent` and `reconnectState.parked` undefined.

- [ ] **Step 7: Add the sentinel, the parked state, and the resume key**

In `internal/tui/reconnect.go`:

```go
// ErrLinkPermanent marks a dial failure that an identical retry cannot fix —
// a rejected key, a changed host key, an algorithm mismatch.
//
// The dialer wraps it; the loop tests with errors.Is. Keeping it a sentinel
// rather than a bool on the result message means the classification travels
// with the error it describes and cannot be dropped by a future call site that
// forgets to copy a field.
var ErrLinkPermanent = errors.New("remote link failure is permanent")
```

Add to `reconnectState`:

```go
	// parked stops the retry loop after a failure that cannot heal on its own.
	// The banner stays up — the session is paused, not over — and the resume key
	// restarts the loop once the operator has fixed the cause.
	parked bool
```

In the `redialResultMsg` arm, before scheduling the next attempt:

```go
	if errors.Is(msg.err, ErrLinkPermanent) {
		// Every reconnect is a full authentication. Retrying a rejected key
		// produces a steady stream of failed authentications from the
		// operator's own address, which a default fail2ban sshd jail bans —
		// locking them out of a host that was never unreachable.
		m.reconnect.parked = true
		m.reconnect.lastErr = msg.err
		log.Printf("remote: parking reconnect — %v", msg.err)
		return m, nil
	}
```

`lastErr` is an **`error`**, not a string (`reconnect.go:34`), and
`renderReconnectBanner` already applies `firstErrLine` when it renders
(`reconnect.go:337`). Assigning `firstErrLine(...)` here would both fail to
compile and duplicate work the render path already does.
```

The resume key **cannot** go inside `freezeInput`. That method has a value
receiver and returns `(tea.Cmd, bool)`, so it can neither clear `parked` nor
hand back a mutated Model — and `isFreezeEscape`'s contract is "should this key
*end* the session", which resuming is not. Put it in `Update`, ahead of the
`freezeInput` choke point:

```go
// reconnectResumeKey restarts a parked loop. Checked ahead of freezeInput,
// which has a value receiver and so cannot clear the parked state itself.
const reconnectResumeKey = "r"

// resumeReconnect leaves the parked state and arms a fresh attempt.
//
// The attempt counter is deliberately NOT reset: the operator resuming does not
// make the earlier failures un-happen, and restarting at the base delay would
// undo the rate decay that keeps a still-broken key under a fail2ban threshold.
func (m Model) resumeReconnect() (tea.Model, tea.Cmd) {
	m.reconnect.parked = false
	m.reconnect.lastErr = nil // an error field, not a string — see reconnect.go:34
	// scheduleRedial returns (tea.Model, tea.Cmd) and carries the mutated copy
	// forward — Model is a value type, so returning m here instead would drop
	// whatever scheduleRedial set.
	return m.scheduleRedial()
}
```

and at the top of `Update`'s key handling, before `freezeInput` runs:

```go
	if key, ok := msg.(tea.KeyPressMsg); ok &&
		m.reconnect.active && m.reconnect.parked &&
		kbMatches(key.String(), reconnectResumeKey) {
		return m.resumeReconnect()
	}
```

- [ ] **Step 7b: Give the parked state its own banner rung**

`bannerCandidates` branches on `nextAt` — future is a countdown, past is
`Connecting`. While parked, `nextAt` is stale-past, so without an explicit
branch a parked session renders `Connecting…` forever: the most misleading
string available, since nothing is connecting. **Check parked before both
existing phases:**

```go
	if st.parked {
		// No countdown and no "Connecting": the loop has stopped on purpose and
		// will not move until the operator acts. Every rung keeps both keys.
		return []string{
			fmt.Sprintf("Reconnect paused — %s, ctrl+q quits", bannerResumeHint),
			fmt.Sprintf("Paused — %s, ctrl+q", bannerResumeHint),
			fmt.Sprintf("%s · ctrl+q", bannerResumeHint),
		}
	}
```

Extend the existing `bannerCandidates` invariant test
(`internal/tui/reconnect_test.go:1381-1397`) to cover the parked rungs, asserting
`bannerResumeHint` **and** `ctrl+q` on every one. Extend it rather than adding a
second test — one invariant, one place.

- [ ] **Step 8: Wrap the sentinel in the dialer**

**Put this in the verify-failure branch, not the dial-failure branch.** The
`if err != nil` branch at the top of `redialRemote` cannot see an authentication
failure: `transport.SSH` returns a nil conn on failure so `link` is always nil
there, and a dial only fails at spawn level — ssh's binary missing, pipes
exhausted. `exec.Cmd.Start` succeeds as soon as the ssh *binary* launches, long
before it resolves the host or authenticates. Every network and auth failure
instead surfaces as a failed `verifyRemoteLink`. Classifying in the wrong branch
would compile, pass its unit tests against a fake, and never fire in production.

In `cmd/quil/remote.go`, extend the existing verify-failure block (~line 453).
**The snippet below is authoritative on ordering** — classification goes after
`client.Close()`, because `cause` is fully materialised by then and the
load-bearing constraint is only that `LinkErr()` is read *before* `Close`, which
the existing lines already satisfy. Do not move those lines while adding this.

```go
		if verr := verifyRemoteLink(client, linkVerifyTimeout); verr != nil {
			// ... existing comment and cause resolution unchanged ...
			cause := verr
			if link != nil {
				if le := link.LinkErr(); le != nil {
					cause = le
				}
			}
			client.Close()
			if transport.ClassifyLinkFailure(cause.Error()) == transport.LinkFailurePermanent {
				// cause FIRST, sentinel second. %w works in any position since
				// Go 1.20, and errors.Is is unaffected — but the banner renders
				// this string, and leading with the sentinel would spend ~35
				// cells on "remote link failure is permanent: " before reaching
				// ssh's own words, on a row that already fights for width.
				return nil, fmt.Errorf("%v: %w", cause, tui.ErrLinkPermanent)
			}
			return nil, cause
		}
```

`LinkErr()` returns an `error`, so the classifier — which is pure over a string
so it can be table-tested without constructing errors — takes `cause.Error()`.

The existing read-before-`Close()` ordering is load-bearing and already
documented in place: `Close` unblocks the pump via `<-done`, and that path can
return without ever setting `pumpErr`, so a later read comes back nil and loses
ssh's own words. Leave those lines where they are; only append after them.

- [ ] **Step 9: Run everything**

```bash
./scripts/dev.sh test && ./scripts/dev.sh test-race && ./scripts/dev.sh vet
```

Expected: PASS.

- [ ] **Step 10: Verify the banner by rendering it, not by asserting on it**

Seven passing unit tests previously hid two real banner defects on this exact
component (an ssh error that vanished at 80 columns, `ctrl+q` truncated to
`ctr…` at 40). Print the real output at the boundary widths:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.25 \
  go test ./internal/tui -run TestReconnect_ParkedBanner -v
```

Then eyeball a parked banner at widths 40, 60 and 80 — add a temporary
`t.Logf("%q", banner)` if the existing tests do not already print it. Confirm at
each width: the cause is visible or truncated (never absent), and both `r` and
`ctrl+q` survive.

- [ ] **Step 11: Retire the debt and update the registry**

```bash
git rm techdebt/3-3-reconnect-cannot-classify-permanent-ssh-failure.md
```

**Fix the comment that cites it.** `internal/tui/reconnect.go:84`, in the
`reconnectSlowMaxDelay` block, names that exact file path and asserts that
classification does not exist — after this task it is both dangling and false.
Replace the trailing sentences with:

```go
	// attempts can still trip a strict jail. Classification now closes that:
	// a failure ClassifyLinkFailure calls permanent parks the loop instead of
	// retrying it. The decay below still governs everything it cannot classify,
	// which is the default — exit code 255 covers both a permanent denial and a
	// transient timeout, so an unmatched message stays transient on purpose.
```

Verified: no other `.go` file references any of the three deleted techdebt paths.

Registry row:

```markdown
| RD-019 | Park the reconnect loop on a permanent ssh failure; `r` resumes | RD-018 | done |
```

`.claude/CLAUDE.md`, remote-reconnect bullet — append:

> A failure whose stderr matches `transport.ClassifyLinkFailure`'s short
> permanent list (rejected key, changed host key, algorithm mismatch) **parks**
> the loop rather than retrying: every reconnect is a full authentication, and a
> rejected key retried forever is a stream of failed auths from the operator's
> own address that a default fail2ban `sshd` jail bans. Parking keeps the banner
> up and adds `r` to resume. Matching ssh's prose is a documented exception to
> the exit-code-only rule — ssh answers 255 for all of its own failures, so the
> code carries no signal, and OpenSSH ships no translations for these strings.
> An unmatched message is treated as TRANSIENT, because mis-parking a session
> that would have healed is the worse error.

- [ ] **Step 12: Commit**

```bash
git add internal/transport/linkfailure.go internal/transport/linkfailure_test.go \
        internal/tui/reconnect.go internal/tui/reconnect_test.go \
        cmd/quil/remote.go docs/roadmap/remote-daemon.md .claude/CLAUDE.md
git commit -F - <<'EOF'
feat(remote): park reconnect on a permanent ssh failure (RD-019)

Every reconnect is a full authentication with BatchMode=yes, and the design's
own asymmetry makes permanent failure reachable without an attacker: the startup
dial is non-batch so ssh can prompt for a passphrase, every reconnect is batch
because the TUI owns the terminal by then. An operator with a passphrase-only
key authenticates once at launch and then fails publickey forever. A default
fail2ban sshd jail bans the resulting stream of failed auths, and recidive
escalates it across every service on the host.

Rate decay reduced this from ~120/hour to ~33/hour but left ~10 attempts in the
first ten minutes, which a strict jail acts on.

Classification matches a short list of OpenSSH strings. That is a documented
exception to the exit-code-only detection rule: ssh answers 255 for every
failure of its own, so the code cannot discriminate, and the locale dependence
the rule guards against does not apply to untranslated OpenSSH diagnostics. An
unmatched message stays transient — mis-parking a session that would have healed
is the worse failure.
EOF
```

---

### Task 4: Verify on a real link, then close the phase

**Files:** `docs/roadmap/remote-daemon.md` only.

**Interfaces:** Consumes all three preceding tasks.

None of Phase 2 has been exercised against a real ssh link beyond two checks
that both used a *terminal* pane — which code review showed is exactly the pane
type that works either way. These tasks change the transport underneath it, so
the outstanding checks are now prerequisites for calling any of this done.

- [ ] **Step 1: Build the dev pair**

```bash
./scripts/dev.sh build
```

If it refuses because a binary is held, close the running dev TUI first. Never
run bare `./quil` — it attaches to the production daemon.

- [ ] **Step 2: Run the outstanding Phase 2 checks**

Launch `./quil-dev.exe --remote <host>` and confirm `[dev]` in the status bar
before anything destructive. Work down the table in
`docs/roadmap/remote-daemon.md` § Phase 2, highest value first:

| Check | Confirms |
|---|---|
| Reconnect an **opencode** pane, confirm it is not blank | the ghost-replay gate — the case the critical bug and its first fix both got wrong |
| Scroll a reconnected pane to the top | scrollback not doubled (RD-013) |
| Sleep the laptop 2 minutes, wake | reconnect with no intervention |
| Ctrl+Q during an outage | the only exit from a host that never returns |
| Drop the link mid-agent-turn with subagents running | spinner reflects reality (RD-014) |
| Local session, daemon stopped | still exits rather than spinning — local must be unchanged |

- [ ] **Step 3: Verify the two new behaviours specifically**

- **RD-018:** after one reconnect, `grep ssh .quil/quil.log` shows ssh's
  diagnostics. Before this task the log went silent after the first reconnect.
- **RD-019:** with an ssh key that the remote will reject (temporarily remove it
  from `authorized_keys`), drop the link and confirm the banner parks and names
  the cause rather than retrying, and that `r` resumes once the key is restored.

- [ ] **Step 4: Update the status table and commit**

Flip each verified row to **done** with the observed numbers, the way the two
existing done rows record them ("reconnected in 343 ms, 1 attempt"). Real
measurements only — do not fill in plausible-looking values.

```bash
git add docs/roadmap/remote-daemon.md
git commit -F - <<'EOF'
docs: record Phase 2 verification against a real link

EOF
```

---

## Out of scope

- **Phase 3 (RD-020…RD-028)** — remote-correct UI. Feature work, not debt: every
  filesystem-reading surface still reads the laptop's. Tracked in the registry.
- **Phase 4 (RD-030…RD-034)** — mTLS transport. Gated on RD-034.
- **`techdebt/3-3-discovery-scan-cannot-be-interrupted-mid-syscall.md`** — same
  *shape* as RD-017 (a blocking syscall no context can interrupt) but a different
  subsystem, and RD-020 supersedes it by moving those scans server-side.
- **`techdebt/pty/4-1-windows-close-does-not-reap.md`** — Low/Trivial, and in
  `internal/pty`, not the SSH transport. Worth doing; not this plan.
- **[qa/1] `redialRemote` has no unit coverage as a whole.** Reduced rather than
  fixed: Task 3 puts the classification in a pure, table-tested function, so what
  remains untested in `redialRemote` is thin composition. Fully covering it needs
  a new exported seam over `internal/transport`'s unexported `startCommand`,
  which is a larger change than the risk justifies.
