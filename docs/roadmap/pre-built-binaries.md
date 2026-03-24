# Pre-Built Binaries & One-Line Install

| Field | Value |
|-------|-------|
| Priority | 1 |
| Effort | Small |
| Impact | Critical |
| Status | In Progress |
| Depends on | — |

## Problem

The current install path requires Docker or Go 1.25 — a significant adoption barrier. Most developer tools can be installed in under 10 seconds. Every extra step between "I want to try this" and "it's running" loses potential users. Zero friction = higher conversion from README visitor to actual user.

## Proposed Solution

Use **goreleaser** to produce pre-built binaries for every major platform, published as GitHub Releases. Provide multiple install paths so users can pick what's familiar:

| Method | Platform | Command |
|--------|----------|---------|
| Install script | Linux / macOS | `curl -sSfL https://get.aethel.dev \| sh` |
| Homebrew | macOS / Linux | `brew install artyomsv/tap/aethel` |
| Winget | Windows | `winget install aethel` |
| Scoop | Windows | `scoop install aethel` |
| Go install | Any | `go install github.com/artyomsv/aethel/cmd/aethel@latest` |
| GitHub Release | Any | Download from Releases page |

## User Experience

```bash
# macOS / Linux
curl -sSfL https://get.aethel.dev | sh
aethel

# Windows
winget install aethel
aethel

# Go users
go install github.com/artyomsv/aethel/cmd/aethel@latest
```

First-run experience should feel instant. The binary self-bootstraps: creates `~/.aethel/`, writes default plugins, starts daemon on first launch.

## Technical Approach

1. **goreleaser config** (`.goreleaser.yml`) — build matrix for:
   - `linux/amd64`, `linux/arm64`
   - `darwin/amd64` (Intel), `darwin/arm64` (Apple Silicon)
   - `windows/amd64`
   - Two binaries per platform: `aethel` (TUI) and `aetheld` (daemon)

2. **GitHub Actions CI** — trigger on tag push (`v*`), run goreleaser, publish release

3. **Install script** (`get.aethel.dev`) — detect OS/arch, download correct binary, place in PATH

4. **Homebrew tap** — `artyomsv/homebrew-tap` repo with formula

5. **Scoop/Winget manifests** — JSON manifests pointing to GitHub Releases

## Success Criteria

- [ ] `curl -sSfL https://get.aethel.dev | sh && aethel` works on fresh Linux/macOS
- [ ] `winget install aethel && aethel` works on Windows
- [ ] `brew install artyomsv/tap/aethel` works
- [ ] GitHub Release page shows all platform binaries
- [ ] README "Quick Start" section updated with one-line install

## Open Questions

- Domain for install script: `get.aethel.dev` vs GitHub Pages?
- Should the install script also install shell completions?
- Include `aetheld` in same package or auto-extract from `aethel` binary?
