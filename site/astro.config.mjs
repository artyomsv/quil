// @ts-check
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";

// Quil marketing site — astro.config.mjs
//
// SEO is a hard requirement, so:
//   - site URL is set so canonicals + OG URLs + sitemap absolute
//     paths are correct
//   - prefetch is enabled so internal links feel instant
//   - sitemap integration auto-generates sitemap-index.xml at build
//   - no client-side hydration islands by default — every page is
//     pure HTML, zero runtime JS (except Astro's 2 kB prefetch stub)
//
// Deploys to GitHub Pages via .github/workflows/site.yml on every
// push that touches site/** or the workflow itself. The public/
// directory carries the CNAME file which GitHub Pages reads to
// serve the content at quil.cc.

export default defineConfig({
  site: "https://quil.cc",
  trailingSlash: "never",
  prefetch: {
    prefetchAll: true,
    defaultStrategy: "viewport",
  },
  integrations: [
    sitemap({
      changefreq: "weekly",
      priority: 0.7,
      lastmod: new Date(),
      filter: (page) => !page.includes("/404"),
    }),
  ],
  vite: {
    // @ts-expect-error — Astro bundles its own Vite; @tailwindcss/vite
    // peer dep version skew confuses tsc here. Runtime is fine.
    plugins: [tailwindcss()],
  },
  build: {
    inlineStylesheets: "auto",
    format: "directory",
  },
  markdown: {
    shikiConfig: {
      theme: "github-dark-dimmed",
      wrap: true,
    },
  },
});
