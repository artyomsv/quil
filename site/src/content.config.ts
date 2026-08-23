// Astro content collections.
//
// `blog` powers /blog and /blog/<slug>. Posts are plain Markdown under
// src/content/blog/. The glob loader is the Astro 5 way (the old
// `type: "content"` form is gone). Each post's id is its filename
// without extension, which becomes the URL slug.
//
// SEO note: on quil.cc a post is self-canonical (BaseHead derives the
// canonical from the path). The `canonicalOverride` field exists only
// for the rare case where a post was first published elsewhere and we
// want to point our copy at that origin — normally leave it unset, and
// when syndicating to Medium/Dev.to set THEIR canonical to our URL.

import { defineCollection, z } from "astro:content";
import { glob } from "astro/loaders";

const blog = defineCollection({
  loader: glob({ pattern: "**/[^_]*.md", base: "./src/content/blog" }),
  schema: z.object({
    /** Post title — the <h1> and, unless `seoTitle` overrides it, the
     *  <title> stem and og:title. */
    title: z.string(),
    /**
     * Optional <title> stem for search results, replacing `title`.
     * The " · Quil" suffix is still appended, so budget ~53 characters.
     *
     * The <h1> deliberately keeps `title`. These serve different readers:
     * the heading is read by someone already on the page, the <title> by
     * someone choosing between ten blue links. Google truncates the
     * latter near 60 characters — this post's editorial title ran to ~77
     * and was cut mid-phrase in the SERP.
     */
    seoTitle: z.string().optional(),
    /** 120–160 char meta description, unique per post. */
    description: z.string(),
    /** Publish date (ISO, e.g. 2026-06-14). Drives sort order + schema. */
    pubDate: z.coerce.date(),
    /** Optional last-updated date for the BlogPosting schema. */
    updatedDate: z.coerce.date().optional(),
    /** Per-post OG image under public/, e.g. /blog/img/foo.png. */
    ogImage: z.string().optional(),
    /** Target + secondary keywords (Bing/Yandex meta; Google ignores). */
    keywords: z.array(z.string()).default([]),
    /**
     * Optional FAQ. Each entry renders BOTH a visible <details> accordion
     * at the foot of the post AND a FAQPage JSON-LD block. Google only
     * honours FAQ structured data when the same text is visible on the
     * page, so both are driven from this one array — they can't drift.
     * Answers must be plain prose (no Markdown), grounded in the post body.
     */
    faq: z
      .array(z.object({ question: z.string(), answer: z.string() }))
      .default([]),
    /** Hide from listing + sitemap until ready. */
    draft: z.boolean().default(false),
    /** Rare: point our copy's canonical at an external origin. */
    canonicalOverride: z.string().optional(),
  }),
});

export const collections = { blog };
