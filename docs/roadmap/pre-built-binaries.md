# Pre-Built Binaries & One-Line Install

| Field | Value |
|-------|-------|
| Priority | 1 |
| Effort | Small |
| Impact | Critical |
| Status | Done |
| Depends on | — |

## Problem

The current install path requires Docker or Go 1.25 — a significant adoption barrier. Most developer tools can be installed in under 10 seconds. Every extra step between "I want to try this" and "it's running" loses potential users. Zero friction = higher conversion from README visitor to actual user.

## Implemented Solution

GoReleaser cross-compiles pre-built binaries for 5 platform pairs, published as GitHub Releases with SHA256 checksums.

| Method | Platform | Command | Status |
|--------|----------|---------|--------|
| Install script | Linux / macOS | `curl -sSfL .../scripts/install.sh \| sh` | Done |
| Go install | Any | `go install github.com/artyomsv/quil/cmd/quil@latest` | Done |
| GitHub Release | Any | Download from Releases page | Done |
| Homebrew | macOS / Linux | `brew install artyomsv/tap/quil` | Deferred (needs external repo) |
| Winget | Windows | `winget install quil` | Deferred (needs Microsoft Store) |
| Scoop | Windows | `scoop install quil` | Deferred (needs external repo) |

## Technical Implementation

### 1. GoReleaser Config (`.goreleaser.yml`)

Build matrix:
- `linux/amd64`, `linux/arm64`
- `darwin/amd64` (Intel), `darwin/arm64` (Apple Silicon)
- `windows/amd64`
- Two binaries per platform: `quil` (TUI) and `quild` (daemon)
- `.tar.gz` for Unix, `.zip` for Windows
- SHA256 checksums in `checksums.txt`
- Version injected via `-ldflags "-s -w -X main.version={{.Version}}"`

### 2. GitHub Actions (`release.yml`)

Single workflow with two jobs:
- **`release` job** — triggers on push to master, analyzes conventional commits, bumps version, updates `VERSION` + `CHANGELOG.md`, commits, tags, pushes
- **`goreleaser` job** — runs after release job, checks out tagged commit, runs GoReleaser, publishes GitHub Release with archives and checksums

Note: both jobs are in one workflow because tags pushed with `GITHUB_TOKEN` don't trigger other workflows.

### 3. Install Script (`scripts/install.sh`)

POSIX shell script:
- Detects OS (`uname -s`) and architecture (`uname -m`)
- Fetches latest version from GitHub API (supports `GITHUB_TOKEN` for rate limiting)
- Downloads archive + checksums, verifies SHA256
- Installs to `~/.local/bin/` (configurable via `QUIL_INSTALL_DIR`)
- Supports pinned versions via `QUIL_VERSION` env var

### 4. Version Injection

Both `quil` and `quild` have `var version = "dev"` overridden at build time. Consistent `-ldflags` injection across all build paths: GoReleaser, `dev.sh`, `dev.ps1`, `rebuild.ps1`, `Makefile`.

### 5. CI Security

- All GitHub Actions pinned to immutable commit SHAs
- Per-job `permissions: contents: write` (least-privilege)
- Version format validated before use in shell commands

## Success Criteria

- [x] GitHub Release page shows all 5 platform binaries with checksums
- [x] README "Quick Start" section updated with one-line install
- [x] Install script detects OS/arch, downloads, verifies checksum, installs
- [x] `quil version` and `quild version` report correct version
- [ ] `brew install artyomsv/tap/quil` works (deferred — needs Homebrew tap repo)
- [ ] `winget install quil` works on Windows (deferred — needs Microsoft Store)

## Deferred Work

- **Homebrew tap** — needs `artyomsv/homebrew-tap` repo with formula
- **Scoop bucket** — needs `artyomsv/scoop-bucket` repo
- **Winget manifest** — needs Microsoft Store submission
- **Custom domain** — `get.quil.dev` redirect to raw install script
