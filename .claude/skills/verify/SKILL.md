---
name: verify
description: Build and drive the quil daemon to observe a change at runtime. Use when verifying quil daemon/TUI changes end-to-end.
---

# Verifying quil changes

Quil is a Go client-daemon TUI. Host has no Go/make — everything runs in Docker.

## Build (dev binaries, review-fix aware)

CRLF in shell scripts breaks `./scripts/dev.sh build`; build directly:

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine sh -c \
  'VER=$(cat VERSION | tr -d "\r") && F="-s -w -X main.version=$VER-dev" && \
   F_DEV="$F -X main.buildDevMode=true -X main.buildLogLevel=debug -X main.daemonBinary=quild-dev" && \
   GOOS=windows GOARCH=amd64 go build -ldflags "$F_DEV" -o quil-dev.exe  ./cmd/quil && \
   GOOS=windows GOARCH=amd64 go build -ldflags "$F_DEV" -o quild-dev.exe ./cmd/quild && echo BUILD_OK'
```

Windows target needs the gitignored `internal/pty/winconpty/bins/{conpty.dll,OpenConsole.exe}`; copy them from the main checkout if absent.

## Drive the daemon surface (headless)

The full-screen TUI can't be driven headlessly here; the **daemon is a real socket surface**. Never touch the production daemon (`~/.quil`) — always dev (`.quil/` in the worktree, `QUIL_HOME` set).

Start the dev daemon in the background (it restores `.quil/workspace.json`):

```bash
QUIL_HOME="$(pwd -W)/.quil" ./quild-dev.exe --background & sleep 6
```

Observe the real spawn/resize decisions in `.quil/quild.log`. Scope to the current run — the log APPENDS, it does not rotate, so old runs' lines linger:

```bash
awk '/quild v.*starting/{f=1} f' .quil/quild.log | grep -E 'spawn: pane|resize pane|panic|ERROR'
```

Session-resume (escaper) verification: each restored claude pane must spawn with `--resume <its-own-uuid>` (distinct per pane), not the shared `--continue` fallback.

## Stop the dev daemon (kills claude children too)

```bash
taskkill //PID $(cat .quil/quild.pid) //T //F ; rm -f .quil/quild.pid .quil/quild.sock
```

## Gotchas
- `-l`/`gofmt -l` on the CRLF working tree flags every file; check the git blob (`git show :path | gofmt -l /dev/stdin`) instead.
- What is NOT reachable headlessly: the TUI's small-pane preview render (crop/wrap), sidebar overlay compositing, cursor mapping — these are `View()`-side. Cover them with the `internal/tui` unit tests that call `renderPreview`/`overlayRight` directly, plus a human smoke test.
