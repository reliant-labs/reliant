// The packaged desktop renderer's release document (electron/release.config.json
// `renderer`), judged as control-plane's render judges the hosted SPA's
// runtime_config: against the web frontend's own generated key schema. Its
// own module so the check is testable without running sync-release-config.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// The web frontend's generated key schema (forge generate, from
// proto/reliant/config/v1/web_config.proto).
const WEB_CONFIG_SCHEMA_PATH = join(REPO_ROOT, "frontends", "web", "src", "lib", "config_schema_gen.json");

// The desktop app is the managed product: the release renderer must say so,
// or it boots as the self-hosted app with no control plane.
const RENDERER_MUST = { AUTH_MODE: "cloud" };

// Endpoints a packaged build must never aim at the local machine.
const RENDERER_ENDPOINTS = ["API_URL", "CONTROL_PLANE_API_URL", "GATEWAY_URL", "SUPABASE_URL", "APP_URL"];

/**
 * Why a `renderer` block cannot work as the packaged config.js, or [].
 *
 * @param {Record<string, unknown>} renderer
 * @param {{ keys: Array<{name: string, required?: boolean, required_when?: Array<{key: string, equals: string}>, default?: string}> }} [schema]
 */
export function rendererProblems(renderer, schema = JSON.parse(readFileSync(WEB_CONFIG_SCHEMA_PATH, "utf8"))) {
  const problems = [];
  const known = new Set(schema.keys.map((k) => k.name));
  const value = (key) => (typeof renderer[key] === "string" ? renderer[key].trim() : "");
  for (const key of Object.keys(renderer)) {
    if (!known.has(key)) {
      problems.push(`renderer.${key} is not a key the web bundle reads (frontends/web/src/lib/config_schema_gen.json)`);
    }
    if (typeof renderer[key] !== "string") problems.push(`renderer.${key} must be a string`);
  }
  for (const [key, want] of Object.entries(RENDERER_MUST)) {
    if (value(key) !== want) problems.push(`renderer.${key} is "${value(key)}"; the desktop release is the managed app and must set "${want}"`);
  }
  for (const k of schema.keys) {
    if (k.required && !value(k.name)) problems.push(`renderer.${k.name} is empty — the web schema requires it`);
    for (const c of k.required_when || []) {
      if (value(c.key) !== c.equals) continue;
      if (!value(k.name) || value(k.name) === k.default) {
        problems.push(`renderer.${k.name} is empty — required when ${c.key} is "${c.equals}"`);
      }
      break;
    }
  }
  for (const key of RENDERER_ENDPOINTS) {
    if (/localhost|127\.0\.0\.1/.test(value(key))) {
      problems.push(`renderer.${key} is ${value(key)} — a packaged build must not target localhost`);
    }
  }
  return problems;
}
