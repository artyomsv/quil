# Site: plugins.ts `features` and `config` arrays are never rendered

| Field | Value |
|-------|-------|
| Criticality | Low |
| Complexity | Trivial |
| Location | `site/src/data/plugins.ts`, `site/src/pages/plugins.astro:79` |
| Found during | Updating site copy for the Claude resume-session detail panel |
| Date | 2026-07-26 |

## Issue

`site/src/data/plugins.ts` is imported by exactly one file, `site/src/pages/plugins.astro`, whose card template reads only four fields:

```astro
<span class="feat-title">{plugin.name.toLowerCase()}</span>
<p class="feat-body">{plugin.description}</p>
<span class="feat-tag">{tagByKind[plugin.kind]}{plugin.beta && …}</span>
```

Every plugin entry also carries a `features: string[]` (typically 5-8 sentences of detailed copy) and a `config` TOML sample. Neither reaches the DOM — verified by building the site and grepping `dist/plugins/index.html`, which contains none of the `features` strings.

The copy is maintained as though it ships: entries have been extended several times, most recently for the resume picker. It reads as user-facing site content in review, but the only rendered per-plugin prose is the one-paragraph `description`.

## Risks

No user-visible defect — the risk is wasted effort and false confidence. Someone updating site copy for a feature (as happened here) reasonably edits `features`, believes the site is updated, and ships nothing. The detailed prose is also good SEO content that is currently earning nothing.

`site/src/data/features.ts` has the same shape (`detail: string[]`) and IS rendered on `/features`, which makes the asymmetry easy to miss.

## Suggested Solutions

1. **Render it** — the most likely original intent. Add an expandable detail list to each plugin card, mirroring how `/features` renders `feature.detail`. Also surfaces the `config` TOML sample, which is genuinely useful on a plugins page.
2. **Delete both fields** if the one-paragraph card is the deliberate design, and move the detail to `docs/plugin-reference.md` (which already covers most of it). Removes the trap.
3. Whichever is chosen, do it for all entries at once — a half-rendered data file recreates the same ambiguity.
