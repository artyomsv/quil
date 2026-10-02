# Security: clients, tokens and rights

Quil's daemon accepts clients on two transports.

| Transport | Who | Guard | Rights |
|---|---|---|---|
| Local socket (`QUIL_HOME/quild.sock`) | the TUI, `quil mcp` bridges, scripts, `quil --stdio` for ssh | your OS account: socket mode `0600` (Unix), owner-only ACL (Windows — see [Windows status](#windows-status)) | full |
| TCP listener (opt-in, loopback only) | a client with a token — another local account, or another machine via `ssh -L` | a token and a mutual proof | the token's level |

The listener is off until you set `[listener] tcp` in `config.toml` (see [Configuration](configuration.md#listener)). The local socket needs no setup and works exactly as before; nothing on this page changes it except the tighter file permissions described under [Local socket](#local-socket).

## Threat model

- Every process of your account — including any agent running in a pane — has full rights, token management included. That is the trust Quil has always had: an agent can mint a `full` token.
- A web page cannot speak the listener's framing; an HTTP request reads as an oversized first frame and is closed without an answer.
- The listener never binds anything but loopback. Use `ssh -L 7878:127.0.0.1:7878 host` from another machine. If ssh warns that it could not bind the local port, something else holds it — do not connect through it.
- Another local account can keep the 8 pending-login slots busy and slow TCP logins. It cannot affect the local socket.
- While the daemon is stopped, another account could listen on the port. Your client reveals nothing reusable to it and refuses it: the daemon must sign the login with a key only it holds.
- The login is SCRAM-shaped (SCRAM-SHA-256 without password stretching — a token is 256 random bits) and has no channel binding. It resists replay, because each login signs fresh nonces from both sides, and it never reveals the token, but it would not stop a live relay forwarding frames between your client and the real daemon. A relay is not reachable here: the listener is loopback-only, and an impostor holding the port means the daemon has no listener on it. TLS comes in a later phase, together with listening beyond loopback.
- A token is a secret: whoever holds it has its rights until it expires or is revoked. `tokens.json` holds only verifiers: a stolen copy cannot log in, though it would let its holder pass as the daemon to a client.

## Rights levels

| Level | Can | Cannot |
|---|---|---|
| `read-only` | see the workspace, every pane's output, pane status, notes, tasks, notifications, the plugin and client lists, the memory report; follows the project of the daemon's active tab | type, resize, create, close, rename, switch tabs or projects, reorder, rearrange, open filesystem dialogs, read Claude transcripts or input history, see process trees, take control of the size |
| `standard` | everything a user does in the TUI, including typing into a shell (which runs code as you) and the setup dialog's toggles and kube context | start a program by raw arguments (plugin instances such as ssh/stripe), open overlay panes (lazygit), stop the daemon, reload its plugins, set its overlay policy, stop a process from the Processes dialog, check for or stage updates, manage tokens |
| `full` | everything a local client can | manage tokens (local socket only) |

Every TCP connection, whatever its level, may hold at most 4 waiting requests at once (`watch_notifications` and `wait_task`, which each park until something happens); the local socket has no such cap.

`standard` does not stop code execution — typing into a shell is running code. It stops what would leave no trace on screen (a program started by raw arguments) and daemon-wide actions. A sandbox pane can run any image your Docker can reach, inside the sandbox's mount boundary.

`read-only` is the only level that grants no code execution. It never spawns, types into or resizes a pane: a viewer reading a pane that has not started yet gets what is buffered, and the pane stays unstarted.

### What the TUI does with them

The TUI is told its level when it logs in, and applies it before the daemon has to:

- A read-only connection shows `[read-only]` in the status bar. It follows the daemon: the project holding the daemon's active tab, and that tab. Every workspace change aimed at it — creating, closing or renaming panes and tabs, switching tabs, reordering, layout arrangements, pane and border drags, mute, pin and deletion marks, tab colours — is refused on the spot with a flash, and nothing changes on screen. Create, close and rename are greyed in the command palette and context menus. Keystrokes, resizes and other non-view messages are never sent. It always shows each pane at the size the daemon has for it, even when no other client is attached, because a viewer can resize nothing.
- Opening an overlay (lazygit, hunk) and starting a plugin instance (ssh, stripe) are refused for `read-only` and `standard` alike, with a flash, and the overlay rows are greyed in the palette and the pane menu. A `standard` connection can still show or hide an overlay its tab already runs.
- Stop daemon (greyed in F1 → About), plugin reload and stopping a process are refused for `read-only` and `standard` alike, with a flash; the TUI does not quit after a stop it could not make. The overlay policy (your `[overlay]` settings, which a client normally pushes to every daemon it attaches to) is not sent to such a destination at all.
- The setup dialog sends toggle and kube-context NAMES, which the daemon turns into arguments itself. That is why a `standard` token keeps the dialog while raw arguments stay full-only.

## Tokens

```
quil clients token create --name laptop [--rights standard|read-only|full] [--expires 90d|<N>d|never]
quil clients token list
quil clients token revoke <id|name>
```

These talk to the LOCAL daemon (starting it if needed) and are refused under `--remote` and `--connect`. A token is `qtk_<8 hex>_<secret>`, shown once, at creation. Names are 1-32 characters and unique (case-insensitive). The default level is `standard`. Every token expires (default 90 days, at most 3650) unless created with `--expires never`; an expired token stays in `token list` until you revoke it, and the daemon closes its live connections within a minute of expiry. Revoking closes the token's live connections at once: nothing new is sent to them after the revoke, though frames already queued for a connection before it (at most its send buffer, each allowed when it was queued) may still be written. Store tokens like passwords.

Connect: `QUIL_TOKEN=<token> quil --connect 127.0.0.1:7878` or `quil --connect 7878 --token-file ~/.quil-token`. The token is never accepted on the command line, and `QUIL_TOKEN` is removed from the environment at startup, so nothing Quil spawns inherits it. `--connect` takes a bare port or a loopback address only.

Under `--connect` the session is a remote one: `quil daemon`, `quil clients`, `quil restart`, `quil status` and `quil mcp` refuse to run, Quil never starts, restarts or installs a daemon, a version mismatch is refused, and a staged update is not applied at launch (it applies on your next local launch). A lost link logs in again with the same token; the level is re-read on every login.

What you see when a login fails:

| Message | Meaning |
|---|---|
| `no listener at <addr>` | nothing listens there — the daemon is down or `[listener] tcp` is off |
| `token refused (wrong, expired or revoked)` | the daemon refused the login. The daemon's own reason is in `quil.log` (for every login: at launch, from the New Project dialog, and on a reconnect): `login required`, `token refused`, `login timeout`, or `too large` (a proof frame over 4 KiB) |
| `the listener at <addr> could not prove it is your daemon` | whatever answered does not hold this token's verifier — an impostor on the port, or a daemon with another `tokens.json`. Nothing more was sent to it |

A revoked or refused re-login parks the destination with that reason instead of retrying; a refused connection (a restarting daemon) keeps retrying.

## Client ids

Every TUI names itself with a per-process client id, and an id is bound to who presented it: your account on the local socket, or one token. A token client cannot take an id held by another principal (`client id in use`), so a viewer cannot become the size master by copying the owner's id from the client list. The local socket always wins an id.

After a daemon restart, the slot of the previous size master is kept for up to 30 s for the local TUI that held it. A TCP client that names that id meanwhile is attached as an ordinary client and does not inherit the slot; until the reserve ends, other clients see the master named as `reserved-for-local:<id>` (a marker that matches no client), so they stay followers rather than resizing panes.

## Audit log

`QUIL_HOME/audit.log` (JSON lines, 5 MiB x 10): TCP connects and disconnects, logins and their failures, refusals (at most one line per connection and message type per minute), token create/revoke/expiry, and privileged requests from any transport — stop, plugin reload, overlay policy, kill process, update check and staging, and a create carrying raw arguments. It never contains a token, a key, a nonce, a proof, terminal output, notes or input. Every value a client chose is cut to 64 bytes and written as a JSON string, so a chosen name cannot forge a line.

If the audit log or the token store cannot be opened, the TCP listener does not start (the reason is in `quild.log`); the local socket is unaffected.

## Local socket

On Unix the socket is created under a restrictive umask (Linux) and chmodded `0600` at once; a failed chmod stops the daemon. `QUIL_HOME` is created `0700`, and a wider mode is reported in `quild.log`.

On Windows, Quil gives `QUIL_HOME` a protected owner-only access list — your account and SYSTEM, not Administrators — at every daemon start, before the socket, `tokens.json` or `audit.log` is created. Files that already exist in it (the lock and pid files, `quild.log`, everything from earlier runs) get the same list through inheritance, and every file created later inherits it at creation. The socket and `audit.log` are then given the list explicitly as well, and `tokens.json` is created with it, never fixed afterwards. Rotated `audit.log` archives rely on the inherited list. Two consequences of the upgrade:

- **The first start of the updated daemon rewrites the quil folder's access list, recursively.** Every file and folder already under `QUIL_HOME` that inherits its permissions ends up owner + SYSTEM only; an entry set explicitly on one file stays. A tool running as another account that used to read those files (a backup agent, for one) loses access.
- **`QUIL_HOME` must be on a volume with access lists (NTFS).** On FAT or exFAT the folder cannot be protected, and when the socket file cannot be protected either the daemon refuses to start rather than serve an unguarded socket.

## Windows status

**Windows: ACLs set; enforcement not yet verified.** Whether Windows enforces the socket file's own ACL for AF_UNIX connections is settled by this test (run once, as an administrator for step 0). The harness is `TestACLTwoAccount` in `internal/ipc/acl_twoaccount_windows_test.go`; it skips unless `QUIL_ACL_ROLE` is set.

0. Create a second, standard account: `net user quilacl2 <password> /add`, and the probe directory: `mkdir C:\Temp\quil-acl`. Build the probe from the repo root (Git Bash):
   `MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W)":/src -v quil-gomod:/go/pkg/mod -v C:/Temp/quil-acl:/out -w /src -e GOOS=windows -e GOARCH=amd64 golang:1.25 go test -c -o /out/ipc_test.exe ./internal/ipc`
1. Owner, permissive socket: in PowerShell (owner) start the server. `QUIL_ACL_DIR` must be an EXISTING directory the second account can traverse; the server never changes its ACL — it creates a fresh `quil-acl-*` directory inside it, makes only that one permissive, and prints `LISTENING mode=... sock=<path>`:
   `$env:QUIL_ACL_ROLE='server'; $env:QUIL_ACL_DIR='C:\Temp\quil-acl'; $env:QUIL_ACL_SOCKET='permissive'; C:\Temp\quil-acl\ipc_test.exe '-test.run=TestACLTwoAccount' '-test.v' '-test.timeout=30m'`
   and in a second owner PowerShell, with the printed path:
   `$env:QUIL_ACL_ROLE='client'; $env:QUIL_ACL_SOCK='<sock path the server printed>'; C:\Temp\quil-acl\ipc_test.exe '-test.run=TestACLTwoAccount' '-test.v'`
   (Keep the flags quoted in PowerShell: unquoted, it splits `-test.run` at the dot and the binary refuses `-test`.)
   → expect `RESULT: ACCEPTED` (the socket works).
2. Second account, permissive socket (server still running from step 1):
   `runas /user:quilacl2 "cmd /c set QUIL_ACL_ROLE=client&& set QUIL_ACL_SOCK=<sock path the server printed>&& C:\Temp\quil-acl\ipc_test.exe -test.run TestACLTwoAccount -test.v > C:\Temp\quil-acl\step2.txt 2>&1"`
   → expect `RESULT: ACCEPTED` in `step2.txt` (the second account can reach the socket at all).
3. Stop the server (Ctrl+C) and restart it with `$env:QUIL_ACL_SOCKET='protected'` (it creates a NEW `quil-acl-*` directory and prints a new sock path), run the step 2 `runas` command again with that path writing `step3.txt`, then the step 1 owner client again with that path.
   → expect `RESULT: REFUSED` in `step3.txt` and `RESULT: ACCEPTED` for the owner. Delete any `C:\Temp\quil-acl\quil-acl-*` directory left behind by a Ctrl+C, and the `quilacl2` account (`net user quilacl2 /delete`) when done.

What the test settles, and what it does not: it keeps the probe's FOLDER permissive in every step and varies only the socket file's own ACL. If step 3 is refused, the socket file's ACL is enforced — record that here with the date and Windows build, and replace the status line. If it is not refused, the socket file's ACL is NOT enforced and only the owner-only folder ACL around the socket may still keep another account out; this test does not check that, so record only the first half and leave the folder's protection marked "not yet verified".
