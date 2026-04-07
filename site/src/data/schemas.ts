// JSON-LD schema helpers. Each function returns a plain object ready
// for JSON serialisation. The Page layout passes the sitewide ones
// (Organization + WebSite) to every page automatically; per-page
// schemas (SoftwareApplication, HowTo, FAQPage, BreadcrumbList) are
// opt-in.

import { SITE, canonical } from "./seo";

// --- Sitewide --------------------------------------------------------

/** Organization — describes the project/publisher. */
export function organizationSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "Organization",
    name: SITE.name,
    url: SITE.url,
    logo: SITE.url + "/brand/mark.png",
    founder: {
      "@type": "Person",
      name: SITE.author.name,
      url: SITE.author.url,
    },
    sameAs: [SITE.github],
    description: SITE.tagline,
  };
}

/** WebSite — the site itself. */
export function websiteSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "WebSite",
    name: SITE.name,
    url: SITE.url,
    description: SITE.blurb,
    publisher: {
      "@type": "Organization",
      name: SITE.name,
      url: SITE.url,
    },
    inLanguage: SITE.htmlLang,
  };
}

// --- Per-page --------------------------------------------------------

/** SoftwareApplication — home page. */
export function softwareApplicationSchema(): Record<string, unknown> {
  return {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    name: SITE.name,
    description: SITE.blurb,
    url: SITE.url,
    applicationCategory: SITE.software.applicationCategory,
    operatingSystem: SITE.software.operatingSystem,
    softwareVersion: SITE.software.version,
    license: "https://opensource.org/licenses/MIT",
    releaseNotes: SITE.github + "/blob/master/CHANGELOG.md",
    downloadUrl: SITE.github + "/releases/latest",
    author: {
      "@type": "Person",
      name: SITE.author.name,
      url: SITE.author.url,
    },
    offers: {
      "@type": "Offer",
      price: "0",
      priceCurrency: "USD",
    },
    datePublished: SITE.releaseDate,
    image: SITE.url + SITE.defaultOgImage,
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
