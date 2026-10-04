# Sharing a workspace with tokens

This guide shows how to let another person, another account or another machine use your Quil workspace, with less than full control if you want.

It is the "how to" guide. For the exact rules and the threat model, read [Security](security.md).

## The short version

```bash
# 1. On the machine that runs the daemon: turn the listener on
#    (add to ~/.quil/config.toml, then restart the daemon)
[listener]
tcp = "7878"

# 2. Make a token for each person or device
quil clients token create --name anna-laptop --rights standard

# 3. On the client: connect with that token
quil --connect 7878 --token-file ~/anna.token
```

From another machine, open an ssh tunnel first (see [Connect from another machine](#connect-from-another-machine)).

## Two ways to reach a daemon

| | `quil --remote host` | `quil --connect port` |
|---|---|---|
| How it logs in | ssh (your ssh key or password) | a Quil token |
| Rights | full, always | the token's level: read-only, standard or full |
| Needs on the daemon side | ssh access | `[listener] tcp` turned on |
| Port opened | none | one, on 127.0.0.1 only |
| Use it when | you work on your own machine from elsewhere | you share with someone, or want less than full control |

`--remote` did not change. It needs no token. Over ssh, Quil talks to the daemon through its local socket, and your ssh login is the proof of who you are.

Use `--connect` when the person should NOT get everything. Two examples: a teammate who may watch but not type, or a second device that should not be able to stop the daemon.

## The three levels

Each token has one level. You choose it when you make the token, and you cannot change it later: make a new token instead.

### read-only — "watch"

The client can see. It cannot change anything.

- It sees every pane's output, the tabs, projects, notes, notifications and tasks.
- It follows the daemon. It shows the tab and project that is active on the daemon, at the size the daemon uses. When someone with control switches tab, the viewer switches too.
- It cannot type, resize, create, close, rename or move anything. A try gives a short message on the status line, and nothing changes. Those rows are greyed in the palette and menus.
- `[read-only]` shows in its status bar.

Good for: a screen to watch agents work, a demo, a person who helps you by looking.

> **Who does the viewer follow?** The daemon has ONE active tab for all clients. Every client with control moves it when it switches tab. So the viewer shows the tab of whoever switched last. It does not follow one chosen person.

### standard — "work"

The client can do what you normally do in the TUI.

- It can type in panes, create and close panes and tabs, split, rename and use the setup dialog (the toggles and the kube context).
- It CANNOT:
  - start a plugin instance with its own arguments (ssh, stripe)
  - open overlay panes (lazygit, hunk)
  - stop the daemon, reload plugins, or change the overlay settings
  - stop a process in F1 → Processes
  - check for updates or stage them
  - manage tokens

> **Typing in a shell runs code as you.** A standard user can type any command into a terminal pane. Standard does not stop that. It stops actions that leave no trace on screen, and actions on the whole daemon. Give standard only to people you would let use your keyboard.

Good for: a second device of your own, a person you pair with.

### full — "everything"

The client can do almost everything a local client can. Two things stay on the daemon's machine:

- **Tokens.** Only your own account, on the daemon's machine, can make or revoke them.
- **Updates.** Through `--connect` the Quil window never checks for or installs updates, whatever the level: the update would be downloaded on the daemon's machine but installed on yours, two different machines. Update by starting Quil on each machine itself.

Good for: your own devices, when you need lazygit, instances or daemon control.

### Side by side

| Action | read-only | standard | full |
|---|---|---|---|
| See panes, tabs, notes, notifications | yes | yes | yes |
| Type in a pane | no | yes | yes |
| Create, close, rename, split, move | no | yes | yes |
| Switch tabs and projects | no (it follows) | yes | yes |
| Resize panes | no | yes | yes |
| Setup dialog (toggles, kube context) | no | yes | yes |
| Plugin instances (ssh, stripe) | no | no | yes |
| Overlays (lazygit, hunk) | no | no | yes |
| Stop the daemon, reload plugins | no | no | yes |
| Stop a process (F1 → Processes) | no | no | yes |
| Updates | no | no | no (on the daemon's machine only) |
| Make or revoke tokens | no | no | no (on the daemon's machine only) |

## Turn the listener on

The listener is OFF until you turn it on. Do this on the machine that runs the daemon.

1. Open `~/.quil/config.toml`. In dev mode it is `.quil/config.toml` in the repo.
2. Add:
   ```toml
   [listener]
   tcp = "7878"
   ```
   A bare port means `127.0.0.1:<port>`. You can also write `"127.0.0.1:7878"` or `"[::1]:7878"`.
3. Restart the daemon (`quil restart`). The daemon reads this setting only when it starts.
4. Check `quild.log`. It must say:
   ```
   listener: token-authenticated TCP listener on 127.0.0.1:7878
   ```

The listener only listens on this machine (loopback). An address like `0.0.0.0:7878` or `:7878` is refused, and the daemon starts without the listener. To reach it from another machine, use an ssh tunnel (below).

## Make tokens

Run these on the machine that runs the daemon, as your own account. If the daemon is not running, this starts it.

```bash
quil clients token create --name <name> [--rights read-only|standard|full] [--expires 90d|<N>d|never]
```

- `--name`: 1 to 32 characters, unique (case does not matter). Use the person or device: `anna-laptop`, `tv-screen`.
- `--rights`: default `standard`.
- `--expires`: default `90d`. At most `3650d`. `never` means no end date.

Examples:

```bash
# A screen in the office that only watches
quil clients token create --name office-screen --rights read-only --expires never

# Your colleague, for one week
quil clients token create --name bob --rights standard --expires 7d

# Your own laptop, with full control
quil clients token create --name my-laptop --rights full
```

The output looks like this:

```
Created token "bob" (id 719b0977, rights standard, expires 2026-10-09T10:29:22Z).
Store it now — it is not shown again:
qtk_719b0977_<secret>
Use it with:  QUIL_TOKEN=<token> quil --connect <addr>   (or --token-file <path>)
```

**The token shows ONCE.** Quil does not keep it, only a check value made from it. If you lose it, revoke it and make a new one.

You can make as many tokens as you want, also with the same level. Make one per person or device. Then you can remove one without cutting off the others.

### See and remove tokens

```bash
quil clients token list
```
```
ID        NAME           RIGHTS     CREATED           EXPIRES           LAST USED
c07905b4  office-screen  read-only  2026-10-02 12:29  never             2026-10-02 14:03
719b0977  bob            standard   2026-10-02 12:29  2026-10-09 11:29  -
```

```bash
quil clients token revoke bob        # by name
quil clients token revoke 719b0977   # or by id
```

Revoke takes effect at once. Every client that uses the token is disconnected and sees `token refused (wrong, expired or revoked)`. An expired token is cut off within one minute, and it stays in the list until you revoke it.

## Connect

On the client machine, give the token in ONE of these two ways. Never put it on the command line.

**From a file** (best):

```bash
quil --connect 7878 --token-file ~/bob.token
```

The file holds only the token. Spaces and line ends around it are ignored. Protect the file like a password.

**From an environment variable:**

```bash
QUIL_TOKEN=qtk_... quil --connect 7878
```

PowerShell:

```powershell
$env:QUIL_TOKEN='qtk_...'; quil --connect 7878
```

Quil removes `QUIL_TOKEN` from its environment at start, so panes and programs it starts never see it.

`--connect` takes a bare port or a loopback address (`127.0.0.1:7878`, `[::1]:7878`, `localhost:7878`). Nothing else.

### Connect on the same machine

Another account on the same PC uses the port directly:

```bash
quil --connect 7878 --token-file ~/bob.token
```

### Connect from another machine

The listener is not open to the network, so you go through ssh. Open a tunnel from the client machine to the daemon machine:

```bash
ssh -N -L 7878:127.0.0.1:7878 you@daemon-host
```

Leave it running. In a second terminal, on the same client machine:

```bash
quil --connect 7878 --token-file ~/bob.token
```

`-L 7878:127.0.0.1:7878` means: "port 7878 here goes to port 7878 on the daemon machine's own 127.0.0.1". If ssh says it could not bind the local port, something else holds it. Do not connect through it: pick another local port, for example `-L 7900:127.0.0.1:7878` and `quil --connect 7900`.

The daemon machine must accept ssh. For a Windows daemon machine, see [Windows remotes over SSH](remote-windows.md) for the OpenSSH server set-up.

> **Why the ssh step?** The listener has no encryption of its own yet. ssh gives the encryption and stops strangers on the network from reaching the port. A listener with its own encryption (TLS) is planned for later.

## What you see when it fails

| Message | What it means | What to do |
|---|---|---|
| `no listener at 127.0.0.1:7878` | Nothing listens on that port. | Check that the daemon runs, that `[listener] tcp` is set, and that the daemon was restarted after you set it. From another machine: check that the tunnel is open. |
| `token refused (wrong, expired or revoked)` | The daemon does not accept this token. | Check `quil clients token list` on the daemon machine. Make a new token if needed. |
| `the listener at <addr> could not prove it is your daemon` | Whatever answered does not know this token. It can be a program pretending to be the daemon, or a daemon with another token store. | Quil sent nothing secret to it. Check what listens on the port. |

The daemon's own reason for each refusal is in `quil.log` on the client.

A connection that drops (sleep, Wi-Fi) logs in again with the same token. A revoked or refused token stops the retries and shows the reason.

## What a token user cannot do on the command line

Under `--connect`, these commands refuse to run, because they act on a daemon you do not own: `quil daemon`, `quil clients`, `quil restart`, `quil status`, `quil mcp`. Quil also never starts, restarts or updates a daemon in this mode.

## See what happened: the audit log

The daemon writes `audit.log` beside its other files (`~/.quil/audit.log`). It is one JSON object per line:

```json
{"ts":"2026-10-02T10:29:22.56Z","event":"token_created","transport":"local","token_id":"c07905b4","token_name":"office-screen","rights":"read-only"}
```

It records:
- TCP connects and disconnects
- logins and failed logins
- refused actions (at most one line per connection and action type per minute)
- tokens made, revoked or expired
- privileged actions, such as stopping the daemon

It never records a token, a key, terminal output, notes or what someone typed.

## Good habits

- One token per person or device. Never share a token between people.
- Give the lowest level that does the job. Start with `read-only`.
- Use a short `--expires` for guests.
- Revoke a token as soon as it is not needed.
- Keep token files private (`chmod 600 ~/bob.token` on Linux and macOS).
- Check `audit.log` now and then.

## What is coming

The browser client is here: `quil web --connect <addr> --token-file <path>` uses these same tokens and levels. See [Quil in a browser](web.md). TLS on the listener, so that the ssh tunnel is not needed, is still planned.
