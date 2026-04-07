#!/usr/bin/env node
/**
 * build-og.mjs — generate per-page Open Graph SVG images from a template.
 *
 * Reads `public/brand/og-template.svg`, substitutes the __PLACEHOLDER__
 * tokens for each entry in the `pages` array, writes the generated SVG
 * files into `public/og/`. Rasterisation to PNG is a separate step
 * (rsvg-convert via Docker) so this script stays dependency-free and
 * runnable with plain Node.
 *
 * Why one-line vs two-line headline:
 *   Some taglines fit on one line at font-size 74 (e.g. "Install Quil
 *   in 30 seconds"). Others need two lines at smaller font (e.g. "The
 *   workspace that / survives every reboot"). Each page entry chooses
 *   its headline shape.
 *
 * Why not do this at Astro build time:
 *   Astro doesn't ship a PNG rasteriser, and shipping SVG-only OG
 *   images breaks LinkedIn previews. So we rasterise once, check in
 *   the PNGs, and regenerate via `npm run og` only when copy changes.
 *
 * Usage:
 *   node scripts/build-og.mjs          # generate SVGs
 *   # then run the rsvg step to rasterise to PNG
 */

import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, "..");
const TEMPLATE = join(ROOT, "public", "brand", "og-template.svg");
const OUT_DIR = join(ROOT, "public", "og");

/**
 * @typedef {{
 *   slug: string,
 *   path: string,            // path segment for footer display (e.g. "/install")
 *   kicker: string,          // uppercase label (e.g. "GET STARTED")
 *   headline: string,        // headline line 1
 *   headline2?: string,      // optional headline line 2
 *   headlineSize?: number,   // default 88
 *   subline: string,         // supporting tagline
 * }} PageOg
 */

/** @type {PageOg[]} */
const pages = [
  {
    slug: "home",
    path: "",
    kicker: "PERSISTENT WORKFLOW",
    headline: "The workspace that",
    headline2: "survives every reboot.",
    headlineSize: 84,
    subline: "A reboot-proof terminal multiplexer for AI-native development.",
  },
  {
    slug: "install",
    path: "/install",
    kicker: "GET STARTED",
    headline: "Install Quil",
    headline2: "in 30 seconds.",
    headlineSize: 92,
    subline: "Linux, macOS, and Windows. One curl command or a single go install.",
  },
  {
    slug: "features",
    path: "/features",
    kicker: "CAPABILITIES",
    headline: "Every feature",
    headline2: "in v1.0.0.",
    headlineSize: 92,
    subline: "Typed panes, AI session resume, plugin system, notification center.",
  },
  {
    slug: "plugins",
    path: "/plugins",
    kicker: "TYPED PANES",
    headline: "Not every pane",
    headline2: "is a shell.",
    headlineSize: 92,
    subline: "Four built-in plugins. TOML authoring. Hot reload on save.",
  },
  {
    slug: "docs",
    path: "/docs",
    kicker: "DOCUMENTATION",
    headline: "Every document,",
    headline2: "plain Markdown.",
    headlineSize: 88,
    subline: "Vision, PRD, architecture, plugin reference, roadmap.",
  },
  {
    slug: "legal",
    path: "/legal",
    kicker: "LEGAL",
    headline: "MIT licensed.",
    headline2: "No telemetry.",
    headlineSize: 92,
    subline: "Quil stores all state locally under ~/.quil/.",
  },
  {
    slug: "vs-tmux",
    path: "/vs/tmux",
    kicker: "COMPARE",
    headline: "Quil vs tmux",
    headlineSize: 104,
    subline: "The tmux alternative that survives a host reboot.",
  },
  {
    slug: "vs-zellij",
    path: "/vs/zellij",
    kicker: "COMPARE",
    headline: "Quil vs Zellij",
    headlineSize: 100,
    subline: "A workflow orchestrator, not just a multiplexer.",
  },
  {
    slug: "vs-wezterm",
    path: "/vs/wezterm",
    kicker: "COMPARE",
    headline: "Quil vs WezTerm",
    headlineSize: 92,
    subline: "Complementary tools — run Quil inside WezTerm.",
  },
  {
    slug: "vs-screen",
    path: "/vs/screen",
    kicker: "COMPARE",
    headline: "Quil vs GNU Screen",
    headlineSize: 80,
    subline: "The modern multiplexer screen was never designed to be.",
  },
];

/**
 * Escape XML special characters in text that goes into <text> content.
 * The headline and subline never contain quoting, but defensive escape
 * for any ampersand/less-than the copy might pick up later.
 */
function xmlEscape(s) {
  return s
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

/**
 * Substitute all __TOKEN__ placeholders for one page. Two-line vs
 * one-line headlines are handled by shifting the second-line Y up
 * off-canvas when headline2 is empty.
 */
function render(template, page) {
  const size = page.headlineSize ?? 88;

  // Vertical layout maths:
  //   headline line 1 baseline: y = 370
  //   headline line 2 baseline: y = 370 + size*1.05
  //   subline baseline:         y = (line2 ? line2 : line1) + 70
  //
  // When there's no second line we render it off-canvas at y=-100 so
  // the text element exists but is invisible. Simpler than conditional
  // omission which would require template branching.
  const line1Y = 370;
  const line2Y = page.headline2 ? line1Y + Math.round(size * 1.05) : -100;
  const sublineY = (page.headline2 ? line2Y : line1Y) + 60;
  const ariaLabel = page.headline2
    ? `Quil — ${page.headline} ${page.headline2}`
    : `Quil — ${page.headline}`;

  return template
    .replaceAll("__ARIA__", xmlEscape(ariaLabel))
    .replaceAll("__KICKER__", xmlEscape(page.kicker))
    .replaceAll("__HEADLINE__", xmlEscape(page.headline))
    .replaceAll("__HEADLINE2__", xmlEscape(page.headline2 ?? ""))
    .replaceAll("__HEADLINE2_Y__", String(line2Y))
    .replaceAll("__HEADLINE_SIZE__", String(size))
    .replaceAll("__SUBLINE__", xmlEscape(page.subline))
    .replaceAll("__SUBLINE_Y__", String(sublineY))
    .replaceAll("__PATH__", xmlEscape(page.path));
}

function main() {
  const template = readFileSync(TEMPLATE, "utf8");
  mkdirSync(OUT_DIR, { recursive: true });

  for (const page of pages) {
    const svg = render(template, page);
    const outPath = join(OUT_DIR, `${page.slug}.svg`);
    writeFileSync(outPath, svg);
    process.stdout.write(`  ${page.slug}.svg\n`);
  }
  process.stdout.write(`\n${pages.length} OG templates written to public/og/\n`);
}

main();
