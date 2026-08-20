# Contributing to Quil

Thanks for considering a contribution! This page covers what stays at the project root: how to submit code, commit/branch conventions, and PR expectations. Deeper material lives in [docs/](docs/) — pointers below.

## Reporting bugs and ideas

Open an issue with:

- Quil version (`quil --version` and `quild --version` — both must match)
- OS + terminal emulator
- Steps to reproduce
- Relevant excerpts from `~/.quil/quil.log` and `~/.quil/quild.log` (see [Troubleshooting → log files](docs/troubleshooting.md#log-files--where-to-look))

For feature ideas, check [docs/roadmap.md](docs/roadmap.md) and [docs/roadmap/](docs/roadmap/) first — many ideas are already scoped as PRDs.

## Building locally

The full build workflow — Docker-based and native-Go paths, dev/debug variants, cross-compilation — lives in **[docs/installation.md → Build from source](docs/installation.md#build-from-source)**.

The shortest path:

```bash
./scripts/dev.sh build       # Docker, no local Go needed
./scripts/dev.sh test
./scripts/dev.sh test-race
./scripts/dev.sh vet
```

## Code conventions

- **Formatting** — `gofmt` is mandatory. Tabs in Go files, 2 spaces in YAML / TOML / JSON.
- **Naming** — Go conventions. Acronyms stay uppercase (`IPC`, `PTY`, `JSON`, `HTTP`). No `GetXxx` getters — just `Xxx()`.
- **Errors** — return them, wrap with context: `fmt.Errorf("doing X: %w", err)`. Never `panic` for recoverable conditions.
- **Goroutines** — every goroutine must have a clear shutdown path (`context.Context` cancel, done channel, defer).
- **Platform code** — `//go:build` tags (not `// +build`). One file per variant: `foo_unix.go` / `foo_windows.go`.
- **Tests** — same package, `_test.go` suffix. Table-driven where it earns its keep. Run `go test -race ./...` before submitting.
- **No global mutable state** — pass dependencies explicitly. The exception: package-level swappable function vars for testability (already established pattern in `internal/daemon/`).

For deeper architectural rationale see [docs/architecture.md](docs/architecture.md) (24 ADRs cover every meaningful decision).

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/):

```
feat(daemon): propagate TUI CWD to daemon for new panes
fix(tui): correct pane resize on tab switch
refactor(ipc): simplify message encoding
test(pty): add resize test for Unix
docs: clarify MCP redaction model
chore(release): v1.12.0
```

- Imperative mood: "add" not "added"
- First line ≤ 72 characters
- Body wraps at 72 characters, explains the **why** not the **what** (the diff shows the what)
- Reference issues in the footer when applicable: `Closes #42`

## Branch naming

`<type>/<short-description>`, all lowercase, hyphen-separated:

```
feature/plugin-opencode
fix/cors-config
docs/restructure-and-mcp-guide
chore/update-deps
```

## Pull requests

- One logical concern per PR — bundling unrelated changes makes review painful
- Title follows Conventional Commits — this is release-critical, not cosmetic (see below)
- Description has a **Summary** (1–3 bullets) and a **Test plan** (how to verify)
- Aim for under 400 lines of diff. If you must go bigger, split into stacked PRs with a clear sequence.

PRs get squash-merged to `master` for a clean history. GitHub uses the **PR title** as the squash-commit subject, and the release pipeline classifies that subject by conventional-commit type to compute the version bump, update `VERSION` + `CHANGELOG.md`, tag, and publish via GoReleaser.

> **A non-conventional PR title silently skips the release.** The bump regex requires the type followed by `:`, `(`, or `/` (e.g. `fix:`, `fix(tui):`, `fix/…`). A title like `Fix the pane bug` matches nothing, so the release job logs `No version-bumping commits found` and exits without tagging — the merge lands on `master` but never ships. If a merged change didn't produce a release, check the PR title first.

> **A PR that touches `cmd/` or `internal/` must also add a changelog fragment.** CI enforces this. Create `changelog.d/<type>-<slug>.md` holding the bullet text — the release pipeline adds the version header and section heading but never writes prose, so an entry you don't write in the PR is never written at all, and the released version ships with a permanently empty section. If the change genuinely has nothing to tell users (pure refactor, internal cleanup), say so explicitly with a `changelog.d/none-<slug>.md` fragment. Test-only, docs-only, and site-only PRs are exempt. See [changelog.d/README.md](changelog.d/README.md), and validate before pushing with `sh scripts/promote-changelog.sh --check`.
>
> One file per PR is what keeps parallel PRs from conflicting: entries used to go under a shared `## [Unreleased]` heading in `CHANGELOG.md`, so two open PRs edited the same anchor line and the second to merge always conflicted. **Do not edit `CHANGELOG.md` directly** — the release workflow owns it.

## Licensing of contributions

Quil is licensed under the [Apache License 2.0](LICENSE). By opening a pull
request you agree that your contribution is licensed under the same terms —
this is Apache-2.0 section 5, which applies by default to anything you
deliberately submit for inclusion, unless you state otherwise in the PR.

There is no CLA and no copyright assignment. You keep your copyright.

Two consequences worth knowing, because they are easy to get wrong:

- **Only contribute code you have the right to contribute.** Your own work, or
  work under a licence compatible with Apache-2.0 — and if it is the latter,
  say where it came from in the PR description so the attribution can be
  carried into [`NOTICE`](NOTICE) and
  [`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).
- **The name is not covered by the licence.** Apache-2.0 grants rights over the
  code and explicitly not over trade names or marks. If you are distributing a
  modified build rather than contributing one, read [TRADEMARK.md](TRADEMARK.md)
  — the short version is that forks are welcome and should carry their own name.

## Documentation maintenance

When your change adds a new feature, configuration knob, or significant architectural decision:

- Add a changelog fragment — `changelog.d/<type>-<slug>.md`, written for a user rather than a reviewer: what changed for them, and what was wrong before if it's a fix. The release pipeline collects it into a dated section in [CHANGELOG.md](CHANGELOG.md) on the next bump and copies that verbatim to the GitHub release page.
- Update the matching doc in [docs/](docs/):
  - New feature → [features.md](docs/features.md)
  - New config key → [configuration.md](docs/configuration.md)
  - New keybinding → [keybindings.md](docs/keybindings.md)
  - New MCP tool or behaviour change → [mcp.md](docs/mcp.md)
  - New plugin field or strategy → [plugin-reference.md](docs/plugin-reference.md)
  - New ADR → [architecture.md](docs/architecture.md) (use the existing `ADR-N: Title` / `Decision` / `Context` / `Consequences` template)

The TLDR architecture map (the `.claude/CLAUDE.md` file at the repo root) is the agent-facing index — keep that updated too if your change touches a package boundary, persistence path, or external protocol.

## Where to read deeper

| Question | Doc |
|---|---|
| How does Quil work internally? | [docs/architecture.md](docs/architecture.md) — 24 ADRs |
| What's planned? | [docs/roadmap.md](docs/roadmap.md) + [docs/roadmap/](docs/roadmap/) |
| Why does Quil exist? | [docs/vision.md](docs/vision.md) |
| Versioning policy? | [docs/versioning.md](docs/versioning.md) |
| Original PRD? | [docs/prd.md](docs/prd.md) — historical reference |
