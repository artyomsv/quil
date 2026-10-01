// /llms.txt — a plain-Markdown map of the site and the docs for LLM crawlers
// (llmstxt.org shape). Generated from the same data as the pages, so it
// cannot drift; check-dist rule 9 verifies every link in it.
import { getCollection } from "astro:content";
import { SITE, GITHUB_BLOB, canonical } from "@/data/seo";
import { useCases } from "@/data/usecases";
import { shelf } from "@/data/docs";
import { competitors, compareNav, type CompetitorInfo } from "@/data/competitors";

export async function GET() {
  const posts = (await getCollection("blog", ({ data }) => !data.draft))
    .sort((a, b) => b.data.pubDate.valueOf() - a.data.pubDate.valueOf());
  const home = canonical("/");
  const docLinks = (u: (typeof useCases)[number]) =>
    u.read.filter((r) => r.href.startsWith("http")).map((r) => `[${r.label}](${r.href})`).join(", ");

  const lines = [
    `# ${SITE.name}`,
    "",
    `> ${SITE.blurb}`,
    "",
    "Quil is open source (Apache-2.0) and ships as two binaries — `quil`, the terminal UI, and `quild`, the daemon that keeps panes alive — for Linux, macOS and native Windows. The documentation lives in the GitHub repository; this site shows a guided tour and routes to the docs.",
    "",
    "## Use cases",
    "",
    ...useCases.map((u) => `- [${u.title}](${home}#${u.id}): ${u.line} Docs: ${docLinks(u)}`),
    "",
    "## Install",
    "",
    `- [Install Quil](${home}#install): \`curl -sSfL https://raw.githubusercontent.com/artyomsv/quil/master/scripts/install.sh | sh\` on Linux and macOS, a zip on Windows, or \`go install\``,
    `- [Installation guide](${GITHUB_BLOB}docs/installation.md)`,
    "",
    "## Documentation",
    "",
    `- [Field manual](${canonical("/docs/")}): thirteen ways to use Quil, with the keys and the exact doc section for each`,
    ...shelf.map((s) => `- [${s.file}](${GITHUB_BLOB}${s.file}): ${s.what}`),
    "",
    "## Comparisons",
    "",
    ...compareNav.map(({ href }) => {
      const c = competitors[href.split("/")[2] as CompetitorInfo["slug"]];
      return `- [Quil vs ${c.name}](${canonical(href)}): ${c.description}`;
    }),
    "",
    "## Blog",
    "",
    ...posts.map((p) => `- [${p.data.title}](${canonical(`/blog/${p.id}/`)}): ${p.data.description}`),
    "",
    "## Optional",
    "",
    `- [Source code](${SITE.github})`,
    `- [Changelog](${GITHUB_BLOB}CHANGELOG.md)`,
    `- [Legal notice](${canonical("/legal/")})`,
    "",
  ];
  return new Response(lines.join("\n"), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
}
