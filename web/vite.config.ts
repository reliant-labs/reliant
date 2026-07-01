/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

// Get worktree name from parent directory
// When Vite runs, cwd is the 'web' directory, so we need to go up one level
function getWorktreeName(): string {
  try {
    const cwd = process.cwd();
    const parentDir = path.dirname(cwd);
    return path.basename(parentDir);
  } catch (error) {
    console.warn('[Vite] Failed to get worktree name:', error);
    return 'dev';
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // Load .env files from project root instead of web directory
  envDir: path.resolve(__dirname, ".."),
  // Define global constants available in the app
  define: {
    __WORKTREE_NAME__: JSON.stringify(getWorktreeName()),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  base: "./", // Use relative paths for assets
  build: {
    // Optimize build output
    rollupOptions: {
      output: {
        // Create manual chunks for better code splitting
        manualChunks: {
          // Core vendor libraries
          'vendor-react': ['react', 'react-dom'],
          // UI libraries
          'vendor-ui': ['lucide-react', 'clsx', 'tailwind-merge'],
          // Heavy dependencies
          'vendor-markdown': ['react-markdown', 'remark-gfm', 'rehype-highlight'],
          'vendor-highlight': ['highlight.js'],
          // Monaco Editor - separate chunk for lazy loading
          'vendor-monaco': ['@monaco-editor/react', '@monaco-editor/loader'],
          // Date and form libraries
          'vendor-utils': ['date-fns', 'react-hook-form', '@hookform/resolvers'],
          // State management
          'vendor-state': ['zustand', '@tanstack/react-query'],
          // Router
          'vendor-router': ['@tanstack/react-router'],
        },
      },
    },
    // Increase chunk size warning limit
    chunkSizeWarningLimit: 1000,
    // Minification options for better performance
    minify: 'terser',
    terserOptions: {
      compress: {
        drop_console: true, // Remove console logs in production
        drop_debugger: true,
        pure_funcs: ['console.log', 'console.info', 'console.debug', 'console.trace'],
      },
    },
    // Generate source maps for production debugging
    sourcemap: true,
    // Asset inlining threshold
    assetsInlineLimit: 4096, // 4kb
  },
  // Optimize dependencies
  optimizeDeps: {
    include: [
      'react',
      'react-dom',
      'react-markdown',
      '@tanstack/react-query',
      '@tanstack/react-router',
      'zustand',
      '@monaco-editor/react',
      '@monaco-editor/loader',
    ],
    // Exclude Monaco Editor's web workers from optimization
    // They need to be loaded dynamically as blob URLs
    exclude: ['monaco-editor'],
  },
  server: {
    host: "127.0.0.1",
    port: parseInt(process.env.FRONTEND_PORT || "5173"),
    strictPort: true,
    proxy: {
      // Same-origin RPC routing. admin-web's Connect transport uses
      // http://localhost:<vite-port> as its baseUrl, so EVERY RPC is
      // first-party from the browser's POV — no CORS, no per-port
      // bookkeeping. Vite fans out by proto service package:
      //
      //   /controlplane.v1.* → admin-server   (cp-forge backend)
      //   /reliant.v1.*      → reliant-api    (this repo's backend)
      //
      // The two backends are HTTP/h2c Connect; ws:false avoids Vite
      // upgrading the connection unnecessarily. Targets come from the
      // env vars cloud-dev.sh exports (VITE_CONTROL_PLANE_API_URL,
      // VITE_API_URL), with sensible standalone-dev defaults.
      // NOTE: /auth/github/callback is intentionally NOT proxied — the app owns
      // it as an SPA route (GitHubOAuthCallback) so dev behaves exactly like
      // prod Firebase, which can't proxy the callback to the GKE backend. The
      // route exchanges the code via the ExchangeGithubOAuthCode RPC, which
      // rides the /controlplane.v1. proxy below. /auth/github/authorize is a
      // top-level browser navigation (full-page redirect to GitHub), not an XHR
      // through Vite, so it needs no proxy entry either.
      "/controlplane.v1.": {
        target: process.env.VITE_CONTROL_PLANE_API_URL || "http://127.0.0.1:8090",
        changeOrigin: true,
        ws: false,
      },
      "/reliant.v1.": {
        target: process.env.VITE_API_URL || "http://127.0.0.1:3090",
        changeOrigin: true,
        ws: false,
      },
    },
  },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    exclude: [
      "**/node_modules/**",
      "**/dist/**",
      "**/e2e/**", // Exclude Playwright e2e tests
      "**/.{idea,git,cache,output,temp}/**",
      "**/{karma,rollup,webpack,vite,vitest,jest,ava,babel,nyc,cypress,tsup,build}.config.*",
    ],
  },
});