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
    // Surface > 600 KB chunks so we notice if a future feature reverses the
    // route-split work below.
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        // Pull the heaviest deps into named chunks. Anything that imports
        // from `react` / `react-dom` lives in the same chunk so there's
        // exactly one React module instance per page — splitting Radix
        // off from React triggered
        // "Cannot read properties of undefined (reading '__SECRET_INTERNALS_*')"
        // because Radix evaluated before React finished initializing.
        manualChunks: {
          'react-vendor': [
            'react',
            'react-dom',
            'react-router',
            'scheduler',
            '@radix-ui/react-dialog',
            '@radix-ui/react-dropdown-menu',
            '@radix-ui/react-label',
            '@radix-ui/react-popover',
            '@radix-ui/react-scroll-area',
            '@radix-ui/react-select',
            '@radix-ui/react-separator',
            '@radix-ui/react-slot',
            '@radix-ui/react-switch',
            '@radix-ui/react-tabs',
            '@radix-ui/react-toast',
            '@radix-ui/react-tooltip',
          ],
          'refine-vendor': [
            '@refinedev/core',
            '@refinedev/react-router',
            '@refinedev/simple-rest',
            '@refinedev/react-hook-form',
          ],
          recharts: ['recharts'],
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
