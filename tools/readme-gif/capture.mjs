// capture.mjs — plays the quil.cc tour frame by frame under Playwright's fake
// clock, so every run draws the same content (only anti-aliasing noise on a
// few edge pixels can differ between runs).
// Usage (inside the Playwright container; see make.sh): node capture.mjs <dist dir> <frames dir>
import { createServer } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import { extname, join, resolve, sep } from "node:path";
import { chromium } from "playwright";

const [distArg, outArg] = process.argv.slice(2);
if (!distArg || !outArg) {
  console.error("usage: node capture.mjs <dist dir> <frames dir>");
  process.exit(2);
}
const DIST = resolve(distArg);
const OUT = resolve(outArg);
const cut = JSON.parse(await readFile(new URL("./cut.json", import.meta.url), "utf8"));

const TYPES = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css",
  ".svg": "image/svg+xml", ".png": "image/png", ".xml": "application/xml",
  ".txt": "text/plain; charset=utf-8", ".webmanifest": "application/manifest+json",
};
const server = createServer(async (req, res) => {
  let path = decodeURIComponent(new URL(req.url ?? "/", "http://x").pathname);
  if (path.endsWith("/")) path += "index.html";
  const file = resolve(DIST, `.${path}`);
  if (!file.startsWith(DIST + sep)) { res.writeHead(403).end(); return; }
  try {
    const body = await readFile(file);
    res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream" }).end(body);
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const { port } = server.address();

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: cut.viewport[0], height: cut.viewport[1] }, deviceScaleFactor: 1 });
// Fake clock, paused, before any page script runs: the spinner interval, the
// ambient log streams and requestAnimationFrame then start at the same fake
// instant on every run. Nothing below waits on page timers (no waitForFunction).
const T0 = Date.parse("2026-01-01T09:00:00Z");
await page.clock.install({ time: T0 });
await page.clock.pauseAt(T0 + 1000);
// The stops this cut plays (chapter numbers). home.js numbers the stop bar and the tour card
// from them, so a cut of four stops reads 1 / 4 … 4 / 4 rather than 1 / 6, 3 / 6 ….
if (Array.isArray(cut.stops)) await page.addInitScript((s) => { window.__quilCaptureStops = s; }, cut.stops);
await page.goto(`http://127.0.0.1:${port}/#capture`, { waitUntil: "load" });
// The fonts come from Google Fonts, and document.fonts.ready resolves even when
// they never arrive: the frames then show the container's fallback fonts, not
// the site (one render came out that way). Load every face the site uses and
// stop unless each one arrived. The timer is Node's; the page clock is paused.
const FACES = ["400", "500", "700", "800"].map((w) => `${w} 16px "JetBrains Mono"`)
  .concat(["400", "600"].map((w) => `${w} 16px "Inter"`));
const fontsLoaded = await Promise.race([
  page.evaluate(async (specs) => {
    await document.fonts.ready;
    const lists = await Promise.all(specs.map((s) => document.fonts.load(s)));
    return lists.every((faces) => faces.length > 0 && faces.every((f) => f.status === "loaded"));
  }, FACES),
  new Promise((ok) => setTimeout(() => ok(false), 30_000)),
]);
if (!fontsLoaded) {
  throw new Error("capture.mjs: the site fonts (Inter, JetBrains Mono) did not load from Google Fonts, so the frames would show fallback fonts; check the network and run make.sh again");
}
if ((await page.evaluate(() => typeof window.__quilTour)) !== "object") throw new Error("window.__quilTour is missing — is this the tour build?");

await mkdir(OUT, { recursive: true });
const dt = 1000 / cut.fps;
const shoot = (path) => page.screenshot({ path, animations: "disabled" });
let n = 0;
for (const seg of cut.segments) {
  const frames = Math.max(1, Math.round(seg.seconds * cut.fps));
  for (let i = 0; i < frames; i++) {
    const pos = seg.from + (seg.to - seg.from) * (frames === 1 ? 0 : i / (frames - 1));
    await page.evaluate((p) => window.__quilTour.seek(p), pos);
    await page.clock.runFor(dt);
    await shoot(join(OUT, `${String(n++).padStart(5, "0")}.png`));
  }
}
await page.evaluate((p) => window.__quilTour.seek(p), cut.posterPos);
await page.clock.runFor(dt);
await shoot(join(OUT, "..", "poster.png"));
console.log(`capture.mjs: ${n} frames at ${cut.fps} fps (${(n / cut.fps).toFixed(1)} s)`);
await browser.close();
server.close();
