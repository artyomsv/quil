// RSS feed for /blog.
//
// Endpoint rather than a page: this returns XML, so it bypasses the
// Page layout entirely and Astro serves it at /rss.xml verbatim.
//
// Worth having for a one-post blog because the consumers that matter
// here are not human subscribers — they are the readers and answer
// engines that look for a feed to discover new posts, and a feed that
// only starts existing once there is "enough" content misses the
// discovery window for every post published before it.

import rss from "@astrojs/rss";
import { getCollection } from "astro:content";
import { SITE, canonical } from "@/data/seo";

export async function GET(context: { site?: URL }) {
  const posts = await getCollection("blog", ({ data }) => !data.draft);

  // Newest first. `pubDate` is a Date (coerced by the collection schema),
  // so this is a numeric sort, not a lexical one on ISO strings.
  posts.sort((a, b) => b.data.pubDate.valueOf() - a.data.pubDate.valueOf());

  return rss({
    title: `${SITE.name} — blog`,
    description: SITE.tagline,
    // context.site comes from `site` in astro.config.mjs; SITE.url is the
    // same origin and keeps this working if the endpoint is ever rendered
    // without that context.
    site: context.site?.toString() ?? SITE.url,
    items: posts.map((post) => ({
      title: post.data.title,
      description: post.data.description,
      pubDate: post.data.pubDate,
      // Absolute, trailing-slashed, and identical to the canonical the
      // page itself declares — a feed pointing at the slashless form
      // would reintroduce the exact duplicate-URL split that
      // scripts/check-trailing-slash.mjs exists to prevent.
      link: canonical(`/blog/${post.id}/`),
      categories: post.data.keywords,
    })),
    customData: `<language>${SITE.htmlLang}</language>`,
  });
}
