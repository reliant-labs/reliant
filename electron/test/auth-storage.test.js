const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const {
  AuthStorage,
  AUTH_LOAD_OK,
  AUTH_LOAD_EMPTY,
  AUTH_LOAD_UNREADABLE,
  AUTH_FAILURE_DECRYPT_FAILED,
  AUTH_FAILURE_CORRUPT,
  AUTH_FAILURE_INVALID,
  AUTH_FAILURE_ENCRYPTION_UNAVAILABLE,
} = require('../src/auth-storage');

// ---------------------------------------------------------------------------
// Why these tests exist
// ---------------------------------------------------------------------------
//
// Measured in a packaged build (~/Library/Logs/reliant-local/main.log): six
// launches logged
//
//   [AuthStorage] Failed to decrypt auth data: Error while decrypting the
//   ciphertext provided to safeStorage.decryptString.
//
// and the macOS keychain item "reliant-local Safe Storage" has cdat
// 20260826224228Z — 18:42:28 local, the exact timestamp of the LAST such
// failure. The item is deleted and recreated on every prod-local rebuild, and
// each recreation mints a NEW random key, so every blob written by a previous
// key is permanently undecryptable.
//
// The bug was not the decrypt failure; it was that the failure was reported as
// `null`, identical to "no session stored". That is what let the app run
// half-authenticated: signed in per the in-memory store, tokenless on the wire.
//
// A fake safeStorage reproduces key loss exactly — encrypt under key A, then
// swap to key B and read — with no keychain involvement at all.

/** safeStorage double whose key can be rotated to simulate keychain loss. */
function makeFakeSafeStorage({ available = true, key = 'key-A' } = {}) {
  const state = { available, key };
  return {
    state,
    rotateKey: (next) => { state.key = next; },
    isEncryptionAvailable: () => state.available,
    encryptString: (plain) => Buffer.from(`${state.key}:${plain}`, 'utf8'),
    decryptString: (buf) => {
      const raw = buf.toString('utf8');
      const prefix = `${state.key}:`;
      if (!raw.startsWith(prefix)) {
        // Mirrors Electron's real message so the test fails for the right reason.
        throw new Error(
          'Error while decrypting the ciphertext provided to safeStorage.decryptString.'
        );
      }
      return raw.slice(prefix.length);
    },
    getSelectedStorageBackend: () => 'fake',
  };
}

function makeStorage(overrides = {}) {
  const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'reliant-auth-test-'));
  const safeStorage = overrides.safeStorage || makeFakeSafeStorage();
  const storage = new AuthStorage({
    app: { getPath: () => userData },
    safeStorage,
  });
  return { storage, safeStorage, userData };
}

const validSession = () => ({
  access_token: 'access-token-value',
  refresh_token: 'refresh-token-value',
  expires_at: Math.floor(Date.now() / 1000) + 3600,
  user: { id: 'e08d19f2-50b1-4e2e-babd-d78ac2f49269' },
});

// ---------------------------------------------------------------------------
// The core regression: key loss must be distinguishable from "no session"
// ---------------------------------------------------------------------------

test('a session written under a lost key reads as unreadable, not as empty', () => {
  const { storage, safeStorage } = makeStorage();

  assert.equal(storage.saveAuth(validSession()), true);
  assert.equal(storage.readStoredAuth().status, AUTH_LOAD_OK);

  // The keychain item was deleted and recreated: same file, brand new key.
  safeStorage.rotateKey('key-B');

  const result = storage.readStoredAuth();

  // This is the assertion the old implementation could not satisfy: it
  // returned a bare null, which is byte-for-byte what "never signed in"
  // returns, so the renderer had no way to tell a dead session from no session.
  assert.equal(result.status, AUTH_LOAD_UNREADABLE);
  assert.notEqual(result.status, AUTH_LOAD_EMPTY);
  assert.equal(result.reason, AUTH_FAILURE_DECRYPT_FAILED);
  assert.equal(result.session, null);
});

test('never having signed in reads as empty, not as a failure', () => {
  const { storage } = makeStorage();

  const result = storage.readStoredAuth();

  assert.equal(result.status, AUTH_LOAD_EMPTY);
  assert.equal(result.session, null);
  assert.equal(storage.takeLoadFailure(), null);
});

test('an undecryptable blob is cleared exactly once, not re-read forever', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');

  const first = storage.readStoredAuth();
  assert.equal(first.status, AUTH_LOAD_UNREADABLE);
  assert.equal(first.cleared, true);
  assert.equal(fs.existsSync(storage.getAuthPath()), false);

  // The dead blob is gone, so the next read is an ordinary "not signed in"
  // rather than another decrypt failure. Before this fix the file survived and
  // every later read threw again, holding the app in a half-authenticated
  // state indefinitely.
  const second = storage.readStoredAuth();
  assert.equal(second.status, AUTH_LOAD_EMPTY);
});

test('an undecryptable blob is reported as unrecoverable so retrying is pointless', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');

  assert.equal(storage.readStoredAuth().recoverable, false);
});

// ---------------------------------------------------------------------------
// The transient/terminal split — clearing the wrong one signs out a good user
// ---------------------------------------------------------------------------

test('an unavailable keystore is recoverable and never deletes the session', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());

  // Linux keyring locked / not yet started. The ciphertext is still good.
  safeStorage.state.available = false;

  const result = storage.readStoredAuth();
  assert.equal(result.status, AUTH_LOAD_UNREADABLE);
  assert.equal(result.reason, AUTH_FAILURE_ENCRYPTION_UNAVAILABLE);
  assert.equal(result.recoverable, true);
  assert.equal(result.cleared, false);
  assert.equal(fs.existsSync(storage.getAuthPath()), true);

  // And once the keyring unlocks, the very same file loads normally.
  safeStorage.state.available = true;
  assert.equal(storage.readStoredAuth().status, AUTH_LOAD_OK);
});

test('a decryptable blob that is not JSON is terminal and cleared', () => {
  const { storage, safeStorage } = makeStorage();
  storage.ensureAuthDirectory();
  fs.writeFileSync(storage.getAuthPath(), safeStorage.encryptString('{truncated'));

  const result = storage.readStoredAuth();
  assert.equal(result.reason, AUTH_FAILURE_CORRUPT);
  assert.equal(result.recoverable, false);
  assert.equal(fs.existsSync(storage.getAuthPath()), false);
});

test('a session missing its refresh token is terminal and cleared', () => {
  const { storage, safeStorage } = makeStorage();
  storage.ensureAuthDirectory();
  fs.writeFileSync(
    storage.getAuthPath(),
    safeStorage.encryptString(JSON.stringify({ access_token: 'only-access' }))
  );

  const result = storage.readStoredAuth();
  assert.equal(result.reason, AUTH_FAILURE_INVALID);
  assert.equal(result.recoverable, false);
  assert.equal(fs.existsSync(storage.getAuthPath()), false);
});

test('an expired session still loads so Supabase can refresh it', () => {
  const { storage } = makeStorage();
  storage.saveAuth({
    ...validSession(),
    expires_at: Math.floor(Date.now() / 1000) - 3600,
  });

  const result = storage.readStoredAuth();
  assert.equal(result.status, AUTH_LOAD_OK);
  assert.equal(result.session.refresh_token, 'refresh-token-value');
});

// ---------------------------------------------------------------------------
// The latch — the main process usually finds the dead blob before the renderer
// ---------------------------------------------------------------------------

test('a failure found by an earlier read is still reportable to the renderer', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');

  // Main process reads first (Statsig user id at startup) and clears the blob.
  assert.equal(storage.loadStoredAuth(), null);

  // The renderer's later auth:load sees a clean `empty`, so without the latch
  // the incident would be invisible to it — exactly the read ordering in the
  // captured log, where the decrypt failure is logged during Statsig init.
  assert.equal(storage.readStoredAuth().status, AUTH_LOAD_EMPTY);

  const failure = storage.takeLoadFailure();
  assert.ok(failure, 'the earlier failure must still be reportable');
  assert.equal(failure.reason, AUTH_FAILURE_DECRYPT_FAILED);
  assert.equal(failure.recoverable, false);
});

test('taking a failure consumes it so one incident is reported once', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');
  storage.readStoredAuth();

  assert.ok(storage.takeLoadFailure());
  assert.equal(storage.takeLoadFailure(), null);
});

test('signing in again clears a pending failure', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');
  storage.readStoredAuth();
  assert.ok(storage.lastLoadFailure);

  // Re-auth writes a blob under the CURRENT key, so the incident is resolved.
  assert.equal(storage.saveAuth(validSession()), true);
  assert.equal(storage.lastLoadFailure, null);
  assert.equal(storage.readStoredAuth().status, AUTH_LOAD_OK);
});

test('signing out clears a pending failure', () => {
  const { storage, safeStorage } = makeStorage();
  storage.saveAuth(validSession());
  safeStorage.rotateKey('key-B');
  storage.readStoredAuth();

  assert.equal(storage.clearAuth(), true);
  assert.equal(storage.lastLoadFailure, null);
});

// ---------------------------------------------------------------------------
// Back-compat: the session-or-null callers must be unchanged
// ---------------------------------------------------------------------------

test('loadStoredAuth still answers session-or-null for its existing callers', () => {
  const { storage, safeStorage } = makeStorage();

  assert.equal(storage.loadStoredAuth(), null);

  storage.saveAuth(validSession());
  assert.equal(storage.loadStoredAuth().access_token, 'access-token-value');

  safeStorage.rotateKey('key-B');
  assert.equal(storage.loadStoredAuth(), null);
});
