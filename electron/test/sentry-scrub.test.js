// Pins what Electron's main process may hand Sentry: the options main.js passes
// to Sentry.init (sentryMainOptions) are the privacy boundary, so the test
// drives those hooks with realistic payloads.

const test = require('node:test');
const assert = require('node:assert/strict');

const { sentryMainOptions, boundMessage, TRUNCATED_MARKER } = require('../src/sentry-scrub');

const PROMPT = 'Please refactor the payment reconciliation module';
const TOOL_OUTPUT = 'STRIPE_SECRET=sk_live_toolOutputLeak';
const TOKEN = 'rlat_0123456789abcdefghij';
const OAUTH_CODE = 'OAUTHCODE123';
const EMAIL = 'founder@example.com';
const IP = '203.0.113.9';
const CHAT_ID = '3f2a8c1e-9b7d-4e2f-8a6b-1c2d3e4f5a6b';
const FORBIDDEN = [PROMPT, TOOL_OUTPUT, 'sk_live_toolOutputLeak', TOKEN, OAUTH_CODE, EMAIL, IP];

function options(enabled = true) {
  return sentryMainOptions({
    dsn: 'https://public@o0.ingest.sentry.io/0',
    environment: 'production',
    release: 'reliant@1.0.0',
    isEnabled: () => enabled,
  });
}

function assertNoUserContent(payload) {
  const wire = JSON.stringify(payload);
  for (const value of FORBIDDEN) {
    assert.ok(!wire.includes(value), `Sentry payload contains ${JSON.stringify(value)}: ${wire}`);
  }
}

test('opting out of crash reporting drops every event', () => {
  const opts = options(false);
  assert.equal(opts.beforeSend({ message: 'x' }), null);
  assert.equal(opts.beforeSendTransaction({ type: 'transaction' }), null);
});

test('error events keep type, stack and identifiers but no user content', () => {
  const event = {
    message: `daemon spawn failed\n${TOOL_OUTPUT}`,
    exception: {
      values: [{
        type: 'Error',
        value: `spawn reliant daemon: exit 1: ${TOOL_OUTPUT}\n${PROMPT}`,
        stacktrace: { frames: [{ function: 'startDaemon', lineno: 120, vars: { env: TOKEN } }] },
        mechanism: { type: 'onuncaughtexception', handled: false, data: { reason: PROMPT } },
      }],
    },
    extra: { chatId: CHAT_ID, durationMs: 40, stdout: TOOL_OUTPUT, args: ['--token', TOKEN] },
    tags: { 'event.process': 'browser', 'exit.reason': 'crashed', note: PROMPT },
    contexts: {
      os: { name: 'macOS', version: '15.0' },
      electron: { crashed_url: `app://-/index.html?code=${OAUTH_CODE}#/chat/${CHAT_ID}`, details: { reason: 'crashed', exitCode: 11 } },
    },
    request: { url: `app://-/auth?code=${OAUTH_CODE}`, headers: { Authorization: `Bearer ${TOKEN}` }, data: PROMPT },
    breadcrumbs: [
      { category: 'console', level: 'log', message: PROMPT, data: { arguments: [PROMPT] } },
      { category: 'electron', message: 'app.will-quit', data: { trigger: 'menu' } },
    ],
    user: { id: 'user-8d7f', email: EMAIL, ip_address: IP },
  };

  const sent = options().beforeSend(event, {});
  assert.ok(sent);
  assertNoUserContent(sent);

  const ex = sent.exception.values[0];
  assert.equal(ex.type, 'Error');
  assert.ok(ex.value.startsWith('spawn reliant daemon: exit 1: '), ex.value);
  assert.ok(ex.value.endsWith(TRUNCATED_MARKER));
  assert.equal(ex.stacktrace.frames[0].function, 'startDaemon');
  assert.equal(ex.stacktrace.frames[0].vars, undefined);
  assert.deepEqual(sent.extra, { chatId: CHAT_ID, durationMs: 40 });
  assert.deepEqual(sent.tags, { 'event.process': 'browser', 'exit.reason': 'crashed' });
  assert.deepEqual(sent.contexts.os, { name: 'macOS', version: '15.0' });
  assert.deepEqual(sent.contexts.electron, { crashed_url: 'app://-/index.html' });
  assert.deepEqual(sent.request, { url: 'app://-/auth' });
  assert.deepEqual(sent.user, { id: 'user-8d7f' });
  assert.deepEqual(sent.breadcrumbs[0], { category: 'console', level: 'log' });
  assert.deepEqual(sent.breadcrumbs[1], { category: 'electron', message: 'app.will-quit', data: { trigger: 'menu' } });
});

test('console breadcrumbs keep level and category but drop the logged arguments', () => {
  const crumb = options().beforeBreadcrumb({
    category: 'console',
    level: 'warning',
    timestamp: 1700000000,
    message: `[Daemon] stderr: ${TOOL_OUTPUT}`,
    data: { arguments: ['[Daemon] stderr:', TOOL_OUTPUT], logger: 'console' },
  });
  assert.deepEqual(crumb, { category: 'console', level: 'warning', timestamp: 1700000000 });
});

test('network breadcrumbs lose query strings', () => {
  const crumb = options().beforeBreadcrumb({
    category: 'net.request',
    data: { method: 'GET', url: `https://api.reliantapi.com/oauth?code=${OAUTH_CODE}`, status_code: 200 },
  });
  assert.deepEqual(crumb.data, { method: 'GET', url: 'https://api.reliantapi.com/oauth', status_code: 200 });
});

test('boundMessage redacts credentials and keeps identifiers', () => {
  assert.equal(boundMessage(`401: Authorization: Bearer ${TOKEN}`), '401: Authorization: [redacted]');
  assert.equal(boundMessage(`chat ${CHAT_ID} not found`), `chat ${CHAT_ID} not found`);
  assert.equal(boundMessage('connect postgres://admin:s3cret@db/x'), 'connect postgres://[redacted]@db/x');
});
