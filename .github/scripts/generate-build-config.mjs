#!/usr/bin/env node
// Single generator for electron/src/build-config.js, shared by the macOS,
// Linux, and Windows release jobs. Previously this was three copy-pasted
// bash blocks (release.yml lines 510, 709, 894) that could and did drift —
// one is enough, invoked identically from every platform job.
//
// ENDPOINTS come from electron/release.config.json — the file
// .github/scripts/sync-release-config.mjs projects out of control-plane's KCL
// (deploy/kcl/lib/env.k, reliant_desktop_release_config). That is the SINGLE
// declaration of these hostnames; .github/release-config.env was a second
// hand-maintained copy and is gone.
//
// SECRETS still come from process.env (GitHub Actions secrets): the Sentry DSN
// and the Statsig client key are not in a public repo and not in KCL.
//
// Fails the build if an endpoint is unset or targets localhost — see
// validate-endpoint.mjs for the stronger resolvable/reachable check run
// separately in CI.

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const RELEASE_CONFIG_PATH = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "electron",
  "release.config.json",
);

let release;
try {
  release = JSON.parse(readFileSync(RELEASE_CONFIG_PATH, "utf8"));
} catch (e) {
  console.error(
    `Cannot read ${RELEASE_CONFIG_PATH}: ${e.message}\n` +
      "It is generated from control-plane's KCL. Regenerate with:\n" +
      "  node .github/scripts/sync-release-config.mjs --env prod",
  );
  process.exit(1);
}

const endpoints = release.main || {};

const config = {
  STATSIG_CLIENT_KEY: process.env.STATSIG_CLIENT_KEY || "",
  SENTRY_DSN: process.env.SENTRY_DSN || "",
  SENTRY_ENABLED: "true",
  SENTRY_TRACES_SAMPLE_RATE: "0.1",
  SUPABASE_URL: endpoints.SUPABASE_URL || "",
  SUPABASE_ANON_KEY: endpoints.SUPABASE_ANON_KEY || "",
  RELIANT_SERVER_URL: endpoints.RELIANT_SERVER_URL || "",
  RELIANT_API_URL: endpoints.RELIANT_API_URL || "",
  RELIANT_GATEWAY_URL: endpoints.RELIANT_GATEWAY_URL || "",
  // The control-plane (admin-server) origin. The preload injects this as
  // window.RELIANT_CONFIG.controlPlaneURL, which the renderer's
  // getControlPlaneURL() prefers over the baked VITE_ value — so a packaged
  // app can reach the coupon/billing endpoints even though the renderer
  // bundle was built separately. Absent entirely before this change, which is
  // half of why coupons threw "Control plane API URL not configured".
  RELIANT_CONTROL_PLANE_URL: endpoints.RELIANT_CONTROL_PLANE_URL || "",
};

const lines = Object.entries(config)
  .map(([k, v]) => "  " + JSON.stringify(k) + ": " + JSON.stringify(v) + ",")
  .join("\n");

writeFileSync("src/build-config.js", `module.exports = {\n${lines}\n};\n`);

// Fail the build rather than ship an app that points at nothing.
// backend-manager.js falls back to http://localhost:8080 when
// RELIANT_SERVER_URL is unset — correct for an OSS build run from source,
// useless in a packaged app on a user's machine, and silent either way.
const url = config.RELIANT_SERVER_URL;
if (!url) {
  console.error(
    "build-config.js has no RELIANT_SERVER_URL — the packaged app would aim the daemon at localhost:8080. " +
      "Fix it in control-plane deploy/kcl/lib/env.k (reliant_desktop_release_config).",
  );
  process.exit(1);
}
if (/localhost|127\.0\.0\.1/.test(url)) {
  console.error(`build-config.js RELIANT_SERVER_URL is ${url} — a packaged build must not target localhost.`);
  process.exit(1);
}
if (!config.RELIANT_GATEWAY_URL) {
  console.error(
    "build-config.js has no RELIANT_GATEWAY_URL. The daemon gateway host cannot be safely derived from " +
      "RELIANT_SERVER_URL for every environment (prod's `api.` host derives `gateway-api.<domain>`, which " +
      "does not exist — see cmd/reliant/commands/connection.go deriveGatewayURL and " +
      "ELECTRON_PROD_CONFIG_BRIEFING.md). Fix it in control-plane deploy/kcl/lib/env.k.",
  );
  process.exit(1);
}

// Same guard for the control-plane origin. Shipping this empty is not a
// degraded build — it is the coupon screen throwing "Control plane API URL not
// configured" on a user's machine, which is exactly what v1.7.5 did.
if (!config.RELIANT_CONTROL_PLANE_URL) {
  console.error(
    "build-config.js has no RELIANT_CONTROL_PLANE_URL. The packaged app would have no admin-server " +
      "origin, and billing/coupon actions would throw 'Control plane API URL not configured'. " +
      "Fix it in control-plane deploy/kcl/lib/env.k (reliant_desktop_release_config).",
  );
  process.exit(1);
}
if (/localhost|127\.0\.0\.1/.test(config.RELIANT_CONTROL_PLANE_URL)) {
  console.error(
    `build-config.js RELIANT_CONTROL_PLANE_URL is ${config.RELIANT_CONTROL_PLANE_URL} — ` +
      "a packaged build must not target localhost.",
  );
  process.exit(1);
}

console.log(`build-config.js RELIANT_SERVER_URL = ${url}`);
console.log(`build-config.js RELIANT_GATEWAY_URL = ${config.RELIANT_GATEWAY_URL}`);
console.log(`build-config.js RELIANT_CONTROL_PLANE_URL = ${config.RELIANT_CONTROL_PLANE_URL}`);
