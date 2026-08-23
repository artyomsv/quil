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
    version: "1.63.3",
    license: "Apache-2.0",
    operatingSystem: "Linux, macOS, Windows",
    applicationCategory: "DeveloperApplication",
    runtime: "Cross-platform binary",
  },

  /** ISO 8601 release date for structured data + sitemap lastmod. */
  releaseDate: "2026-08-23",
} as const;

export interface Page {
  /** Required: page title without the brand suffix (BaseHead adds " · Quil").
   *  This is the EDITORIAL title — it is what the page calls itself, and on
   *  a blog post it is also the <h1>. When the SERP wants different words
   *  from the ones on the page, override the <title> with `seoTitle` rather
   *  than bending this one; the visible heading and the search result are
   *  allowed to differ, and pretending otherwise is how headings end up
   *  written for a crawler instead of a reader. */
  title: string;
  /** Required: 120-160 char meta description, unique per page. */
  description: string;
  /** Path relative to the site origin, e.g. "/install". */
  path: string;
  /** Optional: per-page OG image path. Falls back to SITE.defaultOgImage. */
  ogImage?: string;
  /** Optional: keywords array — not used by Google but useful for Bing / Yandex. */
  keywords?: string[];
  /**
   * Optional: replaces `title` as the <title> / og:title stem. The brand
   * suffix is still appended unless `brandSuffix` is false.
   *
   * Exists because Google truncates a <title> around 60 characters and
   * the flagship post's editorial title ran to ~77 with the suffix — the
   * result was a search listing that stopped mid-phrase.
   */
  seoTitle?: string;
  /**
   * Optional: append " · Quil" to the title. Default true.
   *
   * Set false where the brand already appears in the stem. "Quil vs tmux
   * · Quil" says the name twice and spends ~7 of the ~60 usable
   * characters doing it.
   */
  brandSuffix?: boolean;
  /**
   * Optional: og:type. Default "website".
   *
   * A blog post is "article" — the difference is what a share card and a
   * crawler each expect to find, and "website" on a post suppresses the
   * article-level fields entirely.
   */
  ogType?: "website" | "article";
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
/**
 * Resolve an asset reference to an absolute URL.
 *
 * An `ogImage` is either site-relative ("/og/home.png") or already
 * absolute (a CDN-hosted card on cdn.stukans.com). Prefixing the origin
 * unconditionally turns the second kind into
 * `https://quil.cchttps://cdn.stukans.com/…` — a string that is not a
 * URL and that no consumer reports as an error; it simply renders no
 * image.
 *
 * This lives here, next to `canonical`, because the guard previously
 * existed ONLY inside BaseHead. The blog template built its
 * BlogPosting `image` with a bare `SITE.url + …` twenty lines away in
 * another file and inherited nothing — so the page carrying 64% of the
 * site's impressions shipped the malformed form in its JSON-LD while
 * its OG tags were correct. Two call sites, one rule: the rule has to
 * be somewhere both can reach it.
 */
export function absoluteURL(ref: string): string {
  return /^https?:\/\//.test(ref) ? ref : SITE.url + ref;
}

export function canonical(path: string): string {
  if (path === "/") return SITE.url + "/";
  const withLeadingSlash = path.startsWith("/") ? path : "/" + path;
  const withTrailingSlash = withLeadingSlash.endsWith("/")
    ? withLeadingSlash
    : withLeadingSlash + "/";
  return SITE.url + withTrailingSlash;
}
