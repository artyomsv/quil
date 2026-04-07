// @ts-check
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";
import { execSync } from "node:child_process";

// Quil marketing site — astro.config.mjs
//
// SEO is a hard requirement, so:
//   - site URL is set so canonicals + OG URLs + sitemap absolute
//     paths are correct
//   - prefetch is enabled so internal links feel instant
//   - sitemap integration auto-generates sitemap-index.xml at build
//     with a per-page lastmod derived from git history
//   - no client-side hydration islands by default — every page is
//     pure HTML, zero runtime JS (except Astro's 2 kB prefetch stub)
//
// Deploys to GitHub Pages via .github/workflows/site.yml on every
// push that touches site/** or the workflow itself. The public/
// directory carries the CNAME file which GitHub Pages reads to
// serve the content at quil.cc.

// --- Per-page lastmod from git history -------------------------------
//
// Astro's sitemap integration lets us override individual sitemap
// items via a `serialize` hook. We map each public URL back to its
// source file, then ask git for the most recent commit that touched
// that file. The result gives search engines an accurate signal of
// when each page actually last changed, rather than "every page was
// touched at build time" (which degrades freshness ranking).
//
// Falls back gracefully to the build timestamp if git isn't
// available (sandbox environments, broken checkouts, etc.).

const buildTime = new Date().toISOString();

/**
 * Run `git log -1 --format=%cI -- <file>` and return the ISO 8601
 * timestamp, or the build time if git fails or the file has no
 * history yet. Cached per file so repeated calls are O(1).
 *
 * @type {Map<string, string>}
 */
const gitCache = new Map();

/**
 * @param {string} file
 * @returns {string}
 */
function gitLastMod(file) {
  const cached = gitCache.get(file);
  if (cached) return cached;
  let result = buildTime;
  try {
    // --format=%cI gives strict ISO 8601 with timezone offset
    const out = execSync(`git log -1 --format=%cI -- "${file}"`, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    if (out) result = out;
  } catch {
    // git missing, not a repo, or file untracked → stick with buildTime
  }
  gitCache.set(file, result);
  return result;
}

/**
 * Route → source file map. Astro's sitemap hook sees normalised URLs
 * without trailing slashes, which matches our trailingSlash: "never"
 * config. Every route we publish needs an entry here; a missing
 * entry falls back to the build time (harmless but less accurate).
 *
 * @type {Record<string, string>}
 */
const routeToFile = {
  "/": "src/pages/index.astro",
  "/install": "src/pages/install.astro",
  "/features": "src/pages/features.astro",
  "/plugins": "src/pages/plugins.astro",
  "/docs": "src/pages/docs.astro",
  "/legal": "src/pages/legal.astro",
  "/vs/tmux": "src/pages/vs/tmux.astro",
  "/vs/zellij": "src/pages/vs/zellij.astro",
  "/vs/wezterm": "src/pages/vs/wezterm.astro",
  "/vs/screen": "src/pages/vs/screen.astro",
};

export default defineConfig({
  site: "https://quil.cc",
  // trailingSlash: "always" tells Astro to emit canonicals + sitemap
  // URLs with a trailing slash (/install/, /features/, ...). This is
  // the only setting that lines up with GitHub Pages' directory
  // serving behaviour: dist/install/index.html is served at /install/
  // and GH Pages 301-redirects /install -> /install/. Emitting the
  // slashed form directly means Google Search Console's sitemap
  // validator sees 200s on every URL, not redirect chains.
  trailingSlash: "always",
  prefetch: {
    prefetchAll: true,
    defaultStrategy: "viewport",
  },
  integrations: [
    sitemap({
      changefreq: "weekly",
      priority: 0.7,
      filter: (page) => !page.includes("/404"),
      serialize(item) {
        // `item.url` is absolute: "https://quil.cc/install"
        // Strip the origin + any trailing slash to get the route key.
        const route = new URL(item.url).pathname.replace(/\/$/, "") || "/";
        const file = routeToFile[route];
        if (file) {
          item.lastmod = gitLastMod(file);
        } else {
          item.lastmod = buildTime;
        }
        return item;
      },
    }),
  ],
  vite: {
    // @ts-expect-error — Astro bundles its own Vite; @tailwindcss/vite
    // peer dep version skew confuses tsc here. Runtime is fine.
    plugins: [tailwindcss()],
  },
  build: {
    inlineStylesheets: "auto",
    format: "directory",
  },
  markdown: {
    shikiConfig: {
      theme: "github-dark-dimmed",
      wrap: true,
    },
  },
});
