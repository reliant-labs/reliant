#!/usr/bin/env node
// Single generator for electron/src/build-config.js, shared by the macOS,
// Linux, and Windows release jobs. Previously this was three copy-pasted
// bash blocks (release.yml lines 510, 709, 894) that could and did drift —
// one is enough, invoked identically from every platform job.
//
// Reads its values from process.env (populated by the calling workflow step
// from .github/release-config.env + GitHub secrets), JSON-encodes them, and
// writes a CommonJS module. Fails the build if RELIANT_SERVER_URL is unset
// or targets localhost — see validate-endpoint.mjs for the stronger
// resolvable/reachable check run separately in CI.

import { writeFileSync } from "node:fs";

const config = {
  STATSIG_CLIENT_KEY: process.env.STATSIG_CLIENT_KEY || "",
  SENTRY_DSN: process.env.SENTRY_DSN || "",
  SENTRY_ENABLED: "true",
  SENTRY_TRACES_SAMPLE_RATE: "0.1",
  SUPABASE_URL: process.env.SUPABASE_URL || "",
  SUPABASE_ANON_KEY: process.env.SUPABASE_ANON_KEY || "",
  RELIANT_SERVER_URL: process.env.RELIANT_SERVER_URL || "",
  RELIANT_API_URL: process.env.RELIANT_API_URL || "",
  RELIANT_GATEWAY_URL: process.env.RELIANT_GATEWAY_URL || "",
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
      "Set RELIANT_SERVER_URL in .github/release-config.env.",
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
      "ELECTRON_PROD_CONFIG_BRIEFING.md). Set RELIANT_GATEWAY_URL explicitly in .github/release-config.env.",
  );
  process.exit(1);
}

console.log(`build-config.js RELIANT_SERVER_URL = ${url}`);
console.log(`build-config.js RELIANT_GATEWAY_URL = ${config.RELIANT_GATEWAY_URL}`);
