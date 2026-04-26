// Single source of truth for site-wide SEO constants. Every page
// eventually imports either this directly or the schema helpers that
// depend on it, so changing anything here propagates to the whole
// site on the next build.

export const SITE = {
  /** Production canonical origin. Used for absolute URLs in OG tags,
   *  canonical link, sitemap, and JSON-LD. */
  url: "https://quil.cc",

  /** Brand name as it should appear in titles. */
  name: "Quil",

  /** Tagline used as the og:site_name backup and as meta-description
   *  fallback when a page forgets to set one. */
  tagline: "The persistent workflow orchestrator for AI-native development",

  /** Short one-line pitch for JSON-LD WebSite.description. */
  blurb:
    "Quil is a reboot-proof terminal multiplexer for developers who run complex multi-tool AI workflows. Type `quil` after a restart and your tabs, panes, AI sessions, and infrastructure tools snap back in under 30 seconds.",

  /** Locale + lang for <html lang="…"> and og:locale. */
  locale: "en_US",
  htmlLang: "en",

  /** Default OG image (1200×630 PNG rendered from the brand SVG).
   *  Used only as a fallback for pages that don't set their own
   *  ogImage. All current pages set per-page cards via build-og.mjs. */
  defaultOgImage: "/og/home.png",

  /** GitHub repository — used for outbound links and structured data. */
  github: "https://github.com/artyomsv/quil",

  /** Author / publisher info for JSON-LD Organization. */
  author: {
    name: "Artjoms Stukans",
    url: "https://github.com/artyomsv",
  },

  /** Software metadata for the SoftwareApplication JSON-LD schema
   *  that ships on the home page. The version is the single source
   *  of truth for the home page hero pill. The release.yml workflow
   *  bumps these two fields automatically as part of its version
   *  bump step — manual edits are normally unnecessary but harmless
   *  (the next release will overwrite both via sed). */
  software: {
    version: "1.10.2",
    license: "MIT",
    operatingSystem: "Linux, macOS, Windows",
    applicationCategory: "DeveloperApplication",
    runtime: "Cross-platform binary",
  },

  /** ISO 8601 release date for structured data + sitemap lastmod. */
  releaseDate: "2026-04-26",
} as const;

export interface Page {
  /** Required: page title without the brand suffix (BaseHead adds " · Quil"). */
  title: string;
  /** Required: 120-160 char meta description, unique per page. */
  description: string;
  /** Path relative to the site origin, e.g. "/install". */
  path: string;
  /** Optional: per-page OG image path. Falls back to SITE.defaultOgImage. */
  ogImage?: string;
  /** Optional: keywords array — not used by Google but useful for Bing / Yandex. */
  keywords?: string[];
}

/**
 * Build the canonical URL for a page.
 *
 * Always emits a trailing slash on non-root paths so the canonical
 * URL matches GitHub Pages' directory-style serving. Without this,
 * the canonical link tag would point at `/install` but GH Pages
 * serves the page at `/install/` (301-redirecting the slashless
 * form), which Google Search Console flags as a sitemap redirect
 * chain and refuses to read.
 *
 * The home URL keeps its single trailing slash ("/").
 */
export function canonical(path: string): string {
  if (path === "/") return SITE.url + "/";
  const withLeadingSlash = path.startsWith("/") ? path : "/" + path;
  const withTrailingSlash = withLeadingSlash.endsWith("/")
    ? withLeadingSlash
    : withLeadingSlash + "/";
  return SITE.url + withTrailingSlash;
}
