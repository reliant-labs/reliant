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
// `api.reliantapi.com` and no `admin.reliantapi.com`.
//
// Endpoints are no longer in the bundle at all: it reads them at runtime from
// config.js, so check 3 now asserts the bundle LOADS that document, and the
// endpoint assertion belongs to whatever serves it (the desktop app's app://
// handler). `--config` names the release config, echoed so a mis-targeted
// build is visible in the log.
//
// ## Usage
//
//   node .github/scripts/verify-bundle.mjs --dist frontends/web/dist
//   node .github/scripts/verify-bundle.mjs --dist frontends/web/dist --config electron/release.config.json
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

const distDir = resolve(argValue("--dist", join(REPO_ROOT, "frontends", "web", "dist")));
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
// frontends/web/src/lib/protocol.ts:
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

// ── 3. The bundle reads its config at runtime ────────────────────────────────
// Endpoints are no longer baked in: the bundle reads them from config.js
// (window.__FORGE_CONFIG__, frontends/web/src/lib/config_gen.ts), which each
// environment serves beside the same bytes — the control plane for the hosted
// SPA, the app:// handler for the desktop app. What the ARTIFACT must carry is
// the tag that loads it, as a blocking classic script ahead of the module
// bundle: without it every value silently takes its self-hosted default, which
// is the v1.7.5 coupon outage's shape (a value "injected" that never was).
// Whether the desktop app's config.js carries this release's endpoints is the
// app:// handler's to assert (electron/), not the bundle's.
console.log("\n[3/4] runtime config document loaded before the bundle");
if (existsSync(indexHtmlPath)) {
  const html = readFileSync(indexHtmlPath, "utf8");
  const configTag = html.search(/<script src="\/config\.js"><\/script>/);
  const moduleTag = html.search(/<script type="module"/);
  if (configTag < 0) {
    fail(
      'index.html does not load /config.js with a classic <script src="/config.js"></script>',
      "Every runtime config value would take its default. frontends/web/index.html must keep " +
        'the <script src="%BASE_URL%config.js"></script> tag.',
    );
  } else if (moduleTag >= 0 && moduleTag < configTag) {
    fail("index.html loads the module bundle before /config.js — the config would arrive too late");
  } else {
    ok("index.html loads /config.js before the module bundle");
  }
}
// ── 4. The asset base path is ABSOLUTE ───────────────────────────────────────
//
// vite.config.ts pins `base: "/"`. A RELATIVE base ("./") is silently fine at
// depth 0 and broken at depth 2+: from /auth/github/callback, `./assets/x.js`
// resolves to /auth/github/assets/x.js, 404s, receives the SPA fallback's
// index.html, and dies with "Expected a JavaScript module". frontends/web/src/routes.tsx
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
