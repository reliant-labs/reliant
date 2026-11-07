"use strict";
// Resolves, downloads, verifies and caches the forge binary for this package's
// version. Zero dependencies and no install scripts: the download happens on
// first run, so `npm install --ignore-scripts` and `npx` both work.
//
// WHAT THE DOWNLOAD IS CHECKED AGAINST. Not the release's checksums.txt: it
// comes from the same release as the archive, so whoever can replace one asset
// can replace both. A published package carries lib/release-checksums.txt
// instead, written by .github/workflows/release-npm.yml only after the
// release's Sigstore signature (release-attest.yml) verified, so the archive
// must match the signed digest, and a release asset swapped after publication
// cannot reach an npm user. Verifying the signature itself here would need the
// sigstore package and its dependency tree; the pin needs nothing, and the
// package that carries it is covered by npm's own provenance attestation.
//
// A copy with no pin (a checkout, a hand-packed tarball) falls back to the
// release's checksums.txt with a warning; FORGE_REQUIRE_SIGNATURE=1 refuses.
//
// Asset names come from scripts/build-release-assets.sh; keep this in step.

const crypto = require("node:crypto");
const fs = require("node:fs");
const https = require("node:https");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const REPO = "reliant-labs/forge";

const OS_NAMES = { linux: "linux", darwin: "darwin", win32: "windows" };
const ARCH_NAMES = { x64: "amd64", arm64: "arm64" };

function platformAsset(platform = process.platform, arch = process.arch) {
  const goos = OS_NAMES[platform];
  const goarch = ARCH_NAMES[arch];
  if (!goos)
    throw new Error(
      `unsupported OS: ${platform} (releases cover linux, darwin and windows)`,
    );
  if (!goarch)
    throw new Error(
      `unsupported CPU: ${arch} (releases cover amd64 and arm64)`,
    );
  return {
    asset: `forge_${goos}_${goarch}.tar.gz`,
    binary: goos === "windows" ? "forge.exe" : "forge",
  };
}

// The release checksums release-npm.yml pins into a published package.
const PINNED_CHECKSUMS = path.join(__dirname, "release-checksums.txt");

function readPinnedChecksums(file = PINNED_CHECKSUMS) {
  try {
    return fs.readFileSync(file, "utf8");
  } catch (err) {
    if (err.code === "ENOENT") return null;
    throw err;
  }
}

function requireSignature(env = process.env) {
  const raw = env.FORGE_REQUIRE_SIGNATURE || "";
  const v = raw.toLowerCase();
  if (v === "1" || v === "true" || v === "yes") return true;
  if (v === "" || v === "0" || v === "false" || v === "no") return false;
  throw new Error(`FORGE_REQUIRE_SIGNATURE must be 1 or 0 (got: ${raw})`);
}

// checkTransport allows https, and plain http only to this machine (a local
// mirror or the tests' fixture server). It is applied to every redirect hop,
// so a redirect cannot downgrade a download to http.
function checkTransport(url) {
  const u = new URL(url);
  if (u.protocol === "https:") return;
  const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(u.hostname);
  if (u.protocol === "http:" && loopback) return;
  throw new Error(
    `refusing to download ${u.protocol}//${u.host}${u.pathname}: only https is allowed`,
  );
}

function releaseUrl(version, env = process.env) {
  const base = (
    env.FORGE_RELEASE_BASE_URL || `https://github.com/${REPO}/releases`
  ).replace(/\/+$/, "");
  checkTransport(base);
  return `${base}/download/v${version.replace(/^v/, "")}`;
}

function cacheRoot(env = process.env, platform = process.platform) {
  if (env.FORGE_NPM_CACHE_DIR) return env.FORGE_NPM_CACHE_DIR;
  if (platform === "win32") {
    return path.join(
      env.LOCALAPPDATA || path.join(os.homedir(), "AppData", "Local"),
      "forge",
      "npm",
    );
  }
  if (platform === "darwin")
    return path.join(os.homedir(), "Library", "Caches", "forge", "npm");
  return path.join(
    env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache"),
    "forge",
    "npm",
  );
}

function download(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    try {
      checkTransport(url);
    } catch (err) {
      return reject(err);
    }
    const get = url.startsWith("http:") ? http.get : https.get;
    get(url, { headers: { "user-agent": "@reliantlabs/forge" } }, (res) => {
      if (
        res.statusCode >= 300 &&
        res.statusCode < 400 &&
        res.headers.location
      ) {
        res.resume();
        if (redirects <= 0)
          return reject(new Error(`too many redirects fetching ${url}`));
        return resolve(
          download(
            new URL(res.headers.location, url).toString(),
            redirects - 1,
          ),
        );
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`GET ${url} returned HTTP ${res.statusCode}`));
      }
      const chunks = [];
      res.on("data", (c) => chunks.push(c));
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    }).on("error", reject);
  });
}

function expectedChecksum(checksums, asset) {
  for (const line of checksums.split("\n")) {
    const m = /^([0-9a-fA-F]{64})\s+\*?(\S+)\s*$/.exec(line);
    if (m && m[2] === asset) return m[1].toLowerCase();
  }
  return null;
}

// cacheDir is where the binary for `version` verified against `want` (the
// pinned sha256 of its archive) is cached, or, with no pin, the separate
// directory for binaries checked only against the release's own checksums.txt.
// Keying verified entries by the signed digest means a binary cached
// unverified, or verified against another pin, never satisfies a pinned run.
function cacheDir(version, want, env = process.env) {
  return path.join(
    cacheRoot(env),
    version.replace(/^v/, ""),
    want === null ? "unverified" : `sha256-${want}`,
  );
}

const NO_PIN =
  "this copy of @reliantlabs/forge carries no signed release checksums (lib/release-checksums.txt; packages published to npm do)";

// ensureBinary returns the path of the cached forge binary for `version`,
// downloading and verifying it first when it is not cached yet. `pinned` is
// the release checksums the package carries (null when it carries none).
// The policy (FORGE_REQUIRE_SIGNATURE, and the pin it demands) is checked
// before the cache is consulted, so a cache hit never bypasses it.
async function ensureBinary(
  version,
  env = process.env,
  { pinned = readPinnedChecksums() } = {},
) {
  const { asset, binary } = platformAsset();
  const strict = requireSignature(env);
  if (pinned === null && strict)
    throw new Error(
      `${NO_PIN}, and FORGE_REQUIRE_SIGNATURE=1 refuses an unverified install; nothing was installed`,
    );
  const source =
    pinned === null
      ? "checksums.txt"
      : `the checksums pinned in @reliantlabs/forge@${version.replace(/^v/, "")}`;
  let want = null;
  if (pinned !== null) {
    want = expectedChecksum(pinned, asset);
    if (!want) throw new Error(`${source} has no entry for ${asset}`);
  }
  const dir = cacheDir(version, want, env);
  const target = path.join(dir, binary);
  if (fs.existsSync(target)) return target;

  const url = releaseUrl(version, env);
  if (pinned === null) {
    process.stderr.write(
      `forge: warning: ${NO_PIN}.\n  Checking the download against the release's own checksums.txt, which cannot detect a tampered release.\n  Set FORGE_REQUIRE_SIGNATURE=1 to refuse an unverified install instead.\n`,
    );
  }
  process.stderr.write(
    `forge: downloading ${asset} for v${version.replace(/^v/, "")}\n`,
  );
  let checksums = pinned;
  let archive;
  try {
    if (checksums === null)
      checksums = (await download(`${url}/checksums.txt`)).toString("utf8");
    archive = await download(`${url}/${asset}`);
  } catch (err) {
    throw new Error(
      `${err.message}\n  v${version} may not have binaries attached yet; try another version of @reliantlabs/forge, or build from source: go install github.com/${REPO}/cmd/forge@v${version}`,
    );
  }

  if (want === null) {
    want = expectedChecksum(checksums, asset);
    if (!want) throw new Error(`${source} has no entry for ${asset}`);
  }
  const got = crypto.createHash("sha256").update(archive).digest("hex");
  if (got !== want)
    throw new Error(
      `sha256 mismatch for ${asset} (${source} expect ${want}, the download is ${got}); nothing was installed` +
        (pinned === null
          ? ""
          : `.\n  The release asset differs from the one that was signed: it may have been tampered with. Report it: https://github.com/${REPO}/security`),
    );

  fs.mkdirSync(dir, { recursive: true });
  const stage = fs.mkdtempSync(path.join(dir, ".stage-"));
  try {
    const tarball = path.join(stage, asset);
    fs.writeFileSync(tarball, archive);
    const res = spawnSync("tar", ["-xzf", tarball, "-C", stage], {
      stdio: ["ignore", "inherit", "inherit"],
    });
    if (res.error || res.status !== 0) {
      throw new Error(
        `could not unpack ${asset} with tar${res.error ? `: ${res.error.message}` : ""}`,
      );
    }
    const unpacked = path.join(stage, binary);
    if (!fs.existsSync(unpacked))
      throw new Error(`${asset} does not contain ${binary}`);
    fs.chmodSync(unpacked, 0o755);
    fs.renameSync(unpacked, target);
  } finally {
    fs.rmSync(stage, { recursive: true, force: true });
  }
  return target;
}

module.exports = {
  platformAsset,
  releaseUrl,
  cacheRoot,
  cacheDir,
  expectedChecksum,
  readPinnedChecksums,
  requireSignature,
  ensureBinary,
};
