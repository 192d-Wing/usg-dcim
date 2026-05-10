/// <reference types="vitest" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8000', changeOrigin: true },
    },
  },
  build: {
    // Surface > 600 KB chunks so we notice if a future feature reverses
    // the route-split work below.
    chunkSizeWarningLimit: 600,
    // No manualChunks. Earlier attempts to group react + react-dom + radix
    // into a single vendor chunk hit Rollup's intra-chunk evaluation order
    // bug where react-dom (or a Radix package importing react-dom internals)
    // ran before react finished initializing and threw
    //   "Cannot read properties of undefined
    //    (reading '__SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED')".
    // Letting Vite emit per-route chunks naturally avoids it; recharts and
    // refine-vendor end up in their own chunks via the dependency graph
    // because they're heavy and only some routes touch them.
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
    globals: false,
  },
});
