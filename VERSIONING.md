# Versioning

Quil follows [Semantic Versioning](https://semver.org/) (SemVer).

## Version Format

`MAJOR.MINOR.PATCH` (e.g., `0.2.0`)

| Component | Incremented when |
|-----------|-----------------|
| **MAJOR** | Breaking changes to CLI, IPC protocol, or config format |
| **MINOR** | New features added in a backwards-compatible manner |
| **PATCH** | Backwards-compatible bug fixes |

## Automatic Version Bumps

Version bumps are determined automatically from **Conventional Commit** messages
on push to `master`:

| Commit prefix | Bump | Example |
|--------------|------|---------|
| `fix:`, `perf:` | Patch | `fix: resolve scroll sync issue` |
| `feat:` | Minor | `feat: add tab color cycling` |
| `feat!:`, `fix!:`, or footer `BREAKING CHANGE:` | Major | `feat!: redesign IPC protocol` |
| `chore:`, `docs:`, `test:`, `ci:`, `style:`, `refactor:` | No bump | `docs: update README` |

If no bumpable commits are found since the last tag, no release is created.

## Version Sources

- **`VERSION`** file at repo root — single source of truth (`0.2.0`)
- **`cmd/quil/main.go`** — build-time injection via `-ldflags "-X main.version=..."`
- **`CHANGELOG.md`** — version header added automatically on release
- **Git tags** — `v0.2.0` format, created by the release workflow

## Release Process

1. Develop on a feature branch
2. Create a PR to `master` — CI runs tests + build
3. Merge PR — release workflow automatically:
   - Reads `VERSION`, analyzes commits, determines bump
   - Updates `VERSION` and `CHANGELOG.md`
   - Commits `chore(release): vX.Y.Z`
   - Creates git tag `vX.Y.Z`
   - Cross-compiles binaries (Linux + macOS, amd64 + arm64)
   - Creates GitHub Release with changelog and `.tar.gz` assets

## Dry Run Mode

Both CI and Release workflows support manual triggering via `workflow_dispatch`
with a **dry_run** toggle:

- **CI (dry run):** Runs tests and build normally, logs a notice that it's a dry run.
- **Release (dry run):** Computes the version bump, builds binaries, extracts
  changelog — but **skips** the commit, tag, push, and release creation.
  Check the workflow logs to verify everything works correctly.

To trigger: Go to Actions → select workflow → "Run workflow" → check "Dry run".

## Manual Override

To force a specific version, edit the `VERSION` file directly before merging.
The release workflow will use whatever is in `VERSION` as the base for the next bump.
