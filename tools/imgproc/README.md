# Screenshot pipeline (`tools/imgproc`)

Crops OS/title-bar chrome and dead black space from raw Quil screenshots,
then exports CDN-ready variants in multiple sizes and formats.

Source masters are tracked in `sources/` (this dir). Outputs go to
`out/screenshots/` (gitignored) ready to sync to a DigitalOcean Space —
they're regenerable from the masters, so only the masters are committed.

> Built on Node + [`sharp`](https://sharp.pixelplumbing.com/) (Python isn't
> installed on this machine, and sharp is the right tool for batch
> resize/format). The `manifest.json` + `PROFILES` shape ports to Pillow
> 1:1 if you ever want a Python version.

## Usage

```bash
cd tools/imgproc
npm install              # once

npm run probe            # report detected crop per image (no output written)
npm run build            # crop + export every variant to out/screenshots/
node process-screenshots.mjs pane-restoration.png   # just one source
```

## What it does

1. **Detects + strips the Windows Terminal chrome** — the title bar (top)
   and drag handle (bottom) by row-brightness, keeping Quil's own tab bar
   and status bar.
2. **Trims dead black space** to the app background colour (`bg` in the
   manifest). Edge-to-edge "content" shots are untouched; the MCP shot's
   empty lower half and the centred dialogs get cropped tight.
3. **Exports per-use-case variants** (see profiles below).

### Crop types (set per image in `manifest.json`)

| `type` | Behaviour | Use for |
|---|---|---|
| `content` | Strip chrome, keep the app edge-to-edge | Full-pane shots (restore, focus, lazygit, MCP, …) |
| `dialog` | Strip chrome, then tight-crop to the centred dialog box + padding | Modal dialogs (setup, settings, welcome, splits) |

## Export profiles (`PROFILES` in `process-screenshots.mjs`)

| Profile | Size | Formats | Intended use | Filename |
|---|---|---|---|---|
| `full` | ≤2048w | webp | archival / hi-DPI | `<name>-2048.webp` |
| `hero` | 1280w | webp + png | blog hero, site sections, README hero | `<name>-1280.webp` / `.png` |
| `card` | 800w | webp | feature cards, thumbnails | `<name>-800.webp` |
| `og` | 1200×630 (cover) | png | social/OG cards | `<name>-og.png` |

WebP for the web (small + crisp on text), PNG where universal support
matters (README renderers, social scrapers). Text screenshots are **not**
exported as JPEG — it smears the small type.

## Uploading to the CDN

Live base: **`https://cdn.stukans.com/quil/screenshots/`** (DigitalOcean Space
fronted by Cloudflare). `npm run build` writes `out/screenshots/urls.md` with
the full URL of every variant, ready to paste.

Filenames are content-addressed by width, so they can be cached forever and
busted by changing the name. Example with the AWS CLI pointed at Spaces (note
the `quil/screenshots/` key prefix that maps to the URL path):

```bash
aws s3 sync out/screenshots/ s3://<space-name>/quil/screenshots/ \
  --endpoint-url https://<region>.digitaloceanspaces.com \
  --acl public-read \
  --cache-control "public, max-age=31536000, immutable" \
  --content-type-by-extension
```

(or `s3cmd sync out/screenshots/ s3://<space>/quil/screenshots/ --acl-public
--add-header="Cache-Control: public, max-age=31536000, immutable"`.)

The origin currently returns `max-age=3600`; because every filename is
width-versioned, it's safe to bump that to a year + `immutable` (above) and
let Cloudflare hold it.

## Where each variant goes

| Asset | Variant | Where |
|---|---|---|
| Blog hero (reboot post) | `pane-restoration-1280.webp` | top of the post |
| Blog inline ("multiple agents") | `focus-screen-1280.webp` | mid-post |
| OG / social card (post + home) | `pane-restoration-og.png` | `og:image` |
| README hero | `pane-restoration-1280.png` | under the title |
| README highlights ×4 | `pane-restoration` / `claude-code-quil-mcp` / `focus-screen` / `claude-code-setup-dialog` `-1280.png` | features section |
| /features cards | `*-800.webp` | per-feature |
| /features section heroes | `*-1280.webp` | reboot, MCP, focus, notes, lazygit |
| /vs/tmux ("survives reboot") | `pane-restoration-1280.webp` | next to the matrix |
| /docs quick-start | `welcome-page-800.webp`, `f1-settings-page-800.webp` | first-launch walkthrough |
| README "See it" row 3 | `pane-resize-800.webp`, `mouse-right-click-menu-800.webp` | drag-resize + context-menu cards |
| /features cards | `pane-resize-800.webp` (mouse-pane-resize), `mouse-right-click-menu-800.webp` (pane-context-menu) | per-feature |

Reference webp with a png fallback where you want belt-and-suspenders:

```html
<picture>
  <source srcset="https://cdn.stukans.com/quil/screenshots/pane-restoration-1280.webp" type="image/webp" />
  <img src="https://cdn.stukans.com/quil/screenshots/pane-restoration-1280.png" alt="Quil restoring panes and Claude Code sessions after a reboot" />
</picture>
```
