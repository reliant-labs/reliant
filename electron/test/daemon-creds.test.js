const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const {
  endpointKey,
  readDaemonStore,
  writeDaemonStore,
  upsertEntry,
  deleteEntry,
  readEntry,
  mintDaemonPAT,
  sessionNeedsRefresh,
  refreshSupabaseSession,
  ensureDaemonPATForOrigin,
  ENSURE_MINTED,
  ENSURE_ALREADY_CURRENT,
  ENSURE_NO_SESSION,
  ENSURE_UNAVAILABLE,
  ENSURE_FAILED,
  MINT_RPC_PATH,
  MINT_MAX_ATTEMPTS,
  REFRESH_TOKEN_PATH,
  TOKEN_EXPIRY_MARGIN_S,
} = require('../src/daemon-creds');

// ----------------------------------------------------------------------------
// endpointKey — parity with internal/auth/daemon_file.go:endpointKey
// ----------------------------------------------------------------------------
//
// These cases mirror the Go semantics: lowercase scheme://host (host already
// includes :port when explicit), path/query/fragment stripped, "" for any
// input the URL parser rejects. Drift here means Electron writes one origin
// key and the daemon reads a different one — silent split-brain.

test('endpointKey: http with explicit port', () => {
  assert.equal(endpointKey('http://localhost:3123'), 'http://localhost:3123');
});

test('endpointKey: scheme + host uppercase + path/query stripped', () => {
  assert.equal(
    endpointKey('HTTPS://API.example.com/path?q=1'),
    'https://api.example.com'
  );
});

test('endpointKey: production-style HTTPS, no port', () => {
  assert.equal(endpointKey('https://reliantapi.com'), 'https://reliantapi.com');
});

test('endpointKey: empty input → empty', () => {
  assert.equal(endpointKey(''), '');
  assert.equal(endpointKey('   '), '');
  assert.equal(endpointKey(null), '');
  assert.equal(endpointKey(undefined), '');
});

test('endpointKey: unparseable input → empty', () => {
  assert.equal(endpointKey('not-a-url'), '');
  // A bare scheme with no host is also invalid for our purposes.
  assert.equal(endpointKey('https://'), '');
});

test('endpointKey: IPv6 with port preserved', () => {
  assert.equal(endpointKey('http://[::1]:8080'), 'http://[::1]:8080');
});

test('endpointKey: fragment stripped', () => {
  assert.equal(
    endpointKey('https://staging.reliantapi.com/grpc#x'),
    'https://staging.reliantapi.com'
  );
});

// ----------------------------------------------------------------------------
// readDaemonStore / writeDaemonStore / upsertEntry — file format parity
// ----------------------------------------------------------------------------
//
// We override `filePath` so the test doesn't touch the user's real
// ~/.reliant/daemon.json. Each test sets up its own tmpdir, exercises the
// read-modify-write, then asserts:
//   1. Unrelated entries are preserved (no clobber).
//   2. The new entry lands under the right (origin, account) key.
//   3. The JSON parses and uses the expected snake_case field names.
//   4. The file mode is 0600 (POSIX only — fs mode bits aren't meaningful
//      on Windows, so we skip the mode check there).
//
// The on-disk shape is nested, and mirroring it exactly is the whole point of
// these assertions:
//
//   { "origins":          { "<origin>": { "<account>": creds } },
//     "default_accounts": { "<origin>": "<account>" } }
//
// Go's readStore treats a document with NO top-level `origins` member as a
// stale format and returns an EMPTY store. So a mirror that writes the old
// flat `{[origin]: creds}` shape does not fail loudly — every entry Electron
// writes is silently discarded on the daemon's next read, presenting as a
// daemon that behaves as though it was never given a PAT. These tests assert
// the nested shape structurally rather than round-tripping through our own
// reader, which would pass just as happily on the wrong format.

function makeTmpFile(name) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-creds-test-'));
  return { dir, file: path.join(dir, name || 'daemon.json') };
}

/** A store document in the nested on-disk shape. */
function seedStore(origins, defaultAccounts = {}) {
  return { origins, default_accounts: defaultAccounts };
}

test('the on-disk document has the nested shape Go requires', () => {
  // Structural, deliberately not via readDaemonStore: reading back what we
  // wrote proves only self-consistency, and self-consistency on the WRONG
  // format is exactly the failure being guarded against.
  const { dir, file } = makeTmpFile();
  try {
    upsertEntry({
      apiUrl: 'https://reliantapi.com',
      pat: 'rlnt_pat_shape',
      sub: 'user-abc',
      filePath: file,
    });

    const doc = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(Object.keys(doc).sort(), ['default_accounts', 'origins']);
    assert.equal(doc.origins['https://reliantapi.com']['user-abc'].pat, 'rlnt_pat_shape');
    assert.equal(doc.default_accounts['https://reliantapi.com'], 'user-abc');

    // The old flat shape must not linger anywhere at the root.
    assert.equal(
      Object.prototype.hasOwnProperty.call(doc, 'https://reliantapi.com'),
      false,
      'a top-level origin key is the OLD flat format — Go discards the whole file'
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('an account with no sub is stored under _default, matching Go', () => {
  // "" and "_default" must be ONE entry, not two. The string is shared with
  // daemoninstance.DefaultSubSegment so a credential and the instance
  // directory that consumes it agree on what "no account" is called.
  const { dir, file } = makeTmpFile();
  try {
    upsertEntry({ apiUrl: 'http://localhost:3123', pat: 'rlnt_pat_anon', filePath: file });

    const doc = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(Object.keys(doc.origins['http://localhost:3123']), ['_default']);
    assert.equal(doc.default_accounts['http://localhost:3123'], '_default');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('two accounts on ONE origin coexist instead of overwriting each other', () => {
  // The bug the nesting exists to fix. Keyed by origin alone, the second
  // account's mint clobbered the first's PAT, and whichever user signed in
  // last silently owned the machine's only credential.
  const { dir, file } = makeTmpFile();
  try {
    upsertEntry({ apiUrl: 'https://reliantapi.com', pat: 'pat-alice', sub: 'alice', filePath: file });
    upsertEntry({ apiUrl: 'https://reliantapi.com', pat: 'pat-bob', sub: 'bob', filePath: file });

    const doc = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(Object.keys(doc.origins['https://reliantapi.com']).sort(), ['alice', 'bob']);
    assert.equal(doc.origins['https://reliantapi.com'].alice.pat, 'pat-alice');
    assert.equal(doc.origins['https://reliantapi.com'].bob.pat, 'pat-bob');

    // Writing claims the default, so the most recently registered account is
    // what a no-account lookup resolves to.
    assert.equal(doc.default_accounts['https://reliantapi.com'], 'bob');
    assert.equal(readEntry({ apiUrl: 'https://reliantapi.com', filePath: file }).pat, 'pat-bob');
    // Naming an account still gets that account, not the default.
    assert.equal(
      readEntry({ apiUrl: 'https://reliantapi.com', sub: 'alice', filePath: file }).pat,
      'pat-alice'
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('lookup resolves default, then a lone entry, and otherwise NOTHING', () => {
  // The ordering is the contract. An origin holding several accounts with no
  // recorded default must resolve to nothing rather than to an arbitrary one —
  // an arbitrary pick resolves differently as iteration order changes, which
  // is a split-brain that reproduces only sometimes.
  const { dir, file } = makeTmpFile();
  const creds = (pat, sub) => ({ pat, server_url: 'https://x.test', sub });
  try {
    // Lone entry, no recorded default: unambiguous, so it is used.
    fs.writeFileSync(file, JSON.stringify(seedStore({ 'https://x.test': { solo: creds('p-solo', 'solo') } })));
    assert.equal(readEntry({ apiUrl: 'https://x.test', filePath: file }).pat, 'p-solo');

    // Two entries, no recorded default: nothing.
    fs.writeFileSync(
      file,
      JSON.stringify(seedStore({ 'https://x.test': { a: creds('p-a', 'a'), b: creds('p-b', 'b') } }))
    );
    assert.equal(readEntry({ apiUrl: 'https://x.test', filePath: file }), null);

    // Two entries WITH a recorded default: the default.
    fs.writeFileSync(
      file,
      JSON.stringify(
        seedStore(
          { 'https://x.test': { a: creds('p-a', 'a'), b: creds('p-b', 'b') } },
          { 'https://x.test': 'b' }
        )
      )
    );
    assert.equal(readEntry({ apiUrl: 'https://x.test', filePath: file }).pat, 'p-b');

    // An account named explicitly but absent resolves to nothing — NEVER to a
    // neighbour's credentials.
    assert.equal(readEntry({ apiUrl: 'https://x.test', sub: 'carol', filePath: file }), null);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a document in the OLD flat format reads as empty, exactly as Go treats it', () => {
  // Go: "a successfully-parsed document with no `origins` member is an older
  // format, not an empty new one". If this mirror read it as data instead, the
  // two sides would disagree about whether a credential exists at all.
  const { dir, file } = makeTmpFile();
  try {
    fs.writeFileSync(
      file,
      JSON.stringify({
        'https://reliantapi.com': { pat: 'rlnt_pat_flat', server_url: 'https://reliantapi.com' },
      })
    );
    assert.deepEqual(readDaemonStore({ filePath: file }), { origins: {}, default_accounts: {} });
    assert.equal(readEntry({ apiUrl: 'https://reliantapi.com', filePath: file }), null);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('upsertEntry preserves unrelated entries and adds new one at correct key', () => {
  const { dir, file } = makeTmpFile();
  try {
    // Seed with two unrelated origins. Mimics a user who has registered
    // against staging + a localhost dev origin at some point.
    const seed = seedStore({
      'https://staging.reliantapi.com': {
        _default: {
          pat: 'rlnt_pat_existing_staging',
          server_url: 'https://staging.reliantapi.com',
          gateway_url: '',
          registered_at: '2025-01-01T00:00:00.000Z',
        },
      },
      'http://localhost:3123': {
        _default: {
          pat: 'rlnt_pat_existing_local',
          server_url: 'http://localhost:3123',
          gateway_url: 'http://localhost:3124',
          registered_at: '2025-01-02T00:00:00.000Z',
        },
      },
    });
    fs.writeFileSync(file, JSON.stringify(seed, null, 2));

    upsertEntry({
      apiUrl: 'https://reliantapi.com',
      gatewayUrl: '',
      pat: 'rlnt_pat_new_prod',
      filePath: file,
    });

    const raw = fs.readFileSync(file, 'utf8');
    const after = JSON.parse(raw);

    // Both pre-existing entries survive unchanged.
    assert.deepEqual(
      after.origins['https://staging.reliantapi.com'],
      seed.origins['https://staging.reliantapi.com'],
      'staging entry must be preserved verbatim'
    );
    assert.deepEqual(
      after.origins['http://localhost:3123'],
      seed.origins['http://localhost:3123'],
      'localhost entry must be preserved verbatim'
    );

    // New entry lands at the right key with the right field names.
    const fresh = after.origins['https://reliantapi.com']._default;
    assert.ok(fresh, 'new entry must be present');
    assert.equal(fresh.pat, 'rlnt_pat_new_prod');
    assert.equal(fresh.server_url, 'https://reliantapi.com');
    assert.equal(fresh.gateway_url, '');
    assert.ok(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(fresh.registered_at),
      'registered_at must be ISO 8601'
    );

    // POSIX-only: file mode 0600 ensures the PAT isn't world-readable.
    // On Windows, mode bits are advisory and not enforced — skip.
    if (process.platform !== 'win32') {
      const mode = fs.statSync(file).mode & 0o777;
      assert.equal(mode, 0o600, `expected file mode 0600, got ${mode.toString(8)}`);
    }
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('readDaemonStore returns an empty store on missing file', () => {
  const { dir, file } = makeTmpFile('does-not-exist.json');
  try {
    // Both members present, so every caller can write without a null check —
    // same contract as Go's newStore.
    assert.deepEqual(readDaemonStore({ filePath: file }), { origins: {}, default_accounts: {} });
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('readDaemonStore returns an empty store on garbage JSON (matches Go fall-through)', () => {
  const { dir, file } = makeTmpFile();
  try {
    fs.writeFileSync(file, 'not json at all{{{');
    assert.deepEqual(readDaemonStore({ filePath: file }), { origins: {}, default_accounts: {} });
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('upsertEntry throws on invalid apiUrl (empty endpoint key)', () => {
  const { dir, file } = makeTmpFile();
  try {
    assert.throws(
      () => upsertEntry({ apiUrl: 'not-a-url', pat: 'rlnt_pat_x', filePath: file }),
      /invalid --server URL/
    );
    assert.equal(fs.existsSync(file), false, 'no file should have been written');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ----------------------------------------------------------------------------
// daemon_id is NOT in this file + logout clearing
// ----------------------------------------------------------------------------
//
// The stable server-assigned daemon id used to live in the credentials entry,
// keyed by origin alone — so every worktree on one machine read and
// re-asserted the SAME id, and the gateway evicted one of them on every
// registration. It now lives in a `daemon-id` file in the instance's own data
// dir, owned by the daemon: identity is per INSTANCE (origin, account,
// workspace) while a PAT is per ACCOUNT. Go has a matching test asserting the
// key never reappears on disk. Electron neither writes nor manages that file.

test('upsertEntry never writes a daemon_id — the id is not in this file', () => {
  const { dir, file } = makeTmpFile();
  try {
    // Even with a stale key present from an older write, the rewritten entry
    // must not carry one forward: the field no longer exists on either side,
    // so preserving it would resurrect a key Go asserts is gone.
    fs.writeFileSync(
      file,
      JSON.stringify(
        seedStore({
          'http://localhost:3123': {
            'user-abc': {
              pat: 'rlnt_pat_old',
              server_url: 'http://localhost:3123',
              sub: 'user-abc',
              daemon_id: 'stable-daemon-id-123',
            },
          },
        })
      )
    );

    upsertEntry({
      apiUrl: 'http://localhost:3123',
      gatewayUrl: 'http://localhost:3124',
      pat: 'rlnt_pat_new',
      sub: 'user-abc',
      filePath: file,
    });

    const after = JSON.parse(fs.readFileSync(file, 'utf8'))
      .origins['http://localhost:3123']['user-abc'];
    assert.equal(after.pat, 'rlnt_pat_new', 'PAT must be updated');
    assert.equal(
      Object.prototype.hasOwnProperty.call(after, 'daemon_id'),
      false,
      'daemon_id must not be written — it lives in the instance data dir now'
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('a fresh entry carries no daemon_id either', () => {
  const { dir, file } = makeTmpFile();
  try {
    upsertEntry({ apiUrl: 'http://localhost:3123', pat: 'rlnt_pat_new', filePath: file });
    const after = JSON.parse(fs.readFileSync(file, 'utf8'))
      .origins['http://localhost:3123']._default;
    assert.ok(after, 'entry must exist');
    assert.equal(Object.prototype.hasOwnProperty.call(after, 'daemon_id'), false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('deleteEntry removes one account and leaves other origins alone', () => {
  const { dir, file } = makeTmpFile();
  try {
    const seed = seedStore(
      {
        'http://localhost:3123': {
          'user-abc': {
            pat: 'rlnt_pat_local',
            server_url: 'http://localhost:3123',
            gateway_url: '',
            registered_at: '2025-01-02T00:00:00.000Z',
            sub: 'user-abc',
          },
        },
        'https://staging.reliantapi.com': {
          _default: {
            pat: 'rlnt_pat_staging',
            server_url: 'https://staging.reliantapi.com',
            gateway_url: '',
            registered_at: '2025-01-01T00:00:00.000Z',
          },
        },
      },
      { 'http://localhost:3123': 'user-abc' }
    );
    fs.writeFileSync(file, JSON.stringify(seed, null, 2));

    const removed = deleteEntry({ apiUrl: 'http://localhost:3123', filePath: file });
    assert.equal(removed, true, 'must report the entry was removed');

    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    // Emptying an origin removes the origin itself rather than leaving an
    // empty map behind, so the file shrinks back when the last account goes.
    assert.equal(
      Object.prototype.hasOwnProperty.call(after.origins, 'http://localhost:3123'),
      false,
      'an emptied origin must be removed, not left as an empty map'
    );
    assert.equal(
      Object.prototype.hasOwnProperty.call(after.default_accounts, 'http://localhost:3123'),
      false,
      'its default pointer must go too'
    );
    // Unrelated origins are untouched — logout is per-origin, not global.
    assert.deepEqual(
      after.origins['https://staging.reliantapi.com'],
      seed.origins['https://staging.reliantapi.com'],
      'unrelated origin must survive logout'
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('logging out one account leaves the other signed in, with a usable default', () => {
  // Two accounts on one origin. Removing the one the default points at must
  // repoint the default rather than leave it dangling — a dangling default
  // makes every no-account lookup fall through to "several entries, no
  // default" and resolve to nothing, even though a good credential remains.
  const { dir, file } = makeTmpFile();
  try {
    upsertEntry({ apiUrl: 'https://reliantapi.com', pat: 'pat-alice', sub: 'alice', filePath: file });
    upsertEntry({ apiUrl: 'https://reliantapi.com', pat: 'pat-bob', sub: 'bob', filePath: file });

    // bob is the default (most recent write); log bob out.
    assert.equal(deleteEntry({ apiUrl: 'https://reliantapi.com', filePath: file }), true);

    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(Object.keys(after.origins['https://reliantapi.com']), ['alice']);
    assert.equal(after.default_accounts['https://reliantapi.com'], 'alice');
    assert.equal(
      readEntry({ apiUrl: 'https://reliantapi.com', filePath: file }).pat,
      'pat-alice',
      "alice's credential must still resolve after bob logs out"
    );
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('deleteEntry is a no-op when the origin has no entry', () => {
  const { dir, file } = makeTmpFile();
  try {
    fs.writeFileSync(file, JSON.stringify({}, null, 2));
    const removed = deleteEntry({ apiUrl: 'http://localhost:3123', filePath: file });
    assert.equal(removed, false, 'nothing to remove → false');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('deleteEntry returns false for an invalid apiUrl', () => {
  assert.equal(deleteEntry({ apiUrl: 'not-a-url' }), false);
});

test('writeDaemonStore creates the parent directory recursively', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'daemon-creds-test-'));
  const nested = path.join(dir, 'nested', 'inner', 'daemon.json');
  try {
    writeDaemonStore({ 'https://x.example': { pat: 'rlnt_pat_x' } }, { filePath: nested });
    assert.ok(fs.existsSync(nested));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

// ----------------------------------------------------------------------------
// mintDaemonPAT — fetch stubbing
// ----------------------------------------------------------------------------
//
// We stub globalThis.fetch directly. The localhost-HTTPS code path uses
// https.request and is intentionally NOT covered by these tests (it would
// require spinning up a real TLS server with a self-signed cert just to
// confirm we wire rejectUnauthorized:false correctly, which is not worth
// the test infrastructure). The fetch path is what runs in production.

function withFetchStub(stub, fn) {
  const orig = globalThis.fetch;
  globalThis.fetch = stub;
  return (async () => {
    try {
      return await fn();
    } finally {
      globalThis.fetch = orig;
    }
  })();
}

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() {
      return body;
    },
    async text() {
      return JSON.stringify(body);
    },
  };
}

test('mintDaemonPAT: 200 with {token, tokenId} returns parsed', async () => {
  let seenUrl;
  let seenInit;
  await withFetchStub(
    async (url, init) => {
      seenUrl = url;
      seenInit = init;
      return jsonResponse(200, { token: 'rlnt_pat_abc', tokenId: 'tok_1' });
    },
    async () => {
      const result = await mintDaemonPAT({
        apiUrl: 'https://reliantapi.com',
        accessToken: 'jwt-xyz',
        name: 'my-laptop',
      });
      assert.deepEqual(result, { token: 'rlnt_pat_abc', tokenId: 'tok_1' });
    }
  );

  // URL assertion: correct origin + correct Connect path.
  assert.equal(seenUrl, `https://reliantapi.com${MINT_RPC_PATH}`);

  // Header assertions.
  assert.equal(seenInit.method, 'POST');
  assert.equal(seenInit.headers['Content-Type'], 'application/json');
  assert.equal(seenInit.headers.Authorization, 'Bearer jwt-xyz');

  // Body assertion: { name: <hostname> }.
  assert.deepEqual(JSON.parse(seenInit.body), { name: 'my-laptop' });
});

test('mintDaemonPAT: defaults `name` to os.hostname() when not provided', async () => {
  let seenInit;
  await withFetchStub(
    async (_url, init) => {
      seenInit = init;
      return jsonResponse(200, { token: 'rlnt_pat_xyz' });
    },
    async () => {
      await mintDaemonPAT({
        apiUrl: 'https://reliantapi.com',
        accessToken: 'jwt',
      });
    }
  );
  const sent = JSON.parse(seenInit.body);
  assert.equal(sent.name, os.hostname());
});

test('mintDaemonPAT: trims trailing slashes off apiUrl', async () => {
  let seenUrl;
  await withFetchStub(
    async (url) => {
      seenUrl = url;
      return jsonResponse(200, { token: 'rlnt_pat_xyz' });
    },
    async () => {
      await mintDaemonPAT({
        apiUrl: 'https://reliantapi.com///',
        accessToken: 'jwt',
      });
    }
  );
  assert.equal(seenUrl, `https://reliantapi.com${MINT_RPC_PATH}`);
});

test('mintDaemonPAT: 200 missing token throws', async () => {
  await withFetchStub(
    async () => jsonResponse(200, { tokenId: 'tok_1' }), // no `token`
    async () => {
      await assert.rejects(
        () => mintDaemonPAT({ apiUrl: 'https://reliantapi.com', accessToken: 'jwt' }),
        /missing token/i
      );
    }
  );
});

test('mintDaemonPAT: 401 throws with status', async () => {
  await withFetchStub(
    async () => jsonResponse(401, { error: 'unauthenticated' }),
    async () => {
      await assert.rejects(
        () => mintDaemonPAT({ apiUrl: 'https://reliantapi.com', accessToken: 'bad' }),
        /HTTP 401/
      );
    }
  );
});

test('mintDaemonPAT: 5xx throws', async () => {
  await withFetchStub(
    async () => jsonResponse(503, { error: 'overloaded' }),
    async () => {
      await assert.rejects(
        () => mintDaemonPAT({ apiUrl: 'https://reliantapi.com', accessToken: 'jwt' }),
        /HTTP 503/
      );
    }
  );
});

test('mintDaemonPAT: network error throws', async () => {
  await withFetchStub(
    async () => {
      throw new Error('ECONNREFUSED');
    },
    async () => {
      await assert.rejects(
        () => mintDaemonPAT({ apiUrl: 'https://reliantapi.com', accessToken: 'jwt' }),
        /ECONNREFUSED/
      );
    }
  );
});

test('mintDaemonPAT: missing apiUrl throws synchronously', async () => {
  await assert.rejects(
    () => mintDaemonPAT({ apiUrl: '', accessToken: 'jwt' }),
    /missing apiUrl/
  );
});

test('mintDaemonPAT: missing accessToken throws synchronously', async () => {
  await assert.rejects(
    () => mintDaemonPAT({ apiUrl: 'https://reliantapi.com', accessToken: '' }),
    /missing accessToken/
  );
});

// ----------------------------------------------------------------------------
// mintDaemonPAT — retry on transient failures
// ----------------------------------------------------------------------------
//
// `air` may be mid-restart of `reliant server api` when BackendManager fires
// the mint preflight, so the admin-server forwarder returns 502 (or ECONNREFUSED
// upstream) for a short window. mintDaemonPAT retries those up to
// MINT_MAX_ATTEMPTS times. These tests cover:
//   - terminal success after transient failures (502 → 502 → 200, mixed)
//   - exhausted retries (all 502 → throw with attempt count == MAX)
//   - non-retryable codes don't retry (401 once)
//   - schema mismatch (200 missing token) doesn't retry
// We inject a fake `sleep` so the retry loop runs without real backoff.

function makeSequenceFetch(responses) {
  const calls = { count: 0, urls: [], inits: [] };
  const stub = async (url, init) => {
    const i = calls.count++;
    calls.urls.push(url);
    calls.inits.push(init);
    const entry = responses[i];
    if (!entry) {
      throw new Error(`fetch stub: no scripted response for call ${i + 1}`);
    }
    if (entry instanceof Error || typeof entry === 'function') {
      // Allow scripting a thrown error too.
      if (typeof entry === 'function') return await entry();
      throw entry;
    }
    return entry;
  };
  return { stub, calls };
}

const noopSleep = async () => {};

test('mintDaemonPAT: sanity — retries are bounded by MINT_MAX_ATTEMPTS = 3', () => {
  // If someone bumps this constant we want the test changes to be conscious.
  assert.equal(MINT_MAX_ATTEMPTS, 3);
});

test('mintDaemonPAT: 502 → 502 → 200 returns parsed result and calls fetch 3 times', async () => {
  const { stub, calls } = makeSequenceFetch([
    jsonResponse(502, { error: 'bad gateway' }),
    jsonResponse(502, { error: 'bad gateway' }),
    jsonResponse(200, { token: 'rlnt_pat_after_retry', tokenId: 'tok_2' }),
  ]);
  await withFetchStub(stub, async () => {
    const result = await mintDaemonPAT({
      apiUrl: 'https://reliantapi.com',
      accessToken: 'jwt',
      sleep: noopSleep,
    });
    assert.deepEqual(result, { token: 'rlnt_pat_after_retry', tokenId: 'tok_2' });
  });
  assert.equal(calls.count, 3, 'fetch should be called 3 times across retries');
});

test('mintDaemonPAT: 502 → 502 → 502 throws after exhausting retries (3 calls)', async () => {
  const { stub, calls } = makeSequenceFetch([
    jsonResponse(502, { error: 'bad gateway' }),
    jsonResponse(502, { error: 'bad gateway' }),
    jsonResponse(502, { error: 'bad gateway' }),
  ]);
  await withFetchStub(stub, async () => {
    await assert.rejects(
      () =>
        mintDaemonPAT({
          apiUrl: 'https://reliantapi.com',
          accessToken: 'jwt',
          sleep: noopSleep,
        }),
      /HTTP 502/
    );
  });
  assert.equal(calls.count, 3, 'fetch should be called exactly MINT_MAX_ATTEMPTS times');
});

test('mintDaemonPAT: 502 → network error → 200 returns parsed (3 calls)', async () => {
  // The middle call throws a network error (ECONNREFUSED via message-substring
  // path — matches existing `throw new Error("ECONNREFUSED")` test pattern).
  const { stub, calls } = makeSequenceFetch([
    jsonResponse(502, { error: 'bad gateway' }),
    async () => {
      throw new Error('connect ECONNREFUSED 127.0.0.1:3001');
    },
    jsonResponse(200, { token: 'rlnt_pat_recovered', tokenId: 'tok_3' }),
  ]);
  await withFetchStub(stub, async () => {
    const result = await mintDaemonPAT({
      apiUrl: 'https://reliantapi.com',
      accessToken: 'jwt',
      sleep: noopSleep,
    });
    assert.deepEqual(result, { token: 'rlnt_pat_recovered', tokenId: 'tok_3' });
  });
  assert.equal(calls.count, 3);
});

test('mintDaemonPAT: 401 throws on first attempt WITHOUT retrying', async () => {
  const { stub, calls } = makeSequenceFetch([
    jsonResponse(401, { error: 'unauthenticated' }),
    // Subsequent entries would be used if the retry path fires — fail loudly.
    jsonResponse(200, { token: 'rlnt_pat_should_not_reach' }),
  ]);
  await withFetchStub(stub, async () => {
    await assert.rejects(
      () =>
        mintDaemonPAT({
          apiUrl: 'https://reliantapi.com',
          accessToken: 'bad-jwt',
          sleep: noopSleep,
        }),
      /HTTP 401/
    );
  });
  assert.equal(calls.count, 1, '4xx must not trigger a retry');
});

test('mintDaemonPAT: 200 with missing token throws WITHOUT retrying', async () => {
  const { stub, calls } = makeSequenceFetch([
    jsonResponse(200, { tokenId: 'tok_no_token_field' }), // no `token`
    // Sentinel — should not be reached.
    jsonResponse(200, { token: 'rlnt_pat_should_not_reach' }),
  ]);
  await withFetchStub(stub, async () => {
    await assert.rejects(
      () =>
        mintDaemonPAT({
          apiUrl: 'https://reliantapi.com',
          accessToken: 'jwt',
          sleep: noopSleep,
        }),
      /missing token/i
    );
  });
  assert.equal(calls.count, 1, 'schema mismatch is terminal — must not retry');
});

// ----------------------------------------------------------------------------
// ensureDaemonPATForOrigin — orchestration that BackendManager delegates to
// ----------------------------------------------------------------------------
//
// The store path is derived from os.homedir(), so we redirect HOME (and
// USERPROFILE on Windows) to a tmpdir for each test. mintDaemonPAT is stubbed
// at the fetch level — we don't re-cover its failure modes here.

function withFakeHome(fn) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'ensure-pat-test-'));
  const origHome = process.env.HOME;
  const origUserProfile = process.env.USERPROFILE;
  process.env.HOME = home;
  process.env.USERPROFILE = home;
  return (async () => {
    try {
      return await fn(home);
    } finally {
      if (origHome === undefined) delete process.env.HOME; else process.env.HOME = origHome;
      if (origUserProfile === undefined) delete process.env.USERPROFILE; else process.env.USERPROFILE = origUserProfile;
      fs.rmSync(home, { recursive: true, force: true });
    }
  })();
}

test('ensureDaemonPATForOrigin: skips silently when authStorage is null', async () => {
  // Fetch should never be called.
  await withFakeHome(async (home) => {
    await withFetchStub(
      async () => {
        throw new Error('fetch must not be called when authStorage is null');
      },
      async () => {
        await ensureDaemonPATForOrigin({
          authStorage: null,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        });
      }
    );
    // No file written.
    assert.equal(
      fs.existsSync(path.join(home, '.reliant', 'daemon.json')),
      false,
      'no daemon.json should be created when authStorage is null'
    );
  });
});

test('ensureDaemonPATForOrigin: skips mint when no session is stored', async () => {
  await withFakeHome(async (home) => {
    const authStorage = { loadStoredAuth: () => null };
    await withFetchStub(
      async () => {
        throw new Error('fetch must not be called when there is no session');
      },
      async () => {
        await ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        });
      }
    );
    assert.equal(
      fs.existsSync(path.join(home, '.reliant', 'daemon.json')),
      false,
      'no daemon.json when no session'
    );
  });
});

test('ensureDaemonPATForOrigin: mints + writes when no existing entry', async () => {
  await withFakeHome(async (home) => {
    const authStorage = {
      loadStoredAuth: () => ({
        access_token: 'jwt-abc',
        user: { id: 'user-1' },
      }),
    };
    await withFetchStub(
      async () => jsonResponse(200, { token: 'rlnt_pat_fresh', tokenId: 'tok_1' }),
      async () => {
        await ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: 'http://127.0.0.1:19190/reliant-dev',
        });
      }
    );
    const file = path.join(home, '.reliant', 'daemon.json');
    assert.ok(fs.existsSync(file), 'daemon.json should be written');
    const store = JSON.parse(fs.readFileSync(file, 'utf8'));
    const entry = store.origins['https://reliantapi.com']['user-1'];
    assert.ok(entry, 'entry must exist under the (origin, account) key');
    assert.equal(entry.pat, 'rlnt_pat_fresh');
    assert.equal(entry.server_url, 'https://reliantapi.com');
    assert.equal(entry.gateway_url, 'http://127.0.0.1:19190/reliant-dev');
    assert.equal(entry.sub, 'user-1');
  });
});

test('ensureDaemonPATForOrigin: mints for the new user and LEAVES the other account intact', async () => {
  await withFakeHome(async (home) => {
    // Seed an existing entry that belongs to a DIFFERENT user.
    //
    // This assertion changed with the (origin, account) keying, and the change
    // is the point rather than an accommodation. The old flat store had ONE
    // slot per origin, so minting for a second user necessarily destroyed the
    // first's PAT — "overwrites" was the best available behavior, not the
    // desired one. Now each account has its own entry, so a user switch adds a
    // credential instead of replacing one, and switching back does not require
    // a re-mint.
    const dir = path.join(home, '.reliant');
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, 'daemon.json');
    const oldEntry = {
      pat: 'rlnt_pat_OLD_user',
      server_url: 'https://reliantapi.com',
      gateway_url: '',
      sub: 'user-OLD',
      registered_at: '2025-01-01T00:00:00.000Z',
    };
    fs.writeFileSync(
      file,
      JSON.stringify(
        {
          origins: { 'https://reliantapi.com': { 'user-OLD': oldEntry } },
          default_accounts: { 'https://reliantapi.com': 'user-OLD' },
        },
        null,
        2
      )
    );

    const authStorage = {
      loadStoredAuth: () => ({
        access_token: 'jwt-new',
        user: { id: 'user-NEW' },
      }),
    };
    await withFetchStub(
      async () => jsonResponse(200, { token: 'rlnt_pat_NEW_user' }),
      async () => {
        await ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        });
      }
    );
    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    const entry = after.origins['https://reliantapi.com']['user-NEW'];
    assert.equal(entry.pat, 'rlnt_pat_NEW_user', "must write the new user's PAT");
    assert.equal(entry.sub, 'user-NEW');

    assert.deepEqual(
      after.origins['https://reliantapi.com']['user-OLD'],
      oldEntry,
      "the other account's credential must survive a user switch"
    );
    assert.equal(
      after.default_accounts['https://reliantapi.com'],
      'user-NEW',
      'the newly registered account becomes the default'
    );
  });
});

test('ensureDaemonPATForOrigin: skips mint when existing entry sub matches current session', async () => {
  await withFakeHome(async (home) => {
    // Seed an entry that already belongs to the current user.
    const dir = path.join(home, '.reliant');
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, 'daemon.json');
    const seedEntry = {
      pat: 'rlnt_pat_CURRENT',
      server_url: 'https://reliantapi.com',
      gateway_url: '',
      sub: 'user-1',
      registered_at: '2025-01-01T00:00:00.000Z',
    };
    fs.writeFileSync(
      file,
      JSON.stringify(
        {
          origins: { 'https://reliantapi.com': { 'user-1': seedEntry } },
          default_accounts: { 'https://reliantapi.com': 'user-1' },
        },
        null,
        2
      )
    );

    const authStorage = {
      loadStoredAuth: () => ({
        access_token: 'jwt-current',
        user: { id: 'user-1' },
      }),
    };
    await withFetchStub(
      async () => {
        throw new Error('fetch must not be called when sub already matches');
      },
      async () => {
        await ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        });
      }
    );
    // File contents unchanged.
    const after = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.deepEqual(after.origins['https://reliantapi.com']['user-1'], seedEntry);
  });
});
// ----------------------------------------------------------------------------
// sessionNeedsRefresh — staleness detection
// ----------------------------------------------------------------------------
//
// `expires_at` is unix SECONDS. The margin (TOKEN_EXPIRY_MARGIN_S) treats
// about-to-expire tokens as stale so the mint doesn't race the expiry.
// A session with no usable expires_at is "unknown", NOT stale — the
// 401-triggered reactive refresh handles those.

test('sessionNeedsRefresh: expired token → true', () => {
  const nowMs = 1_700_000_000_000;
  const session = { expires_at: nowMs / 1000 - 100 };
  assert.equal(sessionNeedsRefresh(session, nowMs), true);
});

test('sessionNeedsRefresh: token expiring inside the margin → true', () => {
  const nowMs = 1_700_000_000_000;
  const session = { expires_at: nowMs / 1000 + TOKEN_EXPIRY_MARGIN_S / 2 };
  assert.equal(sessionNeedsRefresh(session, nowMs), true);
});

test('sessionNeedsRefresh: fresh token → false', () => {
  const nowMs = 1_700_000_000_000;
  const session = { expires_at: nowMs / 1000 + 3600 };
  assert.equal(sessionNeedsRefresh(session, nowMs), false);
});

test('sessionNeedsRefresh: missing/garbage expires_at → false (unknown, not stale)', () => {
  assert.equal(sessionNeedsRefresh({}, 1_700_000_000_000), false);
  assert.equal(sessionNeedsRefresh(null, 1_700_000_000_000), false);
  assert.equal(sessionNeedsRefresh({ expires_at: 'soon' }, 1_700_000_000_000), false);
  assert.equal(sessionNeedsRefresh({ expires_at: 0 }, 1_700_000_000_000), false);
});

// ----------------------------------------------------------------------------
// refreshSupabaseSession — GoTrue exchange + session-shape normalization
// ----------------------------------------------------------------------------

test('refreshSupabaseSession: posts refresh_token to GoTrue and merges the new session', async () => {
  let seenUrl;
  let seenInit;
  const oldSession = {
    access_token: 'jwt-stale',
    refresh_token: 'rt-old',
    expires_at: 1_000,
    user: { id: 'user-1', email: 'u@example.com' },
  };
  const before = Math.floor(Date.now() / 1000);
  const result = await withFetchStub(
    async (url, init) => {
      seenUrl = url;
      seenInit = init;
      // GoTrue omits expires_at here — the helper must derive it from expires_in.
      return jsonResponse(200, {
        access_token: 'jwt-fresh',
        refresh_token: 'rt-new',
        token_type: 'bearer',
        expires_in: 3600,
        user: { id: 'user-1', email: 'u@example.com' },
      });
    },
    () =>
      refreshSupabaseSession({
        authUrl: 'https://proj.supabase.co/',
        anonKey: 'anon-key-1',
        session: oldSession,
      })
  );

  // Endpoint + credential assertions. Trailing slash on authUrl must not
  // produce a `//auth` path.
  assert.equal(seenUrl, `https://proj.supabase.co${REFRESH_TOKEN_PATH}`);
  assert.equal(seenInit.method, 'POST');
  assert.equal(seenInit.headers['Content-Type'], 'application/json');
  assert.equal(seenInit.headers.apikey, 'anon-key-1');
  assert.deepEqual(JSON.parse(seenInit.body), { refresh_token: 'rt-old' });

  // Session-shape assertions: rotated tokens, derived expires_at, user kept.
  assert.equal(result.access_token, 'jwt-fresh');
  assert.equal(result.refresh_token, 'rt-new');
  assert.equal(result.token_type, 'bearer');
  assert.ok(
    result.expires_at >= before + 3600 && result.expires_at <= before + 3601,
    `expires_at should be derived from expires_in (got ${result.expires_at})`
  );
  assert.deepEqual(result.user, { id: 'user-1', email: 'u@example.com' });
});

test('refreshSupabaseSession: keeps old refresh_token when GoTrue omits a rotated one', async () => {
  const result = await withFetchStub(
    async () =>
      jsonResponse(200, {
        access_token: 'jwt-fresh',
        expires_at: 2_000_000_000,
      }),
    () =>
      refreshSupabaseSession({
        authUrl: 'https://proj.supabase.co',
        anonKey: 'anon-key-1',
        session: { access_token: 'jwt-stale', refresh_token: 'rt-old', user: { id: 'u1' } },
      })
  );
  assert.equal(result.refresh_token, 'rt-old', 'must never brick the store on a missing rotation');
  assert.equal(result.expires_at, 2_000_000_000, 'explicit expires_at wins over derivation');
  assert.deepEqual(result.user, { id: 'u1' }, 'user carried forward when response omits it');
});

test('refreshSupabaseSession: non-2xx throws with status', async () => {
  await withFetchStub(
    async () => jsonResponse(400, { error: 'invalid_grant' }),
    async () => {
      await assert.rejects(
        () =>
          refreshSupabaseSession({
            authUrl: 'https://proj.supabase.co',
            anonKey: 'anon-key-1',
            session: { refresh_token: 'rt-revoked' },
          }),
        /HTTP 400/
      );
    }
  );
});

test('refreshSupabaseSession: 200 missing access_token throws', async () => {
  await withFetchStub(
    async () => jsonResponse(200, { refresh_token: 'rt-new' }),
    async () => {
      await assert.rejects(
        () =>
          refreshSupabaseSession({
            authUrl: 'https://proj.supabase.co',
            anonKey: 'anon-key-1',
            session: { refresh_token: 'rt-old' },
          }),
        /missing access_token/
      );
    }
  );
});

test('refreshSupabaseSession: missing provider config / refresh_token throws synchronously', async () => {
  await assert.rejects(
    () => refreshSupabaseSession({ authUrl: '', anonKey: '', session: { refresh_token: 'x' } }),
    /not configured/
  );
  await assert.rejects(
    () =>
      refreshSupabaseSession({
        authUrl: 'https://proj.supabase.co',
        anonKey: 'anon-key-1',
        session: {},
      }),
    /no refresh_token/
  );
});

// ----------------------------------------------------------------------------
// ensureDaemonPATForOrigin — session refresh orchestration
// ----------------------------------------------------------------------------
//
// The cold-launch bug this exists for: the disk session's access token has
// outlived its ~1h TTL, so minting with it verbatim 401s, the daemon spawns
// credential-less, falls into its interactive OAuth flow, and the app shows
// "No machine connected". These tests pin the refresh-then-mint orchestration
// and its fallbacks. Call order is asserted via the sequence stub's URL log.

const AUTH_PROVIDER = {
  authUrl: 'https://proj.supabase.co',
  anonKey: 'anon-key-1',
};
const REFRESH_URL = `https://proj.supabase.co${REFRESH_TOKEN_PATH}`;
const MINT_URL = `https://reliantapi.com${MINT_RPC_PATH}`;

function expiredSession(overrides = {}) {
  return {
    access_token: 'jwt-stale',
    refresh_token: 'rt-1',
    expires_at: Math.floor(Date.now() / 1000) - 100,
    user: { id: 'user-1' },
    ...overrides,
  };
}

function freshSession(overrides = {}) {
  return expiredSession({
    access_token: 'jwt-live',
    expires_at: Math.floor(Date.now() / 1000) + 3600,
    ...overrides,
  });
}

test('ensureDaemonPATForOrigin: expired session → refresh, persist, mint with FRESH token', async () => {
  await withFakeHome(async (home) => {
    const saved = [];
    const authStorage = {
      loadStoredAuth: () => expiredSession(),
      saveAuth: (s) => {
        saved.push(s);
        return true;
      },
    };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(200, {
        access_token: 'jwt-fresh',
        refresh_token: 'rt-2',
        expires_in: 3600,
        user: { id: 'user-1' },
      }),
      jsonResponse(200, { token: 'rlnt_pat_after_refresh' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        authUrl: AUTH_PROVIDER.authUrl,
        authAnonKey: AUTH_PROVIDER.anonKey,
      })
    );

    // Call order: GoTrue refresh FIRST, then the mint.
    assert.deepEqual(calls.urls, [REFRESH_URL, MINT_URL]);
    // The mint must carry the REFRESHED token, not the stale one.
    assert.equal(calls.inits[1].headers.Authorization, 'Bearer jwt-fresh');

    // Rotated session persisted so the renderer inherits rt-2, not rt-1.
    assert.equal(saved.length, 1, 'refreshed session must be persisted exactly once');
    assert.equal(saved[0].access_token, 'jwt-fresh');
    assert.equal(saved[0].refresh_token, 'rt-2');

    // And the PAT landed in daemon.json.
    const store = JSON.parse(
      fs.readFileSync(path.join(home, '.reliant', 'daemon.json'), 'utf8')
    );
    assert.equal(store.origins['https://reliantapi.com']['user-1'].pat, 'rlnt_pat_after_refresh');
    assert.equal(store.origins['https://reliantapi.com']['user-1'].sub, 'user-1');
  });
});

test('ensureDaemonPATForOrigin: fresh session → mints directly, NO refresh call', async () => {
  await withFakeHome(async (home) => {
    const authStorage = {
      loadStoredAuth: () => freshSession(),
      saveAuth: () => {
        throw new Error('saveAuth must not be called when no refresh happens');
      },
    };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(200, { token: 'rlnt_pat_no_refresh' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        authUrl: AUTH_PROVIDER.authUrl,
        authAnonKey: AUTH_PROVIDER.anonKey,
      })
    );
    assert.deepEqual(calls.urls, [MINT_URL], 'must go straight to the mint');
    assert.equal(calls.inits[0].headers.Authorization, 'Bearer jwt-live');
    const store = JSON.parse(
      fs.readFileSync(path.join(home, '.reliant', 'daemon.json'), 'utf8')
    );
    assert.equal(store.origins['https://reliantapi.com']['user-1'].pat, 'rlnt_pat_no_refresh');
  });
});

test('ensureDaemonPATForOrigin: fresh-looking token but mint 401s → refresh + retry once', async () => {
  await withFakeHome(async (home) => {
    const saved = [];
    const authStorage = {
      loadStoredAuth: () => freshSession(), // expires_at lies (revoked / clock skew)
      saveAuth: (s) => {
        saved.push(s);
        return true;
      },
    };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(401, { code: 'unauthenticated', message: 'invalid or expired token' }),
      jsonResponse(200, {
        access_token: 'jwt-fresh',
        refresh_token: 'rt-2',
        expires_in: 3600,
        user: { id: 'user-1' },
      }),
      jsonResponse(200, { token: 'rlnt_pat_after_401_retry' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        authUrl: AUTH_PROVIDER.authUrl,
        authAnonKey: AUTH_PROVIDER.anonKey,
      })
    );
    assert.deepEqual(calls.urls, [MINT_URL, REFRESH_URL, MINT_URL]);
    assert.equal(calls.inits[2].headers.Authorization, 'Bearer jwt-fresh');
    assert.equal(saved.length, 1);
    const store = JSON.parse(
      fs.readFileSync(path.join(home, '.reliant', 'daemon.json'), 'utf8')
    );
    assert.equal(store.origins['https://reliantapi.com']['user-1'].pat, 'rlnt_pat_after_401_retry');
  });
});

test('ensureDaemonPATForOrigin: 401 with NO refresh config → single attempt, swallowed (pre-existing behavior)', async () => {
  await withFakeHome(async (home) => {
    const authStorage = { loadStoredAuth: () => expiredSession() };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(401, { code: 'unauthenticated' }),
      // Sentinel — a second call means an unwanted refresh/retry fired.
      jsonResponse(200, { token: 'rlnt_pat_should_not_reach' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        // authUrl / authAnonKey deliberately absent (OSS build, no config).
      })
    );
    assert.deepEqual(calls.urls, [MINT_URL], 'no refresh possible → exactly one mint attempt');
    assert.equal(
      fs.existsSync(path.join(home, '.reliant', 'daemon.json')),
      false,
      'failed mint must not write daemon.json'
    );
  });
});

test('ensureDaemonPATForOrigin: pre-mint refresh fails → falls back to stored token, no second refresh on 401', async () => {
  await withFakeHome(async (home) => {
    const authStorage = { loadStoredAuth: () => expiredSession() };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(500, { error: 'gotrue down' }), // refresh attempt
      jsonResponse(401, { code: 'unauthenticated' }), // mint with the stale token
      // Sentinel — a third call means the consumed refresh was retried.
      jsonResponse(200, { token: 'rlnt_pat_should_not_reach' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        authUrl: AUTH_PROVIDER.authUrl,
        authAnonKey: AUTH_PROVIDER.anonKey,
      })
    );
    assert.deepEqual(
      calls.urls,
      [REFRESH_URL, MINT_URL],
      'one refresh attempt total — the 401 path must not retry a refresh that already failed'
    );
    assert.equal(calls.inits[1].headers.Authorization, 'Bearer jwt-stale');
    assert.equal(fs.existsSync(path.join(home, '.reliant', 'daemon.json')), false);
  });
});

test('ensureDaemonPATForOrigin: saveAuth blowing up does not block the mint (never-throw contract)', async () => {
  await withFakeHome(async (home) => {
    const authStorage = {
      loadStoredAuth: () => expiredSession(),
      saveAuth: () => {
        throw new Error('keychain locked');
      },
    };
    const { stub, calls } = makeSequenceFetch([
      jsonResponse(200, {
        access_token: 'jwt-fresh',
        refresh_token: 'rt-2',
        expires_in: 3600,
        user: { id: 'user-1' },
      }),
      jsonResponse(200, { token: 'rlnt_pat_despite_persist_failure' }),
    ]);
    await withFetchStub(stub, () =>
      ensureDaemonPATForOrigin({
        authStorage,
        apiUrl: 'https://reliantapi.com',
        gatewayUrl: '',
        authUrl: AUTH_PROVIDER.authUrl,
        authAnonKey: AUTH_PROVIDER.anonKey,
      })
    );
    assert.deepEqual(calls.urls, [REFRESH_URL, MINT_URL]);
    const store = JSON.parse(
      fs.readFileSync(path.join(home, '.reliant', 'daemon.json'), 'utf8')
    );
    assert.equal(
      store.origins['https://reliantapi.com']['user-1'].pat,
      'rlnt_pat_despite_persist_failure'
    );
  });
});

// ─── The outcome contract ───────────────────────────────────────────────────
//
// BackendManager.maybeRepairDaemonCredentials SWITCHES on what this function
// returns: it retries ENSURE_FAILED (the mint lost a race with control-plane's
// boot and a later attempt can win) and stops on ENSURE_ALREADY_CURRENT
// (the store is already correct, so the RPC is short-circuited and no number
// of retries can change anything).
//
// Getting that backwards in either direction is a bug with teeth — retrying
// already-current spins forever against a no-op, while treating a failure as
// terminal is exactly the single-shot behaviour that left a daemon idling
// without credentials and the user staring at "no daemon connected". The
// reconciler's own tests stub this function, so without these the contract
// they depend on is asserted nowhere.

test('ensureDaemonPATForOrigin outcome: minted, when it writes a fresh credential', async () => {
  await withFakeHome(async () => {
    const authStorage = {
      loadStoredAuth: () => ({ access_token: 'jwt-abc', user: { id: 'user-1' } }),
    };
    const outcome = await withFetchStub(
      async () => jsonResponse(200, { token: 'rlnt_pat_fresh', tokenId: 'tok_1' }),
      async () =>
        ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        })
    );
    assert.equal(outcome, ENSURE_MINTED);
  });
});

test('ensureDaemonPATForOrigin outcome: already-current, and NO request is made', async () => {
  // The short-circuit that makes retrying pointless. Asserted together with
  // "fetch must not be called", because the outcome name is only meaningful
  // if it really does mean no RPC happened.
  await withFakeHome(async (home) => {
    const authStorage = {
      loadStoredAuth: () => ({ access_token: 'jwt-abc', user: { id: 'user-1' } }),
    };
    // Seed a credential that already belongs to the signed-in user.
    await withFetchStub(
      async () => jsonResponse(200, { token: 'rlnt_pat_seeded', tokenId: 'tok_0' }),
      async () =>
        ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        })
    );
    assert.ok(fs.existsSync(path.join(home, '.reliant', 'daemon.json')), 'precondition: seeded');

    const outcome = await withFetchStub(
      async () => {
        throw new Error('fetch must not be called when the store is already current');
      },
      async () =>
        ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        })
    );
    assert.equal(outcome, ENSURE_ALREADY_CURRENT);
  });
});

test('ensureDaemonPATForOrigin outcome: failed, when the API is not listening yet', async () => {
  // THE RACE, in miniature: control-plane has not bound its port, so every
  // mint attempt is refused. This must come back retryable — it is the one
  // outcome a second attempt can turn into success.
  await withFakeHome(async () => {
    const authStorage = {
      loadStoredAuth: () => ({ access_token: 'jwt-abc', user: { id: 'user-1' } }),
    };
    const outcome = await withFetchStub(
      async () => {
        const err = new Error('connect ECONNREFUSED 127.0.0.1:8090');
        err.code = 'ECONNREFUSED';
        throw err;
      },
      async () =>
        ensureDaemonPATForOrigin({
          authStorage,
          apiUrl: 'http://localhost:8090',
          gatewayUrl: '',
        })
    );
    assert.equal(outcome, ENSURE_FAILED);
  });
});

test('ensureDaemonPATForOrigin outcome: no-session / unavailable are distinct from failure', async () => {
  // Both mean "nothing to repair", and the reconciler must stay quiet for
  // them rather than treating a signed-out user as a fault to retry.
  await withFakeHome(async () => {
    const noSession = await withFetchStub(
      async () => {
        throw new Error('fetch must not be called without a session');
      },
      async () =>
        ensureDaemonPATForOrigin({
          authStorage: { loadStoredAuth: () => null },
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        })
    );
    assert.equal(noSession, ENSURE_NO_SESSION);

    const noStorage = await withFetchStub(
      async () => {
        throw new Error('fetch must not be called without auth storage');
      },
      async () =>
        ensureDaemonPATForOrigin({
          authStorage: null,
          apiUrl: 'https://reliantapi.com',
          gatewayUrl: '',
        })
    );
    assert.equal(noStorage, ENSURE_UNAVAILABLE);
  });
});
