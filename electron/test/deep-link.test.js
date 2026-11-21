// reliant:// links (src/deep-link.js): which open an app page, which open a
// project, and which are refused. A deep link is input from outside the app,
// so every way to smuggle another origin or a page outside the allowlist in
// has a row.

const test = require('node:test');
const assert = require('node:assert/strict');

const { appRouteOf, parseDeepLink } = require('../src/deep-link');

const link = (params) => `reliant://open?${new URLSearchParams(params)}`;

test('opens a chat, a thread and the new-chat view, in either URL form', () => {
  for (const route of [
    '/c/chat-1',
    '/c/chat-1/t/thread-2',
    '/c/chat-1?panel=changes&view=detailed',
    '/o/acme/c/chat-1',
    '/o/acme/c/chat-1/t/thread-2#m-9',
    '/new',
    '/new?project=p-1&machine=d-1',
    '/o/acme/new?project=p-1',
  ]) {
    assert.deepEqual(parseDeepLink(link({ url: route })), { kind: 'route', route }, route);
  }
});

test('refuses anything that is not a page of this app on the allowlist', () => {
  for (const url of [
    '',
    'c/chat-1',
    '//evil.example/c/1',
    '/\\evil.example',
    '\\\\evil.example',
    'https://evil.example/c/1',
    'javascript:alert(1)',
    '/c/1\n/x',
    '/settings/account',
    '/auth',
    '/newish',
    '/m/chats/1',
  ]) {
    assert.equal(appRouteOf(url), null, JSON.stringify(url));
    assert.notEqual(parseDeepLink(link({ url })).kind, 'route', JSON.stringify(url));
  }
});

test('reads an encoded `//` as the path it decodes to, and refuses it', () => {
  assert.equal(parseDeepLink('reliant://open?url=%2F%2Fevil.example%2Fc%2F1').kind, 'ignored');
});

test('keeps the project link working as it always has', () => {
  assert.deepEqual(parseDeepLink(link({ path: '/Users/me/code/app' })), { kind: 'project', path: '/Users/me/code/app' });
  // Decoded once more, as main.js always read it.
  assert.deepEqual(parseDeepLink('reliant://open?path=%252FUsers%252Fme'), { kind: 'project', path: '/Users/me' });
});

test('prefers a valid url over a path, and falls back to the path when the url is refused', () => {
  assert.deepEqual(parseDeepLink(link({ url: '/c/1', path: '/x' })), { kind: 'route', route: '/c/1' });
  assert.deepEqual(parseDeepLink(link({ url: '//evil', path: '/x' })), { kind: 'project', path: '/x' });
});

test('ignores other hosts, schemes and empty links', () => {
  assert.equal(parseDeepLink('reliant://settings?url=/c/1').kind, 'ignored');
  assert.equal(parseDeepLink('https://open?url=/c/1').kind, 'ignored');
  assert.equal(parseDeepLink('reliant://open').kind, 'ignored');
  assert.equal(parseDeepLink('not a url').kind, 'ignored');
});
