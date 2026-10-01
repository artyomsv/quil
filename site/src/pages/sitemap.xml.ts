// /sitemap.xml — one flat <urlset> (spec Part A).
//
// It replaces @astrojs/sitemap's sitemap-index.xml + sitemap-0.xml. Search
// Console holds both old URLs as "pending" and never fetched them in 108
// days; Google keeps a backoff per sitemap URL, so a NEW URL is what gets a
// fresh fetch. No changefreq/priority: Google ignores both.
import { getCollection } from "astro:content";
import { canonical } from "@/data/seo";
import { staticRoutes, type Route } from "@/data/routes";
import { lastModified } from "@/lib/lastmod";

const xmlEscape = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

export async function GET() {
  const posts = await getCollection("blog", ({ data }) => !data.draft);
  const postFile = (p: (typeof posts)[number]) => p.filePath ?? `src/content/blog/${p.id}.md`;
  const routes: Route[] = staticRoutes.map((r) =>
    r.path === "/blog/" ? { ...r, sources: [...r.sources, ...posts.map(postFile)] } : r,
  );
  for (const p of posts) routes.push({ path: `/blog/${p.id}/`, sources: [postFile(p)] });

  const urls = routes.map((r) => {
    const lastmod = lastModified(r.sources);
    return `  <url><loc>${xmlEscape(canonical(r.path))}</loc>${lastmod ? `<lastmod>${lastmod}</lastmod>` : ""}</url>`;
  });
  const xml = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls.join("\n")}\n</urlset>\n`;
  return new Response(xml, { headers: { "Content-Type": "application/xml; charset=utf-8" } });
}
