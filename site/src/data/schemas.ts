// JSON-LD schema helpers. Each function returns a plain object ready
// for JSON serialisation. The Page layout passes the sitewide ones
// (Organization + WebSite) to every page automatically; per-page
// schemas (SoftwareApplication, HowTo, FAQPage, BreadcrumbList) are
// opt-in.

import { SITE, canonical } from "./seo";

// --- Sitewide --------------------------------------------------------

/**
 * Organization — describes the project/publisher.
 *
 * The `@id` is a stable IRI that other schemas (WebSite, SoftwareApplication)
 * reference with `{ "@id": ... }` instead of duplicating the full object.
 * Google's parser deduplicates the graph via these IDs, so the "Quil
 * the software" card cross-links cleanly with "Quil the organization".
 */
export function organizationSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "Organization",
    "@id": SITE.url + "/#organization",
    name: SITE.name,
    url: SITE.url,
    logo: {
      "@type": "ImageObject",
      url: SITE.url + "/brand/mark.png",
      width: 512,
      height: 512,
    },
    founder: {
      "@type": "Person",
      "@id": SITE.author.url,
      name: SITE.author.name,
      url: SITE.author.url,
    },
    sameAs: [SITE.github, SITE.author.url],
    description: SITE.tagline,
  };
}

/** WebSite — the site itself. References the Organization via @id. */
export function websiteSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "WebSite",
    "@id": SITE.url + "/#website",
    name: SITE.name,
    url: SITE.url,
    description: SITE.blurb,
    publisher: {
      "@id": SITE.url + "/#organization",
    },
    inLanguage: SITE.htmlLang,
  };
}

// --- Per-page --------------------------------------------------------

/**
 * SoftwareApplication — home page.
 *
 * Enriched with the fields Google's "software info" knowledge card
 * actually renders:
 *   - screenshot       → displayed in the right-hand card
 *   - fileSize         → shown next to the download button
 *   - installUrl       → "Install" button target
 *   - codeRepository   → shown as "Repository: github.com/…"
 *   - operatingSystem  → "Platforms: Linux, macOS, Windows"
 *   - softwareRequirements / memoryRequirements → "System requirements"
 *   - applicationCategory → surfaces in the Developer Tools category
 *   - offers.price=0  → labels the app as "Free"
 *
 * Every field is factually grounded — no synthetic ratings, no fake
 * file sizes. Google's policy is to penalise SoftwareApplication
 * schemas that claim ratings without a visible review widget, so we
 * explicitly omit aggregateRating until real reviews exist.
 */
export function softwareApplicationSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    "@id": SITE.url + "/#software",
    name: SITE.name,
    alternateName: "quil",
    description: SITE.blurb,
    url: SITE.url,
    applicationCategory: SITE.software.applicationCategory,
    applicationSubCategory: "TerminalMultiplexer",
    operatingSystem: SITE.software.operatingSystem,
    softwareVersion: SITE.software.version,
    softwareRequirements: "Linux kernel 4.x+, macOS 11+, or Windows 10+",
    memoryRequirements: "150 MB RSS typical",
    fileSize: "~40 MB",
    license: "https://opensource.org/licenses/MIT",
    releaseNotes: SITE.github + "/blob/master/CHANGELOG.md",
    downloadUrl: SITE.github + "/releases/latest",
    installUrl: SITE.url + "/install",
    codeRepository: SITE.github,
    programmingLanguage: "Go",
    runtimePlatform: "Go 1.25",
    author: {
      "@type": "Person",
      "@id": SITE.author.url,
      name: SITE.author.name,
      url: SITE.author.url,
    },
    publisher: {
      "@id": SITE.url + "/#organization",
    },
    offers: {
      "@type": "Offer",
      price: "0",
      priceCurrency: "USD",
      availability: "https://schema.org/InStock",
    },
    datePublished: SITE.releaseDate,
    dateModified: SITE.releaseDate,
    image: SITE.url + "/og/home.png",
    screenshot: SITE.url + "/og/home.png",
    keywords: [
      "terminal multiplexer",
      "persistent terminal",
      "tmux alternative",
      "ai terminal",
      "claude code",
      "developer productivity",
    ],
  };
}

/**
 * ItemList — used on /features, /plugins, /docs to declare the page
 * as a structured list. Google surfaces ItemList in two ways:
 *   1. A carousel rich result when the list items have their own
 *      canonical URLs (our case: feature detail anchors, plugin
 *      slugs, doc file paths)
 *   2. A "featured results" list snippet in SERP
 *
 * Each item is a `ListItem` with `position` (1-indexed) and a
 * nested `item` object describing the target. For the three pages
 * we pass slightly different item types (SoftwareFeature, SoftwareApplication,
 * or simple Thing) depending on what each list represents.
 */
export function itemListSchema(opts: {
  name: string;
  description: string;
  url: string;
  items: { name: string; description: string; url?: string }[];
}): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "ItemList",
    name: opts.name,
    description: opts.description,
    url: opts.url,
    numberOfItems: opts.items.length,
    itemListElement: opts.items.map((it, i) => ({
      "@type": "ListItem",
      position: i + 1,
      name: it.name,
      description: it.description,
      ...(it.url ? { url: it.url } : {}),
    })),
  };
}

/** HowTo — install page. */
export function howToSchema(opts: {
  name: string;
  description: string;
  totalTime?: string;
  steps: { name: string; text: string }[];
}): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "HowTo",
    name: opts.name,
    description: opts.description,
    totalTime: opts.totalTime,
    step: opts.steps.map((s, i) => ({
      "@type": "HowToStep",
      position: i + 1,
      name: s.name,
      text: s.text,
    })),
  };
}

/** FAQPage — home page and vs/* pages. */
export function faqSchema(faq: { question: string; answer: string }[]): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    mainEntity: faq.map((q) => ({
      "@type": "Question",
      name: q.question,
      acceptedAnswer: {
        "@type": "Answer",
        text: q.answer,
      },
    })),
  };
}

/** BreadcrumbList — every page except home. */
export function breadcrumbSchema(
  items: { name: string; path: string }[],
): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: items.map((item, i) => ({
      "@type": "ListItem",
      position: i + 1,
      name: item.name,
      item: canonical(item.path),
    })),
  };
}
