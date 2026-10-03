# Sandbox: sign in once, remember the image, F1 settings page

Issue: #251. Status: approved design, 2026-10-03.

## Problem

From the owner's testing of sandbox panes:

1. In browser mode (the default) every new sandbox pane has its own Claude
   config directory, so every new pane asks for a browser sign-in.
   `[sandbox] shared_claude_config = true` fixes that, but it is a hand-edited,
   daemon-global switch read once at daemon start.
2. The Ctrl+N dialog pre-fills the image only from `[sandbox] default_image`,
   which ships empty, so `quil-sandbox:latest` is retyped for every pane.
3. None of the sandbox settings are reachable from F1 → Settings.

Two facts in the code shape the design:

- **The shared switch is global and the resume path reads it once.**
  `setSharedClaudeRoot` (`internal/daemon/sandbox_resume.go`) records it at
  daemon start, and `sandbox_spawn.go` reads `d.cfg.Sandbox.SharedClaudeConfig`
  at every spawn. Flipping the switch moves EVERY existing sandbox pane to the
  other directory on the next daemon start: its transcript is then in the
  directory it no longer mounts, so it restores as a fresh session.
- **The dialog shows the TUI's config, the pane gets the daemon's.** An
  untouched sign-in row sends `Auth: ""`, which the daemon resolves through its
  OWN `[sandbox] auth`. For a remote project that is another machine's config,
  so the row can display one mode while the pane gets the other — breaking the
  row's own rule ("must never show a choice the pane would not actually get").

## Decisions (owner, 2026-10-03)

| Question | Decision |
|---|---|
| How does "sign in once" work? | A **per-pane** choice in the Ctrl+N dialog, not a global switch |
| How is the image remembered? | **Last used, per host**, in a client-side state file; `config.toml` is not written on create |
| One default image or one per agent? | **One for all agents** (`scripts/sandbox-image.sh --with codex,opencode` builds one image holding all three) |
| Defaults | Unchanged: browser, own directory, no image |

## Design

### 1. Per-pane Claude config directory

**Wire.** `ipc.SandboxSpec` gains `ClaudeConfig string \`json:"claude_config,omitempty"\``:

- `"own"` — the pane's own directory (today's default).
- `"shared"` — the one shared directory, `$QUIL_HOME/sandbox/claude`.
- `""` — follow `[sandbox] shared_claude_config`. What every older client,
  every restore of a pre-change snapshot and MCP without the field send.

**Daemon.** `applySandboxSpec` validates it exactly like `Auth`: any other
value is logged (by length, never content) and dropped to `""`. Stored on
`Pane.SandboxClaudeConfig` (PluginMu-protected, written once at creation) and
PERSISTED next to `SandboxAuth` in the snapshot (`sandbox_claude_config`,
pointer, present whenever `SandboxImage` is set). An unrecognised value read
back from a snapshot falls back to the config, like `SandboxAuth`.
`ipc.PaneState` serves both the broadcast and `workspace.json`, so the key is
on the wire too (as `sandbox_auth` is), and the frozen oracle
`internal/ipc/state_oracle_test.go` gains it the way it gained `size_seq` —
the typed builder is what changes, never the oracle's existing rows.

**One reader, one path.** A new `Daemon.paneSharesClaudeConfig(pane, plugin)
bool`, mirroring `paneAuthMode`:

1. A plugin that is not Claude (`!plugin.UsesClaudeAuthName`) never shares.
2. The pane's `"shared"` / `"own"` wins.
3. `""` reads `d.cfg.Sandbox.SharedClaudeConfig` — unchanged for every
   existing pane and every older client.

A new helper `sharedClaudeConfigDir(quilDir)` is the only place the shared
path is spelled. Both consumers use the decision and the helper:

- `prepareSandbox` (`sandbox_spawn.go:159`) sets `m.SharedClaudeRoot` from
  them instead of reading the config.
- The resume path (`hostTranscriptPath`, `sandbox_resume.go:54`) derives the
  root from the pane's recorded choice plus the config bool, and the helper.
  The package var `sharedClaudeRoot` — set only when the config is on — is
  replaced by a config-bool var set once at start, because a `"shared"` pane on
  a config-off daemon must still map to the shared root.

Splitting the two would mount one directory and look for the transcript in the
other, which is the `--session-id` exit-129 case `sandbox_resume.go` documents.

**Token panes.** The dialog's Token option sends `claude_config: "own"`, so a
token pane created from the dialog never enters the shared directory, and
`reconcileClaudeAuthMode`'s stamp on it (`sandbox_authstamp.go:105`) does not
alternate between modes. `""` + token still follows the config, so existing
panes do not move. The one remaining way to mix modes in the shared directory
is a hand-edited `auth = "token"` + `shared_claude_config = true` used by an
MCP create with no `sandbox_claude_config`; the F1 page never writes that pair
(§4), and the docs name it.

**Codex and opencode — a fix, not "unchanged".** Today `prepareSandbox` mounts
the shared directory read-write into EVERY sandbox container when the config is
on, so a codex container could plant a hook or MCP server that every shared
Claude pane then runs. Rule 1 above closes that: `plugin.UsesClaudeAuthName` is
already the predicate that gates the sign-in, the token, the CLI env, the stamp
and the dialog row, and the shared mount joins them.

**Downgrade.** An older daemon ignores `claude_config` and the snapshot key,
and uses its config (own directory by default). That fails toward isolation:
the user signs in again and the pane starts a fresh session. No security
property depends on the new field being understood.

### 2. The Ctrl+N sign-in row: three options

The row holds a CHOICE key (`browser` / `shared` / `token`), not an auth mode,
and one table maps a choice to both wire fields — so `"shared"` can never reach
`SandboxSpec.Auth`, where `applySandboxSpec` would drop it:

| Choice | Label | Sends | Detail line (when focused) |
|---|---|---|---|
| `browser` | Browser | `auth: "browser"`, `claude_config: "own"` | sign in in this container · full subscription |
| `shared` | Shared | `auth: "browser"`, `claude_config: "shared"` | sign in once for all Shared panes · they share hooks, MCP servers, history |
| `token` | Token | `auth: "token"`, `claude_config: "own"` | no sign-in · saves a token every later Claude uses |

`m.sandboxAuth` / `effectiveSandboxAuth` become `m.sandboxSignIn` /
`effectiveSandboxSignIn`. Browser stays first. Ctrl+T's first pane is covered
for free: `FirstPaneSpec.Sandbox` is built by the same `sandboxSpec()`.

**The row now always sends what it displays** — an untouched row resolves the
default from the TUI's config and sends it explicitly, instead of `""`. This is
what makes the default reach a remote project and keeps the row honest there.
`TestSandboxSpec_CarriesTheChosenAuth` (`sandbox_field_test.go:629`, asserts
untouched `Auth == ""`) is inverted accordingly; the daemon-side "absent
follows the config" test stays, because MCP and older clients still send `""`.

Default resolution from the TUI config, one function shared with §4: token
(via `ResolveAuth`) → Token, whatever `shared_claude_config` says; browser +
shared → Shared; browser → Browser.

### 3. Remember the image, per host

- `config.SandboxImagePath(dest)` — `sandbox-image.json` for the local daemon,
  `sandbox-image-<destFileKey>.json` for a remote, following `RecentCWDsPath`
  exactly (same key function, same traversal reasoning).
- Content: `{"image": "<ref>"}`. TUI-owned, single writer.
- **Written on a successful submit of a sandbox pane**, scoped to
  `createPaneDialogDest()` (the dialog's pinned destination — the same reason
  the recent-CWD save uses it). Written only when it changed.
- **Read** when the dialog opens (`resetSandboxField(dest)`) and when the
  switch is toggled on with an empty field: last image for that dest, else
  `[sandbox] default_image`, else empty (the field still says "image
  required" — Quil never invents an image).
- Disk access goes through a store the `Model` holds
  (`SetSandboxImageStore(load, save)`), installed by `cmd/quil/main.go`. A
  `Model` built directly — as ~46 tests do — has no store and remembers nothing,
  so no test touches the real `~/.quil` (the `SetRecentCWDs` precedent). The
  file-backed store's own tests set `QUIL_HOME` to `t.TempDir()`.
- The value is user-typed, stored verbatim, validated daemon-side as today
  (`applySandboxSpec`), and rendered through `sanitizeRemoteText` as today.
- Accepted divergence from the recent-CWD precedent: under `shared_data`
  (ADR-33) recent directories live on the daemon, so two TUIs on one daemon
  share them; the remembered image stays per client. The owner chose
  client-side; a second client simply remembers its own last image.
- Once a host has a remembered image, `default_image` no longer pre-fills for
  it. To change it, type a different image once — that becomes the memory.
  There is no "forget" control (an empty image is refused at submit anyway).

### 4. F1 → Settings → Sandbox

A `submenu` row "Sandbox" in `settingsFields` opens a new screen
(`internal/tui/dialog_sandboxsettings.go`), built like
`dialog_notifysettings.go` but with two kinds of row:

- **Default image** — text row, edits `[sandbox] default_image`. Hint:
  "First image for a host. After that, Ctrl+N remembers the last image used
  there."
- **Sign-in** — Browser / Shared / Token, cycled with ←/→/space. Displays the
  §2 resolution (a legacy token + shared file shows Token). Writes both keys
  every time, so the page can never write the mixed pair:
  Browser → `auth = "browser"`, `shared_claude_config = false`;
  Shared → `auth = "browser"`, `shared_claude_config = true`;
  Token → `auth = "token"`, `shared_claude_config = false`.
  One warning line for the selected option (the detail lines above).
- A footer line: "Defaults for Ctrl+N. Existing panes keep their mode."

Saved through the existing `m.configChanged` → `config.Save` path. These are
client-side defaults: they apply at once, for local and remote projects alike,
because the dialog sends the choice on the wire. The daemon also reads the same
keys at its start as the fallback for `""` (MCP and older clients); the page
does not need to say "restart" for its own purpose, and the docs state the
MCP fallback.

`config.Save` writes the whole file. That is the existing behaviour of every
settings row and the reason the image memory lives in its own file instead.

### 5. MCP

`create_pane` gains `sandbox_claude_config` (`own` | `shared`, empty = config
default), passed through to `SandboxSpec.ClaudeConfig`, and joins
`usesDialogOptions` (`mcp_tools.go:304`). Validation is the daemon's.

**Accepted version gap.** A field cannot be version-gated, only a request type
can (`templates.md`). So `shared` sent to an older daemon — an MCP call to an
un-upgraded remote, or a dev TUI against an older daemon — silently becomes
`own`. Release TUIs are exact-match version-gated, so a release TUI never shows
Shared to a daemon that drops it. The failure is a repeated sign-in, never a
loss of isolation.

### 6. User-facing text that becomes false

- `runSandboxSignIn` tells waiters "This happens once; every sandbox pane after
  this is signed in" (`sandbox_signin.go:245`). With three modes that is true
  only of token panes; reword to say so.
- `sandboxIdentity`'s remedy message (`sandbox_spawn.go:352`) names only
  `auth = "browser"` as the alternative; add Shared (sign in once).

## Error handling

- Unknown `claude_config` on the wire or in a snapshot: logged by length,
  treated as `""`. Never fails a spawn — it is a sign-in preference, not an
  isolation property (same rule as `auth`).
- Image file unreadable or malformed: ignored, falls back to `default_image`.
  Write failure: logged, the pane is still created.

## Testing

- `applySandboxSpec` records `own`/`shared`, drops unknown values (a mutation
  dropping the wire value must fail a test).
- Snapshot round trip of `SandboxClaudeConfig`, including the absent key.
- Call site: `prepareSandbox` mounts the shared root for a `shared` pane while
  the config says off, and the per-pane root for an `own` pane while the config
  says on. A codex/opencode pane never gets the shared root, config on or off.
- Resume: a `shared` pane on a config-OFF daemon maps to the shared root; an
  `own` pane on a config-ON daemon maps to its own root; `""` follows config.
- Dialog, through `Update`: three options in order; untouched row sends the
  resolved default explicitly; every choice maps to the table's pair, and
  `"shared"` never appears in `Auth`.
- Image memory, through `Update`: submit writes the per-dest file; reopening
  pre-fills it; another dest does not see it; no file → `default_image`.
- Settings page: rows read and write the right config keys and set
  `configChanged`; the sign-in row maps all three options both ways; a legacy
  token + shared config displays Token and saving any option writes both keys.
- MCP: `sandbox_claude_config` reaches the payload.
- Live, dev mode only, repository `E:\Projects\Stukans\quil`, image
  `quil-sandbox:latest`: two Shared panes → one browser sign-in; a Browser
  pane still signs in on its own; restart a Shared pane → its session resumes.

## Docs

`.claude/rules/sandbox.md` (per-pane config directory section),
`docs/sandbox-panes.md`, `docs/configuration.md`, `docs/mcp.md`,
`SandboxConfig` comments, `ipc.SandboxSpec` comment, one `changelog.d`
fragment.

## Out of scope

- Per-agent default images.
- A daemon-side config reload IPC.
- Changing any default.
