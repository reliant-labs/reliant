// node --test .github/scripts/*.test.mjs
//
// Two halves:
//
//  1. verify-bundle.mjs itself, on synthetic dist trees (no build, so this is
//     fast): it fails a bundle carrying the code that broke v1.8.1's release,
//     and passes the same bundle without it.
//
//  2. PR CI's `web` job builds and verifies the bundle exactly as release.yml's
//     prepare job does. Before that, verify-bundle ran only once a tag had been
//     pushed, so the v1.8.1 false positive merged green and failed the release
//     instead. If either workflow's build drifts, this fails on the PR that
//     moved it.

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const VERIFY = join(REPO_ROOT, ".github", "scripts", "verify-bundle.mjs");
const read = (...parts) => readFileSync(join(REPO_ROOT, ...parts), "utf8");

// ── 1. verify-bundle.mjs ────────────────────────────────────────────────────

const CONFIG = JSON.parse(read("electron", "release.config.json"));

/** A minimal dist that passes every check: index.html, a hashed chunk, every configured host. */
function fakeDist(t, extraJs = "") {
  const dir = mkdtempSync(join(tmpdir(), "verify-bundle-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "assets"));
  writeFileSync(join(dir, "index.html"), '<script type="module" crossorigin src="/assets/index-Ab12Cd.js"></script>\n');
  const hosts = Object.values(CONFIG.vite)
    .filter((v) => typeof v === "string" && /^https?:\/\//.test(v))
    .map((v) => new URL(v).hostname);
  writeFileSync(join(dir, "assets", "index-Ab12Cd.js"), `const h=[${hosts.map((h) => `"https://${h}"`).join(",")}];${extraJs}\n`);
  return dir;
}

const verify = (dir) => spawnSync(process.execPath, [VERIFY, "--dist", dir], { encoding: "utf8" });

test("a bundle carrying apiOrigin's old ternary fails [2/4], as v1.8.1's release did", (t) => {
  // What terser made of ActivateTriggerDialog's
  //   getGRPCBaseURLPublic() ?? (typeof window !== "undefined" ? window.location.origin : "")
  const dir = fakeDist(t, 'function Bz(){return Gx()??(typeof window<"u"?window.location.origin:"")}');
  const { status, stdout, stderr } = verify(dir);
  assert.equal(status, 1, stdout + stderr);
  assert.match(stderr, /::error::verify-bundle: bundle still contains the same-origin transport branch/);
  assert.match(stderr, /assets\/index-Ab12Cd\.js/, "names the offending asset");
});

test("the same bundle without it passes every check", (t) => {
  const dir = fakeDist(t, 'function Bz(){return Gx()??globalThis.location?.origin??""}');
  const { status, stdout, stderr } = verify(dir);
  assert.equal(status, 0, stdout + stderr);
  assert.match(stdout, /verify-bundle: all checks passed/);
});

// ── 2. PR CI builds what the release builds ─────────────────────────────────

/** One job's lines, with comments dropped and `\`-continued lines joined. */
function jobBlock(yml, job) {
  const lines = yml.split("\n");
  const start = lines.indexOf(`  ${job}:`);
  assert.notEqual(start, -1, `no \`${job}:\` job`);
  let end = lines.findIndex((line, i) => i > start && /^ {2}[A-Za-z0-9_-]+:\s*$/.test(line));
  if (end === -1) end = lines.length;
  return lines
    .slice(start, end)
    .filter((line) => !/^\s*#/.test(line))
    .join("\n")
    .replace(/\\\n\s*/g, "");
}

const PR_WEB = jobBlock(read(".github", "workflows", "pr-ci.yml"), "web");
const RELEASE_PREPARE = jobBlock(read(".github", "workflows", "release.yml"), "prepare");

const EXPORT = `jq -er '.vite | to_entries[] | "\\(.key)=\\(.value)"' electron/release.config.json >> "$GITHUB_ENV"`;
const BUILD = /working-directory: frontends\/web\n\s+run: NODE_ENV=production npm run build\n/;
const VERIFY_STEP = "run: node .github/scripts/verify-bundle.mjs --dist frontends/web/dist\n";

for (const [name, block] of [
  ["release.yml's prepare job", RELEASE_PREPARE],
  ["pr-ci.yml's web job", PR_WEB],
]) {
  test(`${name} exports .vite, builds with NODE_ENV=production in frontends/web/, then verifies the bundle`, () => {
    const exported = block.indexOf(EXPORT);
    const built = block.search(BUILD);
    const verified = block.indexOf(VERIFY_STEP);
    assert.notEqual(exported, -1, `${name} no longer exports release.config.json's .vite block with:\n  ${EXPORT}`);
    assert.notEqual(built, -1, `${name} no longer runs \`NODE_ENV=production npm run build\` in frontends/web/`);
    assert.notEqual(verified, -1, `${name} no longer runs:\n  ${VERIFY_STEP}`);
    assert.ok(exported < built && built < verified, `${name} must export the config, then build, then verify`);
  });

  test(`${name} sets NODE_ENV on the build command only, never on npm ci`, () => {
    // npm reads NODE_ENV too: production skips devDependencies, vite and
    // typescript included. A job- or step-level NODE_ENV leaks onto installs.
    const uses = block.split("\n").filter((line) => line.includes("NODE_ENV"));
    assert.deepEqual(
      uses.map((line) => line.trim()),
      ["run: NODE_ENV=production npm run build"],
      `${name} sets NODE_ENV somewhere other than the build command`,
    );
  });
}

test("PR CI's web job runs on every change that can alter the bundle it verifies", () => {
  assert.match(PR_WEB, /\n {4}if: needs\.changes\.outputs\.web == 'true'\n/);

  const changes = jobBlock(read(".github", "workflows", "pr-ci.yml"), "changes");
  const m = /if grep -qE '([^']+)' <<< "\$\{changed\}"; then web=true; fi/.exec(changes);
  assert.ok(m, "the `changes` job no longer classifies the web lane with `if grep -qE '<regex>' ...; then web=true; fi`; update this test");
  const webLane = new RegExp(m[1]);
  for (const path of [".github/scripts/verify-bundle.mjs", "electron/release.config.json", "frontends/web/src/App.tsx", "frontends/web/package-lock.json", "package.json"]) {
    assert.ok(webLane.test(path), `a change to ${path} must run the web job`);
  }
  for (const path of ["go.mod", "internal/db/db.go", "docs/index.mdx"]) {
    assert.ok(!webLane.test(path), `a change to ${path} alone cannot alter the bundle`);
  }
});
