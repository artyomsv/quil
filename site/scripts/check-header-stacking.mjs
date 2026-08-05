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
 * Does this selector match `el`? true / false / null = cannot tell.
 *
 * Decided on the selector's SUBJECT — its last compound — because that
 * is the element a rule actually styles. `.site-header details.nav-dd`
 * has subject `details.nav-dd`, so it styles a descendant and can never
 * apply to the header however much of the header's name it contains.
 * Judging the whole selector string instead flags every descendant rule
 * in the sheet as unreadable, burying the one that matters.
 *
 * `null` means "ask a human" — the subject matches, but an ancestor
 * combinator or state qualifier decides the rest and neither can be read
 * from a stylesheet alone. resolve_ escalates those rather than skipping
 * them: a selector this cannot read is exactly where the next regression
 * hides, so quietly ignoring `body > main { z-index: 99 }` would hand
 * back a green build over a dead dropdown.
 */
function matchesSelector(sel, el) {
  // Pseudo-elements style a generated box, never the element itself.
  if (/::|:(?:before|after|first-line|first-letter)\b/.test(sel)) return false;

  const compounds = sel.split(/[\s>+~]+/).filter(Boolean);
  const subject = compounds[compounds.length - 1] ?? "";
  const hasAncestorContext = compounds.length > 1;

  // Strip state qualifiers to read the structural part of the subject.
  const bare = subject
    .replace(/:[-\w]+(\([^)]*\))?/g, "")
    .replace(/\[[^\]]*\]/g, "");
  const tag = bare.match(/^[a-zA-Z][\w-]*/)?.[0] ?? null;
  const classes = [...bare.matchAll(/\.([-\w]+)/g)].map((m) => m[1]);
  const hasId = /#[-\w]+/.test(bare);

  if (tag && tag !== el.tag) return false;
  if (!classes.every((c) => el.classes.includes(c))) return false;
  if (!tag && !classes.length && !hasId) return false; // `*` and friends

  // The subject matches. Anything beyond a plain compound is context this
  // cannot evaluate, so abstain instead of guessing either way.
  return hasAncestorContext || /[:[#]/.test(subject) ? null : true;
}

/** (id, class, tag) specificity of a plain compound selector. */
function specificity(sel) {
  return [
    (sel.match(/#[-\w]+/g) || []).length,
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

/**
 * Resolve the winning declaration of `prop` for `el` across all sheets.
 *
 * Returns { winner, unmodelled }. `unmodelled` is every rule that could
 * plausibly affect the answer but which this cannot evaluate — a
 * selector needing a live DOM, or a declaration inside a conditional
 * group. Those are NOT folded into the cascade: treating a
 * `@media (max-width: 820px)` rule as unconditional would report a
 * failure that no browser ever sees, and ignoring it would green-light
 * a viewport where the dropdown is genuinely dead. Neither is a verdict
 * this can honestly reach, so it hands them to a human instead.
 */
function resolve_(rules, el, prop) {
  let winner = null;
  const unmodelled = [];
  rules.forEach((rule, order) => {
    const decls = declarations(rule.body).filter((d) => d.prop === prop);
    if (!decls.length) return;

    for (const sel of rule.prelude.split(",")) {
      const s = sel.trim();
      if (!s) continue;

      // null only ever means "the subject matches but the context is
      // unreadable", so every one of them is a genuine candidate — no
      // relevance filter is needed to keep the report from drowning.
      const verdict = matchesSelector(s, el);
      if (verdict === null) {
        unmodelled.push({
          selector: s,
          at: rule.at,
          prop,
          why: "selector needs a live DOM to evaluate",
        });
        continue;
      }
      if (verdict !== true) continue;

      if (rule.at.length) {
        unmodelled.push({
          selector: s,
          at: rule.at,
          prop,
          why: "declared inside a conditional group",
        });
        continue;
      }

      for (const d of decls) {
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
  return { winner, unmodelled };
}

const rules = CSS_FILES.flatMap((f) =>
  parseRules(readFileSync(resolve(here, "..", f), "utf8")),
);

const header = { tag: "header", classes: ["site-header"] };
const main = { tag: "main", classes: [] };

const failures = [];
const describe = (w) =>
  w ? `${w.value}  (from \`${w.selector}\`${w.at.length ? ` inside ${w.at.join(" / ")}` : ""})` : "<unset>";

const resolved = [
  resolve_(rules, header, "position"),
  resolve_(rules, header, "z-index"),
  resolve_(rules, main, "z-index"),
];
const [{ winner: headerPos }, { winner: headerZ }, { winner: mainZ }] = resolved;

// Abstentions are reported BEFORE the verdict, because a verdict reached
// while ignoring a rule that might overturn it is not a verdict.
const unmodelled = resolved.flatMap((r) => r.unmodelled);
if (unmodelled.length) {
  failures.push(
    `this guard cannot model ${unmodelled.length} rule(s) that may affect the answer:\n` +
      unmodelled
        .map(
          (u) =>
            `      \`${u.selector}\` { ${u.prop} }` +
            `${u.at.length ? ` inside ${u.at.join(" / ")}` : ""} — ${u.why}`,
        )
        .join("\n") +
      `\n    Verify by hand that the header still out-stacks main, then either teach\n` +
      `    this script that construct or move the declaration out of it.`,
  );
}

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
