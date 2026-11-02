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
    releaseUrl("v0.1.46", { FORGE_RELEASE_BASE_URL: "http://x/r/" }),
    "http://x/r/download/v0.1.46",
  );
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

// serves a fake release for `version`; sumOverride corrupts the checksum.
function serve(version, { sumOverride } = {}) {
  const { asset, binary } = platformAsset();
  const stage = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-fixture-"));
  const script =
    binary === "forge.exe"
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
        base: `http://127.0.0.1:${server.address().port}`,
      }),
    ),
  );
}

test("ensureBinary downloads, verifies, caches and reuses the binary", async () => {
  const { server, hits, base } = await serve("9.9.9");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  const env = { FORGE_RELEASE_BASE_URL: base, FORGE_NPM_CACHE_DIR: cache };
  try {
    const first = await ensureBinary("9.9.9", env);
    assert.ok(first.startsWith(cache), first);
    assert.ok(fs.existsSync(first));
    const downloads = hits.length;
    assert.equal(await ensureBinary("9.9.9", env), first);
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
      ensureBinary("9.9.8", {
        FORGE_RELEASE_BASE_URL: base,
        FORGE_NPM_CACHE_DIR: cache,
      }),
      /sha256 mismatch/,
    );
    const { binary } = platformAsset();
    assert.ok(!fs.existsSync(path.join(cache, "9.9.8", binary)));
  } finally {
    server.close();
  }
});

test("ensureBinary explains a version with no binaries", async () => {
  const { server, base } = await serve("9.9.7");
  const cache = fs.mkdtempSync(path.join(os.tmpdir(), "forge-npm-cache-"));
  try {
    await assert.rejects(
      ensureBinary("1.0.0", {
        FORGE_RELEASE_BASE_URL: base,
        FORGE_NPM_CACHE_DIR: cache,
      }),
      /HTTP 404[\s\S]*binaries attached/,
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
