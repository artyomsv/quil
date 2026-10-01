#!/usr/bin/env node
/**
 * check-dist — runs scripts/lib/dist-checks.mjs over site/dist after
 * `astro build`. It is the last step of `npm run build`, so pull requests
 * (ci.yml `site` job) and deploys (site.yml) run it the same way.
 */
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { checkSite, jsClosure, localScripts, readPage, urlForDistFile } from "./lib/dist-checks.mjs";

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

const distText = (file) => readFileSync(join(DIST, file), "utf8");
// A file missing from dist is not counted here; rule 10 fails the build for it.
const home = pages.get("/");
const homeJs = home ? jsClosure(localScripts(home).map((file) => [file, "/"]), files, distText).files : new Set();
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
  distText,
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
