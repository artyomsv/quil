# Contributing to Quil

## Prerequisites

- Git
- **One of:**
  - Docker (recommended — no local Go required)
  - Go 1.24+

## Building

### With Docker (no local Go or make required)

```bash
./scripts/dev.sh build        # Build both binaries
./scripts/dev.sh test         # Run tests
./scripts/dev.sh test-race    # Tests with race detector
./scripts/dev.sh vet          # Lint
./scripts/dev.sh cross        # Cross-compile all platforms
./scripts/dev.sh image        # Build minimal Docker image
./scripts/dev.sh clean        # Remove built binaries
```

### With local Go + make

```bash
make build
make test
make test-race
make vet
make cross
```

> **Note:** The Docker image is for building release binaries. Running the daemon
> inside a container is not practical — it needs host PTY access for terminal
> session management.

## Project Structure

```
cmd/
├── quil/          # TUI client entry point
└── quild/         # Daemon entry point
internal/
├── config/          # TOML configuration loading
├── daemon/          # Session manager, message routing, daemon lifecycle
├── ipc/             # IPC protocol, client, server
├── pty/             # Cross-platform PTY (Unix via creack/pty, Windows via ConPTY)
└── tui/             # Bubble Tea model, tabs, panes, styles
```

## Code Conventions

- **Formatting:** `gofmt` — enforced by build. Use tabs for Go files.
- **Naming:** Follow Go conventions — exported names are PascalCase, unexported are camelCase.
- **Build tags:** Platform-specific code uses `//go:build` tags (not `// +build`).
- **Error handling:** Return errors, don't panic. Wrap errors with context using `fmt.Errorf("context: %w", err)`.
- **Tests:** Place tests in the same package (`_test.go` suffix). Use table-driven tests where appropriate.

## Platform-Specific Code

PTY and IPC layers have platform-specific implementations:

| File | Platforms |
|---|---|
| `internal/pty/session_unix.go` | Linux, macOS, FreeBSD |
| `internal/pty/session_windows.go` | Windows |

When modifying these, ensure the `Session` interface in `session.go` is satisfied on all platforms. Verify with:

```bash
./scripts/dev.sh cross   # or: make cross (with local Go)
```

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(daemon): add state persistence
fix(tui): correct pane resize on tab switch
refactor(ipc): simplify message encoding
test(pty): add resize test for Unix
docs: update architecture decisions
```

- Imperative mood: "add" not "added"
- Max 72 characters on the first line
- Include body for non-trivial changes

## Branch Naming

```
feature/state-persistence
fix/pane-resize-crash
chore/update-dependencies
```

## Pull Requests

- One logical concern per PR
- Title follows Conventional Commits format
- Include a summary and test plan in the description
- Keep PRs under 400 lines when possible

## Architecture Decisions

When making significant design choices, document them in [ARCHITECTURE.md](ARCHITECTURE.md) using the ADR format:

```markdown
## ADR-N: Title

**Decision:** What was decided.

**Context:** Why this decision was needed.

**Consequences:** What follows from this decision.
```
