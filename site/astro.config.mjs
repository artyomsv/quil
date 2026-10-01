// @ts-check
import { defineConfig } from "astro/config";

// Quil marketing site — astro.config.mjs
//
// The sitemap is NOT made here any more: src/pages/sitemap.xml.ts builds one
// flat /sitemap.xml from src/data/routes.ts (spec Part A explains the move).
//
// Deploys to GitHub Pages via .github/workflows/site.yml; public/CNAME makes
// GitHub Pages serve it at quil.cc.
export default defineConfig({
  site: "https://quil.cc",
  // GitHub Pages serves dist/x/index.html at /x/ and 301s /x to /x/, so the
  // slashed form is the only one that answers 200 — canonicals, sitemap and
  // every internal href use it (scripts/check-trailing-slash.mjs).
  trailingSlash: "always",
  prefetch: { prefetchAll: true, defaultStrategy: "viewport" },
  vite: {
    build: {
      // Never inline a script. Astro inlines an import-free script smaller
      // than this limit as <script type="module">…</script>, and the CSP in
      // BaseHead.astro (script-src 'self') blocks inline scripts — that is
      // why the platform tabs on /install/ did nothing in production.
      // Returning undefined keeps Astro's default (4 KB) for CSS.
      assetsInlineLimit: (file) => (file.endsWith(".js") ? false : undefined),
    },
  },
  build: { inlineStylesheets: "auto", format: "directory" },
  markdown: { shikiConfig: { theme: "github-dark-dimmed", wrap: true } },
});
