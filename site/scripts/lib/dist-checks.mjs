/**
 * dist-checks — the rules a built quil.cc must satisfy, as pure functions.
 *
 * check-dist.mjs reads dist/ and the repository and hands plain data to
 * checkSite(); nothing in this file touches the file system. That split is
 * what lets dist-checks.test.mjs show every rule failing on the input it
 * exists for. Rules and their reasons: Part I of
 * docs/superpowers/specs/2026-10-01-site-redesign-design.md.
 */

export const ORIGIN = "https://quil.cc";
export const SITEMAP_URL = `${ORIGIN}/sitemap.xml`;
export const GITHUB_BLOB = "https://github.com/artyomsv/quil/blob/master/";
export const HOME_JS_BUDGET = 50 * 1024;
export const DESCRIPTION_RANGE = [50, 200];

/** "vs/tmux/index.html" → "/vs/tmux/"; "404.html" → "/404.html". */
export function urlForDistFile(rel) {
  const p = rel.replace(/\\/g, "/").replace(/^\/+/, "");
  if (p === "index.html") return "/";
  if (p.endsWith("/index.html")) return `/${p.slice(0, -"index.html".length)}`;
  return `/${p}`;
}

/**
 * A site-absolute href → the dist file it needs, its URL path and fragment.
 * Null for anything else: external, protocol-relative, mailto:, same-page "#x".
 */
export function internalTarget(href) {
  if (!href.startsWith("/") || href.startsWith("//")) return null;
  const hash = href.indexOf("#");
  const fragment = hash >= 0 ? href.slice(hash + 1) : null;
  const url = (hash >= 0 ? href.slice(0, hash) : href).split("?")[0];
  const file = url.endsWith("/") ? `${url.slice(1)}index.html` : url.slice(1);
  return { file, url, fragment };
}

/** The <loc>/<lastmod> pairs of a <urlset>. */
export function parseSitemap(xml) {
  const out = [];
  for (const m of xml.matchAll(/<url>([\s\S]*?)<\/url>/g)) {
    const loc = /<loc>([^<]*)<\/loc>/.exec(m[1])?.[1].trim();
    const lastmod = /<lastmod>([^<]*)<\/lastmod>/.exec(m[1])?.[1].trim();
    if (loc) out.push(lastmod ? { loc, lastmod } : { loc });
  }
  return out;
}

const decode = (s) =>
  s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

function attr(tag, name) {
  const m = new RegExp(`\\s${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)')`, "i").exec(tag);
  return m ? decode(m[1] ?? m[2]) : null;
}

/** Everything the rules read from one HTML document. */
export function readPage(html) {
  const metas = html.match(/<meta\b[^>]*>/gi) ?? [];
  const links = html.match(/<link\b[^>]*>/gi) ?? [];
  const meta = (key, value) => {
    const tag = metas.find((t) => (attr(t, key) ?? "").toLowerCase() === value);
    return tag ? attr(tag, "content") : null;
  };
  const link = (rel) => {
    const tag = links.find((t) => (attr(t, "rel") ?? "").toLowerCase() === rel);
    return tag ? attr(tag, "href") : null;
  };
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)];
  const isLd = (open) => /type\s*=\s*["']application\/ld\+json["']/i.test(open);
  const refresh = meta("http-equiv", "refresh");
  return {
    titles: [...html.matchAll(/<title>([\s\S]*?)<\/title>/gi)].map((m) => decode(m[1].trim())),
    description: meta("name", "description"),
    robots: meta("name", "robots"),
    ogImage: meta("property", "og:image"),
    canonical: link("canonical"),
    sitemapLink: link("sitemap"),
    refresh: refresh === null ? null : (/url\s*=\s*(\S+)/i.exec(refresh)?.[1] ?? ""),
    h1Count: (html.match(/<h1\b/gi) ?? []).length,
    jsonLd: scripts.filter((m) => isLd(m[1])).map((m) => m[2]),
    inlineScripts: scripts.filter((m) => !isLd(m[1]) && !/\ssrc\s*=/i.test(m[1])).length,
    scriptSrcs: scripts.map((m) => attr(`<x${m[1]}>`, "src")).filter((s) => s !== null),
    ids: new Set([...html.matchAll(/\sid\s*=\s*["']([^"']+)["']/gi)].map((m) => m[1])),
    hrefs: [...html.matchAll(/<a\b[^>]*?\shref\s*=\s*["']([^"']*)["']/gi)].map((m) => decode(m[1])),
  };
}

/** GitHub's anchor for a Markdown heading: lowercase; keep letters, digits, spaces, "-" and "_"; spaces become "-". */
export function githubSlug(heading) {
  return heading
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .replace(/\s/g, "-");
}

/** Every anchor GitHub generates for a Markdown file, plus explicit <a id|name>. */
export function markdownAnchors(md) {
  const out = new Set();
  const seen = new Map();
  let fence = false;
  for (const line of md.split(/\r?\n/)) {
    if (/^\s*(```|~~~)/.test(line)) { fence = !fence; continue; }
    if (fence) continue;
    const m = /^#{1,6}\s+(.*?)\s*#*\s*$/.exec(line);
    if (!m) continue;
    const base = githubSlug(m[1]);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    out.add(n ? `${base}-${n}` : base);
  }
  for (const m of md.matchAll(/<a\s+(?:id|name)\s*=\s*["']([^"']+)["']/gi)) out.add(m[1]);
  return out;
}

/**
 * Check one link found on page `from`. Shared by the page rules and the
 * llms.txt rule, so a link means the same thing wherever it appears.
 */
export function checkHref(site, from, href, fail) {
  const { pages, files } = site;
  const page = pages.get(from);
  if (href === "" || href === "#") { fail(`${from}: empty link href="${href}"`); return; }
  if (href.startsWith("#")) {
    if (page && !page.ids.has(href.slice(1))) fail(`${from}: link ${href} has no target id on the page`);
    return;
  }
  const t = internalTarget(href);
  if (t) {
    if (!files.has(t.file)) { fail(`${from}: broken link ${href}`); return; }
    const target = pages.get(t.url);
    if (target && target.refresh !== null) fail(`${from}: links to the redirect stub ${href}; link to ${target.refresh} instead`);
    else if (target && t.fragment && !target.ids.has(t.fragment)) fail(`${from}: link ${href} has no id "${t.fragment}" on ${t.url}`);
    return;
  }
  if (href.startsWith(GITHUB_BLOB)) {
    const rest = href.slice(GITHUB_BLOB.length);
    const hash = rest.indexOf("#");
    const path = decodeURIComponent(hash >= 0 ? rest.slice(0, hash) : rest);
    const fragment = hash >= 0 ? rest.slice(hash + 1) : "";
    const text = site.repoFile(path);
    if (text === null) fail(`${from}: GitHub link to a file not in the repository: ${path}`);
    else if (fragment && path.endsWith(".md") && !markdownAnchors(text).has(fragment)) {
      fail(`${from}: GitHub link to a heading that does not exist: ${path}#${fragment}`);
    }
  }
}

/** All rules. Returns one message per violation; empty when the site passes. */
export function checkSite(site) {
  const errors = [];
  const fail = (msg) => errors.push(msg);
  const { pages, files } = site;
  const isStub = (p) => p.refresh !== null;
  const want = new Set(
    [...pages].filter(([url, p]) => url !== "/404.html" && !isStub(p)).map(([url]) => ORIGIN + url),
  );

  // Rule 1 — sitemap.xml lists exactly the built pages.
  if (site.sitemapXml === null) fail("dist/sitemap.xml is missing");
  else {
    const entries = parseSitemap(site.sitemapXml);
    const listed = new Set(entries.map((e) => e.loc));
    for (const loc of want) if (!listed.has(loc)) fail(`sitemap.xml does not list ${loc}`);
    for (const e of entries) {
      if (!want.has(e.loc)) fail(`sitemap.xml lists ${e.loc}, which is not a built page`);
      if (!e.loc.startsWith(`${ORIGIN}/`) || !e.loc.endsWith("/")) fail(`sitemap <loc> must be ${ORIGIN}/…/ : ${e.loc}`);
      if (e.lastmod === undefined) {
        if (site.ci) fail(`sitemap entry has no <lastmod> in CI: ${e.loc}`);
        continue;
      }
      const t = Date.parse(e.lastmod);
      if (Number.isNaN(t)) fail(`sitemap <lastmod> is not a date: ${e.loc} ${e.lastmod}`);
      else if (t > site.now + 86_400_000) fail(`sitemap <lastmod> is in the future: ${e.loc} ${e.lastmod}`);
    }
  }

  // Rule 4, robots half.
  if (site.robotsTxt === null) fail("dist/robots.txt is missing");
  else if (!site.robotsTxt.split(/\r?\n/).some((l) => l.trim() === `Sitemap: ${SITEMAP_URL}`)) {
    fail(`robots.txt must say "Sitemap: ${SITEMAP_URL}"`);
  }
  // GitHub Pages reads the custom domain from dist/CNAME; without it the
  // deploy silently falls back to artyomsv.github.io.
  if ((site.cname ?? "").trim() !== "quil.cc") fail("dist/CNAME must contain quil.cc");

  for (const [url, p] of pages) {
    if (isStub(p)) {
      // Rule 3 — redirect stubs.
      const t = internalTarget(p.refresh);
      if (!t || !files.has(t.file)) fail(`${url}: redirect target is not a built file: ${p.refresh}`);
      if (!p.canonical || !want.has(p.canonical)) fail(`${url}: redirect canonical must be a listed page, is ${p.canonical ?? "missing"}`);
    } else {
      // Rule 2 — the head of every page, the 404 included.
      if (p.titles.length !== 1) fail(`${url}: has ${p.titles.length} <title> elements, want 1`);
      if (p.h1Count !== 1) fail(`${url}: has ${p.h1Count} <h1> elements, want 1`);
      const d = (p.description ?? "").length;
      const [lo, hi] = DESCRIPTION_RANGE;
      if (d < lo || d > hi) fail(`${url}: meta description is ${d} characters, want ${lo}–${hi}`);
      const noindex = /noindex/i.test(p.robots ?? "");
      if (url === "/404.html") {
        if (!noindex) fail(`${url}: must carry <meta name="robots" content="noindex">`);
        if (p.canonical) fail(`${url}: must not carry a canonical`);
      } else {
        if (noindex) fail(`${url}: carries noindex`);
        if (p.canonical !== ORIGIN + url) fail(`${url}: canonical is ${p.canonical ?? "missing"}, want ${ORIGIN + url}`);
      }
      if (!p.ogImage) fail(`${url}: has no og:image`);
      else if (p.ogImage.startsWith(`${ORIGIN}/`)) {
        if (!files.has(p.ogImage.slice(ORIGIN.length + 1))) fail(`${url}: og:image is not in dist: ${p.ogImage}`);
      } else if (!p.ogImage.startsWith("https://")) fail(`${url}: og:image must be an absolute https URL: ${p.ogImage}`);
      // Rule 4, page half.
      if (p.sitemapLink !== "/sitemap.xml") fail(`${url}: <link rel="sitemap"> is ${p.sitemapLink ?? "missing"}, want /sitemap.xml`);
      // Rule 7.
      p.jsonLd.forEach((block, i) => {
        try { JSON.parse(block); } catch (e) { fail(`${url}: JSON-LD block ${i + 1} does not parse (${e.message})`); }
      });
      if (p.inlineScripts > 0) fail(`${url}: has ${p.inlineScripts} inline <script>; the CSP (script-src 'self') blocks it`);
    }
    // Rules 5 and 6 — every link resolves.
    for (const href of p.hrefs) checkHref(site, url, href, fail);
  }

  // Rule 9 — llms.txt exists and every link in it resolves like a page link.
  if (site.llmsTxt === null || site.llmsTxt === undefined) fail("dist/llms.txt is missing");
  else {
    for (const m of site.llmsTxt.matchAll(/\]\((\S+?)\)/g)) {
      const href = m[1].startsWith(`${ORIGIN}/`) ? m[1].slice(ORIGIN.length) : m[1];
      checkHref(site, "/llms.txt", href, fail);
    }
  }

  // Rule 8 — the home page's JavaScript budget.
  if (site.homeJsGzipBytes > HOME_JS_BUDGET) {
    fail(`/ loads ${(site.homeJsGzipBytes / 1024).toFixed(1)} KB of JavaScript (gzip), budget ${HOME_JS_BUDGET / 1024} KB`);
  }
  return errors;
}
