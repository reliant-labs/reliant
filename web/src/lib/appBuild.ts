import type { BuildInfo } from "./releaseIdentity";

/**
 * This bundle's own build info, as vite-plugin-version-json.ts baked it in.
 *
 * Null where nothing was baked — vitest runs the app without the plugin — so
 * callers say "unknown" rather than throwing on an undefined global.
 */
export function appBuild(): BuildInfo | null {
  return typeof __RELIANT_BUILD__ === "undefined" ? null : __RELIANT_BUILD__;
}
