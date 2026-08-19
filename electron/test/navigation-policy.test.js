const test = require("node:test");
const assert = require("node:assert");
const { shouldOpenExternally } = require("../src/navigation-policy");

const APP = "http://127.0.0.1:5183/chat/abc";

test("same-origin route changes stay in the app", () => {
  // The regression: a SPA redirect to /auth is a different URL at the same
  // origin. Externalizing it spawned a browser tab per 401.
  assert.strictEqual(shouldOpenExternally("http://127.0.0.1:5183/auth", APP), false);
  assert.strictEqual(
    shouldOpenExternally("http://127.0.0.1:5183/auth?redirect=%2Fsettings", APP),
    false,
  );
  assert.strictEqual(shouldOpenExternally("http://127.0.0.1:5183/", APP), false);
});

test("navigating to the identical URL stays in the app", () => {
  assert.strictEqual(shouldOpenExternally(APP, APP), false);
});

test("outbound links still go to the system browser", () => {
  assert.strictEqual(shouldOpenExternally("https://github.com/login", APP), true);
  assert.strictEqual(shouldOpenExternally("https://reliantlabs.io", APP), true);
});

test("a different port or host is a different origin", () => {
  // Each worktree runs its own frontend; port 5184 is a different instance and
  // genuinely does not belong in this window.
  assert.strictEqual(shouldOpenExternally("http://127.0.0.1:5184/auth", APP), true);
  assert.strictEqual(shouldOpenExternally("http://localhost:5183/auth", APP), true);
});

test("scheme changes are external", () => {
  assert.strictEqual(shouldOpenExternally("https://127.0.0.1:5183/auth", APP), true);
});

test("non-http schemes are handed to the OS", () => {
  assert.strictEqual(shouldOpenExternally("mailto:support@reliantlabs.io", APP), true);
  assert.strictEqual(shouldOpenExternally("file:///etc/passwd", APP), true);
});

test("unparseable targets are treated as external", () => {
  assert.strictEqual(shouldOpenExternally("not a url", APP), true);
  assert.strictEqual(shouldOpenExternally("", APP), true);
});

test("an unknown current URL keeps navigation in-app", () => {
  // No origin to compare against — externalizing here would throw the app's own
  // first navigation at the browser.
  assert.strictEqual(shouldOpenExternally("http://127.0.0.1:5183/auth", ""), false);
});

test("app:// routes stay in-app", () => {
  // The packaged renderer is served from app://, so its own route changes are
  // in-app by definition. The generic "not http(s) => external" rule would
  // hand every one of them to shell.openExternal — a browser tab per
  // navigation, with the window stranded on the old route.
  const PACKAGED = "app://bundle/";
  assert.strictEqual(shouldOpenExternally("app://bundle/chat/abc", PACKAGED), false);
  assert.strictEqual(shouldOpenExternally("app://bundle/auth", PACKAGED), false);
  assert.strictEqual(shouldOpenExternally("app://bundle/", PACKAGED), false);
});

test("outbound links still leave a packaged window", () => {
  const PACKAGED = "app://bundle/";
  assert.strictEqual(shouldOpenExternally("https://reliantlabs.io/docs", PACKAGED), true);
  assert.strictEqual(shouldOpenExternally("mailto:support@reliantlabs.io", PACKAGED), true);
});

test("the scheme matches the one app-protocol actually serves", () => {
  // navigation-policy duplicates the scheme name to stay dependency-free;
  // this is the assertion that keeps the two from drifting.
  const { APP_ORIGIN } = require("../src/app-protocol");
  assert.strictEqual(shouldOpenExternally(`${APP_ORIGIN}/chat/abc`, APP_ORIGIN), false);
});
