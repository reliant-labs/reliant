import os from "node:os";

import type { NextConfig } from "next";

// Hosts besides localhost that may load `next dev`: a Reliant workspace
// preview host (RELIANT_PREVIEW_URL_TEMPLATE), this machine's own addresses,
// and any FORGE_DEV_ALLOWED_ORIGINS (comma-separated).
const devOrigins = (): string[] => {
  const out = new Set<string>();
  const preview = process.env.RELIANT_PREVIEW_URL_TEMPLATE;
  if (preview) {
    try {
      out.add(new URL(preview.replace("{port}", process.env.PORT || "3000")).hostname);
    } catch {
      // A malformed template allows nothing extra.
    }
  }
  for (const addrs of Object.values(os.networkInterfaces())) {
    for (const addr of addrs ?? []) {
      if (!addr.internal && addr.family === "IPv4") out.add(addr.address);
    }
  }
  for (const host of (process.env.FORGE_DEV_ALLOWED_ORIGINS ?? "").split(",")) {
    if (host.trim()) out.add(host.trim());
  }
  return [...out];
};

const nextConfig: NextConfig = {
  // Production: a static export into out/ — plain HTML, JS and CSS that
  // hosted static hosting (or a bucket, or a CDN) serves with no Node runtime.
  // deploy/kcl/<env>/main.k names out/ as the frontend's public_dir.
  //
  // An export can only contain pages it can enumerate at build time, so a
  // dynamic segment (`[slug]`) needs generateStaticParams(), and server
  // actions, middleware, `cookies()`/`headers()` and a GET route handler that
  // answers dynamically are unavailable. `forge lint --static-export` names
  // each one with its file:line.
  //
  // Development keeps the full `next dev` server; the conditional is what
  // lets the dev-only log route (src/app/%5F_forge/log) run there.
  ...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),
  // forge asks for browser source maps when IT builds for a deploy
  // (FORGE_SOURCEMAPS=1) and moves them out of the exported site.
  productionBrowserSourceMaps: process.env.FORGE_SOURCEMAPS === "1",
  // next/image's default loader is an optimizing server route, which an
  // export does not have. Size images for the web before they go in public/.
  images: { unoptimized: true },
  ...(process.env.NODE_ENV === "development" ? { allowedDevOrigins: devOrigins() } : {}),
};

export default nextConfig;
