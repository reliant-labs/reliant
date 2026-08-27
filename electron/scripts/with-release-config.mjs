#!/usr/bin/env node
// Runs a command with electron/release.config.json expanded into its
// environment. Every packaging target goes through here, so there is exactly
// ONE place a build's endpoints can come from.
//
//   node scripts/with-release-config.mjs npm run pack:mac
//
// ## Why a wrapper instead of setting the vars in each npm script
//
// release.config.json is GENERATED from control-plane's KCL
// (deploy/kcl/lib/env.k -> .github/scripts/sync-release-config.mjs). Nothing
// downstream may restate its contents: a hardcoded URL in a build script is a
// second declaration that drifts silently, which is the exact defect that
// shipped v1.7.5 with no control-plane URL.
//
// The two blocks land in different consumers, and neither reads the JSON file
// itself, which is why they have to become environment variables here:
//
//   .vite -> read straight out of the environment by `vite build`
//   .main -> read by generate-build-config.mjs, which writes
//            electron/src/build-config.js (the main-process endpoints)
//
// This is the same expansion release.yml performs with jq into $GITHUB_ENV.
// CI keeps using jq because $GITHUB_ENV has to outlive the step; locally a
// process wrapper is the equivalent, and both read the one generated file.
//
// ## These values OVERRIDE the calling shell. That is the point.
//
// A dev shell here has usually already exported RELIANT_API_URL /
// RELIANT_SERVER_URL / RELIANT_GATEWAY_URL and sometimes VITE_* pointing at a
// LOCAL stack — scripts/dev.sh and .dev-ports.sh both do it. `vite build` reads
// VITE_* straight from the environment, so deferring to an inherited value
// would bake localhost into a build labelled "prod", and the mistake would only
// surface later as a packaged app talking to a port on the builder's laptop.
//
// CI never hits this because a fresh runner has none of them set; locally it is
// the DEFAULT case. So each key is assigned unconditionally.
//
// Secrets (Sentry DSN, Statsig key) are deliberately NOT here. They come from
// GitHub Actions secrets in CI and are simply absent locally, which leaves
// telemetry inert in a local build.

import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ELECTRON_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");
const REPO_ROOT = join(ELECTRON_DIR, "..");
const RELEASE_CONFIG_PATH = join(ELECTRON_DIR, "release.config.json");
const CONTROL_PLANE_DIR =
  process.env.CONTROL_PLANE_DIR || resolve(REPO_ROOT, "..", "control-plane");
const KCL_ENTRYPOINT = "deploy/kcl/desktop_release.k";

// Which environment's endpoints this build carries. The committed file records
// the env it was generated for, so a build that does not say defers to it.
const BUILD_ENV = process.env.RELIANT_RELEASE_ENV || "";

/**
 * Render the config straight out of control-plane's KCL.
 *
 * THIS IS THE SOURCE OF TRUTH, and it is consulted first on every build that
 * can reach it. The committed release.config.json is a CACHE for the one case
 * that structurally cannot: reliant is public, control-plane is private, and a
 * tag-triggered release in the public repo has no credential to check the KCL
 * out. Treating the cache as authoritative everywhere is what let a hand-edit
 * reach a packaged build — verified: poisoning VITE_API_URL in the JSON put
 * "https://POISONED.example.com" into the build environment with no complaint.
 *
 * Returns null (not an error) when the KCL is genuinely unreachable, so an OSS
 * contributor with no control-plane checkout still builds from the cache.
 */
function renderFromKCL(env) {
  const entry = join(CONTROL_PLANE_DIR, KCL_ENTRYPOINT);
  if (!existsSync(entry)) return null;
  try {
    const out = execFileSync(
      "kcl",
      ["run", KCL_ENTRYPOINT, "-D", `env=${env}`, "-S", "release_config", "--format", "json"],
      { cwd: CONTROL_PLANE_DIR, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
    );
    return JSON.parse(out);
  } catch (err) {
    if (err.code === "ENOENT") return null; // kcl not installed
    // The KCL is present but does not render: that is a real defect in the
    // source of truth, not a reason to fall back to a stale copy of it.
    console.error(
      `with-release-config: control-plane KCL failed to render for env=${env}:\n` +
        `${err.stderr || err.message}`,
    );
    process.exit(1);
  }
}

const [command, ...commandArgs] = process.argv.slice(2);
if (!command) {
  console.error("usage: node scripts/with-release-config.mjs <command> [args...]");
  process.exit(2);
}

if (!existsSync(RELEASE_CONFIG_PATH)) {
  console.error(
    `with-release-config: ${RELEASE_CONFIG_PATH} is missing.\n` +
      "It is generated from control-plane's KCL. Regenerate with:\n" +
      "  node .github/scripts/sync-release-config.mjs --env prod",
  );
  process.exit(1);
}

let committed;
try {
  committed = JSON.parse(readFileSync(RELEASE_CONFIG_PATH, "utf8"));
} catch (err) {
  console.error(`with-release-config: cannot parse release.config.json: ${err.message}`);
  process.exit(1);
}

// KCL first; the committed file only names the env when the caller did not.
const targetEnv = BUILD_ENV || committed.env;
const rendered = renderFromKCL(targetEnv);

let release;
let source;
if (rendered) {
  release = rendered;
  source = "control-plane KCL (live)";

  // The cache disagreeing with the source of truth is a real condition, not a
  // preference to resolve silently. Building from the live render is correct,
  // but a stale committed file will ship from CI — where the KCL is
  // unreachable — so it must be regenerated and committed, and saying so here
  // is the only place a human sees it before that happens.
  const flat = (c) => JSON.stringify({ ...(c.vite || {}), ...(c.main || {}) }, Object.keys({ ...(c.vite || {}), ...(c.main || {}) }).sort());
  if (flat(rendered) !== flat(committed)) {
    console.warn(
      `with-release-config: WARNING — electron/release.config.json is STALE.\n` +
        `  Building from the live KCL render (correct), but CI builds read the committed file.\n` +
        `  Regenerate and commit it:\n` +
        `    node .github/scripts/sync-release-config.mjs --env ${targetEnv}`,
    );
  }
} else {
  release = committed;
  source = "committed release.config.json (control-plane not reachable)";
}

const exported = { ...(release.vite || {}), ...(release.main || {}) };

// Fail loudly rather than package a mislabelled build. generate-build-config.mjs
// and sync-release-config.mjs guard the same values at their own boundaries;
// this is the guard for the renderer path, which otherwise has none — `vite
// build` treats an unset VITE_* as legitimately empty and reports nothing.
const REQUIRED = [
  "VITE_API_URL",
  "VITE_CONTROL_PLANE_API_URL",
  "VITE_GATEWAY_URL",
  "RELIANT_API_URL",
  "RELIANT_SERVER_URL",
  "RELIANT_GATEWAY_URL",
  "RELIANT_CONTROL_PLANE_URL",
];
const problems = REQUIRED.filter(
  (key) => !exported[key] || /localhost|127\.0\.0\.1/.test(exported[key]),
).map((key) => `${key} is "${exported[key] ?? ""}"`);

if (problems.length) {
  console.error(
    `with-release-config: refusing to build "${release.env}" against a missing or local endpoint:\n` +
      `  - ${problems.join("\n  - ")}\n` +
      "Fix it in control-plane deploy/kcl/lib/env.k (reliant_desktop_release_config).",
  );
  process.exit(1);
}

console.log(`==> release config: env=${release.env} — source: ${source}`);
for (const key of ["VITE_API_URL", "VITE_CONTROL_PLANE_API_URL", "RELIANT_API_URL", "RELIANT_GATEWAY_URL"]) {
  console.log(`    ${key.padEnd(26)} = ${exported[key]}`);
}

const result = spawnSync(command, commandArgs, {
  cwd: process.cwd(),
  stdio: "inherit",
  env: { ...process.env, ...exported },
  // npm resolves to npm.cmd on Windows, which needs a shell to be executable.
  shell: process.platform === "win32",
});

if (result.error) {
  console.error(`with-release-config: failed to run ${command}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
