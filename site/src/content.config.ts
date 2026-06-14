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
    /** Post title — also the <h1>, the <title> stem, and og:title. */
    title: z.string(),
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
    /** Hide from listing + sitemap until ready. */
    draft: z.boolean().default(false),
    /** Rare: point our copy's canonical at an external origin. */
    canonicalOverride: z.string().optional(),
  }),
});

export const collections = { blog };
