import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

import { devLogPlugin } from "./src/lib/devlog-plugin";

// The dev server's port. `forge env up dev` sets PORT to the port it
// allocated for this site; a bare `npm run dev` falls back to Vite's own.
const port = Number(process.env.PORT) || 5173;

// Hosts besides localhost that may load the dev server: a Reliant workspace
// preview host (RELIANT_PREVIEW_URL_TEMPLATE, `https://{port}-<id>...`) and
// any FORGE_DEV_ALLOWED_ORIGINS (comma-separated; a leading "." allows every
// subdomain). Vite answers anything else with "Blocked request".
const devAllowedHosts = (): string[] => {
  const out = new Set<string>();
  const preview = process.env.RELIANT_PREVIEW_URL_TEMPLATE;
  if (preview) {
    try {
      out.add(new URL(preview.replace("{port}", String(port))).hostname);
    } catch {
      // A malformed template allows nothing extra.
    }
  }
  for (const host of (process.env.FORGE_DEV_ALLOWED_ORIGINS ?? "").split(",")) {
    if (host.trim()) out.add(host.trim());
  }
  return [...out];
};

export default defineConfig({
  // devLogPlugin is dev-only: it receives the browser console lines that
  // installDevLogging() posts from src/main.tsx and prints them to this dev
  // server's stdout, which `forge env up` tees to
  // .forge/logs/<env>/frontend_<name>.log. Delete both to opt out.
  plugins: [react(), tailwindcss(), devLogPlugin()],
  build: {
    // The static export hosted static hosting serves. deploy/kcl/<env>/main.k
    // names this directory as the frontend's public_dir; move both together.
    outDir: "dist",
    // forge asks for source maps when IT builds for a deploy
    // (FORGE_SOURCEMAPS=1) and moves them out of the served tree, so they
    // symbolicate errors without being public.
    sourcemap: process.env.FORGE_SOURCEMAPS === "1",
  },
  server: {
    port,
    strictPort: Boolean(process.env.PORT),
    host: true,
    allowedHosts: devAllowedHosts(),
  },
});
