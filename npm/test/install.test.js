"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawn, execFileSync } = require("node:child_process");

const {
  platformAsset,
  expectedChecksum,
  releaseUrl,
  readPinnedChecksums,
  requireSignature,
  ensureBinary,
} = require("../lib/install.js");
const pkg = require("../package.json");

test("platformAsset maps node platform/arch to release asset names", () => {
  assert.deepEqual(platformAsset("linux", "x64"), {
    asset: "forge_linux_amd64.tar.gz",
    binary: "forge",
  });
  assert.deepEqual(platformAsset("darwin", "arm64"), {
    asset: "forge_darwin_arm64.tar.gz",
    binary: "forge",
  });
  assert.deepEqual(platformAsset("win32", "arm64"), {
    asset: "forge_windows_arm64.tar.gz",
    binary: "forge.exe",
  });
  assert.throws(() => platformAsset("freebsd", "x64"), /unsupported OS/);
  assert.throws(() => platformAsset("linux", "ia32"), /unsupported CPU/);
});

test("expectedChecksum finds the asset line, text or binary mode", () => {
  const a = "a".repeat(64);
  const b = "b".repeat(64);
  const sums = `${a}  forge_linux_amd64.tar.gz\n${b} *forge_darwin_arm64.tar.gz\n`;
  assert.equal(expectedChecksum(sums, "forge_linux_amd64.tar.gz"), a);
  assert.equal(expectedChecksum(sums, "forge_darwin_arm64.tar.gz"), b);
  assert.equal(expectedChecksum(sums, "forge_windows_amd64.tar.gz"), null);
});

test("releaseUrl pins the package version and honours the override", () => {
  assert.equal(
    releaseUrl("0.1.46", {}),
    "https://github.com/reliant-labs/forge/releases/download/v0.1.46",
  );
  assert.equal(
    releaseUrl("v0.1.46", { FORGE_RELEASE_BASE_URL: "https://x/r/" }),
    "https://x/r/download/v0.1.46",
  );
  assert.equal(
    releaseUrl("v0.1.46", { FORGE_RELEASE_BASE_URL: "http://127.0.0.1:9/r" }),
    "http://127.0.0.1:9/r/download/v0.1.46",
  );
});

test("releaseUrl refuses a plain-http override to another host", () => {
  assert.throws(
    () => releaseUrl("0.1.46", { FORGE_RELEASE_BASE_URL: "http://x/r/" }),
    /only https is allowed/,
  );
  assert.throws(
    () => releaseUrl("0.1.46", { FORGE_RELEASE_BASE_URL: "file:///tmp/r" }),
    /only https is allowed/,
  );
});

test("requireSignature accepts 1/0 spellings and rejects anything else", () => {
  assert.equal(requireSignature({}), false);
  assert.equal(requireSignature({ FORGE_REQUIRE_SIGNATURE: "0" }), false);
  assert.equal(requireSignature({ FORGE_REQUIRE_SIGNATURE: "1" }), true);
  assert.equal(requireSignature({ FORGE_REQUIRE_SIGNATURE: "TRUE" }), true);
  assert.throws(
    () => requireSignature({ FORGE_REQUIRE_SIGNATURE: "please" }),
    /must be 1 or 0/,
  );
});

test("readPinnedChecksums is null when the package carries no pin", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-pin-"));
  assert.equal(readPinnedChecksums(path.join(dir, "absent.txt")), null);
  fs.writeFileSync(path.join(dir, "pin.txt"), "x");
  assert.equal(readPinnedChecksums(path.join(dir, "pin.txt")), "x");
});

test("package.json is publishable as @reliantlabs/forge", () => {
  assert.equal(pkg.name, "@reliantlabs/forge");
  assert.match(pkg.version, /^\d+\.\d+\.\d+$/);
  assert.equal(pkg.bin.forge, "bin/forge.js");
  assert.equal(pkg.publishConfig.access, "public");
  assert.equal(
    pkg.scripts.postinstall,
    undefined,
    "no install scripts: --ignore-scripts must be enough",
  );
  assert.equal(pkg.dependencies, undefined, "the shim has no dependencies");
});

// serves a fake release for `version`; sumOverride corrupts the checksum,
// redirects maps a URL path to the Location it answers 302 with, and
// shScript replaces the fake binary's body (POSIX only).
function serve(version, { sumOverride, redirects = {}, shScript } = {}) {
  const { asset, binary } = platformAsset();
  const stage = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-fixture-"));
  const script = shScript
    ? shScript
    : binary === "forge.exe"
      ? "@echo off\r\necho forge fake %*\r\n"
      : '#!/bin/sh\necho "forge fake $*"\nexit 3\n';
  fs.writeFileSync(path.join(stage, binary), script, { mode: 0o755 });
  execFileSync("tar", ["-czf", path.join(stage, asset), "-C", stage, binary]);
  const archive = fs.readFileSync(path.join(stage, asset));
  const sum =
    sumOverride || crypto.createHash("sha256").update(archive).digest("hex");
  const files = {
    [`/download/v${version}/${asset}`]: archive,
    [`/download/v${version}/checksums.txt`]: Buffer.from(`${sum}  ${asset}\n`),
  };
  const hits = [];
  const server = http.createServer((req, res) => {
    hits.push(req.url);
    if (redirects[req.url]) {
      res.writeHead(302, { location: redirects[req.url] }).end();
      return;
    }
    const body = files[req.url];
    if (!body) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200).end(body);
  });
  return new Promise((resolve) =>
    server.listen(0, "127.0.0.1", () =>
      resolve({
        server,
        hits,
        asset,
        sum: crypto.createHash("sha256").update(archive).digest("hex"),
        base: `http://127.0.0.1:${server.address().port}`,
      }),
    ),
  );
}

// cachedBinaries lists every forge binary under a cache root, at any depth.
function cachedBinaries(cache) {
  const { binary } = platformAsset();
  if (!fs.existsSync(cache)) return [];
  return fs
    .readdirSync(cache, { recursive: true })
    .filter((f) => path.basename(f) === binary);
}

test("ensureBinary downloads, verifies, caches and reuses the binary", async () => {
  const { server, hits, base } = await serve("9.9.9");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  const env = { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache };
  try {
    const first = await ensureBinary("9.9.9", env, { pinned: null });
    assert.ok(first.startsWith(cache), first);
    assert.ok(fs.existsSync(first));
    const downloads = hits.length;
    assert.equal(await ensureBinary("9.9.9", env, { pinned: null }), first);
    assert.equal(
      hits.length,
      downloads,
      "second call must be served from the cache",
    );
  } finally {
    server.close();
  }
});

test("ensureBinary refuses a checksum mismatch and caches nothing", async () => {
  const { server, base } = await serve("9.9.8", {
    sumOverride: "ab".repeat(32),
  });
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary(
        "9.9.8",
        { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache },
        { pinned: null },
      ),
      /sha256 mismatch/,
    );
    assert.deepEqual(cachedBinaries(cache), []);
  } finally {
    server.close();
  }
});

test("ensureBinary explains a version with no binaries", async () => {
  const { server, base } = await serve("9.9.7");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary(
        "1.0.0",
        { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache },
        { pinned: null },
      ),
      /HTTP 404[\s\S]*binaries attached/,
    );
  } finally {
    server.close();
  }
});

// RF-2: an attacker who can write release assets replaces the archive AND
// checksums.txt, so the two agree. The pin the package carries is the signed
// digest, so the swap is refused, and the release's checksums.txt is not read.
test("ensureBinary refuses a release whose archive and checksums.txt were both replaced", async () => {
  const { server, hits, asset, base } = await serve("9.9.6");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  const signed = `${"cd".repeat(32)}  ${asset}\n`;
  try {
    await assert.rejects(
      ensureBinary(
        "9.9.6",
        { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache },
        { pinned: signed },
      ),
      /sha256 mismatch[\s\S]*pinned in @reliantlabs\/forge@9\.9\.6[\s\S]*tampered/,
    );
    assert.deepEqual(cachedBinaries(cache), []);
    assert.ok(
      !hits.some((h) => h.endsWith("/checksums.txt")),
      `a pinned package must not read the release's checksums.txt: ${hits}`,
    );
  } finally {
    server.close();
  }
});

test("ensureBinary installs against the pinned checksums alone", async () => {
  const { server, hits, asset, sum, base } = await serve("9.9.5");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    const bin = await ensureBinary(
      "9.9.5",
      {
        FORGE_RELEASE_BASE_URL: base,
        FORGE_NPM_CACHE_DIR: cache,
        FORGE_REQUIRE_SIGNATURE: "1",
      },
      { pinned: `${sum}  ${asset}\n` },
    );
    assert.ok(fs.existsSync(bin));
    assert.deepEqual(hits, [`/download/v9.9.5/${asset}`]);
  } finally {
    server.close();
  }
});

test("ensureBinary refuses a pin with no entry for this platform", async () => {
  const { server, base } = await serve("9.9.4");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary(
        "9.9.4",
        { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache },
        { pinned: `${"0".repeat(64)}  forge_other_os.tar.gz\n` },
      ),
      /pinned in @reliantlabs\/forge@9\.9\.4 has no entry/,
    );
  } finally {
    server.close();
  }
});

// A missing signature: with FORGE_REQUIRE_SIGNATURE=1 a package that carries
// no signed checksums installs nothing and touches no network.
test("ensureBinary with FORGE_REQUIRE_SIGNATURE=1 refuses an unpinned package", async () => {
  const { server, hits, base } = await serve("9.9.3");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary(
        "9.9.3",
        {
          FORGE_RELEASE_BASE_URL: base,
          FORGE_NPM_CACHE_DIR: cache,
          FORGE_REQUIRE_SIGNATURE: "1",
        },
        { pinned: null },
      ),
      /no signed release checksums[\s\S]*FORGE_REQUIRE_SIGNATURE=1 refuses/,
    );
    assert.deepEqual(hits, []);
    assert.deepEqual(cachedBinaries(cache), []);
  } finally {
    server.close();
  }
});

test("ensureBinary refuses a redirect that downgrades to http", async () => {
  const { asset } = platformAsset();
  const { server, base } = await serve("9.9.2", {
    redirects: {
      [`/download/v9.9.2/${asset}`]: `http://example.invalid/${asset}`,
    },
  });
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary(
        "9.9.2",
        { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache },
        { pinned: `${"0".repeat(64)}  ${asset}\n` },
      ),
      /refusing to download http:\/\/example\.invalid[\s\S]*only https/,
    );
  } finally {
    server.close();
  }
});

// The bin script end to end: runs the cached binary, forwards args and the exit code.
test(
  "bin/forge.js execs the downloaded binary with args and its exit code",
  { skip: process.platform === "win32" },
  async () => {
    const { server, base } = await serve(pkg.version);
    const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
    try {
      const res = await new Promise((resolve) => {
        const child = spawn(
          process.execPath,
          [path.join(__dirname, "..", "bin", "forge.js"), "version", "--short"],
          {
            env: {
              ...process.env,
              FORGE_RELEASE_BASE_URL: base,
              FORGE_NPM_CACHE_DIR: cache,
            },
          },
        );
        let stdout = "";
        child.stdout.on("data", (d) => (stdout += d));
        child.on("close", (code) => resolve({ code, stdout }));
      });
      assert.equal(res.stdout.trim(), "forge fake version --short");
      assert.equal(res.code, 3, "the binary's exit code must be forwarded");
    } finally {
      server.close();
    }
  },
);

// `npx -y @reliantlabs/forge mcp` is an MCP stdio server: stdout belongs to
// the protocol, so on the very first run (the download) the shim must write
// nothing to stdout but the binary's own bytes, and pass stdin through.
test(
  "bin/forge.js keeps stdout to the binary's bytes, for `forge mcp` over stdio",
  { skip: process.platform === "win32" },
  async () => {
    const { server, base } = await serve(pkg.version, {
      shScript: '#!/bin/sh\n[ "$1" = mcp ] || exit 9\ncat\n',
    });
    const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
    const request =
      '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n';
    try {
      const res = await new Promise((resolve) => {
        const child = spawn(
          process.execPath,
          [path.join(__dirname, "..", "bin", "forge.js"), "mcp"],
          {
            env: {
              ...process.env,
              FORGE_RELEASE_BASE_URL: base,
              FORGE_NPM_CACHE_DIR: cache,
            },
          },
        );
        let stdout = "";
        let stderr = "";
        child.stdout.on("data", (d) => (stdout += d));
        child.stderr.on("data", (d) => (stderr += d));
        child.on("close", (code) => resolve({ code, stdout, stderr }));
        child.stdin.end(request);
      });
      assert.equal(res.code, 0, res.stderr);
      assert.equal(res.stdout, request, "stdout must carry the protocol alone");
      assert.match(
        res.stderr,
        /downloading/,
        "the download notice goes to stderr",
      );
    } finally {
      server.close();
    }
  },
);

// A binary cached by an unpinned run (checked only against the release's own
// checksums.txt) must never satisfy a run that requires a signature: strict
// mode still refuses an unpinned copy, and a pinned run downloads and verifies
// its own copy instead of executing the unverified one.
test("ensureBinary never lets an unverified cache entry satisfy a verified run", async () => {
  const { server, hits, asset, sum, base } = await serve("9.9.1");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  const env = { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache };
  try {
    const unverified = await ensureBinary("9.9.1", env, { pinned: null });
    const before = hits.length;

    await assert.rejects(
      ensureBinary(
        "9.9.1",
        { ...env, FORGE_REQUIRE_SIGNATURE: "1" },
        { pinned: null },
      ),
      /no signed release checksums[\s\S]*FORGE_REQUIRE_SIGNATURE=1 refuses/,
    );
    await assert.rejects(
      ensureBinary(
        "9.9.1",
        { ...env, FORGE_REQUIRE_SIGNATURE: "maybe" },
        { pinned: null },
      ),
      /FORGE_REQUIRE_SIGNATURE must be 1 or 0/,
    );
    assert.equal(hits.length, before, "a refusal must not touch the network");

    const verified = await ensureBinary(
      "9.9.1",
      { ...env, FORGE_REQUIRE_SIGNATURE: "1" },
      { pinned: `${sum}  ${asset}\n` },
    );
    assert.notEqual(verified, unverified);
    assert.deepEqual(hits.slice(before), [`/download/v9.9.1/${asset}`]);
  } finally {
    server.close();
  }
});

// The verified cache is keyed by the pinned digest: a binary verified against
// one pin is not reused for a package that pins another digest.
test("ensureBinary keys the verified cache by the pinned digest", async () => {
  const { server, hits, asset, sum, base } = await serve("9.9.0");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  const env = { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache };
  try {
    const first = await ensureBinary("9.9.0", env, {
      pinned: `${sum}  ${asset}\n`,
    });
    assert.equal(
      await ensureBinary("9.9.0", env, { pinned: `${sum}  ${asset}\n` }),
      first,
    );
    const downloads = hits.length;
    await assert.rejects(
      ensureBinary("9.9.0", env, { pinned: `${"ef".repeat(32)}  ${asset}\n` }),
      /sha256 mismatch/,
    );
    assert.equal(hits.length, downloads + 1, "another pin must download again");
  } finally {
    server.close();
  }
});
