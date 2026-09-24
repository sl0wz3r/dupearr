import { fileURLToPath, URL } from 'node:url';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// Backend used by `npm run dev` (override with DUPEARR_URL=http://host:port npm run dev).
const backend = process.env.DUPEARR_URL ?? 'http://localhost:3873';

export default defineConfig({
  // Relative base: the Go server injects <base href="{urlBase}/"> so the same build works under any UrlBase.
  base: './',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 700,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              // Framework code changes rarely: keep it in its own long-cacheable chunk.
              name: 'vendor-react',
              test: /node_modules[\\/](react|react-dom|scheduler|react-router|@tanstack)[\\/]/,
            },
          ],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/ping': { target: backend, changeOrigin: true },
      '/initialize.json': { target: backend, changeOrigin: true },
      '/login': {
        target: backend,
        changeOrigin: true,
        // GET /login is the SPA route; only the POST (credentials) goes to the backend.
        bypass: (req) => (req.method === 'GET' ? '/index.html' : undefined),
      },
      '/logout': { target: backend, changeOrigin: true },
      '/backup': { target: backend, changeOrigin: true },
    },
  },
  preview: {
    port: 4173,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
    restoreMocks: true,
  },
});
