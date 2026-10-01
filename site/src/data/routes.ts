// Every page the sitemap lists, with the files that hold its words.
// A page's <lastmod> is the newest commit over `sources` (src/lib/lastmod.ts),
// so `sources` names CONTENT — the page and its data file — not shared chrome.
// Blog posts are added from the content collection by src/pages/sitemap.xml.ts.
//
// scripts/check-dist.mjs fails the build when this list and the built pages
// disagree, so a new page cannot be forgotten here.
import { competitors } from "./competitors";

export interface Route {
  /** Path with leading and trailing slash, e.g. "/vs/tmux/". */
  path: string;
  /** Files or directories, relative to site/, whose last commit dates the page. */
  sources: string[];
}

const vs: Route[] = (Object.keys(competitors) as (keyof typeof competitors)[]).map((slug) => ({
  path: `/vs/${slug}/`,
  sources: [`src/pages/vs/${slug}.astro`, "src/data/competitors.ts"],
}));

export const staticRoutes: Route[] = [
  {
    path: "/",
    sources: [
      "src/pages/index.astro", "src/components/home", "src/tour", "src/data/usecases.ts", "src/data/faq.ts",
      "src/data/features.ts", "src/data/plugins.ts", "src/data/competitors.ts",
    ],
  },
  { path: "/docs/", sources: ["src/pages/docs.astro", "src/data/docs.ts", "src/components/docs"] },
  { path: "/blog/", sources: ["src/pages/blog/index.astro"] },
  { path: "/legal/", sources: ["src/pages/legal.astro"] },
  ...vs,
];
