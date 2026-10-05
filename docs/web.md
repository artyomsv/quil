# Quil in a browser

`quil web` shows your workspace in a web page. It is the same workspace as the one in your terminal: the same projects, tabs and panes. You can use the terminal and the browser at the same time.

## What you can do

In the browser you can:

- watch every project, tab and pane live;
- scroll back through a pane's history (the page keeps the last 1 000 lines of every pane, even while its tab is hidden);
- type into a pane, and paste into it;
- switch tabs and projects;
- take control of the pane size (see [Size](#size));
- change the workspace (see [Editing](#editing));
- read and dismiss notifications (see [Notifications](#notifications));
- open lazygit and hunk over a pane (see [Overlays](#overlays));
- use your keymap (see [Keys](#keys)).

The palette, pane notes and settings are not in the browser. Use the terminal for those. The browser tells you when you press a key for one of them.

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
| `full` | Everything above, including overlays and saved instances that carry raw arguments. |
| `standard` | Everything above, except starting an overlay and starting a saved instance that carries raw arguments. |
| `read-only` | Watch only. The tab bar shows a **read-only** badge. Typing, pasting, switching and all editing are off. |

A read-only browser **follows the daemon**: it shows each pane at the size the daemon uses, and it never changes that size. Page tabs and the terminal never fight over the size.

Each browser tab logs in on its own with the token.

## Editing

Each pane has a header with a menu. From the menu you can:

- split the pane to the right or below;
- replace it with another pane (**Replace…**);
- rename it, mute it, restart it;
- move it to another tab of the same project (one **Move to <tab name>** entry for each other tab);
- close it. Quil asks first. A pane that runs in a git worktree also asks if you want the worktree removed.

Tabs work the same way. Press `+` in the tab bar to make a new tab. Double-click a tab to rename it. The tab menu sets a color or closes the tab. Drag the border between two panes to change the split.

**New pane…**, **Replace…** and `+` open the create-pane dialog. It has the same fields as the terminal dialog:

- pane type, and saved instances (you can create, edit and delete them);
- folder, with a folder browser and recent folders;
- kube context;
- toggles (for example a permission mode);
- worktree: an existing worktree, or a new branch;
- sandbox: image and sign-in;
- resume: pick an earlier session.

The dialog opens in the project folder. One difference from the terminal: while **new branch** is chosen, the dialog hides **resume**. A new branch starts in an empty checkout, where an old session cannot be found.

Notes:

- A saved instance is read from the files of the machine that runs `quil web`, never from the page. With `standard` rights you can save instances, but you cannot start one that carries raw arguments. That needs `full` rights.
- A new worktree can take a while. The pane shows a spinner and the real pane takes its place when git is done. If git fails, the pane shows the error and the notification list gets a **worktree failed** card (a System event, so it shows unless you hide that group).
- With `read-only` rights all of these controls are gone.
- A closed pane stays closed. The daemon keeps every saved layout in step with the panes that exist.
- In rare cases a blank slot shows for a moment: another client saves a layout while your terminal is splitting a pane. The pane fills the slot as soon as it arrives.

## Notifications

The notification list is the same as in the terminal, with the same filter. Press `Alt+N` to open it. You can dismiss one notification or all of them. Click one to jump to its pane. Tabs show an unread mark for panes you have not looked at.

## Overlays

`Alt+G` opens lazygit and `Alt+D` opens hunk over the pane area. Each tab has one overlay at a time. Press the key again to hide it. If the pane's folder holds several git repositories, a list asks which one, as in the terminal. If the tab's overlay was opened on another repository, the key opens a new one on this pane's repository. While an overlay shows, your keys go to it. Hiding it in the browser does not hide it in the terminal. A read-only browser can show or hide an overlay that exists, but cannot start one. A standard browser cannot start one either.

## Keys

The browser uses your keymap: your preset (including the tmux preset and its prefix) and your overrides. Press `F1` to see the list of active keys and any conflicts.

- The browser keeps `Ctrl+W`, `Ctrl+T`, `Ctrl+N`, `Ctrl+Tab`, `Ctrl+Shift+T`, `Ctrl+Shift+N` and `Ctrl+Shift+W` for itself. When an action uses one of them, the browser uses another chord. See [Keybindings](keybindings.md#keys-in-the-browser). The F1 list shows which chord works.
- A key that runs an action does not go to the pane. All other keys go to the pane.
- Alt composes text on macOS only, so on macOS `Alt+letter` types the text your layout makes. On Windows and Linux, Alt works as a modifier, and AltGr types text.
- Your keymap is read when the page loads. Change the preset in the terminal (**F1 → Settings → Keys**), then reload the page.

## Paste

A paste goes in one at a time. A big paste waits for the pane to take its parts. If the pane stops reading, the paste waits. If the connection drops or the pane restarts during a paste, the page says the paste may be partly delivered. It never sends the same part twice. Keys you type during a paste go out after it ends.

## Size

A pane has one size in the daemon. When two clients look at one workspace, one of them is the **size master**. The master sets the size. The other clients are **followers**.

- A follower shows each pane at the master's size. If that size does not fit the page, the page makes the font smaller until it does.
- A follower never sends a size to the daemon.
- Press **Take control** in the tab bar to become the master. The panes then fit your browser window.
- A window smaller than 40 columns by 10 rows sends no size at all.

Opening a browser tab does not change the size your terminal uses. If your terminal is the master, the browser just follows.

## Limits

- **Very slow client.** If a browser tab cannot keep up, the daemon may drop some live output for it. Quil reconnects the tab and the screen is correct again after the next repaint, as in the terminal client. The banner says **This tab fell behind — reconnecting**.
- **Keys the browser keeps.** Ctrl+W, Ctrl+T, Ctrl+N and Ctrl+Tab are used by the browser itself. They never reach a pane. Use the terminal for programs that need them. Quil actions on these keys get another chord (see [Keys](#keys)).
- **`bindings.toml`.** If you have no `bindings.toml`, the browser and the terminal both use the old `[keybindings]` table in `config.toml`.
- **Two instance writers.** The terminal and the browser each read the instance file again before they change it, so neither erases what the other saved. Only two saves at the same instant can still lose one. If the file does not parse, both refuse to change it.
- **Browsers.** Chrome, Edge and Firefox are supported. Safari should work but is not tested.
- **Tabs.** At most 16 browser tabs at once.
- **Builds without the web page.** `./scripts/dev.sh cross`, `./scripts/dev.sh image` and the Dockerfile build binaries **without** the web page. There, `quil web` prints "This build has no web UI", and the page reads: "This build of quil has no web UI. Install a release build, or build it with ./scripts/dev.sh build." `./scripts/dev.sh build` and release builds include it.
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
- **Reload or open a second browser tab**: each browser tab has its own client id, kept for that tab. A reload normally keeps the id. If the old connection has not closed yet, the tab gets a new id: the old id is released only after the old connection closes. A copied tab gets a new id.

### The log

`quil web` writes `web.log` in the quil folder (`~/.quil/web.log`, or `.quil/web.log` for a dev build). It records logins (ok, wrong code, too many), refused pages, tabs opening and closing, and errors. It never records the login code or the key.

Next to it, the daemon's own log is `quild.log`. See [Troubleshooting](troubleshooting.md).
