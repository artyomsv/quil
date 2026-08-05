/**
 * Guard: the sticky header must out-stack <main>.
 *
 * The compare dropdown is a `position: absolute` menu inside the header
 * that hangs BELOW the header's border box, into the region `<main>`
 * occupies. Hit-testing follows paint order, so if `<main>` paints at or
 * above the header, `<main>` swallows every click aimed at the menu —
 * while the menu stays perfectly VISIBLE, because `<main>` is transparent
 * there. Visible-but-unclickable is the exact symptom this guards.
 *
 * That shipped once: a blanket
 *
 *     main, header.site-header, footer.site-footer { position: relative; z-index: 3 }
 *
 * matched the header at specificity (0,1,1) and beat the header's own
 * `.site-header { position: sticky; z-index: 40 }` at (0,1,0) — regardless
 * of source order. The header silently lost BOTH its stickiness and its
 * stacking rank, tying with `main`, which wins a tie by coming later in
 * the DOM.
 *
 * Greping for the selector would not have caught it: nothing is
 * misspelled, and every individual rule reads as correct. The bug only
 * exists in the RESOLUTION between two rules, so this resolves them.
 *
 * Run: node scripts/check-header-stacking.mjs
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const CSS_FILES = ["src/styles/global.css", "src/styles/ember.css"];

/** Split a stylesheet into flat {prelude, body, at} rules, descending into @media/@supports/@layer.
 *
 * Comments are stripped FIRST. A comment sitting above a rule otherwise
 * glues onto that rule's first selector, and a selector carrying a
 * comment matches nothing — silently dropping the leading selector of
 * every documented rule, which is most of them here. */
function parseRules(css, at = []) {
  css = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const out = [];
  let depth = 0;
  let selStart = 0;
  let blockStart = 0;

  for (let i = 0; i < css.length; i++) {
    const c = css[i];
    if (c === "{") {
      if (depth === 0) blockStart = i;
      depth++;
    } else if (c === "}") {
      depth--;
      if (depth === 0) {
        const prelude = css.slice(selStart, blockStart).trim();
        const body = css.slice(blockStart + 1, i);
        if (prelude.startsWith("@")) {
          // Conditional groups still contain rules that match the element.
          if (/^@(media|supports|layer|container)\b/.test(prelude)) {
            out.push(...parseRules(body, [...at, prelude]));
          }
        } else if (prelude) {
          out.push({ prelude, body, at });
        }
        selStart = i + 1;
      }
    }
  }
  return out;
}

/**
 * Does this single selector match `el`?
 *
 * Deliberately narrow: only compound selectors built from a tag and/or
 * classes (no combinators, pseudos, attributes or ids). Anything more
 * complex is CONTEXT-dependent, so this cannot decide it from the
 * stylesheet alone — and a guard that guesses is worse than one that
 * abstains. Such selectors are reported separately rather than ignored.
 */
function matchesCompound(sel, el) {
  if (/[\s>+~]/.test(sel)) return null; // combinator — needs a DOM
  if (/[:[#]/.test(sel)) return null; // pseudo/attr/id — needs state
  const tag = sel.match(/^[a-zA-Z][\w-]*/)?.[0] ?? null;
  if (tag && tag !== el.tag) return false;
  const classes = [...sel.matchAll(/\.([-\w]+)/g)].map((m) => m[1]);
  if (!classes.length && !tag) return false;
  return classes.every((c) => el.classes.includes(c));
}

/** (id, class, tag) specificity of a compound selector. */
function specificity(sel) {
  return [
    0,
    (sel.match(/\.[-\w]+/g) || []).length,
    /^[a-zA-Z][\w-]*/.test(sel) ? 1 : 0,
  ];
}

const cmp = (a, b) => a[0] - b[0] || a[1] - b[1] || a[2] - b[2];

/** Top-level `prop: value` pairs (skips nested blocks). */
function declarations(body) {
  const decls = [];
  let depth = 0;
  let buf = "";
  for (const c of body) {
    if (c === "(") depth++;
    else if (c === ")") depth--;
    if (c === ";" && depth === 0) {
      decls.push(buf);
      buf = "";
    } else buf += c;
  }
  decls.push(buf);

  return decls
    .map((d) => {
      const i = d.indexOf(":");
      if (i < 0) return null;
      const prop = d.slice(0, i).trim().toLowerCase();
      let value = d.slice(i + 1).trim();
      const important = /!important$/i.test(value);
      if (important) value = value.replace(/!important$/i, "").trim();
      return prop && value ? { prop, value, important } : null;
    })
    .filter(Boolean);
}

/** Resolve the winning declaration of `prop` for `el` across all sheets. */
function resolve_(rules, el, prop) {
  let winner = null;
  rules.forEach((rule, order) => {
    for (const sel of rule.prelude.split(",")) {
      const s = sel.trim();
      if (!s) continue;
      if (matchesCompound(s, el) !== true) continue;
      for (const d of declarations(rule.body)) {
        if (d.prop !== prop) continue;
        const cand = {
          value: d.value,
          important: d.important,
          spec: specificity(s),
          order,
          selector: s,
          at: rule.at,
        };
        // !important first, then specificity, then source order. Written
        // out longhand: `cmp(...) || order` reads naturally and is wrong,
        // because cmp returns -1 for a LESS specific candidate and -1 is
        // truthy, so every later rule would win whatever its specificity.
        winner = (() => {
          if (!winner) return cand;
          if (cand.important !== winner.important) {
            return cand.important ? cand : winner;
          }
          const bySpec = cmp(cand.spec, winner.spec);
          if (bySpec !== 0) return bySpec > 0 ? cand : winner;
          return cand.order >= winner.order ? cand : winner;
        })();
      }
    }
  });
  return winner;
}

const rules = CSS_FILES.flatMap((f) =>
  parseRules(readFileSync(resolve(here, "..", f), "utf8")),
);

const header = { tag: "header", classes: ["site-header"] };
const main = { tag: "main", classes: [] };

const failures = [];
const describe = (w) =>
  w ? `${w.value}  (from \`${w.selector}\`${w.at.length ? ` inside ${w.at.join(" / ")}` : ""})` : "<unset>";

const headerPos = resolve_(rules, header, "position");
const headerZ = resolve_(rules, header, "z-index");
const mainZ = resolve_(rules, main, "z-index");

if (headerPos?.value !== "sticky") {
  failures.push(
    `header position resolves to ${describe(headerPos)} — expected \`sticky\`.\n` +
      `    A non-sticky header also means its own .site-header block lost the cascade.`,
  );
}

const hz = Number(headerZ?.value);
const mz = Number(mainZ?.value);
if (!Number.isFinite(hz) || !Number.isFinite(mz) || hz <= mz) {
  failures.push(
    `header z-index (${describe(headerZ)}) must be strictly greater than main z-index (${describe(mainZ)}).\n` +
      `    On a tie, <main> paints last and swallows clicks on the compare dropdown.`,
  );
}

if (failures.length) {
  console.error("check-header-stacking: FAIL\n");
  for (const f of failures) console.error(`  - ${f}\n`);
  process.exit(1);
}

console.log(
  `check-header-stacking: ok  (header ${headerPos.value}, z-index ${hz} > main z-index ${mz})`,
);
