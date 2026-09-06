#!/usr/bin/env node
// Asserts on a BUILT Vite bundle, before it ships.
//
// ## Why this file exists
//
// Every regression this guards against is invisible in source review and
// produces a perfectly green build. The source is correct; the ARTIFACT is
// wrong, because a value that was supposed to be injected at build time was
// not, or a build-mode flag folded the wrong way. The only place the truth is
// visible is the output, so that is where the assertion goes.
//
// Three of these checks already existed as inline shell in control-plane's
// deploy-reliant-web.yml and gated the HOSTED SPA. The desktop release —
// `release.yml`, which builds the artifact users actually download — ran none
// of them, which is how v1.7.5 shipped with VITE_CONTROL_PLANE_API_URL
// undefined and coupons throwing "Control plane API URL not configured".
// Confirmed by grepping the released .dmg: 87 renderer assets containing
// `api.reliantapi.com` and no `admin.reliantapi.com`. Check 3 below IS that
// forensic grep, promoted to a gate.
//
// ## The oracle is DERIVED, not typed
//
// The expected hostnames come from `electron/release.config.json` — the same
// file that SUPPLIED the build env. That is the whole point. The web gate
// hardcoded three URLs, which made it a fourth copy of an endpoint table that
// already lives in control-plane's KCL, is projected into release.config.json
// by sync-release-config.mjs, and is drift-gated against KCL in CI. Enumerating
// the file rather than a list someone remembered to update is what closes the
// 15-vs-22 variable gap BY CONSTRUCTION: a new endpoint added to the config is
// required in the bundle by default, with no second place to update.
//
// ## Usage
//
//   node .github/scripts/verify-bundle.mjs --dist web/dist
//   node .github/scripts/verify-bundle.mjs --dist web/dist --config electron/release.config.json
//
// Exits non-zero on any failed check, printing a GitHub Actions ::error:: line.

import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

const args = process.argv.slice(2);
function argValue(flag, fallback) {
  const i = args.indexOf(flag);
  return i !== -1 ? args[i + 1] : fallback;
}

const distDir = resolve(argValue("--dist", join(REPO_ROOT, "web", "dist")));
const configPath = resolve(
  argValue("--config", join(REPO_ROOT, "electron", "release.config.json")),
);

const problems = [];
function fail(message, detail) {
  problems.push({ message, detail });
}
function ok(message) {
  console.log(`  ok: ${message}`);
}

/**
 * Endpoints whose hostname is legitimately ABSENT from a correct production
 * bundle, with the reason. Everything else in `.vite` is required — this list
 * is the only escape hatch, and adding to it is a deliberate, reviewable act.
 *
 * Without this the gate would false-fail on a perfectly good build, and a gate
 * that cries wolf gets disabled, which is strictly worse than no gate.
 */
const NOT_IN_BUNDLE = {
  VITE_GATEWAY_URL:
    "read only by getGatewayURL(), reachable only through envPrefix(), which " +
    "returns early when VITE_CLI_DEFAULTS_BAKED is \"true\" (see " +
    "web/src/lib/cli-commands.ts). In a prod build that branch is " +
    "dead-code-eliminated, so the hostname is correctly absent.",
};

/** Every .js/.css file under dist/assets, read once and reused by all checks. */
function readAssets(dir) {
  const assetsDir = join(dir, "assets");
  if (!existsSync(assetsDir)) return [];
  const out = [];
  for (const name of readdirSync(assetsDir)) {
    const full = join(assetsDir, name);
    // Source maps are excluded deliberately. A map embeds the ORIGINAL source,
    // so `admin.reliantapi.com` appears in it even when the value was never
    // injected into the emitted code — asserting over maps would make the gate
    // pass on exactly the build it exists to catch.
    if (!statSync(full).isFile()) continue;
    if (name.endsWith(".map")) continue;
    if (!/\.(js|css)$/.test(name)) continue;
    out.push({ name, text: readFileSync(full, "utf8") });
  }
  return out;
}

if (!existsSync(distDir)) {
  console.error(`::error::verify-bundle: no build output at ${distDir}`);
  process.exit(1);
}
if (!existsSync(configPath)) {
  console.error(`::error::verify-bundle: no release config at ${configPath}`);
  process.exit(1);
}

const config = JSON.parse(readFileSync(configPath, "utf8"));
const viteConfig = config.vite ?? {};
const assets = readAssets(distDir);

console.log(`verify-bundle: asserting on ${distDir}`);
console.log(`  oracle: ${configPath} (env=${config.env})`);
console.log(`  ${assets.length} emitted asset file(s), source maps excluded\n`);

// ── 1. It is a real SPA build ────────────────────────────────────────────────
// A dist with an index.html but no fingerprinted chunk means the build emitted
// something unexpected. Hashed names are also what make cache-busting safe.
console.log("[1/4] real SPA build");
const indexHtmlPath = join(distDir, "index.html");
if (!existsSync(indexHtmlPath)) {
  fail(`${indexHtmlPath} is missing — this is not a Vite build output`);
} else {
  const hashed = assets.filter((a) => /^index-.+\.js$/.test(a.name));
  if (hashed.length < 1) {
    fail(`no hashed assets/index-*.js chunk in ${distDir} — refusing to ship`);
  } else {
    ok(`index.html + ${hashed.length} hashed index chunk(s)`);
  }
}

// ── 2. Not a dev build pointed at its own origin ─────────────────────────────
//
// web/src/lib/protocol.ts:
//   isSameOriginTransport = () => import.meta.env.DEV && !isPackagedRendererOrigin()
//
// In a correct production build that conjunct is statically false, the branch
// is dead-code-eliminated, and every Connect transport dials the absolute
// per-env backend. If NODE_ENV leaks in as "development", Vite folds DEV to
// TRUE inside a production bundle, the branch SURVIVES, and the shipped app
// POSTs its RPCs at its OWN origin. With no dev-server proxy in front of it,
// the SPA fallback answers those POSTs with index.html; Connect cannot parse
// HTML; useCurrentUser never settles; /onboarding spins forever with every
// request returning 200 and no console error. The live hosted bundle really
// did ship this way.
console.log("\n[2/4] no dev-mode leak");
const DEV_LEAK_MARKER = "?window.location.origin:";
const leaked = assets.filter((a) => a.text.includes(DEV_LEAK_MARKER));
if (leaked.length) {
  fail(
    "bundle still contains the same-origin transport branch — " +
      "import.meta.env.DEV was TRUE at build time (a leaked NODE_ENV=development)",
    leaked.map((a) => `assets/${a.name}`).join("\n    "),
  );
} else {
  ok("same-origin transport branch eliminated (genuine production build)");
}

// ── 3. The bundle carries this environment's real backend config ─────────────
// The v1.7.5 forensic grep. A value documented as "injected at build time" was
// never injected; the defect was not the wrong default, it was that the wrong
// default was SILENT.
console.log("\n[3/4] endpoints baked in (derived from release config)");
for (const [key, value] of Object.entries(viteConfig)) {
  if (typeof value !== "string" || !/^https?:\/\//.test(value)) continue;

  let host;
  try {
    host = new URL(value).hostname;
  } catch {
    fail(`vite.${key} is not a parseable URL: ${value}`);
    continue;
  }

  if (key in NOT_IN_BUNDLE) {
    console.log(`  skip: ${key} (${host}) — ${NOT_IN_BUNDLE[key]}`);
    continue;
  }

  if (assets.some((a) => a.text.includes(host))) {
    ok(`${key} → ${host}`);
  } else {
    fail(
      `built bundle does not contain ${host} (vite.${key}) — VITE_* injection did not happen`,
      "This is the exact shape of the v1.7.5 coupon outage. Either the build " +
        "did not export this key, or the code reading it was eliminated — if " +
        "the latter is correct and intended, add it to NOT_IN_BUNDLE with a reason.",
    );
  }
}

// A localhost backend URL in a shipped artifact is the "builds green, useless
// on a user's machine" failure. Only the API-ish keys: an unrelated localhost
// default (OTLP exporter, Spotlight) is not a shipping defect.
const LOCALHOST_NEAR_API = /(VITE_API_URL|VITE_GRPC_URL|VITE_CONTROL_PLANE_API_URL)[^,]{0,40}localhost/;
const localhostHits = assets.filter((a) => LOCALHOST_NEAR_API.test(a.text));
if (localhostHits.length) {
  fail(
    "a backend URL in the bundle points at localhost",
    localhostHits.map((a) => `assets/${a.name}`).join("\n    "),
  );
} else {
  ok("no backend URL points at localhost");
}

// ── 4. The asset base path is ABSOLUTE ───────────────────────────────────────
//
// vite.config.ts pins `base: "/"`. A RELATIVE base ("./") is silently fine at
// depth 0 and broken at depth 2+: from /auth/github/callback, `./assets/x.js`
// resolves to /auth/github/assets/x.js, 404s, receives the SPA fallback's
// index.html, and dies with "Expected a JavaScript module". web/src/routes.tsx
// declares 21 routes with 2+ segments, and they are exactly the OAuth and
// billing return URLs a user arrives at COLD from an external redirect with no
// warm bundle — which is why this class presents as a redirect bug.
//
// The packaged app depends on the same property: app://bundle works BECAUSE the
// base is absolute.
console.log("\n[4/4] absolute asset base path");
if (existsSync(indexHtmlPath)) {
  const html = readFileSync(indexHtmlPath, "utf8");
  const refs = [...html.matchAll(/(?:src|href)="([^"]+)"/g)].map((m) => m[1]);
  const assetRefs = refs.filter((r) => /assets\//.test(r));
  const relative = assetRefs.filter((r) => !r.startsWith("/"));

  if (!assetRefs.length) {
    fail("index.html references no assets/ files at all — unexpected build output");
  } else if (relative.length) {
    fail(
      `index.html uses a RELATIVE asset base — deep routes will 404 and white-screen`,
      `${relative.length} relative reference(s): ${relative.slice(0, 5).join(", ")}\n    ` +
        `Fix: vite.config.ts must set base: "/" (absolute), not "./".`,
    );
  } else {
    ok(`${assetRefs.length} asset reference(s), all root-absolute`);
  }
}

// ── Report ───────────────────────────────────────────────────────────────────
console.log("");
if (problems.length) {
  for (const { message, detail } of problems) {
    console.error(`::error::verify-bundle: ${message}`);
    if (detail) console.error(`    ${detail}`);
  }
  console.error(`\nverify-bundle: FAILED (${problems.length} problem(s)) — refusing to ship ${distDir}`);
  process.exit(1);
}
console.log(`verify-bundle: all checks passed for ${distDir}`);
