# Quil in a browser

`quil web` shows your workspace in a web page. It is the same workspace as the one in your terminal: the same projects, tabs and panes. You can use the terminal and the browser at the same time.

## What you can do

In the browser you can:

- watch every project, tab and pane live;
- scroll back through a pane's history (the page keeps the last 1 000 lines of every pane, even while its tab is hidden);
- type into a pane;
- switch tabs and projects;
- take control of the pane size (see [Size](#size)).

You cannot yet create panes, close panes, rename things or change settings from the browser. Use the terminal for those.

## Start it

```
quil web
```

Quil starts the daemon if it is not running, then prints a line like this:

```
Quil web: http://127.0.0.1:41873/
Login code: 7K2QD-M9X4T  (Enter: new code, Ctrl+C: stop)
```

It also opens your browser on that address. Type the login code into the page. You are in.

- The code works **once**. After a good login it is used up.
- Press **Enter** in the terminal for a new code. The old code stops working.
- Press **Ctrl+C** to stop. Every browser tab loses its connection.
- Case does not matter, and a dash or a space is ignored. `7k2qd m9x4t` works.

Options:

| Flag | What it does |
|---|---|
| `--port 7880` | Use this port. The default is a free port that Quil picks. |
| `--listen 127.0.0.1:7880` | Use this address. It must be a loopback address. |
| `--no-open` | Do not open the browser. |

Quil never listens on anything but loopback (`127.0.0.1`, `::1`, `localhost`).

## Why you type the code

The code is not in the link. This is on purpose.

On a shared machine, other accounts can read the command that opens your browser. A code in that command would be a code for them too. So Quil prints the code only in your terminal, and you type it.

A wrong code does not break the right one. It only makes the next wrong try wait longer (a quarter of a second, then up to two seconds).

### A second secret, for the same reason

A good login gives your browser two things:

1. a session cookie, and
2. a key, which the page keeps in its own browser storage.

Why two? Cookies do not care about ports. If another account on the same machine runs a web server on a different local port, your browser sends it your Quil cookie too. With only a cookie, that page could use your workspace.

Browser storage **is** separate for each port. So the key stays with the Quil page. Every tab must send the key before Quil connects it to the daemon. A cookie alone opens nothing.

You do not have to do anything. If the key is lost (you cleared site data, or restarted `quil web`), the page shows the login form again.

### A flood of wrong codes can lock you out

Quil handles at most 8 logins at the same time. A wrong code holds its place for the whole wait. This is what makes guessing slow.

The price: if another local account sends wrong codes without stopping, all 8 places stay full. You then get "too many logins" until the flood stops.

To fix it, stop the flood. Or press Ctrl+C and run `quil web` again for a new code and a new port. The log (`web.log`) shows `login refused: too many in progress`.

## From another machine

`quil web` listens on loopback only. To use it from your laptop, forward a port with ssh.

On the machine that runs quil, look at the port in the printed address. Say it is `41873`. Then, on your laptop:

```
ssh -N -L 7880:127.0.0.1:41873 you@host
```

Open `http://localhost:7880/` and type the code. For a fixed port, start with `quil web --port 41873 --no-open`.

If ssh says it could not bind port 7880, something else holds it. Pick another local port and do not use that one.

## Use a token (`--connect`)

By default the browser has the same rights as your terminal: full. To give a browser fewer rights, or to reach a daemon on a shared listener, use a token. See [Sharing a workspace](sharing-a-workspace.md) to make one.

```
quil web --connect 7878 --token-file ~/bob.token
```

`--connect` takes the same address and the same token as `quil --connect`. You can also put the token in `QUIL_TOKEN`. Quil reads the token from the file or the variable, never from the command line.

The page gets the rights of the token:

| Level | In the browser |
|---|---|
| `full`, `standard` | Type, switch, resize, take control. |
| `read-only` | Watch only. The tab bar shows a **read-only** badge. Typing and switching are off. |

A read-only browser **follows the daemon**: it shows each pane at the size the daemon uses, and it never changes that size. Page tabs and the terminal never fight over the size.

Each browser tab logs in on its own with the token.

## Size

A pane has one size in the daemon. When two clients look at one workspace, one of them is the **size master**. The master sets the size. The other clients are **followers**.

- A follower shows each pane at the master's size. If that size does not fit the page, the page makes the font smaller until it does.
- A follower never sends a size to the daemon.
- Press **Take control** in the tab bar to become the master. The panes then fit your browser window.
- A window smaller than 40 columns by 10 rows sends no size at all.

Opening a browser tab does not change the size your terminal uses. If your terminal is the master, the browser just follows.

## Limits

- **Very slow client.** If a browser tab cannot keep up, the daemon may drop some live output for it. Quil reconnects the tab and the screen is correct again after the next repaint, as in the terminal client. The banner says **This tab fell behind — reconnecting**.
- **Keys the browser keeps.** Ctrl+W, Ctrl+T, Ctrl+N and Ctrl+Tab are used by the browser itself. They never reach a pane. Use the terminal for programs that need them.
- **Browsers.** Chrome, Edge and Firefox are supported. Safari should work but is not tested.
- **Tabs.** At most 16 browser tabs at once.
- **Builds without the web page.** `./scripts/dev.sh cross`, `./scripts/dev.sh image` and the Dockerfile build binaries **without** the web page. There, `quil web` prints "This build has no web UI" and the page says the same. `./scripts/dev.sh build` and release builds include it.
- **No remote daemon.** `quil web` does not run over `--remote`. Run it on the daemon's machine, or use `--connect`.

## Troubleshooting

The page shows a banner when its connection closes. The number is the WebSocket close code.

| Banner | Code | What it means | What to do |
|---|---|---|---|
| (login form) | 1008, "login required" | The page has no valid key: new browser data, or a restarted `quil web`. | Type a login code. |
| The web server refused this page: … | 1008, other | The server refused the page for another reason. | Reload. If it stays, look in `web.log`. |
| This tab fell behind — reconnecting | 4002 | The page was too slow, or resynced too often. | Nothing. It reconnects. If it repeats, close busy tabs. |
| The daemon is unavailable — reconnecting | 4003 | The daemon stopped or did not answer. | Wait, or run `quil daemon start`. |
| Token refused: … | 4004 | The token was wrong, expired or revoked. | Make a new token. Restart `quil web`. |
| Version mismatch: … | 4005 | The daemon has another version than `quil web`. | Start `quil` once, or restart the daemon, so both match. |
| Closed by an agent | 4006 | An agent or another client closed this client. | Reload the page. |
| The web server stopped | 1001 | You stopped `quil web` (Ctrl+C). | Start it again. |
| Connection lost — reconnecting | 1006 | The network dropped. | Nothing. It reconnects. |

Code 4001 (resync) shows no banner. The page reconnects at once and keeps its place.

Other things you may see:

- **"Too many logins"** (HTTP 429): see [A flood of wrong codes](#a-flood-of-wrong-codes-can-lock-you-out).
- **Blank page that says "no web UI"**: this build has no web page. Install a release build.
- **403 when you open the address**: you used a name that is not loopback (for example a domain that points to `127.0.0.1`). Use `localhost` or `127.0.0.1`.
- **Reload or open a second browser tab**: each browser tab has its own client id, kept for that tab. A reload keeps the id, and a reload within 10 seconds of a resync also keeps the tab's place. A copied tab gets a new id.

### The log

`quil web` writes `web.log` in the quil folder (`~/.quil/web.log`, or `.quil/web.log` for a dev build). It records logins (ok, wrong code, too many), refused pages, tabs opening and closing, and errors. It never records the login code or the key.

Next to it, the daemon's own log is `quild.log`. See [Troubleshooting](troubleshooting.md).
