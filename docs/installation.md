# Installation

Pre-built binaries are the recommended path. Quil ships single-static-binary releases for Linux, macOS, and Windows.

## Table of contents

- [Pre-built binary (recommended)](#pre-built-binary-recommended)
  - [Linux / macOS](#linux--macos)
  - [Windows](#windows)
- [Via Go](#via-go)
- [Build from source](#build-from-source)
  - [With Docker (no local Go required)](#with-docker-no-local-go-required)
  - [With local Go and make](#with-local-go-and-make)
- [Verify your install](#verify-your-install)
- [Uninstall](#uninstall)

---

## Pre-built binary (recommended)

### Linux / macOS

One-liner — detects OS + architecture, downloads the right archive from the latest GitHub Release, verifies SHA-256, installs to `~/.local/bin/`:

```bash
curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh
```

Make sure `~/.local/bin` is on your `PATH`. Add this to your `~/.bashrc` / `~/.zshrc` if it isn't:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

The installer drops two binaries: `quil` (TUI client) and `quild` (daemon).

### Windows

1. Open [Releases](https://github.com/artyomsv/quil/releases/latest).
2. Download `quil-windows-amd64.zip`.
3. Extract `quil.exe` and `quild.exe` somewhere on your `PATH` — e.g., `%LOCALAPPDATA%\Programs\Quil\`.
4. Open Windows Terminal and run `quil`.

> **Tip:** Windows Terminal captures `Ctrl+V` for its own paste action before the TUI sees it. Use `F8` to paste inside Quil — see [Keybindings → Clipboard](keybindings.md#clipboard).

> **Bundled console host (Windows 10):** the Windows build embeds Microsoft's MIT-licensed OpenConsole (`OpenConsole.exe` + `conpty.dll`, from the [`Microsoft.Windows.Console.ConPTY`](https://www.nuget.org/packages/Microsoft.Windows.Console.ConPTY) redistributable). On **Windows 10** it is extracted at first run to `%USERPROFILE%\.quil\conpty\<version>\` and used to host panes, because the Windows 10 inbox console host mis-renders some TUIs (e.g. Claude Code's input box shows an extra space after the first typed character). On **Windows 11** the inbox console host is already correct, so nothing is extracted. Attribution and license text: [`THIRD_PARTY_LICENSES.md`](https://github.com/artyomsv/quil/blob/master/THIRD_PARTY_LICENSES.md).

---

## Via Go

If you have Go 1.25+ installed and want the latest from `master`:

```bash
go install github.com/artyomsv/quil/cmd/quil@latest
go install github.com/artyomsv/quil/cmd/quild@latest
```

Binaries land in `$GOBIN` (defaults to `~/go/bin/`). Make sure that's on your `PATH`.

---

## Build from source

You need this if you're hacking on Quil or want a build with debug instrumentation.

### With Docker (no local Go required)

`scripts/dev.sh` (or `dev.ps1` on Windows) is a Docker wrapper around the Go toolchain — no local install needed:

```bash
./scripts/dev.sh build        # Build all 6 binaries (prod + dev + debug pairs)
./scripts/dev.sh test         # Run the full test suite
./scripts/dev.sh test-race    # Tests with race detector (CGo handled automatically)
./scripts/dev.sh vet          # go vet
./scripts/dev.sh cross        # Cross-compile to all 5 platforms (Linux/macOS amd64+arm64, Windows amd64)
./scripts/dev.sh image        # Build the scratch-based Docker image
./scripts/dev.sh clean        # Remove built binaries
```

The Go module cache is persisted in a Docker volume (`quil-gomod`) for fast repeated builds.

### Build variants

`build` produces three matched binary pairs via compile-time ldflags:

| Variant | TUI binary | Daemon binary | Behaviour |
|---|---|---|---|
| **prod** | `quil` | `quild` | Stripped (`-s -w`), normal install location |
| **dev** | `quil-dev` | `quild-dev` | Auto dev mode (`QUIL_HOME=.quil/` next to the binary), debug logging — fully isolated from production |
| **debug** | `quil-debug` | `quild-debug` | Debug logging but **shares production `~/.quil/`** — for diagnosing real workspace issues |

Run dev variants directly — no flags needed:

```bash
./quil-dev        # uses ./.quil/ for state — won't touch ~/.quil/
```

See [contributing](../CONTRIBUTING.md) for the full development workflow.

### With local Go and make

If you have Go 1.25+ and GNU Make:

```bash
make build         # builds quil + quild for the host platform
make test
make test-race
make vet
make cross         # all 5 target platforms
```

Output lands in the repository root.

---

## Verify your install

```bash
quil --version
quild --version
```

Both should report the same version. Mismatch is a known footgun — the TUI handshakes with the daemon on attach and refuses to proceed (or auto-restarts the daemon if it's older). See [Features → Client/daemon version handshake](features.md#clientdaemon-version-handshake).

Once installed, see [Quick start](quick-start.md) for the first-launch walkthrough.

---

## Uninstall

```bash
# Remove binaries
rm ~/.local/bin/quil ~/.local/bin/quild

# Stop the daemon if it's running
quil daemon stop 2>/dev/null || true

# Remove all state (WARNING: drops your workspace snapshot, ghost buffers, plugin configs,
# instance presets, notes, MCP logs, clipboard paste history)
rm -rf ~/.quil/
```

If you also wired Quil into an AI client (Claude Desktop, Cursor, …) via MCP, remove the `quil` entry from that client's config too. See [MCP → wiring](mcp.md#wiring-quil-into-your-ai-client) for client-specific file locations.
