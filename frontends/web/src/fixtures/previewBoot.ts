// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: on a dev fixture preview's page (`/forge-…-preview`,
 * src/fixtures/preview.tsx), the runtime config names a control plane when
 * the dev stack's does not (reliant's own `forge env up dev` runs without
 * one). The console reads control-plane data only when one is configured
 * (services/controlPlane/config.ts, read once at module scope), and a
 * preview's control plane is the fixture transport: so the URL is the page's
 * own origin, as in the public demo (config.demo.ts), and nothing dials it.
 *
 * A bare import in main.tsx, before lib/configGuard, so it runs before any
 * module reads the config. Production builds drop its body
 * (`import.meta.env.DEV`), and a page that is not a preview is untouched.
 */

const PREVIEW_PATH = /^\/forge-[a-z-]+-preview\/?$/;

if (import.meta.env.DEV && typeof window !== "undefined" && PREVIEW_PATH.test(window.location.pathname)) {
  const config = (window.__FORGE_CONFIG__ ??= {});
  if (!config.CONTROL_PLANE_API_URL) config.CONTROL_PLANE_API_URL = window.location.origin;
}

export {};
