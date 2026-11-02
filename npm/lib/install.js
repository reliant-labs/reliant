"use strict";
// Resolves, downloads, verifies and caches the forge binary for this package's
// version. Zero dependencies and no install scripts: the download happens on
// first run, so `npm install --ignore-scripts` and `npx` both work.
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

function releaseUrl(version, env = process.env) {
  const base = (
    env.FORGE_RELEASE_BASE_URL || `https://github.com/${REPO}/releases`
  ).replace(/\/+$/, "");
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

// ensureBinary returns the path of the cached forge binary for `version`,
// downloading and verifying it first when it is not cached yet.
async function ensureBinary(version, env = process.env) {
  const { asset, binary } = platformAsset();
  const dir = path.join(cacheRoot(env), version.replace(/^v/, ""));
  const target = path.join(dir, binary);
  if (fs.existsSync(target)) return target;

  const url = releaseUrl(version, env);
  process.stderr.write(
    `forge: downloading ${asset} for v${version.replace(/^v/, "")}\n`,
  );
  let checksums, archive;
  try {
    checksums = (await download(`${url}/checksums.txt`)).toString("utf8");
    archive = await download(`${url}/${asset}`);
  } catch (err) {
    throw new Error(
      `${err.message}\n  v${version} may not have binaries attached yet; try another version of @reliantlabs/forge, or build from source: go install github.com/${REPO}/cmd/forge@v${version}`,
    );
  }

  const want = expectedChecksum(checksums, asset);
  if (!want) throw new Error(`checksums.txt has no entry for ${asset}`);
  const got = crypto.createHash("sha256").update(archive).digest("hex");
  if (got !== want)
    throw new Error(
      `sha256 mismatch for ${asset} (expected ${want}, got ${got}); nothing was installed`,
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
  expectedChecksum,
  ensureBinary,
};
