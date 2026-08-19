const test = require('node:test');
const assert = require('node:assert');
const path = require('node:path');

const {
  APP_INDEX_URL,
  APP_ORIGIN,
  contentTypeFor,
  resolveRequestPath,
} = require('../src/app-protocol');

const WEB_ROOT = path.resolve('/packaged/Resources/web');

// A stand-in filesystem: only these files "exist".
const PRESENT = new Set(
  [
    'index.html',
    'assets/index-D98vjdRH.js',
    'assets/index-BZqrqbFH.css',
    'favicon.svg',
    'fonts/Inter-roman.woff2',
  ].map((p) => path.join(WEB_ROOT, ...p.split('/')))
);

const isFile = (candidate) => PRESENT.has(candidate);

test('serves the root-absolute asset paths the Vite bundle actually requests', () => {
  // The v1.6.3 bug in one assertion. The shipped index.html contains
  // <script src="/assets/index-D98vjdRH.js">, which under file:// resolved to
  // file:///assets/... — the filesystem root — and never loaded, leaving a
  // blank window. Under app:// it must resolve inside the bundle.
  const resolved = resolveRequestPath(
    `${APP_ORIGIN}/assets/index-D98vjdRH.js`,
    WEB_ROOT,
    isFile
  );

  assert.ok(resolved, 'root-absolute asset request must resolve');
  assert.strictEqual(
    resolved.filePath,
    path.join(WEB_ROOT, 'assets', 'index-D98vjdRH.js')
  );
  assert.strictEqual(resolved.isFallback, false);
});

test('serves index.html at the origin root', () => {
  const resolved = resolveRequestPath(APP_INDEX_URL, WEB_ROOT, isFile);
  assert.ok(resolved);
  assert.strictEqual(resolved.filePath, path.join(WEB_ROOT, 'index.html'));
});

test('falls back to index.html for SPA routes so deep links and reloads work', () => {
  // The other half of what file:// could not do: history.pushState under
  // file:// produces file:///chat/abc, and reloading that 404s. A route with
  // no file behind it must return the app shell.
  for (const route of ['/chat/abc', '/auth/github/callback', '/settings']) {
    const resolved = resolveRequestPath(`${APP_ORIGIN}${route}`, WEB_ROOT, isFile);
    assert.ok(resolved, `route ${route} must resolve`);
    assert.strictEqual(resolved.filePath, path.join(WEB_ROOT, 'index.html'));
    assert.strictEqual(resolved.isFallback, true, `${route} should be a fallback`);
  }
});

test('a missing ASSET 404s instead of falling back to index.html', () => {
  // Serving HTML in place of a missing .js is what produces the opaque
  // "Expected a JavaScript module" error. A real 404 names the actual problem.
  const resolved = resolveRequestPath(
    `${APP_ORIGIN}/assets/does-not-exist.js`,
    WEB_ROOT,
    isFile
  );
  assert.strictEqual(resolved, null);
});

test('refuses path traversal that survives URL normalization', () => {
  // Plain "../" is collapsed by the URL parser before it reaches us, so the
  // vector that actually matters is an ENCODED separator: %2f is not a path
  // separator at parse time but becomes one at decode time. These must be
  // refused by the containment check, not by the parser.
  const escapes = [
    `${APP_ORIGIN}/..%2f..%2fetc/passwd`,
    `${APP_ORIGIN}/%2e%2e%2f%2e%2e%2fetc/passwd`,
    `${APP_ORIGIN}/assets%2f..%2f..%2f..%2fetc/passwd`,
  ];
  for (const url of escapes) {
    assert.strictEqual(
      resolveRequestPath(url, WEB_ROOT, () => true),
      null,
      `${url} must be refused`
    );
  }
});

test('a sibling directory sharing a name prefix is outside the root', () => {
  // Guards the containment check specifically: "/packaged/Resources/web-backup"
  // must not pass a naive startsWith("/packaged/Resources/web") test. Reached
  // via an encoded separator, since a plain "../" would be normalized away.
  const resolved = resolveRequestPath(
    `${APP_ORIGIN}/..%2fweb-backup%2fsecret.txt`,
    WEB_ROOT,
    () => true
  );
  assert.strictEqual(resolved, null);
});

test('decodes percent-encoded paths, and refuses malformed ones', () => {
  const withSpace = path.join(WEB_ROOT, 'fonts', 'Inter roman.woff2');
  const resolved = resolveRequestPath(
    `${APP_ORIGIN}/fonts/Inter%20roman.woff2`,
    WEB_ROOT,
    (c) => c === withSpace
  );
  assert.ok(resolved);
  assert.strictEqual(resolved.filePath, withSpace);

  assert.strictEqual(
    resolveRequestPath(`${APP_ORIGIN}/assets/%zz.js`, WEB_ROOT, isFile),
    null
  );
});

test('serves JavaScript with a MIME type the module loader accepts', () => {
  // A module script served as anything else is rejected outright, which
  // reproduces the blank window this scheme exists to prevent.
  for (const file of ['index.js', 'chunk.mjs']) {
    assert.strictEqual(contentTypeFor(file), 'text/javascript');
  }
  assert.strictEqual(contentTypeFor('index.html'), 'text/html');
  assert.strictEqual(contentTypeFor('index.css'), 'text/css');
  assert.strictEqual(contentTypeFor('Inter.woff2'), 'font/woff2');
  assert.strictEqual(contentTypeFor('map.bin'), 'application/octet-stream');
});
