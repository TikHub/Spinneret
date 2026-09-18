import { fileURLToPath, URL } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The Go server (SPINNERET_HTTP_ADDR, default :8080) serves the Connect APIs,
// the SSE stream and health endpoints; the dev server proxies them.
const apiTarget = process.env.SPINNERET_API_URL ?? 'http://localhost:8080';

// Libraries needed to boot any page share one long-lived "vendor" chunk; Monaco and
// ECharts get their own lazily loaded chunks; other dependencies (tables, dates, ...)
// are split by Rollup next to the pages that use them.
const VENDOR_PACKAGES = [
  /^react(-dom)?$/,
  /^scheduler$/,
  /^@tanstack\/(react-router|router-core|history|react-query|query-core|store|react-store)$/,
  /^@connectrpc\//,
  /^@bufbuild\/protobuf$/,
  /^(react-)?i18next$/,
  /^@radix-ui\//,
  /^@floating-ui\//,
  /^(react-remove-scroll|react-remove-scroll-bar|react-style-singleton|use-callback-ref|use-sidecar|aria-hidden|get-nonce|tslib|detect-node-es)$/,
  /^(sonner|tailwind-merge|clsx|class-variance-authority|lucide-react)$/,
];

function packageName(id: string): string | undefined {
  const match = /node_modules\/(?:\.pnpm\/[^/]+\/node_modules\/)?((?:@[^/]+\/)?[^/]+)/.exec(id);
  return match?.[1];
}

function chunkFor(id: string): string | undefined {
  // Build helpers must live in the eager vendor chunk; otherwise Rollup may put them
  // into a lazy chunk (e.g. monaco) that the entry then has to load up front.
  if (
    id.includes('vite/preload-helper') ||
    id.includes('commonjsHelpers') ||
    id.includes('vite/modulepreload-polyfill')
  ) {
    return 'vendor';
  }
  const pkg = packageName(id);
  if (!pkg) return undefined;
  if (pkg === 'monaco-editor' || pkg === '@monaco-editor/react' || pkg === '@monaco-editor/loader')
    return 'monaco';
  if (pkg === 'echarts' || pkg === 'zrender') return 'echarts';
  if (VENDOR_PACKAGES.some((re) => re.test(pkg))) return 'vendor';
  return undefined;
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '^/spinneret\\.v1\\..*': { target: apiTarget, changeOrigin: false },
      '/api': { target: apiTarget, changeOrigin: false, ws: false },
      '/healthz': { target: apiTarget },
      '/readyz': { target: apiTarget },
    },
  },
  worker: {
    format: 'es',
  },
  build: {
    target: 'es2022',
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 4096,
    rollupOptions: {
      output: {
        manualChunks: chunkFor,
      },
    },
  },
});
