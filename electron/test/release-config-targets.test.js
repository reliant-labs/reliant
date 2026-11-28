// Guards the rule that makes the desktop build reproducible: EVERY packaging
// target gets its endpoints from electron/release.config.json, which is
// projected out of control-plane's KCL by .github/scripts/sync-release-config.mjs.
//
// The failure this prevents is the v1.7.5 one, and it is silent: a build target
// that packages WITHOUT the release config produces an installer that builds
// green and ships with no control-plane URL — which is what made coupons throw
// "Control plane API URL not configured" on real users' machines. The renderer
// reads its endpoints from config.js, which the main process answers from
// src/release-renderer-config.js (generated from the `renderer` block); the
// main process reads src/build-config.js (the `main` block). Both are written
// by generate-build-config.mjs, under the wrapper.
//
// Worse locally: a dev shell often exports RELIANT_API_URL pointing at a local
// stack. A target that merely DEFAULTS
// the config instead of OVERRIDING it bakes localhost into a build labelled
// "prod". Hence the override assertion below.

const test = require('node:test');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const ELECTRON_DIR = path.join(__dirname, '..');
const PKG = JSON.parse(fs.readFileSync(path.join(ELECTRON_DIR, 'package.json'), 'utf8'));
const RELEASE_CONFIG_PATH = path.join(ELECTRON_DIR, 'release.config.json');
const RELEASE_CONFIG = JSON.parse(fs.readFileSync(RELEASE_CONFIG_PATH, 'utf8'));

// The public build targets. Each one must reach electron-builder through the
// release-config wrapper — not around it.
const PUBLIC_BUILD_TARGETS = [
  'build',
  'dist',
  'dist:mac',
  'dist:mac:dev',
  'dist:mac:prod-local',
  'dist:pr',
  'dist:win',
  'dist:linux',
];

const WRAPPER = 'with-release-config.mjs';

const CONTROL_PLANE_DIR =
  process.env.CONTROL_PLANE_DIR || path.resolve(ELECTRON_DIR, '..', '..', 'control-plane');

/** Is an executable named `name` on PATH? */
function onPath(name) {
  const exts = process.platform === 'win32' ? (process.env.PATHEXT || '.EXE').split(';') : [''];
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    if (!dir) continue;
    for (const ext of exts) {
      try {
        fs.accessSync(path.join(dir, name + ext), fs.constants.X_OK);
        return true;
      } catch {
        // not here
      }
    }
  }
  return false;
}

/**
 * Why the wrapper cannot render control-plane's KCL here, or null when it can.
 * Mirrors the checks of .github/scripts/release-config-kcl.mjs
 * (renderReleaseConfig's `unavailable`): a control-plane checkout, forge (or
 * reliant) to evaluate the entrypoint, and `kcl` to render it. Where any is
 * missing the wrapper falls back to the committed cache, so the tests that
 * prove the KCL is authoritative have nothing to prove and skip.
 */
function kclUnreachable() {
  if (!fs.existsSync(path.join(CONTROL_PLANE_DIR, 'deploy/kcl/desktop_release.k'))) {
    return `no control-plane checkout at ${CONTROL_PLANE_DIR}`;
  }
  if (!onPath('forge') && !onPath('reliant')) return 'neither forge nor reliant is on PATH';
  if (!onPath('kcl')) return 'no kcl binary on PATH';
  return null;
}

/**
 * Does this target reach its packaging step through the wrapper?
 *
 * Resolved transitively so a target may be a plain alias (`dist` -> `build`)
 * or a wrapper invocation of an inner target (`dist:mac` -> wrapper -> pack:mac).
 * Once the wrapper is entered, everything it spawns inherits the exported
 * environment, so the chain below it does not need to repeat the check.
 */
function resolvesThroughWrapper(name, seen = new Set()) {
  if (seen.has(name)) return false;
  seen.add(name);

  const script = PKG.scripts?.[name];
  if (!script) return false;
  if (script.includes(WRAPPER)) return true;

  const alias = script.trim().match(/^pnpm run ([\w:.-]+)$/);
  if (alias) return resolvesThroughWrapper(alias[1], seen);

  return false;
}

test('every public build target sources its config through the release-config wrapper', () => {
  for (const target of PUBLIC_BUILD_TARGETS) {
    assert.ok(
      PKG.scripts?.[target],
      `electron/package.json has no "${target}" script — every build mode must be a named target`,
    );
    assert.ok(
      resolvesThroughWrapper(target),
      `"${target}" packages the app without going through scripts/${WRAPPER}, so it would ` +
        `build with whatever VITE_*/RELIANT_* the calling shell happened to have — ` +
        `undefined in CI, localhost in a dev shell. Route it through the wrapper.`,
    );
  }
});

test('the wrapper exists and is the only place the release config is expanded', () => {
  const wrapperPath = path.join(ELECTRON_DIR, 'scripts', WRAPPER);
  assert.ok(fs.existsSync(wrapperPath), `missing electron/scripts/${WRAPPER}`);

  // The old prod-local script expanded release.config.json itself with jq.
  // That was a second implementation of this logic; it must not come back.
  const prodLocal = fs.readFileSync(
    path.join(ELECTRON_DIR, '..', 'scripts', 'build-electron-prod-local.sh'),
    'utf8',
  );
  assert.ok(
    !/jq -er '\(\.vite \+ \.main\)/.test(prodLocal),
    'build-electron-prod-local.sh expands release.config.json itself — that is a parallel ' +
      `config path. It must inherit the environment from scripts/${WRAPPER}.`,
  );
});

test('the wrapper OVERRIDES an ambient dev-shell value rather than deferring to it', () => {
  // The real regression: building "prod" from a dev shell. RELIANT_API_URL is
  // already exported, and so (from an earlier run) may be the renderer
  // document, pointing at a local stack.
  const out = execFileSync(
    process.execPath,
    [
      path.join(ELECTRON_DIR, 'scripts', WRAPPER),
      process.execPath,
      '-e',
      'const r = JSON.parse(process.env.RELIANT_RENDERER_CONFIG); console.log(JSON.stringify({renderer: r.API_URL, main: process.env.RELIANT_API_URL, cp: r.CONTROL_PLANE_API_URL}))',
    ],
    {
      cwd: ELECTRON_DIR,
      encoding: 'utf8',
      env: {
        ...process.env,
        RELIANT_RENDERER_CONFIG: JSON.stringify({ API_URL: 'http://localhost:3090' }),
        RELIANT_API_URL: 'http://localhost:8090',
      },
    },
  );

  const seen = JSON.parse(out.trim().split('\n').pop());
  assert.strictEqual(seen.renderer, RELEASE_CONFIG.renderer.API_URL);
  assert.strictEqual(seen.main, RELEASE_CONFIG.main.RELIANT_API_URL);
  assert.strictEqual(seen.cp, RELEASE_CONFIG.renderer.CONTROL_PLANE_API_URL);
});

test('a hand-edited release.config.json does NOT reach the build when the KCL is reachable', (t) => {
  // THE SOURCE-OF-TRUTH CONTRACT. The committed JSON is a CACHE of control-plane's
  // KCL, not an authority. Before this was enforced, editing the file put the
  // edited value straight into the build environment — verified by poisoning
  // VITE_API_URL and watching "https://POISONED.example.com" arrive unchallenged.
  // That made KCL a generator rather than the source of truth, which is the
  // whole requirement this path exists to satisfy.
  //
  // Skips (rather than fails) where the KCL genuinely cannot be reached — a
  // fork with no control-plane checkout, or no kcl binary — because the cache
  // is legitimately authoritative there. That fallback is covered below.
  const unreachable = kclUnreachable();
  if (unreachable) {
    t.skip(`${unreachable}; the committed cache is the correct source here`);
    return;
  }

  const original = fs.readFileSync(RELEASE_CONFIG_PATH, 'utf8');
  const poisoned = JSON.parse(original);
  poisoned.renderer.API_URL = 'https://POISONED.example.com';

  try {
    fs.writeFileSync(RELEASE_CONFIG_PATH, JSON.stringify(poisoned, null, 2));
    const out = execFileSync(
      process.execPath,
      [
        path.join(ELECTRON_DIR, 'scripts', WRAPPER),
        process.execPath,
        '-e',
        'console.log(JSON.stringify({renderer: JSON.parse(process.env.RELIANT_RENDERER_CONFIG).API_URL}))',
      ],
      { cwd: ELECTRON_DIR, encoding: 'utf8' },
    );
    const seen = JSON.parse(out.trim().split('\n').pop());
    assert.notStrictEqual(
      seen.renderer,
      'https://POISONED.example.com',
      'a hand-edit to the generated cache reached the build — KCL is not authoritative',
    );
    assert.strictEqual(
      seen.renderer,
      RELEASE_CONFIG.renderer.API_URL,
      'the build must carry the value KCL declares',
    );
  } finally {
    // Restore byte-for-byte: this file is committed, and a test must not leave
    // the working tree dirty for the other agents sharing this checkout.
    fs.writeFileSync(RELEASE_CONFIG_PATH, original);
  }
});

test('the committed config is still used when control-plane is unreachable', () => {
  // The OSS path. reliant is public and control-plane is private, so a fork —
  // and the public repo's own tag-triggered release — cannot render the KCL.
  // The cache must remain a working fallback there, or the OSS build breaks.
  const out = execFileSync(
    process.execPath,
    [
      path.join(ELECTRON_DIR, 'scripts', WRAPPER),
      process.execPath,
      '-e',
      'console.log(JSON.stringify({renderer: JSON.parse(process.env.RELIANT_RENDERER_CONFIG).API_URL}))',
    ],
    {
      cwd: ELECTRON_DIR,
      encoding: 'utf8',
      env: { ...process.env, CONTROL_PLANE_DIR: '/nonexistent-control-plane' },
    },
  );
  const seen = JSON.parse(out.trim().split('\n').pop());
  assert.strictEqual(seen.renderer, RELEASE_CONFIG.renderer.API_URL);
});

test('prod-local keeps the three behaviors that make it runnable alongside an installed Reliant', () => {
  const script = fs.readFileSync(
    path.join(ELECTRON_DIR, '..', 'scripts', 'build-electron-prod-local.sh'),
    'utf8',
  );
  const builderConfig = fs.readFileSync(
    path.join(ELECTRON_DIR, 'electron-builder.local.js'),
    'utf8',
  );

  // 1. Ad-hoc re-signing. @electron/fuses rewrites bytes inside the Electron
  //    Framework AFTER packaging, invalidating its signature; without this the
  //    kernel SIGKILLs the app before any JS runs (CODESIGNING / Invalid Page).
  assert.match(
    script,
    /codesign --force/,
    'the ad-hoc re-sign is gone — the fuse-rewritten framework would be SIGKILLed at launch',
  );

  // 2. Keychain grant cleanup. The ACL binds to the ad-hoc code hash, which
  //    changes every rebuild, so a stale grant means a prompt on every launch.
  assert.match(
    script,
    /security delete-generic-password -s "reliant-local Safe Storage"/,
    'the keychain grant cleanup is gone — every launch would re-prompt',
  );

  // 3. Separate appId + userData, so it does not take the single-instance lock
  //    from (or overwrite the session of) the installed Reliant.
  assert.match(builderConfig, /appId:\s*"com\.reliantlabs\.reliant\.local"/);
  assert.match(builderConfig, /name:\s*"reliant-local"/);
});

test('release.config.json carries every key the packaged app cannot start without', () => {
  const requiredRenderer = [
    'API_URL',
    'CONTROL_PLANE_API_URL',
    'GATEWAY_URL',
    'AUTH_MODE',
    'SUPABASE_URL',
    'SUPABASE_ANON_KEY',
  ];
  const requiredMain = [
    'RELIANT_SERVER_URL',
    'RELIANT_API_URL',
    'RELIANT_GATEWAY_URL',
    'RELIANT_CONTROL_PLANE_URL',
  ];

  assert.strictEqual(RELEASE_CONFIG.vite, undefined, 'release.config.json still has a `vite` block; the renderer reads config.js');
  for (const key of requiredRenderer) {
    const value = RELEASE_CONFIG.renderer?.[key];
    assert.ok(value, `release.config.json renderer.${key} is empty`);
    assert.doesNotMatch(value, /localhost|127\.0\.0\.1/, `renderer.${key} targets localhost`);
  }
  assert.strictEqual(RELEASE_CONFIG.renderer.AUTH_MODE, 'cloud', 'the desktop release is the managed app');
  for (const key of requiredMain) {
    const value = RELEASE_CONFIG.main?.[key];
    assert.ok(value, `release.config.json main.${key} is empty`);
    assert.doesNotMatch(value, /localhost|127\.0\.0\.1/, `main.${key} targets localhost`);
  }
});

test('a KCL change reaches the generated main-process config, not just the environment', (t) => {
  // THE END-TO-END CONTRACT: change a value in forge's KCL, and it must appear
  // in the artifact the packaged app actually reads.
  //
  // The wrapper exporting the right value is NOT sufficient on its own.
  // generate-build-config.mjs writes electron/src/build-config.js — the
  // main-process endpoints — and it read release.config.json directly. With a
  // changed gateway_url in the KCL the wrapper exported the NEW value while
  // build-config.js was still written with the OLD one, so the cache remained
  // the authority for exactly the half nobody was looking at.
  //
  // This edits the real KCL, because that is the thing a person changes. Skips
  // where the KCL cannot be rendered; that fallback is covered above.
  const unreachable = kclUnreachable();
  if (unreachable) {
    t.skip(unreachable);
    return;
  }
  const envK = path.join(CONTROL_PLANE_DIR, 'deploy/kcl/lib/env.k');
  if (!fs.existsSync(envK)) {
    t.skip(`no ${envK}`);
    return;
  }

  const buildConfigPath = path.join(ELECTRON_DIR, 'src', 'build-config.js');
  const rendererConfigPath = path.join(ELECTRON_DIR, 'src', 'release-renderer-config.js');
  const originalEnvK = fs.readFileSync(envK, 'utf8');
  const read = (p) => (fs.existsSync(p) ? fs.readFileSync(p, 'utf8') : null);
  const originalConfig = read(buildConfigPath);
  const originalRendererConfig = read(rendererConfigPath);

  const REAL = 'https://gateway.reliantapi.com';
  const CHANGED = 'https://CHANGED-BY-TEST.reliantapi.com';
  if (!originalEnvK.includes(`gateway_url = "${REAL}"`)) return; // shape moved

  try {
    fs.writeFileSync(
      envK,
      originalEnvK.replace(`gateway_url = "${REAL}"`, `gateway_url = "${CHANGED}"`),
    );

    execFileSync(
      process.execPath,
      [
        path.join(ELECTRON_DIR, 'scripts', WRAPPER),
        process.execPath,
        path.join(ELECTRON_DIR, '..', '.github', 'scripts', 'generate-build-config.mjs'),
      ],
      { cwd: ELECTRON_DIR, encoding: 'utf8' },
    );

    // Assert on the ARTIFACT, not stdout — build-config.js is what the packaged
    // main process reads.
    assert.match(
      fs.readFileSync(buildConfigPath, 'utf8'),
      /CHANGED-BY-TEST\.reliantapi\.com/,
      'a KCL edit did not reach build-config.js — the cached file is still the authority',
    );
    // And the renderer's config.js, which the main process serves from it.
    assert.match(
      fs.readFileSync(rendererConfigPath, 'utf8'),
      /"GATEWAY_URL": "https:\/\/CHANGED-BY-TEST\.reliantapi\.com"/,
      'a KCL edit did not reach release-renderer-config.js — the packaged config.js would serve the cached value',
    );
  } finally {
    // Both files are committed; never leave the tree dirty for other agents.
    fs.writeFileSync(envK, originalEnvK);
    for (const [p, original] of [[buildConfigPath, originalConfig], [rendererConfigPath, originalRendererConfig]]) {
      if (original !== null) fs.writeFileSync(p, original);
      else fs.rmSync(p, { force: true });
    }
  }
});
