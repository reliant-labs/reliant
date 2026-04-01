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
      "/api": {
        // Use HTTPS when TLS is enabled (default), HTTP when DISABLE_TLS=true
        target: process.env.DISABLE_TLS === 'true' 
          ? `http://127.0.0.1:${process.env.BACKEND_PORT || "8080"}`
          : `https://127.0.0.1:${process.env.BACKEND_PORT || "8080"}`,
        changeOrigin: true,
        ws: true,
        secure: false, // Accept self-signed certificates
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
