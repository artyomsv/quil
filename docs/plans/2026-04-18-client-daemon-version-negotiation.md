# Client-Daemon Version Negotiation — Implementation Plan

**Date:** 2026-04-18
**Target release:** 1.8.0

> **For agentic workers:** Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement task-by-task. Steps use `- [ ]` checkboxes for tracking.

## Goal

Remove the manual "stop daemon → replace both binaries → restart" dance on Quil upgrade. At TUI launch, compare TUI and daemon versions:

| Situation | Action |
|---|---|
| Same version | Attach normally (current behavior) |
| TUI < daemon | Blocking dialog nudging upgrade; **TUI refuses to attach** |
| TUI > daemon | Confirmation dialog; on OK, gracefully stop daemon and auto-start new one |
| No daemon running | Spawn new daemon (current behavior) |
| Version req times out or unparseable | Treat as "pre-versioning daemon" → upgrade-confirmation flow |

## Non-Goals

- Two-way IPC protocol versioning (we version binaries, not message schemas)
- Hot-swap without restart
- Dev-mode enforcement (dev/debug variants and unparseable versions skip the check entirely)

## Architecture

1. **Version source.** Both binaries already embed `main.version` via GoReleaser ldflags. Introduce a tiny shared `internal/version/` package so TUI, daemon, and tests all read/compare from one place.
2. **New IPC pair.** `MsgVersionReq` (client → daemon, empty payload) and `MsgVersionResp` (daemon → client, `{Version string}`), correlated via `Message.ID` (request-response pattern already used by MCP bridge).
3. **Handshake before attach.** The TUI's existing daemon-connect path (`cmd/quil/main.go`) gains a thin handshake step:
   - Connect socket (already auto-starts a daemon if socket missing).
   - Send `MsgVersionReq` with a request ID. Wait up to 2 seconds for `MsgVersionResp`.
   - Compare via semver.
   - If mismatched, open the TUI into a blocking full-screen dialog variant (`dialogVersionMismatch`) instead of the normal post-attach flow.
4. **Restart flow (client > daemon).** On user confirmation: send `MsgShutdown`, wait for socket file to disappear (poll, 5s cap), then call the existing daemon-spawn path. When the new socket appears, re-handshake and — if versions match this time — attach normally. If they still don't match, show an error dialog pointing at the PATH hazard.
5. **Dev-mode skip.** When `QUIL_HOME` is overridden or the binary is the `-dev` variant, skip the handshake entirely. Same daemon/client pair always match in the project's `.quil/` dir.

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/version/version.go` (new) | `Current()`, `SetCurrent(string)`, `Compare(a, b string) (int, error)` |
| `internal/version/version_test.go` (new) | Semver compare table-driven tests (pre-release suffixes tolerated) |
| `internal/ipc/messages.go` | Add `MsgVersionReq`, `MsgVersionResp`, `VersionRespPayload` |
| `internal/daemon/daemon.go` | Handler for `MsgVersionReq` → responds with `version.Current()` |
| `internal/daemon/daemon_test.go` (if present) | Roundtrip test: MsgVersionReq → MsgVersionResp |
| `cmd/quild/main.go` | Call `version.SetCurrent(version)` before serving IPC |
| `cmd/quil/main.go` | Call `version.SetCurrent(version)`; invoke new `versionHandshake()` before Model startup |
| `cmd/quil/handshake.go` (new) | `versionHandshake(client) → (matched bool, daemonVer string, err error)` |
| `cmd/quil/restart_daemon.go` (new) | `restartDaemon(client)` — shutdown + wait + respawn |
| `internal/tui/dialog.go` | Add `dialogVersionMismatch` iota; render + key handler for both variants |
| `internal/tui/model.go` | Model gets `versionMismatch *versionMismatchState` carrying state for the dialog |

## Tasks

### Task 1: `internal/version/` package

**Files:** `internal/version/version.go` (new), `internal/version/version_test.go` (new)

- [ ] **Step 1: Write package**

```go
// Package version is the single source of truth for the running binary's
// version string. Both quil (TUI) and quild (daemon) set Current() from
// their main.version ldflag; tests and runtime code read via Current().
package version

import (
	"fmt"
	"strconv"
	"strings"
)

var current = "dev"

// SetCurrent stores the binary's version. Call exactly once from main().
func SetCurrent(v string) { current = strings.TrimSpace(v) }

// Current returns the version set by SetCurrent, or "dev" if unset.
func Current() string { return current }

// Parsed returns the numeric major.minor.patch from a semver-like string.
// Accepts "1.2.3", "1.2.3-rc1", "v1.2.3". Strips leading "v" and any
// pre-release / build suffix after the first "-" or "+".
func Parsed(v string) (major, minor, patch int, err error) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	for _, sep := range []string{"-", "+"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("not semver: %q", v)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return 0, 0, 0, fmt.Errorf("not numeric %q in %q: %w", p, v, convErr)
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], nil
}

// Compare returns -1 if a<b, 0 if a==b, +1 if a>b. Returns a non-nil error
// when either side is unparseable; callers must decide how to interpret.
func Compare(a, b string) (int, error) {
	am, an, ap, err := Parsed(a)
	if err != nil {
		return 0, err
	}
	bm, bn, bp, err := Parsed(b)
	if err != nil {
		return 0, err
	}
	switch {
	case am != bm:
		return sign(am - bm), nil
	case an != bn:
		return sign(an - bn), nil
	case ap != bp:
		return sign(ap - bp), nil
	}
	return 0, nil
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
```

- [ ] **Step 2: Table-driven tests**

Cover: equal, major/minor/patch wins, `v` prefix stripped, `1.10.0 > 1.9.0` (lexical trap), pre-release/build suffix stripped, empty/garbage → error.

### Task 2: IPC message

**Files:** `internal/ipc/messages.go`, `internal/daemon/daemon.go`, daemon tests

- [ ] **Step 1: Add message types**

In `internal/ipc/messages.go`, add to the `Msg*` constant block:
```go
MsgVersionReq  = "version_req"
MsgVersionResp = "version_resp"
```
And add the payload struct:
```go
type VersionRespPayload struct {
    Version string `json:"version"`
}
```
`MsgVersionReq` has no payload — empty body.

- [ ] **Step 2: Daemon handler**

In `internal/daemon/daemon.go` dispatch switch (look for other `case ipc.Msg...` examples — same pattern as `MsgScreenshotPaneReq`):
```go
case ipc.MsgVersionReq:
    respondTo(conn, msg.ID, ipc.MsgVersionResp, ipc.VersionRespPayload{
        Version: version.Current(),
    })
```
Add the import.

- [ ] **Step 3: Roundtrip test**

Either extend an existing daemon IPC test or add one that spins up a daemon, sends `MsgVersionReq` with an ID, awaits `MsgVersionResp`, asserts version field matches `version.Current()`.

### Task 3: Wire version into both binaries

**Files:** `cmd/quil/main.go`, `cmd/quild/main.go`

- [ ] Both mains already have a `var version = "dev"` (or equivalent ldflag sink). Call `version.SetCurrent(version)` EARLY in `main()` — before any IPC starts, before any config load that might log it. One line each.

### Task 4: Client-side handshake

**Files:** `cmd/quil/handshake.go` (new), `cmd/quil/main.go`

- [ ] **Step 1: Write the handshake function**

```go
// versionHandshake sends MsgVersionReq and compares the response to the
// TUI's own version. Returns:
//   matched  - true when versions are byte-for-byte equal
//   cmp      - -1 if TUI < daemon, +1 if TUI > daemon, 0 if equal
//   daemonVer - raw string the daemon reported (for UI)
//   err      - transport or parse error; callers typically treat as "old daemon"
func versionHandshake(client *ipc.Client) (matched bool, cmp int, daemonVer string, err error) { ... }
```
Timeout: 2s. If the response never arrives, return `err == ErrTimeout` with `daemonVer == ""`.

- [ ] **Step 2: Call from main** right after `client.Connect()` and before `NewModel()`.

- [ ] **Step 3: Dev-mode skip**

If `config.IsDevMode()` (detect via `QUIL_HOME` set, or build-tag ldflag `buildDevMode`), skip the handshake and proceed. Log at debug level.

- [ ] **Step 4: Unparseable skip**

If `version.Current()` parses with error (e.g., `"dev"`), skip the handshake with a debug log.

### Task 5: Dialog variants

**Files:** `internal/tui/dialog.go`, `internal/tui/model.go`

- [ ] **Step 1: Add dialog iota**

```go
dialogVersionMismatch
```
- [ ] **Step 2: State struct on Model**

```go
type versionMismatchState struct {
    tuiVer    string
    daemonVer string // may be empty when handshake timed out
    clientOlder bool // true when TUI < daemon, drives which variant renders
    cursor    int   // focused button index
}
```
- [ ] **Step 3: Render**

When `clientOlder == true`:
```
Quil needs an update

 Your TUI is v{tuiVer}.
 The running daemon is v{daemonVer}.
 
 Please download v{daemonVer} (or newer) from:
 https://github.com/artyomsv/quil/releases

 [Copy URL]   [Quit]
```
When `clientOlder == false`:
```
Daemon restart required

 TUI version: v{tuiVer}
 Daemon version: v{daemonVer}   (or "unknown — pre-versioning daemon")
 
 Continue to restart the daemon to v{tuiVer}. This will respawn all
 panes. Your workspace (tabs, layouts, CWDs, notes) is preserved.

 [Restart daemon]   [Quit]
```

- [ ] **Step 4: Key handler**

Tab / Left / Right cycle buttons; Enter fires. No Esc bypass when `clientOlder` (blocking). When `!clientOlder`, Esc = Quit for consistency.

### Task 6: Restart flow

**Files:** `cmd/quil/restart_daemon.go` (new)

- [ ] **Step 1: Send MsgShutdown**

Use existing IPC client.

- [ ] **Step 2: Wait for socket disappearance**

Poll `os.Stat(config.SocketPath())` every 100 ms up to 5 s. After timeout, try to remove stale PID file (mirroring the startup cleanup path).

- [ ] **Step 3: Spawn new daemon**

Reuse `findDaemonBinary()` + the existing `proc_{unix,windows}.go` spawn path. **Important:** for the restart path, prefer executable-adjacent binary over PATH (reverse of the default order) to avoid spawning an older `quild` that happens to be on PATH. If the current TUI is `quil-1.7.0.exe`, its adjacent `quild.exe` is almost certainly the matching 1.7.0 daemon. Accept a small helper like `findDaemonBinaryForUpgrade()`.

- [ ] **Step 4: Wait for socket appearance + re-handshake**

Poll up to 5 s. On connect, call `versionHandshake` again. If versions now match, continue with normal attach. If still mismatched, show an error dialog: "Restarted daemon still reports {v}. Check PATH — another quild may be shadowing the bundled one."

### Task 7: Tests and manual smoke

- [ ] Unit: `internal/version` table-driven cases (see Task 1 Step 2)
- [ ] Unit: handshake function with a fake IPC (timeout path, success path, parse-error path)
- [ ] Integration: spin up real daemon on a temp socket path, perform handshake, assert payload
- [ ] Manual smoke on Windows after building two variants at different versions:
  1. Run `quil-1.6.0.exe`, keep TUI open in another pane to start daemon 1.6.0.
  2. Close TUI. Leave daemon 1.6.0 running.
  3. Run `quil-1.7.0.exe`. Expect: "Daemon restart required" dialog → confirm → panes respawn → version badge reads 1.7.0.
  4. Now close TUI but keep daemon 1.7.0. Run `quil-1.6.0.exe`. Expect blocking "Quil needs an update" dialog; TUI refuses to enter.

## Rollout Notes

- This is the FIRST release that speaks `MsgVersionReq`. Running the new TUI against a 1.7.0 daemon will hit the timeout path — that's expected and handled: it falls into the upgrade-confirmation flow. One manual "Restart daemon" click, and the user is on matched-version forever after.
- Ship in 1.8.0. The `feat:` of this plan triggers minor bump via the newly-hardened parser.

## Risks

- **PATH shadowing**: addressed via `findDaemonBinaryForUpgrade()` preferring adjacent over PATH, plus the post-restart re-handshake guard.
- **Windows .exe file lock during restart**: the new daemon file is already on disk (user extracted archive). We don't replace files at runtime — we just stop the old process and start the new one. No lock issue.
- **Daemon hang on MsgShutdown**: the 5-second socket-disappearance timeout is the safety net; if exceeded, we log and attempt to proceed with spawn (new daemon will refuse if old socket still listening, giving a clean error).
- **Future IPC wire-format changes**: handshake timeout is 2 s — a much older daemon that crashes on unknown message types would cause us to re-handshake against the freshly spawned daemon. Old daemons today just log-and-ignore unknown types, so timeout is the expected path.
