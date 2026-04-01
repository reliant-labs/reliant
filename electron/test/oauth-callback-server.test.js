const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const { createOAuthCallbackServerManager } = require('../src/oauth-callback-server');

function httpGet(url) {
  return new Promise((resolve, reject) => {
    const req = http.get(url, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (chunk) => {
        body += chunk;
      });
      res.on('end', () => {
        resolve({ statusCode: res.statusCode, body });
      });
    });

    req.on('error', reject);
  });
}

test('OAuth callback server captures callback and shuts down after single use', async () => {
  let receivedCallbackUrl = null;

  const manager = createOAuthCallbackServerManager({
    timeoutMs: 2000,
    logger: { info() {}, warn() {}, error() {} },
    onCallback: (callbackUrl) => {
      receivedCallbackUrl = callbackUrl;
    },
  });

  const redirectUrl = await manager.start();
  const redirect = new URL(redirectUrl);
  const callbackUrl = `${redirectUrl}?code=test-code-123&state=abc`;

  const response = await httpGet(callbackUrl);
  assert.equal(response.statusCode, 200);
  assert.match(response.body, /Successfully signed in\. You can close this tab\./i);

  assert.ok(receivedCallbackUrl, 'callback URL should be captured');
  assert.equal(
    receivedCallbackUrl,
    `http://127.0.0.1/auth/callback?code=test-code-123&state=abc`
  );

  await new Promise((resolve) => setTimeout(resolve, 80));

  await assert.rejects(
    httpGet(`http://127.0.0.1:${redirect.port}/auth/callback?code=second`),
    /ECONNREFUSED|socket hang up/
  );

  manager.stop('test_cleanup');
});

test('OAuth callback server times out and invokes timeout hook', async () => {
  let timeoutTriggered = false;

  const manager = createOAuthCallbackServerManager({
    timeoutMs: 50,
    logger: { info() {}, warn() {}, error() {} },
    onTimeout: () => {
      timeoutTriggered = true;
    },
  });

  const redirectUrl = await manager.start();
  const { port } = new URL(redirectUrl);

  await new Promise((resolve) => setTimeout(resolve, 120));

  assert.equal(timeoutTriggered, true, 'timeout hook should be invoked');

  await assert.rejects(
    httpGet(`http://127.0.0.1:${port}/auth/callback?code=late`),
    /ECONNREFUSED|socket hang up/
  );

  manager.stop('test_cleanup');
});
