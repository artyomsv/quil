#!/usr/bin/env node
/**
 * check-dist — runs scripts/lib/dist-checks.mjs over site/dist after
 * `astro build`. It is the last step of `npm run build`, so pull requests
 * (ci.yml `site` job) and deploys (site.yml) run it the same way.
 */
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { dirname, join, posix, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { checkSite, readPage, urlForDistFile } from "./lib/dist-checks.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const DIST = resolve(here, "..", "dist");
const REPO = resolve(here, "..", "..");

function walk(dir, base = dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const full = join(dir, e.name);
    return e.isDirectory() ? walk(full, base) : [full.slice(base.length + 1).split(sep).join("/")];
  });
}
const read = (p) => (existsSync(p) ? readFileSync(p, "utf8") : null);

const files = new Set(walk(DIST));
const pages = new Map();
for (const f of files) if (f.endsWith(".html")) pages.set(urlForDistFile(f), readPage(readFileSync(join(DIST, f), "utf8")));

/** Every JS file an entry loads, following static and dynamic imports. */
function jsClosure(entry, seen = new Set()) {
  if (seen.has(entry) || !files.has(entry)) return seen;
  seen.add(entry);
  const code = readFileSync(join(DIST, entry), "utf8");
  for (const m of code.matchAll(/(?:\bimport|\bfrom)\s*\(?\s*["']([^"']+\.js)["']/g)) {
    const spec = m[1];
    jsClosure(spec.startsWith("/") ? spec.slice(1) : posix.join(posix.dirname(entry), spec), seen);
  }
  return seen;
}
const homeJs = new Set();
for (const src of pages.get("/")?.scriptSrcs ?? []) if (src.startsWith("/")) jsClosure(src.slice(1), homeJs);
const homeJsGzipBytes = [...homeJs].reduce((n, f) => n + gzipSync(readFileSync(join(DIST, f))).length, 0);

const errors = checkSite({
  pages,
  files,
  sitemapXml: read(join(DIST, "sitemap.xml")),
  robotsTxt: read(join(DIST, "robots.txt")),
  cname: read(join(DIST, "CNAME")),
  llmsTxt: read(join(DIST, "llms.txt")),
  repoFile: (path) => {
    const full = resolve(REPO, path);
    return full.startsWith(REPO + sep) ? read(full) : null;
  },
  homeJsGzipBytes,
  ci: process.env.CI === "true",
  now: Date.now(),
});

if (errors.length) {
  console.error(`check-dist: FAIL — ${errors.length} problem(s)\n`);
  for (const e of errors) console.error(`  - ${e}`);
  process.exit(1);
}
console.log(`check-dist: ok  (${pages.size} HTML files; / loads ${(homeJsGzipBytes / 1024).toFixed(1)} KB JS gzip)`);
