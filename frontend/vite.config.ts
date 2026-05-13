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
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        // Conservative vendor split. The earlier failed attempt grouped
        // react + react-dom + Radix into a single vendor chunk and tripped
        // Rollup's intra-chunk evaluation-order bug — react-dom (or a
        // Radix package) ran before react finished initializing and threw
        //   "Cannot read properties of undefined
        //    (reading '__SECRET_INTERNALS_DO_NOT_USE_OR_YOU_WILL_BE_FIRED')".
        // The packages below live strictly above the public React API
        // and do NOT reach into react-dom internals, so co-locating
        // their files in one chunk is safe. React itself stays unsplit.
        manualChunks(id) {
          if (id.includes('node_modules/@cloudscape-design/')) {
            return 'cloudscape';
          }
          if (id.includes('node_modules/@refinedev/')) {
            return 'refine';
          }
          if (id.includes('node_modules/@tanstack/react-query')) {
            return 'react-query';
          }
          return undefined;
        },
      },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.{ts,tsx}'],
    globals: false,
  },
});
