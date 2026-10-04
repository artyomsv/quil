import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The build lands inside the Go package so go:embed picks it up.
// emptyOutDir stays false: the committed .keep must survive every build;
// scripts/dev.sh clears index.html and assets/ itself.
export default defineConfig({
  plugins: [svelte()],
  base: './',
  build: {
    outDir: '../internal/webgw/dist',
    emptyOutDir: false,
    assetsDir: 'assets',
    sourcemap: false,
  },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
});
