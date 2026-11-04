#!/usr/bin/env node
// Projects the packaged desktop app's release config OUT of control-plane's
// KCL and into `electron/release.config.json`, the committed generated file
// the release pipeline reads.
//
// ## Why this file exists
//
// The packaged Electron app used to get its renderer config from a `.env`
// heredoc hand-written in release.yml. That heredoc set 11 fewer variables
// than the app actually reads, so the shipped desktop build had no
// VITE_CONTROL_PLANE_API_URL and no VITE_GATEWAY_URL — which is why clicking a
// coupon threw "Control plane API URL not configured". Verified against the
// real shipped v1.7.5 .dmg: none of its 87 renderer assets contain
// `admin.reliantapi.com`.
//
// The values now come from ONE declaration, in control-plane's
// `deploy/kcl/lib/env.k` (`reliant_desktop_release_config`) — the same lambda
// the hosted web app's `reliant_web_vite_env` is built from. One place to
// change a hostname; every surface re-projected from it.
//
// ## Why it is projected into a committed file rather than read live
//
// This repo is PUBLIC; control-plane is PRIVATE, and this repo's Actions
// secrets hold no cross-repo token. A release run therefore cannot check out
// the KCL, and handing a public OSS release a private credential just to build
// itself would be a worse coupling than the duplication it replaces.
//
// So this follows forge's own codegen model — authored in KCL, generated here,
// committed, and drift-gated in CI (`--check`). The content is public
// hostnames plus a publishable anon key, so nothing sensitive crosses the
// boundary; the only thing being protected is single-authorship, and the
// drift gate enforces that mechanically instead of by convention.
//
// ## Usage
//
//   node .github/scripts/sync-release-config.mjs --env prod     # write
//   node .github/scripts/sync-release-config.mjs --check        # verify, fork-safe
//   node .github/scripts/sync-release-config.mjs --check --require   # verify, authoritative
//
// `--check` re-renders and diffs without writing; it exits non-zero when the
// committed file has drifted from the KCL, and is a no-op (with a clear skip
// message) when the control-plane checkout or `kcl` is unavailable, so an
// outside contributor's fork does not fail on a private repo it cannot see.
//
// ## --require: the skip is the failure mode
//
// That skip is load-bearing for forks and structurally dangerous everywhere
// else, because a gate that cannot fail is indistinguishable from a gate that
// passes. The invocation in control-plane's `ci.yml` is the AUTHORITATIVE
// drift gate — it checks out reliant as a sibling and points CONTROL_PLANE_DIR
// at the KCL — so if that one ever renders `unavailable`, it has stopped
// checking anything and must say so loudly. control-plane's own CI has a
// recorded instance of exactly this class: "This job passed only because a
// cache hit skipped the install."
//
// `--require` turns every skip path into a hard failure. Use it wherever the
// renderer is *supposed* to be present; omit it only on the fork-friendly path
// where a contributor genuinely cannot supply the private KCL.

import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { ReleaseConfigRenderError, renderReleaseConfig } from "./release-config-kcl.mjs";
import { rendererProblems } from "./release-renderer-check.mjs";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const OUTPUT_PATH = join(REPO_ROOT, "electron", "release.config.json");

// The sibling control-plane checkout holding the KCL source of truth.
const CONTROL_PLANE_DIR =
  process.env.CONTROL_PLANE_DIR || resolve(REPO_ROOT, "..", "control-plane");

const args = process.argv.slice(2);
const checkOnly = args.includes("--check");
// --require: refuse to skip. See the "--require" note in the header.
const requireRender = args.includes("--require");
const envIdx = args.indexOf("--env");
const requestedEnv = envIdx !== -1 ? args[envIdx + 1] : undefined;

function fail(message) {
  console.error(`sync-release-config: ${message}`);
  process.exit(1);
}

/** Render the config for `env` out of control-plane's KCL (release-config-kcl.mjs). */
function renderFromKCL(env) {
  try {
    return renderReleaseConfig(CONTROL_PLANE_DIR, env);
  } catch (err) {
    if (err instanceof ReleaseConfigRenderError) fail(err.message);
    throw err;
  }
}

/**
 * Guard the rendered values before they can reach an installer.
 *
 * The recurring incident class here is a packaged app that silently ships
 * pointing at localhost or at nothing — it builds green and fails on a user's
 * machine. generate-build-config.mjs already guards the main-process URLs;
 * these are the renderer-side equivalents, including the two that were
 * MISSING from the shipped v1.7.5 build.
 */
function validate(config, env) {
  const problems = [];

  if (config.env !== env) {
    problems.push(`rendered env is "${config.env}" but "${env}" was requested`);
  }

  // The RENDERER block is the packaged app's config.js (the main process
  // answers app://bundle/config.js with it; electron/src/runtime-config.js),
  // so it is judged exactly as control-plane's render judges the hosted
  // SPA's runtime_config: against the web frontend's own generated schema.
  if (config.vite) {
    problems.push(
      "a `vite` block is present — the renderer reads no build-time VITE_* any more; " +
        "its values belong in `renderer` (config.js keys, e.g. API_URL)",
    );
  }
  const renderer = config.renderer;
  if (!renderer || typeof renderer !== "object") {
    problems.push("`renderer` is missing — the packaged renderer would have no config.js");
  } else {
    problems.push(...rendererProblems(renderer));
  }

  const requiredMain = [
    "RELIANT_SERVER_URL",
    "RELIANT_API_URL",
    "RELIANT_GATEWAY_URL",
    "RELIANT_CONTROL_PLANE_URL",
  ];
  for (const key of requiredMain) {
    const value = config.main?.[key];
    if (!value) problems.push(`main.${key} is empty — the packaged main process needs it`);
    else if (/localhost|127\.0\.0\.1/.test(value)) {
      problems.push(`main.${key} is ${value} — a packaged build must not target localhost`);
    }
  }

  if (problems.length) {
    fail(
      `refusing to ship a desktop build with broken config:\n  - ${problems.join("\n  - ")}\n` +
        `Fix it in control-plane deploy/kcl/lib/env.k (reliant_desktop_release_config).`,
    );
  }
}

function serialize(config) {
  return (
    JSON.stringify(config, null, 2) + "\n"
  );
}

if (checkOnly) {
  if (!existsSync(OUTPUT_PATH)) {
    fail(`${OUTPUT_PATH} is missing — run: node .github/scripts/sync-release-config.mjs --env prod`);
  }
  const committed = JSON.parse(readFileSync(OUTPUT_PATH, "utf8"));
  const env = requestedEnv || committed.env;
  const { config, unavailable } = renderFromKCL(env);

  if (unavailable) {
    if (requireRender) {
      fail(
        `--require was passed but the KCL could not be rendered: ${unavailable}.\n` +
          `This invocation is an authoritative drift gate; skipping would mean it\n` +
          `verified nothing while reporting success. Ensure \`kcl\` AND \`forge\` are\n` +
          `installed (forge supplies the \`forge\` KCL module that control-plane's KCL\n` +
          `imports — kcl alone cannot resolve it) and that CONTROL_PLANE_DIR points at\n` +
          `a control-plane checkout (currently ${CONTROL_PLANE_DIR}).`,
      );
    }
    // A fork, or a machine without the private sibling repo. Validate what is
    // committed (that part is always possible) and skip the drift comparison
    // rather than failing on something the contributor cannot fix.
    console.log(`sync-release-config: skipping drift check — ${unavailable}`);
    validate(committed, committed.env);
    console.log(`sync-release-config: committed config for env=${committed.env} passes validation`);
    process.exit(0);
  }

  validate(config, env);
  if (serialize(config) !== readFileSync(OUTPUT_PATH, "utf8")) {
    fail(
      `electron/release.config.json has DRIFTED from control-plane's KCL.\n` +
        `This file is generated — do not hand-edit it.\n` +
        `Regenerate with: node .github/scripts/sync-release-config.mjs --env ${env}`,
    );
  }
  console.log(`sync-release-config: electron/release.config.json matches KCL for env=${env}`);
  process.exit(0);
}

if (!requestedEnv) {
  fail("--env is required (prod), or pass --check to verify the committed file");
}
const { config, unavailable } = renderFromKCL(requestedEnv);
if (unavailable) fail(`cannot render: ${unavailable}`);
validate(config, requestedEnv);
writeFileSync(OUTPUT_PATH, serialize(config));
console.log(`sync-release-config: wrote electron/release.config.json for env=${requestedEnv}`);
console.log(`  renderer.CONTROL_PLANE_API_URL = ${config.renderer.CONTROL_PLANE_API_URL}`);
console.log(`  renderer.GATEWAY_URL           = ${config.renderer.GATEWAY_URL}`);
