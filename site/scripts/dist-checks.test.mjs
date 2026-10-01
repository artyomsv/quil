// Unit tests for scripts/lib/dist-checks.mjs. Run from site/: node --test scripts/dist-checks.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  ORIGIN, urlForDistFile, internalTarget, parseSitemap, readPage,
  githubSlug, markdownAnchors, checkSite,
} from "./lib/dist-checks.mjs";

function html({
  title = "Quil — a test page",
  description = "A description that is long enough to pass the fifty character floor.",
  canonical = null, robots = null, h1 = 1,
  og = `${ORIGIN}/og/home.png`, sitemap = "/sitemap.xml", head = "", body = "",
} = {}) {
  return [
    "<!doctype html><html><head>",
    `<title>${title}</title>`,
    `<meta name="description" content="${description}">`,
    canonical ? `<link rel="canonical" href="${canonical}">` : "",
    robots ? `<meta name="robots" content="${robots}">` : "",
    og ? `<meta property="og:image" content="${og}">` : "",
    sitemap ? `<link rel="sitemap" href="${sitemap}">` : "",
    head,
    "</head><body>",
    "<h1>Heading</h1>".repeat(h1),
    body,
    "</body></html>",
  ].join("");
}

const stub = (to) => readPage(`<title>moved</title><link rel="canonical" href="${ORIGIN}/"><meta http-equiv="refresh" content="0; url=${to}">`);

/** A small site that passes every rule. Each test breaks exactly one thing. */
function goodSite() {
  return {
    pages: new Map([
      ["/", readPage(html({ canonical: `${ORIGIN}/`, body: '<p id="install"></p><a href="/docs/#agents">docs</a><a href="#install">install</a><script type="module" src="/_astro/home.js"></script>' }))],
      ["/docs/", readPage(html({ canonical: `${ORIGIN}/docs/`, body: '<article id="agents"></article><a href="https://github.com/artyomsv/quil/blob/master/docs/mcp.md#the-36-tools">mcp</a>' }))],
      ["/404.html", readPage(html({ robots: "noindex", body: '<a href="/">home</a>' }))],
    ]),
    files: new Set(["index.html", "docs/index.html", "404.html", "og/home.png", "sitemap.xml", "robots.txt", "_astro/home.js"]),
    sitemapXml: `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>${ORIGIN}/</loc><lastmod>2026-09-30T10:00:00+02:00</lastmod></url><url><loc>${ORIGIN}/docs/</loc><lastmod>2026-09-29T08:00:00Z</lastmod></url></urlset>`,
    robotsTxt: `User-agent: *\nAllow: /\n\nSitemap: ${ORIGIN}/sitemap.xml\n`,
    cname: "quil.cc\n",
    llmsTxt: `# Quil\n\n> Summary.\n\n## Docs\n\n- [Docs](${ORIGIN}/docs/#agents): the field manual\n- [MCP](https://github.com/artyomsv/quil/blob/master/docs/mcp.md#the-36-tools): tools\n`,
    repoFile: (path) => (path === "docs/mcp.md" ? "# MCP\n\n## The 36 tools\n" : null),
    homeJsGzipBytes: 10_000,
    ci: true,
    now: Date.parse("2026-10-01T00:00:00Z"),
  };
}

const errorsOf = (mutate) => { const s = goodSite(); mutate(s); return checkSite(s); };
function expectError(mutate, re) {
  const errors = errorsOf(mutate);
  assert.ok(errors.some((e) => re.test(e)), `expected ${re}, got:\n${errors.join("\n") || "(no errors)"}`);
}
const setDocs = (s, opts) => s.pages.set("/docs/", readPage(html({ canonical: `${ORIGIN}/docs/`, ...opts })));

test("urlForDistFile", () => {
  assert.equal(urlForDistFile("index.html"), "/");
  assert.equal(urlForDistFile("vs/tmux/index.html"), "/vs/tmux/");
  assert.equal(urlForDistFile("404.html"), "/404.html");
  assert.equal(urlForDistFile("docs\\index.html"), "/docs/");
});

test("internalTarget", () => {
  assert.deepEqual(internalTarget("/docs/#agents"), { file: "docs/index.html", url: "/docs/", fragment: "agents" });
  assert.deepEqual(internalTarget("/"), { file: "index.html", url: "/", fragment: null });
  assert.deepEqual(internalTarget("/rss.xml"), { file: "rss.xml", url: "/rss.xml", fragment: null });
  assert.equal(internalTarget("https://quil.cc/"), null);
  assert.equal(internalTarget("//cdn.example/x"), null);
  assert.equal(internalTarget("#faq"), null);
});

test("parseSitemap reads loc and an optional lastmod", () => {
  assert.deepEqual(
    parseSitemap("<urlset><url><loc> https://quil.cc/ </loc></url><url><loc>https://quil.cc/a/</loc><lastmod>2026-01-02</lastmod></url></urlset>"),
    [{ loc: "https://quil.cc/" }, { loc: "https://quil.cc/a/", lastmod: "2026-01-02" }],
  );
});

test("readPage finds a meta refresh target", () => {
  assert.equal(stub("/#install").refresh, "/#install");
  assert.equal(readPage(html()).refresh, null);
});

test("readPage counts inline scripts but not JSON-LD or src scripts", () => {
  const p = readPage('<script type="application/ld+json">{}</script><script type="module" src="/a.js"></script><script type="module">alert(1)</script>');
  assert.equal(p.inlineScripts, 1);
  assert.deepEqual(p.scriptSrcs, ["/a.js"]);
  assert.equal(p.jsonLd.length, 1);
});

test("readPage ends a script at </script followed by whitespace, as browsers do", () => {
  const tabNewline = String.fromCharCode(9, 10);
  const p = readPage(`<script type="application/ld+json">{"a":1}</script ><script type="module">alert(1)</script${tabNewline} x>`);
  assert.deepEqual(p.jsonLd, ['{"a":1}']);
  assert.equal(p.inlineScripts, 1);
});

test("githubSlug follows GitHub's heading anchors", () => {
  assert.equal(githubSlug("The 36 tools"), "the-36-tools");
  assert.equal(githubSlug("Client/daemon version handshake"), "clientdaemon-version-handshake");
  assert.equal(githubSlug("Input history (AI panes)"), "input-history-ai-panes");
  assert.equal(githubSlug("Rearranging a tab's panes"), "rearranging-a-tabs-panes");
  assert.equal(githubSlug("`quil mcp` server"), "quil-mcp-server");
});

test("markdownAnchors numbers duplicates and skips fenced code", () => {
  const a = markdownAnchors("# Intro\n\n## Setup\n\n```sh\n# not a heading\n```\n\n## Setup\n");
  assert.ok(a.has("intro") && a.has("setup") && a.has("setup-1"));
  assert.ok(!a.has("not-a-heading"));
});

test("a good site passes", () => assert.deepEqual(checkSite(goodSite()), []));

test("rule 1: a page missing from the sitemap", () =>
  expectError((s) => { s.sitemapXml = s.sitemapXml.replace(/<url><loc>https:\/\/quil\.cc\/docs\/<\/loc>.*?<\/url>/, ""); }, /does not list https:\/\/quil\.cc\/docs\//));
test("rule 1: the sitemap lists a URL that is not a page", () =>
  expectError((s) => { s.sitemapXml = s.sitemapXml.replace("</urlset>", `<url><loc>${ORIGIN}/gone/</loc></url></urlset>`); }, /lists https:\/\/quil\.cc\/gone\/, which is not a built page/));
test("rule 1: a redirect stub must not be listed", () =>
  expectError((s) => {
    s.pages.set("/install/", stub("/#install"));
    s.files.add("install/index.html");
    s.sitemapXml = s.sitemapXml.replace("</urlset>", `<url><loc>${ORIGIN}/install/</loc><lastmod>2026-09-01</lastmod></url></urlset>`);
  }, /lists https:\/\/quil\.cc\/install\/, which is not a built page/));
test("rule 1: lastmod is required in CI", () =>
  expectError((s) => { s.sitemapXml = s.sitemapXml.replace(/<lastmod>[^<]*<\/lastmod>/, ""); }, /no <lastmod> in CI/));
test("rule 1: lastmod is optional outside CI", () =>
  assert.deepEqual(errorsOf((s) => { s.ci = false; s.sitemapXml = s.sitemapXml.replace(/<lastmod>[^<]*<\/lastmod>/, ""); }), []));
test("rule 1: lastmod in the future", () =>
  expectError((s) => { s.sitemapXml = s.sitemapXml.replace("2026-09-29T08:00:00Z", "2027-01-01T00:00:00Z"); }, /in the future/));
test("rule 2: description too long", () =>
  expectError((s) => setDocs(s, { description: "x".repeat(201) }), /meta description is 201 characters/));
test("rule 2: two h1", () => expectError((s) => setDocs(s, { h1: 2 }), /2 <h1> elements/));
test("rule 2: wrong canonical", () =>
  expectError((s) => s.pages.set("/docs/", readPage(html({ canonical: `${ORIGIN}/docs` }))), /canonical is https:\/\/quil\.cc\/docs, want/));
test("rule 2: 404 without noindex", () => expectError((s) => s.pages.set("/404.html", readPage(html())), /404\.html: must carry/));
test("rule 2: og:image missing from dist", () => expectError((s) => s.files.delete("og/home.png"), /og:image is not in dist/));
test("rule 3: a stub's target must exist", () =>
  expectError((s) => { s.pages.set("/old/", stub("/nowhere/")); s.files.add("old/index.html"); }, /redirect target is not a built file/));
test("rule 4: robots.txt names the old sitemap", () =>
  expectError((s) => { s.robotsTxt = "User-agent: *\nSitemap: https://quil.cc/sitemap-index.xml\n"; }, /robots\.txt must say/));
test("rule 4: dist/CNAME must name quil.cc (GitHub Pages reads the custom domain from it)", () =>
  expectError((s) => { s.cname = null; }, /dist\/CNAME must contain quil\.cc/));
test("rule 4: a page links the old sitemap", () =>
  expectError((s) => setDocs(s, { sitemap: "/sitemap-index.xml" }), /rel="sitemap"> is \/sitemap-index\.xml/));
test("rule 5: broken internal link", () => expectError((s) => setDocs(s, { body: '<a href="/nope/">x</a>' }), /broken link \/nope\//));
test("rule 5: missing fragment on another page", () => expectError((s) => setDocs(s, { body: '<a href="/#faq">x</a>' }), /no id "faq" on \//));
test("rule 5: missing same-page fragment", () =>
  expectError((s) => s.pages.set("/", readPage(html({ canonical: `${ORIGIN}/`, body: '<a href="#nowhere">x</a>' }))), /#nowhere has no target id/));
test("rule 5: a link to a redirect stub", () =>
  expectError((s) => {
    s.pages.set("/install/", stub("/#install"));
    s.files.add("install/index.html");
    setDocs(s, { body: '<a href="/install/">x</a>' });
  }, /links to the redirect stub \/install\//));
test("rule 5: an empty link", () => expectError((s) => setDocs(s, { body: '<a href="#">x</a>' }), /empty link/));
test("rule 6: GitHub link to a missing file", () => expectError((s) => { s.repoFile = () => null; }, /not in the repository: docs\/mcp\.md/));
test("rule 6: GitHub link to a missing heading", () =>
  expectError((s) => { s.repoFile = () => "# MCP\n\n## Tools\n"; }, /heading that does not exist: docs\/mcp\.md#the-36-tools/));
test("rule 7: an inline script", () =>
  expectError((s) => setDocs(s, { body: '<script type="module">document.body.dataset.x=1</script>' }), /inline <script>/));
test("rule 7: JSON-LD that does not parse", () =>
  expectError((s) => setDocs(s, { head: '<script type="application/ld+json">{"@type":}</script>' }), /JSON-LD block 1 does not parse/));
test("rule 8: home JavaScript over budget", () => expectError((s) => { s.homeJsGzipBytes = 51 * 1024; }, /budget 50 KB/));
test("rule 9: llms.txt is required", () => expectError((s) => { s.llmsTxt = null; }, /dist\/llms\.txt is missing/));
test("rule 9: a broken link in llms.txt", () =>
  expectError((s) => { s.llmsTxt += `- [Gone](${ORIGIN}/gone/): no\n`; }, /\/llms\.txt: broken link \/gone\//));
