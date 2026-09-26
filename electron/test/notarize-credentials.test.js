// Notarization cannot be tested end to end without real Apple credentials and a
// macOS runner, so this pins the parts that are pure logic — which is where the
// v1.7.14 macOS outage actually lived.
//
// That release shipped Windows and Linux but not macOS. Notarization failed with
//   HTTP status code: 403. A required agreement is missing or has expired.
// because the Apple ACCOUNT HOLDER changed and the pipeline authenticated as a
// specific person. Two separate defects made it worse than it had to be:
//
//   1. The retry classifier matched 'Authentication failed' / 'Invalid
//      credentials' / '401' but NOT 403, so an unrecoverable identity error was
//      treated as transient and burned all three attempts.
//   2. The message that finally surfaced blamed the network and Apple's servers,
//      pointing the on-call engineer away from the real cause.
//
// The tests below cover classification and credential resolution. What they do
// NOT cover — and cannot, here — is whether notarytool accepts the credentials
// we hand it. Only a real signed build on a mac runner proves that.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');

const core = require('../build/notarize-core.js');
const safeHook = require('../build/notarize-safe.js');
const lenientHook = require('../build/notarize.js');

const APPLE_ENV_VARS = [
  'APPLE_API_KEY',
  'APPLE_API_KEY_ID',
  'APPLE_API_ISSUER',
  'APPLE_ID',
  'APPLE_APP_SPECIFIC_PASSWORD',
  'APPLE_TEAM_ID',
  'SKIP_NOTARIZATION'
];

// Clear every Apple variable before setting the ones a case cares about, so a
// developer's own exported APPLE_ID cannot change the result locally.
async function withEnv(vars, fn) {
  const saved = {};
  for (const key of APPLE_ENV_VARS) {
    saved[key] = process.env[key];
    delete process.env[key];
  }
  Object.assign(process.env, vars);
  try {
    return await fn();
  } finally {
    for (const key of APPLE_ENV_VARS) {
      if (saved[key] === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = saved[key];
      }
    }
  }
}

function fakeContext(platform) {
  return {
    electronPlatformName: platform,
    appOutDir: path.join(os.tmpdir(), 'reliant-notarize-test'),
    packager: { appInfo: { productFilename: 'Reliant' } }
  };
}

const OUTAGE_MESSAGE =
  'HTTP status code: 403. A required agreement is missing or has expired.';

// Runs one of the afterSign hooks end to end with @electron/notarize replaced
// by a recorder, and returns the options each notarize() call received.
//
// notarize-core.js destructures notarize at require time, so the stub has to be
// in the module cache BEFORE a fresh copy of core (and the hook, which requires
// core) is loaded. The cache is restored afterwards so no other test sees the
// stub.
async function runHookWithStubbedNotarize(hookFile) {
  const notarizeModulePath = require.resolve('@electron/notarize');
  const corePath = require.resolve('../build/notarize-core.js');
  const hookPath = require.resolve(`../build/${hookFile}`);
  const saved = {};
  for (const p of [notarizeModulePath, corePath, hookPath]) {
    saved[p] = require.cache[p];
  }

  const calls = [];
  require.cache[notarizeModulePath] = {
    id: notarizeModulePath,
    filename: notarizeModulePath,
    loaded: true,
    exports: { notarize: async (opts) => { calls.push(opts); } }
  };
  delete require.cache[corePath];
  delete require.cache[hookPath];

  try {
    await require(hookPath).default(fakeContext('darwin'));
  } finally {
    for (const [p, mod] of Object.entries(saved)) {
      if (mod) require.cache[p] = mod;
      else delete require.cache[p];
    }
  }
  return calls;
}

test('the exact v1.7.14 error is treated as an identity failure, not retried', () => {
  const error = new Error(OUTAGE_MESSAGE);
  assert.equal(core.isIdentityError(error), true);
  assert.equal(
    core.isRetryableNotarizationError(error),
    false,
    'a 403 agreement error must not burn retries — the old code retried it three times'
  );
});

test('an identity failure wins over a transient pattern in the same text', () => {
  // A 403 often arrives as an HTML error page, so the JSON-parse failure fires
  // too. Reading that as "the network is flaky" is what sent debugging the
  // wrong way; the identity signal has to take precedence.
  const error = new Error(`${OUTAGE_MESSAGE} Unexpected token < in JSON at position 0`);
  assert.equal(core.isRetryableNotarizationError(error), false);
});

test('classification reads stdout/stderr, not just message', () => {
  // notarytool is a subprocess: the status code lands on stderr while
  // error.message is only "Command failed".
  const error = new Error('Command failed');
  error.stderr = OUTAGE_MESSAGE;
  assert.equal(core.isIdentityError(error), true);
  assert.equal(core.isRetryableNotarizationError(error), false);
});

test('previously-known auth failures stay non-retryable', () => {
  for (const message of [
    'HTTP status code: 401',
    'Authentication failed',
    'Invalid credentials',
    'Unable to authenticate with the App Store Connect API'
  ]) {
    assert.equal(
      core.isRetryableNotarizationError(new Error(message)),
      false,
      `${message} should not be retried`
    );
  }
});

test('genuinely transient failures are still retried', () => {
  for (const message of [
    'ECONNRESET',
    'EAI_AGAIN',
    'The network connection was lost',
    'Failed to staple your application with code: 68',
    "CloudKit's response is inconsistent"
  ]) {
    assert.equal(
      core.isRetryableNotarizationError(new Error(message)),
      true,
      `${message} should be retried`
    );
  }
});

test('an App Store Connect API key is preferred over an Apple ID', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'reliant-key-'));
  const keyPath = path.join(dir, 'AuthKey.p8');
  fs.writeFileSync(keyPath, 'placeholder');

  await withEnv(
    {
      APPLE_API_KEY: keyPath,
      APPLE_API_KEY_ID: 'T9GPZ92M7K',
      APPLE_API_ISSUER: 'c055ca8c-e5a8-4836-b61d-aa5794eeb3f4',
      APPLE_ID: 'person@example.com',
      APPLE_APP_SPECIFIC_PASSWORD: 'abcd-efgh-ijkl-mnop',
      APPLE_TEAM_ID: 'TEAM123456'
    },
    () => {
      const resolved = core.resolveAppleCredentials();
      assert.equal(resolved.strategy, 'api-key');
      assert.deepEqual(resolved.credentials, {
        appleApiKey: keyPath,
        appleApiKeyId: 'T9GPZ92M7K',
        appleApiIssuer: 'c055ca8c-e5a8-4836-b61d-aa5794eeb3f4'
      });
      // Both auth shapes must never be sent at once — @electron/notarize picks
      // a strategy by which keys are present.
      assert.equal('appleId' in resolved.credentials, false);
      resolved.cleanup();
    }
  );

  fs.rmSync(dir, { recursive: true, force: true });
});

test('APPLE_API_KEY holding .p8 CONTENTS is written to a private temp file', async () => {
  // This is the CI shape: a GitHub secret can carry the key's contents but not
  // a path, while notarytool requires a path.
  const pem = '-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49\n-----END PRIVATE KEY-----';

  await withEnv(
    {
      APPLE_API_KEY: pem,
      APPLE_API_KEY_ID: 'T9GPZ92M7K',
      APPLE_API_ISSUER: 'c055ca8c-e5a8-4836-b61d-aa5794eeb3f4'
    },
    () => {
      const resolved = core.resolveAppleCredentials();
      assert.equal(resolved.strategy, 'api-key');

      const keyPath = resolved.credentials.appleApiKey;
      assert.ok(fs.existsSync(keyPath), 'the key should have been materialized on disk');
      assert.equal(
        path.basename(keyPath),
        'AuthKey_T9GPZ92M7K.p8',
        'notarytool is happiest with the AuthKey_<id>.p8 convention'
      );
      assert.equal(fs.readFileSync(keyPath, 'utf8'), `${pem}\n`);
      assert.equal(
        fs.statSync(keyPath).mode & 0o777,
        0o600,
        'a private key must not be world-readable on a shared runner'
      );

      resolved.cleanup();
      assert.equal(
        fs.existsSync(keyPath),
        false,
        'the temp key must be removed once notarization finishes'
      );
    }
  );
});

test('Apple ID remains a working fallback when no API key is configured', async () => {
  await withEnv(
    {
      APPLE_ID: 'person@example.com',
      APPLE_APP_SPECIFIC_PASSWORD: 'abcd-efgh-ijkl-mnop',
      APPLE_TEAM_ID: 'TEAM123456'
    },
    () => {
      const resolved = core.resolveAppleCredentials();
      assert.equal(resolved.strategy, 'apple-id');
      assert.deepEqual(resolved.credentials, {
        appleId: 'person@example.com',
        appleIdPassword: 'abcd-efgh-ijkl-mnop',
        teamId: 'TEAM123456'
      });
      resolved.cleanup();
    }
  );
});

test('API key secrets that do not exist yet (empty strings in CI) fall back to the Apple ID', async () => {
  // What release.yml actually produces before the owner creates the three
  // secrets: `APPLE_API_KEY: ${{ secrets.APPLE_API_KEY }}` on a secret that
  // does not exist exports the variable as an EMPTY STRING, not unset. This is
  // the state the PR is claimed to be safe in, so it is asserted as such —
  // through the release hook, not just the resolver — and with no warning
  // about a partial config, since nothing was configured.
  await withEnv(
    {
      APPLE_API_KEY: '',
      APPLE_API_KEY_ID: '',
      APPLE_API_ISSUER: '',
      APPLE_ID: 'person@example.com',
      APPLE_APP_SPECIFIC_PASSWORD: 'abcd-efgh-ijkl-mnop',
      APPLE_TEAM_ID: 'TEAM123456'
    },
    async () => {
      const resolved = core.resolveAppleCredentials();
      assert.equal(resolved.strategy, 'apple-id');
      resolved.cleanup();

      const warnings = [];
      const originalWarn = console.warn;
      console.warn = (...args) => warnings.push(args.join(' '));
      let calls;
      try {
        calls = await runHookWithStubbedNotarize('notarize-safe.js');
      } finally {
        console.warn = originalWarn;
      }
      assert.equal(calls.length, 1, 'the strict release hook must notarize, not skip or throw');
      assert.equal(calls[0].appleId, 'person@example.com');
      assert.equal(calls[0].appleApiKey, undefined, 'no API-key field may reach notarize()');
      assert.ok(
        !warnings.some((w) => w.includes('Partial App Store Connect API key')),
        'empty secrets are "not configured", not a partial config'
      );
    }
  );
});

test('a PARTIAL API key config falls back instead of failing the release', async () => {
  // The state this repo is in the moment this merges but before the three new
  // secrets exist. Half-configuring the key must not take macOS down again.
  await withEnv(
    {
      APPLE_API_KEY_ID: 'T9GPZ92M7K',
      APPLE_ID: 'person@example.com',
      APPLE_APP_SPECIFIC_PASSWORD: 'abcd-efgh-ijkl-mnop',
      APPLE_TEAM_ID: 'TEAM123456'
    },
    () => {
      const resolved = core.resolveAppleCredentials();
      assert.equal(resolved.strategy, 'apple-id');
      resolved.cleanup();
    }
  );
});

test('an APPLE_API_KEY path that does not exist fails loudly', async () => {
  await withEnv(
    {
      APPLE_API_KEY: path.join(os.tmpdir(), 'reliant-definitely-not-here.p8'),
      APPLE_API_KEY_ID: 'T9GPZ92M7K',
      APPLE_API_ISSUER: 'c055ca8c-e5a8-4836-b61d-aa5794eeb3f4'
    },
    () => {
      assert.throws(() => core.resolveAppleCredentials(), /no file exists at/);
    }
  );
});

test('no credentials at all reports every variable it looked for', async () => {
  await withEnv({}, () => {
    const resolved = core.resolveAppleCredentials();
    assert.equal(resolved.strategy, 'none');
    assert.equal(resolved.credentials, null);
    assert.deepEqual(resolved.missing, [
      'APPLE_API_KEY',
      'APPLE_API_KEY_ID',
      'APPLE_API_ISSUER',
      'APPLE_ID',
      'APPLE_APP_SPECIFIC_PASSWORD',
      'APPLE_TEAM_ID'
    ]);
  });
});

test('the missing-credentials message names BOTH auth paths', () => {
  // The release job fails with this string and nothing else. It has to be
  // self-contained enough to act on.
  for (const token of [
    'APPLE_API_KEY',
    'APPLE_API_KEY_ID',
    'APPLE_API_ISSUER',
    'APPLE_ID',
    'APPLE_APP_SPECIFIC_PASSWORD',
    'APPLE_TEAM_ID'
  ]) {
    assert.ok(
      core.MISSING_CREDENTIALS_MESSAGE.includes(token),
      `missing-credentials message should mention ${token}`
    );
  }
});

test('both hooks are no-ops on non-darwin platforms', async () => {
  await withEnv({}, async () => {
    await safeHook.default(fakeContext('win32'));
    await lenientHook.default(fakeContext('linux'));
  });
});

test('notarize-safe REQUIRES credentials; notarize skips without them', async () => {
  // The distinction is the whole reason two entry points exist: a release must
  // never quietly produce an unnotarized app, a local --dir build may.
  await withEnv({}, async () => {
    await assert.rejects(
      () => safeHook.default(fakeContext('darwin')),
      /Notarization credentials missing/
    );
    await lenientHook.default(fakeContext('darwin'));
  });
});

test('SKIP_NOTARIZATION=true short-circuits even the strict hook', async () => {
  await withEnv({ SKIP_NOTARIZATION: 'true' }, async () => {
    await safeHook.default(fakeContext('darwin'));
  });
});

test('the two hooks share one implementation and cannot drift', () => {
  // They were near-duplicate copies, which is how the 403 gap survived: fixing
  // it in one file left the other wrong. Nothing but a require of the shared
  // core belongs in either.
  for (const file of ['../build/notarize.js', '../build/notarize-safe.js']) {
    const source = fs.readFileSync(path.join(__dirname, file), 'utf8');
    assert.ok(
      source.includes("require('./notarize-core.js')"),
      `${file} must delegate to notarize-core.js`
    );
    assert.equal(
      source.includes("require('@electron/notarize')"),
      false,
      `${file} must not call @electron/notarize directly — that is how the copies drifted`
    );
  }
});
