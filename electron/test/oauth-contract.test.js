/**
 * The redirect URI the desktop app ADVERTISES to a provider must match the
 * socket it actually BINDS, and must match what the Go side advertises for the
 * same provider.
 *
 * The reported bug looked exactly like a violation of the first rule: the
 * browser landed on `http://localhost:58868/callback` and got
 * ERR_CONNECTION_REFUSED, while the only listener in the log was on port 57656
 * at `/auth/callback`. (It turned out to be one listener torn down by an RPC
 * timeout plus a different flow's listener — not a path mismatch — but nothing
 * in the code ASSERTED the agreement, so it could not be ruled out by reading
 * it. These tests assert it.)
 *
 * The second rule is why the paths must NOT be unified: `/callback` is what
 * Anthropic has registered and `/auth/callback` is what OpenAI has registered.
 * Editing either to match the other trades a cosmetic inconsistency for a
 * redirect_uri the provider rejects.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');

const {
  inferCallbackContract,
  PROVIDER_CALLBACK_CONTRACTS,
  LISTEN_HOST,
} = require('../src/oauth-contract');
const providerLogin = require('../src/oauth-provider-login');

const CLAUDE_AUTHORIZE = 'https://claude.ai/oauth/authorize?redirect_uri={redirect_uri}';
const CODEX_AUTHORIZE = 'https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}';

function httpGet(url) {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        res.resume();
        res.on('end', () => resolve(res.statusCode));
      })
      .on('error', reject);
  });
}

test('Claude keeps the /callback path Anthropic registered', () => {
  assert.equal(inferCallbackContract(CLAUDE_AUTHORIZE).callbackPath, '/callback');
});

test('Codex keeps the /auth/callback path and fixed port OpenAI registered', () => {
  const contract = inferCallbackContract(CODEX_AUTHORIZE);
  assert.equal(contract.callbackPath, '/auth/callback');
  assert.equal(contract.fixedPort, 1455);
});

test('an unknown provider falls back rather than throwing', () => {
  // A template we cannot parse must still produce a usable listener; letting
  // the provider reject the request is a clearer failure than an exception
  // here with no provider context in it.
  const contract = inferCallbackContract('not a url at all');
  assert.equal(contract.callbackPath, '/auth/callback');
  assert.equal(contract.fixedPort, 0);
});

test('provider matching reads the HOST, not the whole URL', () => {
  // A state or redirect parameter can contain another provider's domain. A
  // substring match over the full URL would then pick the wrong path and hand
  // the provider a redirect_uri it never registered.
  const contract = inferCallbackContract(
    'https://auth.openai.com/oauth/authorize?state=https%3A%2F%2Fclaude.ai%2Ffoo',
  );
  assert.equal(contract.callbackPath, '/auth/callback');
});

test('the advertised redirect URI is the one actually bound and served', async (t) => {
  // The whole bug in one assertion: take the URI handed to the provider, and
  // send the callback to THAT — host, port and path together, with nothing
  // reconstructed by the test. Pre-fix there was no local listener at all and
  // this could not pass.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  const url = new URL(redirectUri);
  assert.equal(url.pathname, '/callback', 'must advertise the path Anthropic registered');
  assert.ok(Number(url.port) > 0, `expected a bound port, got ${url.port}`);

  const status = await httpGet(`${redirectUri}?code=abc123&state=xyz`);
  assert.equal(status, 200, 'the advertised URI must be served by a live listener');

  const result = await providerLogin.waitForProviderLogin(flowId);
  assert.equal(result.code, 'abc123');
  assert.equal(result.state, 'xyz');
  assert.equal(result.redirectUri, redirectUri);
});

test('the authorize URL carries the bound redirect URI, encoded', async (t) => {
  const { flowId, redirectUri, authorizeUrl } =
    await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  // The placeholder must be gone and the value must survive a round trip
  // through the query string — an unencoded URI would truncate at its own `?`.
  assert.ok(!authorizeUrl.includes('{redirect_uri}'), authorizeUrl);
  assert.equal(new URL(authorizeUrl).searchParams.get('redirect_uri'), redirectUri);
});

test('the listener binds loopback, but advertises localhost', async (t) => {
  // Providers allow-list the spelling `localhost`; binding `localhost` can
  // resolve to ::1 first and miss an IPv4 request. Both halves are required,
  // and they are deliberately different strings.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  assert.equal(new URL(redirectUri).hostname, 'localhost');
  assert.equal(LISTEN_HOST, '127.0.0.1');

  // Bound on 127.0.0.1 regardless of what is advertised.
  const status = await httpGet(
    `http://127.0.0.1:${new URL(redirectUri).port}/callback?code=bound`,
  );
  assert.equal(status, 200);
});

test('a request to the OTHER provider path is not accepted', async (t) => {
  // Guards against a future "let us just accept both paths" "fix", which would
  // hide a real disagreement between the advertised URI and the listener.
  const { flowId, redirectUri } = await providerLogin.startProviderLogin(CLAUDE_AUTHORIZE);
  t.after(() => providerLogin.cancelProviderLogin(flowId, 'test cleanup'));

  const { port } = new URL(redirectUri);
  assert.equal(await httpGet(`http://127.0.0.1:${port}/auth/callback?code=x`), 404);
});

test('the callback paths match the Go source they mirror', () => {
  // `reliant auth serve` (the browser build's receiver) derives its redirect
  // URI from oauthcallback.InferConfig. Desktop and web must hand the provider
  // the SAME URI, so a change on the Go side has to fail here rather than
  // silently break one of the two surfaces.
  const goSource = fs.readFileSync(
    path.join(__dirname, '../../internal/auth/oauthcallback/oauthcallback.go'),
    'utf8',
  );

  for (const contract of PROVIDER_CALLBACK_CONTRACTS) {
    // Each provider is a `case strings.Contains(host, "<host>"):` block;
    // read the CallbackPath assigned inside it.
    const block = goSource.match(
      new RegExp(
        `strings\\.Contains\\(host,\\s*"${contract.hostMatch.replace(/\./g, '\\.')}"\\)[\\s\\S]*?cfg\\.CallbackPath\\s*=\\s*"([^"]+)"`,
      ),
    );

    assert.ok(block, `no CallbackPath found for ${contract.hostMatch} in oauthcallback.go`);
    assert.equal(
      block[1],
      contract.callbackPath,
      `Go and Electron disagree on the callback path for ${contract.hostMatch}`,
    );
  }
});
