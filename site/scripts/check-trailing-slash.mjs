/**
 * Guard: every internal href must carry its trailing slash.
 *
 * The site is built with `trailingSlash: "always"` and
 * `build.format: "directory"`, `canonical()` appends the slash, the
 * sitemap emits slashed URLs, and GitHub Pages 301s the slashless form
 * to the slashed one. All of that agrees. The internal link graph did
 * not: every nav link, footer link and CTA named the slashless URL.
 *
 * That is not cosmetic, because a redirect is a hint and a link is a
 * vote. On a 14-page site the ~19 sitewide nav links are the
 * overwhelming majority of the canonicalization signal, and Google
 * weighs the whole graph against the declaration. Measured on
 * quil.cc in August 2026, it split the difference:
 *
 *   /vs/herdr   indexed and served separately from /vs/herdr/
 *   /blog       indexed and served separately from /blog/
 *
 * Both members of each pair ranked, for the same content, dividing the
 * signals between two URLs — while six OTHER URLs landed in Search
 * Console's "Page with redirect" bucket, which is the same phenomenon
 * with the declaration winning instead. Search Console reports that
 * bucket as `source: Website`, i.e. "your own markup keeps emitting
 * these", which is exactly what an unguarded href does on every build.
 *
 * Greping for `href="/install"` would not stay green on its own: the
 * hrefs live in four different syntactic forms across the codebase
 * (attribute, expression, template literal, and object literal inside a
 * nav data array), and the Footer's are the object-literal kind, which
 * an attribute-only sweep silently reports as clean.
 *
 * Run: node scripts/check-trailing-slash.mjs
 */
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve, relative, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const SRC = resolve(here, "..", "src");

/** Files whose hrefs reach the rendered page. */
const EXT = /\.(astro|ts|tsx|js|mjs|md)$/;

/**
 * Paths that are files rather than pages, and so never take a slash.
 * Matched on the last segment having an extension — /favicon.svg,
 * /og/home.png, /sitemap-index.xml, /site.webmanifest.
 *
 * The bound is 12 rather than a tidy 4 because "webmanifest" is eleven
 * characters; a shorter cap reports /site.webmanifest as a page that
 * forgot its slash, and "add a slash to the manifest link" is a fix
 * that breaks the manifest.
 */
const LOOKS_LIKE_FILE = /\.[a-z0-9]{2,12}$/i;

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else if (EXT.test(entry)) out.push(full);
  }
  return out;
}

/**
 * Extract every internal link target from a source file.
 *
 * Four forms, because the codebase uses all four and a sweep that
 * models fewer than all four reports a clean run over a real bug:
 *
 *   href="/install"                 attribute
 *   href={"/install"}               expression
 *   href={`/vs/${slug}`}            template literal
 *   { href: "/install", text: … }   object literal in a data array
 *
 * Template literals are normalised by replacing every `${…}` with a
 * placeholder segment, so `/vs/${slug}` is judged on its tail — which
 * is where the slash goes — rather than being skipped for being
 * dynamic. A dynamic tail (`href={`${base}`}`) is unreadable here and
 * is reported as unmodelled rather than passed.
 */
function extract(src) {
  const hits = [];
  const push = (raw, index) => hits.push({ raw, index });

  // href="…" | href='…' | href={"…"} | href={'…'}
  for (const m of src.matchAll(/href\s*=\s*\{?\s*["']([^"'`]*)["']\s*\}?/g)) {
    push(m[1], m.index);
  }
  // href={`…`}
  for (const m of src.matchAll(/href\s*=\s*\{\s*`([^`]*)`\s*\}/g)) {
    push(m[1], m.index);
  }
  // { href: "…" } — nav/footer data arrays
  for (const m of src.matchAll(/\bhref\s*:\s*["']([^"'`]*)["']/g)) {
    push(m[1], m.index);
  }
  // { href: `…` }
  for (const m of src.matchAll(/\bhref\s*:\s*`([^`]*)`/g)) {
    push(m[1], m.index);
  }
  return hits;
}

const failures = [];
const unmodelled = [];

for (const file of walk(SRC)) {
  const src = readFileSync(file, "utf8");
  const rel = relative(resolve(here, ".."), file).replace(/\\/g, "/");
  const lineOf = (i) => src.slice(0, i).split("\n").length;

  for (const { raw, index } of extract(src)) {
    // Interpolation is only a problem when it lands at the very end,
    // where the slash would be. Anywhere else it is just a path segment.
    const endsDynamic = /\$\{[^}]*\}$/.test(raw);
    const path = raw.replace(/\$\{[^}]*\}/g, "X");

    // External, protocol-relative, anchors, mailto:, and expressions
    // that are not literal paths at all.
    if (!path.startsWith("/")) continue;
    if (path.startsWith("//")) continue;

    // Split off a fragment: /features#remote-daemon-ssh still needs the
    // slash on the path part, and this is the form most likely to be
    // missed by eye.
    const [pathPart, fragment] = path.split("#");
    if (pathPart === "") continue; // bare "#anchor"
    if (pathPart === "/") continue; // home
    if (LOOKS_LIKE_FILE.test(pathPart)) continue;

    if (endsDynamic && !fragment) {
      unmodelled.push(
        `${rel}:${lineOf(index)}  href \`${raw}\` ends in an interpolation — ` +
          `cannot tell whether the value carries its slash`,
      );
      continue;
    }

    if (!pathPart.endsWith("/")) {
      const fixed = pathPart + "/" + (fragment ? "#" + fragment : "");
      failures.push(
        `${rel}:${lineOf(index)}  "${raw}" → should be "${fixed.replace(/X/g, "${…}")}"`,
      );
    }
  }
}

// Abstentions print alongside the verdict rather than silently passing:
// an href this cannot read is exactly where the next slashless link hides.
if (unmodelled.length) {
  console.warn("check-trailing-slash: cannot model these hrefs —\n");
  for (const u of unmodelled) console.warn(`  ? ${u}`);
  console.warn(
    "\n  Verify by hand that the interpolated value ends in a slash.\n",
  );
}

if (failures.length) {
  console.error(
    `check-trailing-slash: FAIL — ${failures.length} internal href(s) missing a trailing slash\n`,
  );
  for (const f of failures) console.error(`  - ${f}`);
  console.error(
    "\n  The site is trailingSlash: \"always\". A slashless internal href is a\n" +
      "  vote for a URL that 301s, which splits ranking signals across two\n" +
      "  URLs for the same page. Add the slash.\n",
  );
  process.exit(1);
}

console.log("check-trailing-slash: ok  (all internal hrefs carry a trailing slash)");
