// Guards the two defects that shipped v1.7.12 with NO desktop artifact at all.
//
// Both were silent in review: the workflow diff between v1.7.11 and v1.7.12
// touched neither the signing config nor the Go build line, yet `Build macOS`
// and `Build backend (windows-arm64)` both failed and every macOS/Windows user
// was left on 1.7.11. Neither failure is reachable from a unit test of app
// code, so the assertions below are on the build inputs themselves.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const ELECTRON_DIR = path.join(__dirname, '..');
const REPO_ROOT = path.join(ELECTRON_DIR, '..');

// ── 1. macOS code signing ────────────────────────────────────────────────────
//
// app-builder-lib's createKeychain() makes a throwaway keychain with a random
// password, then imports the .p12 with CSC_KEY_PASSWORD. Through 26.16.0 the
// follow-up `security set-key-partition-list -k` was handed the CERT password
// instead of the KEYCHAIN password:
//
//   security set-key-partition-list -S apple-tool:,apple: -s -k <cscKeyPassword> <keychain>
//
// `-k` means "the keychain's unlock password", so that only worked while the
// keychain happened to still be unlocked; when `security` needs to unlock it,
// the wrong password fails with "SecKeychainUnlock: The user name or passphrase
// you entered is not correct" — the exact v1.7.12 error. macos-26-arm64 image
// 20260831 changed that timing, which is why identical config signed fine on
// image 20260728 (v1.7.11) and failed a week later with secrets untouched
// since April.
//
// Fixed upstream in 26.16.1, which passes `keychainPassword`. Pinning the floor
// here because a downgrade would not fail any other test — it would fail the
// next release, after the tag is public.
const MIN_ELECTRON_BUILDER = [26, 16, 1];

function parseExactVersion(spec) {
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(spec);
  assert.ok(m, `electron-builder must be pinned to an exact version, got "${spec}"`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function gte(a, b) {
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i];
  }
  return true;
}

test('electron-builder is new enough to unlock its signing keychain', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(ELECTRON_DIR, 'package.json'), 'utf8'));
  const spec = pkg.devDependencies?.['electron-builder'] ?? pkg.dependencies?.['electron-builder'];
  assert.ok(spec, 'electron-builder must be declared in electron/package.json');

  assert.ok(
    gte(parseExactVersion(spec), MIN_ELECTRON_BUILDER),
    `electron-builder must be >= ${MIN_ELECTRON_BUILDER.join('.')} (got ${spec}). ` +
      'Earlier versions pass the certificate password to `security set-key-partition-list -k`, ' +
      'which expects the keychain password — macOS signing then fails with ' +
      '"SecKeychainUnlock: The user name or passphrase you entered is not correct" ' +
      'and the release publishes no macOS artifact.'
  );
});

test('the resolved app-builder-lib carries the keychain-password fix', () => {
  const lock = JSON.parse(fs.readFileSync(path.join(ELECTRON_DIR, 'package-lock.json'), 'utf8'));

  // The lockfile is what CI installs (`npm ci`), so assert on it rather than on
  // the range in package.json. app-builder-lib is nested under electron-builder
  // and dmg-builder, so check every copy — signing uses whichever one resolves.
  const resolved = Object.entries(lock.packages ?? {})
    .filter(([p]) => p.endsWith('node_modules/app-builder-lib'))
    .map(([p, meta]) => [p, meta.version]);

  assert.ok(resolved.length > 0, 'expected app-builder-lib in electron/package-lock.json');

  for (const [pkgPath, version] of resolved) {
    assert.ok(
      gte(parseExactVersion(version), MIN_ELECTRON_BUILDER),
      `${pkgPath} resolves to app-builder-lib ${version}; needs >= ${MIN_ELECTRON_BUILDER.join('.')} ` +
        'for the `security set-key-partition-list -k <keychainPassword>` fix.'
    );
  }
});

// ── 2. windows/arm64 backend build ───────────────────────────────────────────
//
// cmd/reliant registers `reliant forge`, so the shipped backend links
// forge/cli -> internal/cli/debug -> delve/service/debugger on every target.
// Delve supports windows/amd64 only; on windows/arm64 it compiles a sentinel
// package named your_windows_architecture_is_not_supported_by_delve whose sole
// purpose is to break the build.
//
// delve v1.26.3 guarded it with `windows && !amd64 && !arm64` (so arm64 built),
// v1.27.1 with `windows && !amd64 && !(arm64 && exp.winarm64)` (so arm64 needs
// the tag). The forge v0.1.11 -> v0.1.13 bump between v1.7.11 and v1.7.12
// pulled in v1.27.1, and the windows-arm64 leg started failing.
//
// Every path that cross-compiles the backend needs the tag, and there are two:
// the release workflow and scripts/build-electron.sh. Missing it in either one
// reproduces the outage on that path only, which is how it would come back.
const WINARM64_TAG = 'exp.winarm64';

const CROSS_COMPILE_SITES = [
  ['.github/workflows/release.yml', /GOOS="\$TARGET_OS"\s+GOARCH="\$TARGET_ARCH"\s+go build[^\n]*/],
  ['scripts/build-electron.sh', /GOOS=\$goos GOARCH=\$goarch go build(?:[^\n]*\\\n)*[^\n]*/],
];

for (const [relPath, buildCommand] of CROSS_COMPILE_SITES) {
  test(`${relPath} builds the backend with -tags ${WINARM64_TAG}`, () => {
    const source = fs.readFileSync(path.join(REPO_ROOT, relPath), 'utf8');
    const match = source.match(buildCommand);

    assert.ok(
      match,
      `could not find the backend cross-compile command in ${relPath}. ` +
        'If it moved, update this test rather than deleting it — it is the only ' +
        'thing standing between a delve bump and a Windows release with no installer.'
    );

    assert.ok(
      match[0].includes(`-tags ${WINARM64_TAG}`),
      `${relPath} cross-compiles the backend without \`-tags ${WINARM64_TAG}\`. ` +
        'The windows/arm64 leg then fails with "found packages native (dump_other.go) and ' +
        'your_windows_architecture_is_not_supported_by_delve", because cmd/reliant links ' +
        'Delve through `reliant forge` and delve >= 1.27 refuses that GOARCH without the tag.'
    );
  });
}

test('the windows-arm64 release target still exists', () => {
  // The delve failure is also "fixable" by dropping the target, which would
  // silently strip Windows-on-ARM support from the product. Dropping it is a
  // product decision; it should not be reachable as a green build fix.
  const workflow = fs.readFileSync(path.join(REPO_ROOT, '.github/workflows/release.yml'), 'utf8');

  assert.match(
    workflow,
    /target_os:\s*windows\s*\n\s*target_arch:\s*arm64/,
    'the windows/arm64 backend matrix leg is gone from release.yml. Removing a shipped ' +
      'platform is a product decision, not a build fix — restore the leg, or remove this ' +
      'test deliberately alongside the Windows ARM64 download links in the release summary.'
  );
});
