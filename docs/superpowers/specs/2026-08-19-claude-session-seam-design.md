# Claude session seam — design

**Date:** 2026-08-19
**Status:** Draft, awaiting review
**Origin:** Analysis of the `hanxu1210/quil` fork (`tclaude-support` branch, 5 commits off v1.62.0)

## Summary

Six changes to how Quil delivers the Claude hook settings, decides which panes
speak the Claude protocol, and reads Claude's transcript store. Three are bug
fixes with consumers today (§1, §5, §6); two are cleanups that remove duplicated
and hardcoded predicates (§3, §4); one corrects a comment that asserts something
unverified (§2). A spike (§7) decides whether a seventh section is ever built.

Delivered as a single PR on `fix/claude-session-seam` — see "Delivery".

> **On line references.** Line numbers below are indicative and drift as the tree
> moves; several are already ~10 lines stale against `master` at v1.62.1. The
> implementation plan's references are the authoritative ones. Function and
> symbol names, not line numbers, identify every site.
>
> **On the old transcript schema.** The `{"type":"ai-title","aiTitle":…}` shape §6
> falls back to is transcribed from the `hanxu1210/quil` fork's test fixtures, not
> from a transcript anyone here has inspected. It is a *fallback reached only when
> the current schema produced nothing*, so being wrong about it costs an untitled
> session — the status quo — rather than a wrong title.

Nothing here adds a plugin. The fork that prompted this work added a second
Claude-family plugin (`tclaude`, Tencent's wrapper) and a 1,014-line parallel
session-reading package to support it. Investigation showed both of that package's
reasons for existing are **our** defects, not properties of the wrapper:

| Fork's symptom | Actual cause | Section |
|---|---|---|
| Sessions under `~/.tclaude`, not `~/.claude` | We ignore `CLAUDE_CONFIG_DIR` | §5 |
| No `promptSource:"typed"` → no titles | Our parser pins one transcript schema | §6 |

Fixing those two makes a second Claude-family plugin a TOML file with an `env`
line, so no parallel package and no session-source wire field are needed.

## Goals

1. Hook settings survive Windows `.cmd` shims (§1).
2. Stop asserting an unverified `--settings` precedence direction (§2).
3. The setup dialog stops hardcoding the value of a validated field (§3).
4. Six hardcoded `"claude-code"` checks become one derived capability (§4).
5. The session picker honours `CLAUDE_CONFIG_DIR` (§5).
6. Title and prompt detection stop failing silently on an unfamiliar transcript
   schema (§6).
7. Settle empirically whether Claude is first- or last-wins on repeated
   `--settings` (§7), which decides whether §8 is ever built.

## Non-goals

- **No `tclaude` plugin.** Decided during brainstorming: the extensibility work
  lands without a second consumer.
- **No `internal/tclaudesessions` package.** §5 and §6 subsume it.
- **No `Source` field on the session IPC payloads.** Considered and dropped —
  see "Rejected alternatives".
- **No `isClaudeFamily(name)` helper.** That is the same hardcode relocated; §4
  derives the capability instead.
- **No branding, URL, or disclaimer changes** from the fork.

---

## §1 — Deliver hook settings as a file path

### Problem

`claudeHookSpawnPrep` (`internal/daemon/daemon.go:3263`) returns
`[]string{"--settings", js}` where `js` is an inline JSON blob.

On Windows, `exec.LookPath("claude")` (`daemon.go:3766`) resolves to `claude.cmd`
— the npm shim — because `PATHEXT` includes `.CMD`. Windows runs a `.cmd` through
`cmd.exe`, which applies its own parsing pass **on top of** the CRT-level argv
quoting that conpty's `Spawn` already did. `cmd.exe` treats `"`, `&`, `|`, `<`,
`>` and `^` by different rules than the CRT parser. Our settings JSON is full of
quotes, so the argument is re-split at the wrong boundaries and a JSON fragment is
taken as a command name.

Observed by the fork as `'...is not recognized as an internal command`, with the
hook silently never registering.

This is the argument-injection class behind CVE-2024-24576. The defence is not
better escaping — the second parser has different rules and cannot be escaped for
generically. The defence is removing metacharacters from the argument entirely.

### Design

Add to `internal/claudehook`:

```go
// WriteSettingsFile writes the hook-settings JSON for paneID and returns its
// absolute path. A path carries no shell metacharacters, so it survives the
// cmd.exe re-parse that an inline JSON argument does not.
func WriteSettingsFile(quilDir, paneID, settingsJSON string) (string, error)
```

- Path: `<quilDir>/sessions/<paneID>.settings.json`, mode `0600`, written through
  the package's existing `atomicWrite`.
- Per-pane, so concurrent spawns never share a file.
- `validatePaneID` guards the path component, as the sibling `.id` and
  `.transcript` writers already do.

`claudeHookSpawnPrep` calls it and returns `{"--settings", path}`.

**Failure behaviour.** A write failure logs a warning and returns `nil, nil` — the
pane spawns without rotation tracking rather than failing to open. This preserves
the function's existing contract for the `BuildSettingsJSON` error path. It
matters because §1 turns a pure function into an effectful one: a full or
read-only disk must not become "the pane will not open".

**Cleanup.** Add `paneID + ".settings.json"` to the name list in
`cleanupPaneArtifacts` (`daemon.go:2143`), alongside `.id` and `.transcript`.

### Test impact

`spawn_args_test.go` and `spawn_env_test.go` pass fake `quilDir` values
(`"/tmp/quil"`, `"/data/quil"`) because the function never touched disk. Both move
to `t.TempDir()`. `TestClaudeHookSpawnPrep` stops asserting on `prefix[1]`
directly and reads the file the path points at.

`t.TempDir()` is required, not merely tidy: a test writing to a real `$QUIL_HOME`
passes green in Docker's throwaway `/root` and pollutes the developer's real
`~/.quil` everywhere else.

### Success criteria

- `dev.sh test internal/daemon` green.
- A Windows-native run of the daemon test binary confirms the settings file is
  written and the path contains no quote characters. (Per repo practice: build the
  test binary in Docker with `go test -c`, run the `.exe` on the host — a
  `GOOS=windows` vet only proves it compiles.)
- Manual: a `claude-code` pane in a dev instance writes
  `.quil/sessions/<paneID>.settings.json` and `.quil/sessions/<paneID>.id`
  appears after the pane starts.

---

## §2 — Correct the `--settings` precedence comment

### Problem

`daemon.go:3267` warns when a user's plugin args already contain `--settings`,
with a comment stating *"Claude treats later wins, so our prepend silently
overrides the user's value."*

The fork's commit `570d1521` reports verifying the opposite — that Claude is
**first**-wins — and rebuilt their entire hook mechanism around that finding. We
have not tested it ourselves.

If they are right, the warning documents the reverse of what happens, and a user
with their own `--settings` is silently losing hook tracking today.

### Design

Until §7 resolves it, state the uncertainty rather than either direction. Change
the comment and the log line to say that Quil prepends its own `--settings` and
that the resulting precedence is **unverified**, so a user supplying their own may
lose either their value or Quil's rotation tracking.

No behaviour change. This section exists so the codebase stops asserting something
we cannot support.

### Success criteria

- `grep -rn 'later.wins' internal/ --include=*.go` returns nothing. The regex
  matters: the claim appears both spaced ("Claude treats later wins", the
  `claudeHookSpawnPrep` doc comment) and hyphenated ("later-wins", the log line
  plus a comment and a subtest name in `spawn_args_test.go`). A grep for the
  spaced form alone reports success while three assertions of the unverified
  direction survive.

---

## §3 — Generalize `Sessions == "claude"` to `Sessions != ""`

### Problem

Four sites in `internal/tui/dialog.go` hardcode the *value* of a field the
registry already validates against a closed set (`registry.go:416`):

| Line | Function |
|---|---|
| 3370 | `enterSetupOrSplit` — does this plugin need the setup dialog at all |
| 3841 | `setupFieldCount` |
| 3881 | `setupFieldKind` |
| 5453 | `renderCreatePaneSetupDialog` |

`setupFieldCount`, `setupFieldKind` and `renderCreatePaneSetupDialog` are three
independently maintained copies of one predicate. Desyncing them produces a
field-index bug — a cursor landing on the wrong row — not a compile error.

### Design

Replace all four with `p.Command.Sessions != ""`.

Behavioural no-op today, since `""` and `"claude"` are the only values
`loadPluginTOML` accepts. The value is that the predicate stops being duplicated
as a literal.

### Success criteria

- `dev.sh test internal/tui` green.
- Manual: the `claude-code` create-pane dialog still shows the session picker as
  the last field before Continue, and cursor navigation reaches every field.

---

## §4 — Derive the Claude-protocol capability instead of hardcoding the name

### Problem

Six sites branch on the literal `"claude-code"`:

| Site | Gates |
|---|---|
| `daemon/claudesessions.go:461` | occupancy — which panes hold a session id |
| `daemon/daemon.go:422` | plugin-state refresh from hook records |
| `daemon/daemon.go:3275` | resume-template promotion |
| `daemon/daemon.go:3688` | hook-id read on restore |
| `daemon/daemon.go:3737` | hook injection at spawn |
| `tui/pane.go:735`, `:744` | `restoresViaSession` + restore-checklist label |

A user who renames the plugin, or any future Claude-compatible tool, needs all six
edited with no compiler assistance. A missed site fails silently: resume stops
working with no error.

### Design

`claude-code` is the only default plugin with `sessions != ""` (surveyed all
eight). The capability is therefore already derivable from data every
Claude-family plugin file must carry.

Add to `internal/plugin`:

```go
// ClaudeSessionSource is the sessions value naming Claude Code's transcript
// store and, with it, Claude's hook and resume protocol.
const ClaudeSessionSource = "claude"

// UsesClaudeSessions reports whether this plugin's sessions are Claude Code
// sessions — which implies the Claude hook protocol, the preassigned session
// id, and the transcript store under the Claude config dir.
func (p *PanePlugin) UsesClaudeSessions() bool
```

Sites holding a `*PanePlugin` (`daemon.go:3275`, `:3688`, `:3737`) call it
directly.

Sites holding only a pane **type string** (`claudesessions.go:461`,
`daemon.go:422`, `tui/pane.go:735`) resolve through the registry. Add a daemon
helper:

```go
func (d *Daemon) usesClaudeSessions(paneType string) bool
```

returning false when `d.registry` is nil or `Get` misses. `Registry.Get` is
`RWMutex`-guarded and already used at twelve other daemon sites.

`daemon.go:3737`'s `switch p.Name` keeps its per-plugin arms — the `claude-code`
and `opencode` arms do genuinely different work — but the Claude arm's **guard**
becomes the capability. The `opencode` arm stays name-based; it has its own
protocol and `sessions = ""`, and giving it a capability of its own has no second
consumer (YAGNI).

### Why a nil lookup is safe

If `claude-code.toml` is absent or malformed the plugin fails to load, and
`daemon.go:4135` already falls the pane back to `terminal`. The capability goes
false exactly when the pane is already unusable, so the degradation is consistent
rather than a new failure mode. `EnsureDefaultPlugins` recreates a deleted default
at startup, so the common case self-heals.

### Known limitation, accepted

`tui/pane.go:735` consults the **TUI's** registry about a pane that, in remote
mode, lives on the **daemon's** machine — and per RD-035 plugin definitions are
deliberately client-local. The answer can therefore disagree with the daemon.

This is not a regression: today's hardcoded list is wrong in the same remote case
*and* additionally wrong for a renamed plugin. It stays acceptable because
`restoresViaSession` only drives a restore-checklist label — cosmetic. **If this
capability ever gates something load-bearing in the TUI, it must move onto the
wire in `PaneInfo` instead.** Recorded here so the next person does not widen its
use silently.

### Success criteria

- `grep -rn '"claude-code"' internal/ cmd/ --include=*.go | grep -v _test`
  returns only the two doc-comment occurrences (`plugin/plugin.go:8`,
  `tui/pane.go:55`) **plus `resumeLabel`'s copy table** — see below.

`resumeLabel` (`tui/pane.go:787`) keeps its `switch paneType` deliberately. It is
a table of user-facing phrasing — `"resuming claude"`, `"reconnecting ssh"`,
`"restarting stripe"` — not a dispatch decision, and no plugin field carries a
verb phrase for it. Inventing one to satisfy a grep would be YAGNI. What *does*
change there is the session-id suffix gate at line 804, which is a real capability
question and moves off the name check with the rest of §4.
- `dev.sh test` green.
- Manual: rename `claude-code.toml`'s `name` to `claude-code-custom`, reload
  plugins, and confirm a pane of that type still gets hook injection and still
  resumes after a dev-daemon restart.

---

## §5 — Honour `CLAUDE_CONFIG_DIR`

### Problem

`CLAUDE_CONFIG_DIR` appears **nowhere** in this repository.
`claudesessions.ProjectDir` (`internal/claudesessions/claudesessions.go:125`)
hardcodes `$HOME/.claude/projects`.

`CLAUDE_CONFIG_DIR` is an upstream Claude Code variable — it relocates the whole
config directory, `projects/` included. Users set it to separate work from
personal configuration. `CommandConfig.Env` also lets a user set it per-plugin in
`claude-code.toml`, in which case the picker contradicts the pane it is about to
spawn — that variant is deliberately left unfixed, for the reason given below.

**Scope is narrower than it first appears, and stating it precisely matters:**

- **Resume is not affected.** The restore path uses `rec.TranscriptPath` — the
  path Claude itself reports through the hook (`daemon.go:424`, `daemon.go:3454`)
  — so it follows a relocated config dir already.
- **Only the session picker breaks** (list and detail), and it breaks silently: an
  empty list is indistinguishable from "no sessions recorded yet".

Severity: medium. Real users, silent failure, confined to one feature.

### Design

In `internal/claudesessions`:

```go
// ConfigDir returns Claude Code's config directory: $CLAUDE_CONFIG_DIR when set,
// else ~/.claude. Returns "" when neither can be resolved.
func ConfigDir() string

// ProjectDirIn maps a CWD to its transcript directory under an explicit config
// dir. ProjectDir is ProjectDirIn(ConfigDir(), cwd).
func ProjectDirIn(configDir, cwd string) string
func ProjectDir(cwd string) string
```

`List` and `ReadDetail` gain `…In(configDir, …)` variants. The existing names keep
their signatures and delegate through `ConfigDir()`, so every current caller is
unchanged and the daemon needs no new plumbing.

**Resolution order:**

1. The daemon process's `CLAUDE_CONFIG_DIR`.
2. `$HOME/.claude`.

A relative or `~`-prefixed value is expanded and absolutised **daemon-side**.

**Remote correctness.** Resolution happens entirely on the daemon. The config dir
describes the daemon's filesystem, and a client-supplied path would be the
laptop's answer about the server's disk — the wrong-machine class the RD-020 /
RD-021 work removed from the dialogs. No new wire field is involved.

### Deliberately not fixed: a per-plugin `Command.Env` override

`CommandConfig.Env` lets a user set `CLAUDE_CONFIG_DIR` on the `claude-code`
plugin itself. The picker will still resolve from the daemon's process env, so
that case stays mismatched.

This is a deliberate stop, not an oversight. `ClaudeSessionsReqPayload` carries
only a CWD — it does not identify which plugin the dialog is for — so honouring a
plugin-scoped value would require adding a field that names the plugin, which is
precisely the `Source`-shaped wire field this design rejects. Resolving it from
"the one plugin with `UsesClaudeSessions()`" would work today only because there
is exactly one, reintroducing by the back door the single-consumer assumption §4
removes.

The env-var case that actually bites users — set once in the shell or system
environment, inherited by the daemon — is covered. If the plugin-scoped case ever
turns up in practice, it is a self-contained follow-up: identify the plugin on the
request and resolve step 1 ahead of the process env.

**No signature changes.** `ConfigDir()` is a pure package function, so
`claudeSessionDetailResponse` (`claudesessions.go:276`) stays a free function —
it needs no registry access and therefore no `*Daemon` receiver.

### Success criteria

- Unit tests for `ConfigDir` covering: env set, env unset, `~`-prefixed value,
  relative value, `HOME` unresolvable. Each uses `t.Setenv` so the suite never
  depends on the developer's real environment.
- Unit test: `ProjectDirIn` with an explicit config dir does not consult the env.
- Manual, dev mode only: set `CLAUDE_CONFIG_DIR` to a scratch directory
  containing a `projects/<escaped-cwd>/<uuid>.jsonl`, open the create-pane dialog
  for `claude-code`, and confirm the picker lists that session and not `~/.claude`'s.

---

## §6 — Version-robust title and prompt detection

### Problem

`readTitle` (`claudesessions.go:433`) pre-filters lines on the literal
`"promptSource":"typed"` and re-checks the parsed field at line 441. `Detail`'s
scan (`:349`, `:393`) does the same for `UserPrompts`, `FirstPrompt` and
`LastPrompt`.

A transcript without that field yields **empty titles and a zero prompt count,
silently** — sessions still list, just by UUID.

`promptSource` is a Claude Code schema detail. The `EscapeCWD` comment
immediately above (`:76`) records that its own algorithm was *"transcribed from
the claude binary (2026-07-05, v2.x)"* and that a similar detail, when wrong,
*"fails quietly rather than loudly"* — the 2026-07-05 incident where every
restored pane fell back to `--continue`. Same fragility class, one function apart.

This is also the real reason the fork needed a second parser: the wrapper pins a
Claude build whose transcripts predate `promptSource` and instead carry
`"type":"ai-title"` entries.

### Design

Strictly additive. The existing path stays the fast path and its result is
preferred, so **no currently-working install changes behaviour**.

`readTitle`, over the same 64 KiB head window, in order:

1. **Typed-prompt pass** — unchanged.
2. **`ai-title` pass** — lines containing `"type":"ai-title"`, taking the
   `aiTitle` field. Reachable only when no typed prompt was found.
3. **Content-shape pass** — `type == "user"`, not `isSidechain`, `promptSource`
   **absent**, and `message.content` decoding as a JSON **string**. Tool results
   are recorded as `type: "user"` with **array** content, so the shape
   distinguishes them without a schema marker.

**The `promptSource`-absent condition is load-bearing, not belt-and-braces.**
Without it, "no behaviour change" is false in a case that really occurs: a
*current*-schema transcript containing zero typed prompts but some non-typed
string-content user entries — slash-command expansions and
compaction-continuation summaries, which are exactly what the
`promptSource == "typed"` filter exists to exclude (see `Detail.UserPrompts`'s
doc comment). Those would newly be promoted to titles and counted as prompts,
and a compaction summary is long enough to make a grotesque title. Gating on the
field's *absence* means the fallback can only ever fire on a schema that does not
record it, which is the only claim we actually want to make.

`ReadDetail` gets the same three-way classification, but as a **second pass run
only when the first found zero prompts** — not folded into one scan. The existing
loop rejects non-typed lines with a byte compare before any JSON parse, and its
comment records that this "is the difference between this being affordable on a
keypress and not — the largest transcript on hand is 88 MB". Classifying every
`"type":"user"` line in one pass would parse every tool result in that file for
every existing user. The conditional second pass keeps the normal case at exactly
its current cost.

`sanitizeTitle` / `sanitizePrompt` are unchanged and apply to every path.

### Success criteria

- A fixture transcript with `promptSource` produces byte-identical output before
  and after — the regression guard for "no behaviour change".
- **A current-schema transcript with `promptSource` present but never `"typed"`
  yields an empty title and a zero prompt count.** This is the guard for the
  overbroad-fallback case above; without the `promptSource`-absent condition it
  would newly return a slash-command expansion or a compaction summary.
- A fixture without `promptSource` but with `ai-title` yields that title.
- A fixture with neither yields the first string-content user prompt.
- A fixture whose only `type: "user"` entries have array content yields an empty
  title and a zero prompt count (tool results are not prompts).
- Sidechain entries are excluded on every path.

---

## §7 — Settle the `--settings` precedence question

A spike, not a code change. Its outcome decides whether §8 exists.

### Experiment

Launch `claude` once with two `--settings` flags, each registering a
`SessionStart` hook whose command writes a distinct marker file into a scratch
directory (not `$QUIL_HOME`, and not any project tree):

```
claude --settings <A.json> --settings <B.json>
```

- Only `A`'s marker exists → **first-wins**. The fork is right; §8 is justified
  and §2's comment is corrected to say so.
- Only `B`'s marker exists → **last-wins**. Our original comment was right; §8 is
  dropped entirely and §2 restores the definite wording.
- Both exist → the flags merge; §8 is dropped and §2 says so.

Run on Windows and on Linux — the shim layer differs and the answer may not.

### Success criteria

The result is recorded in this spec and in the `daemon.go:3267` comment, with the
Claude version tested, because the answer is version-sensitive by nature.

---

## §8 — Project-settings hook registration (BLOCKED on §7)

**Status: blocked. Do not implement until §7 returns first-wins.**

Recorded here so the requirements are not rediscovered.

### What it would do

Register the `SessionStart` hook in the local settings scope Claude auto-loads
(`<cwd>/.claude/settings.local.json`) rather than passing it via `--settings`.
Hooks merge across scopes, so it would survive both a wrapper's own `--settings`
and a user's.

### Mandatory hardening

The fork's implementation has five defects. None is a reason to reject the idea;
all are reasons not to port it verbatim.

1. **Never degrade a malformed settings file to `{}`.** Their `readProjectSettings`
   returns an empty map when `json.Unmarshal` fails and then overwrites, so a
   user's `settings.local.json` with a trailing comma is **silently replaced by
   our hook alone**. Correct behaviour: parse failure returns an error, we log and
   spawn without rotation tracking. Degrade the feature, never the user's file.
2. **Refcount state lives under `$QUIL_HOME`, keyed by a CWD digest** — not in the
   user's repository. This single change fixes three of their defects at once: the
   file stops appearing in `git status` (their `.quil-panes` is not gitignored, as
   `settings.local.json` is), it becomes ours to lock, and it survives a CWD
   deleted before the pane is destroyed (their documented known-limit leak).
3. **Lock the read-modify-write.** Their `addRefcount` / `removeRefcount` /
   `mergeProjectHook` read then write with no lock. Atomic write makes each write
   atomic and does nothing for the window between; two panes spawning in one CWD
   concurrently drop an entry.
4. **Exact-match hook identity.** Their removal matches
   `strings.HasSuffix(cmd, "claude-hook")` while their insertion matches the full
   command — so removal deletes a user hook whose command happens to end that way.
5. **Reap stale refcounts at daemon start.** A hard kill otherwise leaves a paneID
   registered forever and the hook never removed. Feasible once (2) puts the state
   under `$QUIL_HOME`, where startup can cross-check against live panes the way
   restore already prunes layouts.

### Accepted side effect, to be surfaced in review

The hook fires for **any** Claude session started in that directory, including a
user running `claude` by hand. It no-ops without `QUIL_PANE_ID`, but it is a
side effect outside Quil's own state and the user is not told about it.

---

## Rejected alternatives

**A `Source` field on the session IPC payloads.** The fork adds a source string to
`ClaudeSessionsReqPayload` and `ClaudeSessionDetailReqPayload` to route between
transcript stores:

```go
type ClaudeSessionsReqPayload struct {
    CWD    string `json:"cwd"`
    Source string `json:"source,omitempty"` // "" | "claude" | "tclaude"
}
```

Rejected for three reasons. `registry.go:416` validates `sessions` against a
closed set, so with no second plugin the field can only ever carry one value.
Deferring costs nothing — it is a request field with `omitempty`, so adding it
later is compatible in both directions with no version-skew trap. And it is the
wrong shape for the problem it appeared to solve: a label answers "which
product", while the code needs "which directory", resolved daemon-side (§5).

**`isClaudeFamily(name string) bool`.** The fork's helper is
`name == "claude-code" || name == "tclaude"` — the same hardcode centralised
rather than removed. §4 derives the capability from plugin data instead.

**A new `[persistence] hook_protocol` TOML field.** More explicit than §4's
derivation and it separates the transcript store from the protocol. Rejected
because it bumps `claude-code.toml`'s `schema_version`, which fires the migration
dialog for every user who customised that file — a real cost for a distinction
with no second consumer. Revisit if a tool ever speaks Claude's protocol while
storing transcripts elsewhere.

**Persisting the capability on the `Pane`** (resolve at spawn, store in
`workspace.json`). More correct in principle — the protocol a pane speaks was
decided when it spawned — but it costs a persisted-field migration plus a backfill
rule for pre-upgrade panes, to fix only the case where a user edits `sessions` out
of the TOML mid-session. Rare, and arguably that edit *should* change behaviour.

**Porting `internal/tclaudesessions`.** 1,014 lines duplicating logic §5 and §6
make general. Its test fixtures remain a useful reference if a second transcript
schema ever needs coverage.

---

## Delivery

**One PR**, branch `fix/claude-session-seam`, carrying §1–§6.

This is a deliberate departure from `CONTRIBUTING.md`'s *"aim for under 400 lines
of diff … split into stacked PRs"*. The combined change will land well past that
line once tests are counted. The decision was made explicitly by the repo owner;
it is recorded here so the size reads as a choice rather than an oversight, and so
a reviewer knows to expect it.

**PR title:** `fix(claude): correct hook delivery, session store, and dispatch`.
The type is release-critical — it is the squash-commit subject the release
pipeline classifies to compute the version bump. `fix` is correct: §1, §5 and §6
are user-facing fixes. A non-conventional title silently skips the release.

**Changelog:** the PR touches `cmd/` and `internal/`, so CI requires a fragment.
One `changelog.d/fixed-claude-session-seam.md` covering the three user-facing
fixes, with the mandatory three-line `headline:` front-matter block (≤64 bytes, no
`"` or `\`). §2, §3 and §4 have nothing to tell a user and are simply absent from
the prose rather than given a second fragment.

Review order for the reviewer, since one diff hides the seams:

1. §4 first — behavioural no-op, widest blast radius, and a missed site fails
   *silently*. This is the section that most needs eyes.
2. §1 next — the only section that adds a new failure mode (disk write on spawn).
3. §5 and §6 — self-contained in `internal/claudesessions`, verifiable by fixture.
4. §2 and §3 — comment and predicate, skim.

§7 is a spike with no code. §8 stays blocked and gets its own spec if §7 unblocks
it.

## Risks

| Risk | Mitigation |
|---|---|
| §1 makes a spawn-path function effectful; disk-full or read-only `$QUIL_HOME` becomes a new failure mode | Returns `nil, nil` on write failure — pane opens without rotation tracking, never fails to open |
| §4 registry lookup returns nil for a live pane | Only when the plugin failed to load, in which case `daemon.go:4135` already degraded the pane to `terminal` |
| §4's TUI-side lookup disagrees with a remote daemon | Cosmetic only (checklist label); limitation recorded in §4 against future widening |
| §6 changes titles for existing installs | Typed-prompt path runs first and its result wins; a fixture test asserts byte-identical output |
| §5 picks the daemon's env when a plugin-scoped `CLAUDE_CONFIG_DIR` disagrees | Accepted and documented, not mitigated — see §5's "Deliberately not fixed". Honouring the plugin value needs a request field naming the plugin, which is the `Source`-shaped field this design rejects |

## Verification

Per repo practice:

- `./scripts/dev.sh test` and `./scripts/dev.sh vet` for every PR.
- CI runs `go test -race ./...`, which is **not** what `dev.sh test` runs — a
  green local run does not prove the race detector is clean.
- Windows-specific behaviour in §1 is verified by building the test binary in
  Docker with `go test -c` and running the `.exe` on the host.
- All manual verification uses dev mode (`./quil-dev.exe`, state in `.quil/`).
  The production daemon and `~/.quil/` are not touched.
