const test = require("node:test");
const assert = require("node:assert");
const http = require("node:http");

const loopback = require("../src/oauth-loopback");

/** GET a URL and resolve once the response completes. */
function get(url) {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        res.resume();
        res.on("end", () => resolve(res.statusCode));
      })
      .on("error", reject);
  });
}

test.afterEach(() => {
  loopback.closeActive("test cleanup");
});

test("hands back a loopback redirect URI on an OS-assigned port", async () => {
  const { redirectUri } = await loopback.startOAuthRedirect("app://bundle", () => {});

  const url = new URL(redirectUri);
  assert.strictEqual(url.hostname, "127.0.0.1");
  assert.strictEqual(url.pathname, loopback.CALLBACK_PATH);
  // Port 0 => OS-assigned, so it must be a real bound port, never a fixed one
  // that could collide with another instance or another worktree's stack.
  assert.ok(Number(url.port) > 0, `expected a bound port, got ${url.port}`);
});

test("delivers the callback query string into the app origin, unparsed", async () => {
  let delivered = null;
  const { redirectUri } = await loopback.startOAuthRedirect("app://bundle", (url) => {
    delivered = url;
  });

  await get(`${redirectUri}?code=abc123&state=xyz`);

  // The code must arrive at the app's OWN /auth/callback route, so the
  // renderer that holds the PKCE verifier can exchange it with the same
  // component the browser build uses. This process must not exchange anything.
  assert.strictEqual(delivered, "app://bundle/auth/callback?code=abc123&state=xyz");
});

test("passes provider errors through rather than swallowing them", async () => {
  let delivered = null;
  const { redirectUri } = await loopback.startOAuthRedirect("app://bundle", (url) => {
    delivered = url;
  });

  await get(`${redirectUri}?error=access_denied&error_description=User%20cancelled`);

  // A denied consent has to reach the renderer too: /auth/callback renders the
  // error. Dropping it would leave the UI spinning forever.
  assert.ok(delivered.includes("error=access_denied"), delivered);
  assert.ok(delivered.includes("error_description=User%20cancelled"), delivered);
});

test("closes the listener after one callback", async () => {
  const { redirectUri } = await loopback.startOAuthRedirect("app://bundle", () => {});

  await get(`${redirectUri}?code=abc123`);

  // Single-use: leaving the port open would let a later stray redirect land on
  // a flow nobody is waiting for.
  assert.strictEqual(loopback.__getActive(), null);
});

test("ignores paths other than the callback path", async () => {
  let delivered = null;
  const { redirectUri } = await loopback.startOAuthRedirect("app://bundle", (url) => {
    delivered = url;
  });
  const base = new URL(redirectUri).origin;

  assert.strictEqual(await get(`${base}/favicon.ico`), 404);
  assert.strictEqual(delivered, null);
  // Still listening: a stray request must not consume the pending flow.
  assert.notStrictEqual(loopback.__getActive(), null);
});

test("reuses the in-flight listener rather than opening a second port", async () => {
  const first = await loopback.startOAuthRedirect("app://bundle", () => {});
  let secondDelivered = null;
  const second = await loopback.startOAuthRedirect("app://bundle", (url) => {
    secondDelivered = url;
  });

  // A user who starts Google, changes their mind and clicks GitHub would
  // otherwise strand the first port until it timed out, and whichever redirect
  // arrived first would resolve against the wrong listener.
  assert.strictEqual(second.redirectUri, first.redirectUri);

  await get(`${first.redirectUri}?code=second-flow`);
  // The most recent caller is the one waiting, so it must receive the code.
  assert.ok(secondDelivered.includes("code=second-flow"), secondDelivered);
});

test("redirects into the dev server origin when not packaged", async () => {
  let delivered = null;
  const { redirectUri } = await loopback.startOAuthRedirect("http://localhost:3000", (url) => {
    delivered = url;
  });

  await get(`${redirectUri}?code=abc123`);

  assert.strictEqual(delivered, "http://localhost:3000/auth/callback?code=abc123");
});
