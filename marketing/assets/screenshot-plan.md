# Quil — Screenshot & Asset Plan

Reproducible capture plan for marketing visuals: the article, the GitHub README
hero, the OG/social-preview image, and the `/vs/*` pages.

> **Why VHS:** Quil is a Bubble Tea (Charm) app, so [`charmbracelet/vhs`](https://github.com/charmbracelet/vhs)
> is the right capture tool — a `.tape` script drives a real terminal headlessly
> and renders a deterministic GIF/PNG. Same script → same frame every time, so
> assets stay consistent as the UI evolves. Install: `go install github.com/charmbracelet/vhs@latest`
> (needs `ttyd` + `ffmpeg`). For interactive shots VHS can't script cleanly, use a
> manual terminal grab (see "Manual capture" below).

> **⚠️ No fabricated data.** Every shot must show *real* Quil output from a real
> session. Don't mock prices, fake a session id into a screenshot, or doctor a
> count. Use a throwaway demo repo and real commands. Redact only personal paths
> and secrets (see "Redaction").

---

## Global capture settings (keep all assets consistent)

| Setting | Value | Why |
|---|---|---|
| Font | **JetBrains Mono** | Matches the quil.cc brand font |
| Font size | 18–22 | Legible when scaled down on Medium / README |
| Theme | Dark, warm — background `#080402` (the site `theme-color`), accent flame `#ff6a2b`-ish | Matches the site's "ember" palette |
| Terminal width | ~110 cols | Wide enough for splits, not so wide text shrinks |
| Output width | **1280px** (hero/README), **1200×630** (OG), ≤1400px (Medium inline) | Standard targets |
| Format | **GIF** for motion (hero), **PNG** for static | Smaller static = faster pages |
| Cursor | Block, blink off for stills | Cleaner frames |

---

## Shot list

| # | Asset | Used by | Type | Content |
|---|---|---|---|---|
| 1 | **Hero restore** | Article top, README hero, OG image | GIF | Cold `quil` start restoring a real persisted workspace: layout rebuilds, panes drop into their dirs, a Claude Code pane shows a resume line |
| 2 | **Typed panes** | Article (#2), /features | PNG | One tab: 2 Claude Code panes + a shell, each border showing its CWD; one pane border green (work-done indicator) |
| 3 | **Quil resume close-up** | Article (#3) | PNG | A single Claude Code pane just after restore, showing the `claude --resume <id>` line / "resuming" state |
| 4 | **Pane setup dialog** | /features, /plugins | PNG | `Ctrl+N` → Claude Code → setup dialog: directory browser + the permission-mode toggle |
| 5 | **Notification sidebar** | /features | PNG | `Alt+N` open with a few real events (process exit, agent idle) |
| 6 | **vs split** | /vs/tmux, /vs/zellij | PNG | Clean 2x2 split showing tmux-style nested layout |
| 7 | **MCP in a client** | /features, article (optional) | PNG | Claude Desktop (or Claude Code) calling a Quil MCP tool, e.g. `list_panes` returning real panes |

The **hero GIF (1) doubles as the OG/social image** — grab a clean single frame from
it (1200×630) for `og/home.png` and the GitHub social-preview upload.

---

## VHS tapes (starting points — adjust paths/repo to your demo)

### `tape/hero-restore.tape`  → Shot 1

> This captures a **real** restore. Pre-seed a Quil workspace (open a couple of
> panes + a Claude Code pane, let it persist), then this tape records a fresh
> `quil` launch reattaching it. It is NOT a simulated/faked reboot — it shows the
> genuine restore path. (A literal `reboot` can't run inside VHS; the cold `quil`
> start is the honest stand-in and is what users actually type after a reboot.)

```tape
# hero-restore.tape
Output marketing/assets/hero-reboot-restore.gif
Set Theme { "name": "Quil", "background": "#080402", "foreground": "#e8e0d8" }
Set FontFamily "JetBrains Mono"
Set FontSize 20
Set Width 1280
Set Height 720
Set Padding 24
Set TypingSpeed 60ms

# Show the "after reboot" cold prompt
Type "# fresh boot — workspace is gone from memory, persisted on disk"
Enter
Sleep 1s
Type "quil"
Enter
Sleep 4s        # let the layout rebuild + ghost buffers replay + claude --resume fire
Sleep 3s        # hold on the restored workspace
```

### `tape/typed-panes.tape`  → Shot 2 (still)

```tape
# typed-panes.tape  — capture a still frame from an already-arranged workspace
Output marketing/assets/typed-panes.png
Set Theme { "name": "Quil", "background": "#080402", "foreground": "#e8e0d8" }
Set FontFamily "JetBrains Mono"
Set FontSize 20
Set Width 1280
Set Height 720
Set Padding 24

Type "quil"
Enter
Sleep 4s
Screenshot marketing/assets/typed-panes.png
```

For the interactive dialogs (Shots 4–5) you *can* script the keypresses in VHS:

```tape
# e.g. inside a running quil, open the setup dialog
Ctrl+N
Sleep 800ms
Screenshot marketing/assets/pane-setup-dialog.png
```

…but these are often easier to grab manually (below) since they depend on live
state.

---

## Manual capture (Shots 4–7, or anything VHS can't script)

1. Launch Quil in a clean terminal at the global settings above.
2. Arrange the exact state you want (open the dialog, the sidebar, the splits).
3. Grab the window:
   - **Windows:** `Win+Shift+S` (region) → save PNG.
   - **macOS:** `Cmd+Shift+4` then `Space` to grab the window.
   - **Linux:** `flameshot gui` or `grim -g "$(slurp)"`.
4. Crop to content, keep the dark background bleed for a framed look.

For the **MCP shot (7)**: open Claude Desktop with Quil's MCP server wired up
(`{"mcpServers":{"quil":{"command":"quil","args":["mcp"]}}}`), ask it to list
panes, and screenshot the tool call + result. Real tool output only.

---

## Redaction (before publishing any shot)

- Replace your real home path with a neutral one (demo user, e.g. `~/code/demo`).
- Use a throwaway demo repo name — not a client/private project.
- No real API keys, tokens, `.env` contents, or `stripe listen` secrets in frame.
- Session ids are fine to show (they're random UUIDs, not secrets) — but double-check
  nothing sensitive scrolled into the visible ghost buffer.

---

## Where each asset lands

| Asset | Destination |
|---|---|
| Hero GIF | Article top + `README.md` (under the title) + a single frame → GitHub Settings → Social preview (1280×640) |
| OG frame (1200×630) | `site/public/og/home.png` (replaces current) |
| Article images | the `marketing/articles/.../assets/` refs, then upload into the Medium import / quil.cc blog |
| /features, /plugins, /vs images | `site/public/` and reference from the relevant `.astro` pages |
