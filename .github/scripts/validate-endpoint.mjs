#!/usr/bin/env node
// Fails a release build rather than shipping it pointed at nothing, at
// localhost, or at a dead host — the api.reliant.so incident was a hostname
// that was never resolvable, baked in silently. Checks, in order:
//   1. required + non-empty
//   2. not localhost/127.0.0.1
//   3. DNS resolves
//   4. reachable over HTTPS (any HTTP status counts; network/timeout does not)
//
// Usage: node validate-endpoint.mjs <ENV_VAR_NAME> <url> [--required]

import dns from "node:dns/promises";

const [name, url, ...rest] = process.argv.slice(2);
const required = rest.includes("--required");

if (!name) {
  console.error("usage: validate-endpoint.mjs <NAME> <url> [--required]");
  process.exit(1);
}

if (!url) {
  if (required) {
    console.error(
      `${name} is empty — a released build must not ship without it. ` +
        `Fix it in control-plane deploy/kcl/lib/env.k (reliant_desktop_release_config), ` +
        `then regenerate: node .github/scripts/sync-release-config.mjs --env prod`,
    );
    process.exit(1);
  }
  console.log(`${name} is empty (not required) — skipping.`);
  process.exit(0);
}

if (/localhost|127\.0\.0\.1/.test(url)) {
  console.error(`${name} is ${url} — a released build must not target localhost.`);
  process.exit(1);
}

let host;
try {
  host = new URL(url).hostname;
} catch (e) {
  console.error(`${name} is ${url} — not a valid URL: ${e.message}`);
  process.exit(1);
}

try {
  await dns.lookup(host);
} catch (e) {
  console.error(
    `${name} host "${host}" does not resolve (${e.code || e.message}). ` +
      `This is exactly the api.reliant.so dead-domain bug this check exists to catch.`,
  );
  process.exit(1);
}

try {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 8000);
  const res = await fetch(url, { method: "GET", signal: controller.signal });
  clearTimeout(timeout);
  console.log(`${name} = ${url} — resolves and is reachable (HTTP ${res.status}).`);
} catch (e) {
  console.error(
    `${name} host "${host}" resolved but is not reachable over HTTPS (${e.message}). ` +
      `Refusing to ship a build that targets a dead endpoint.`,
  );
  process.exit(1);
}
