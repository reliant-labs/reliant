// node --test .github/scripts/*.test.mjs
//
// Two halves:
//
//  1. verify-bundle.mjs itself, on synthetic dist trees (no build, so this is
//     fast): it fails a bundle carrying the code that broke v1.8.1's release,
//     and passes the same bundle without it.
//
//  2. `task build:web:verify` (ci.yml's `web-bundle` job) builds and verifies
//     the bundle exactly as release.yml's prepare job does. Before that,
//     verify-bundle ran only once a tag had been pushed, so the v1.8.1 false
//     positive merged green and failed the release instead. If either build
//     drifts, this fails on the change that moved it.

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

/** A minimal dist that passes every check: index.html loading config.js, then a hashed chunk. */
function fakeDist(t, extraJs = "", head = '<script src="/config.js"></script>\n') {
  const dir = mkdtempSync(join(tmpdir(), "verify-bundle-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "assets"));
  writeFileSync(join(dir, "index.html"), `${head}<script type="module" crossorigin src="/assets/index-Ab12Cd.js"></script>\n`);
  writeFileSync(join(dir, "assets", "index-Ab12Cd.js"), `const c=window.__FORGE_CONFIG__;${extraJs}\n`);
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

test("a bundle that does not load config.js fails [3/4]", (t) => {
  const dir = fakeDist(t, "", "");
  const { status, stdout, stderr } = verify(dir);
  assert.equal(status, 1, stdout + stderr);
  assert.match(stderr, /does not load \/config\.js/);
});

test("a bundle that loads config.js after the module bundle fails [3/4]", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "verify-bundle-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, "assets"));
  writeFileSync(
    join(dir, "index.html"),
    '<script type="module" crossorigin src="/assets/index-Ab12Cd.js"></script>\n<script src="/config.js"></script>\n',
  );
  writeFileSync(join(dir, "assets", "index-Ab12Cd.js"), "const c=1;\n");
  const { status, stdout, stderr } = verify(dir);
  assert.equal(status, 1, stdout + stderr);
  assert.match(stderr, /before \/config\.js/);
});

// ── 2. CI builds what the release builds ────────────────────────────────────

/** One job's lines, with comments dropped and `\`-continued lines joined. */
function jobBlock(yml, job) {
  const lines = yml.split("\n");
  const start = lines.indexOf(`  ${job}:`);
  assert.notEqual(start, -1, `no \`${job}:\` job`);
  let end = lines.findIndex((line, i) => i > start && /^ {2}[A-Za-z0-9_:-]+:\s*$/.test(line));
  if (end === -1) end = lines.length;
  return lines
    .slice(start, end)
    .filter((line) => !/^\s*#/.test(line))
    .join("\n")
    .replace(/\\\n\s*/g, "");
}

// ci.yml's web-bundle job is `task build:web:verify`; the Taskfile task is
// where its build and verify commands live, once.
const RELEASE_PREPARE = jobBlock(read(".github", "workflows", "release.yml"), "prepare");
const CI_WEB = jobBlock(read(".github", "workflows", "ci.yml"), "web-bundle");
const TASK_WEB = jobBlock(read("Taskfile.yml"), "build:web:verify");

// The bundle reads its config from config.js at runtime, so a build handed the
// renderer's release config through the environment is a build reading it
// again. (`.main` is the main process's, and is exported for its jobs.)
const EXPORT_RE = /'\.(vite|renderer)\b[^\n]*release\.config\.json[^\n]*>> "\$GITHUB_ENV"/;

for (const [name, block, build, verify, buildLine] of [
  [
    "release.yml's prepare job",
    RELEASE_PREPARE,
    /working-directory: frontends\/web\n\s+run: NODE_ENV=production npm run build\n/,
    "run: node .github/scripts/verify-bundle.mjs --dist frontends/web/dist\n",
    "run: NODE_ENV=production npm run build",
  ],
  [
    "Taskfile.yml's build:web:verify",
    TASK_WEB,
    /- cd frontends\/web && NODE_ENV=production npm run build\n/,
    "- node .github/scripts/verify-bundle.mjs --dist frontends/web/dist",
    "- cd frontends/web && NODE_ENV=production npm run build",
  ],
]) {
  test(`${name} builds with NODE_ENV=production in frontends/web/ from no release config, then verifies the bundle`, () => {
    const built = block.search(build);
    const verified = block.indexOf(verify);
    assert.ok(!EXPORT_RE.test(block), `${name} still exports release.config.json into the build environment; the bundle reads config.js`);
    assert.notEqual(built, -1, `${name} no longer runs \`NODE_ENV=production npm run build\` in frontends/web/`);
    assert.notEqual(verified, -1, `${name} no longer runs:\n  ${verify}`);
    assert.ok(built < verified, `${name} must build, then verify`);
  });

  test(`${name} sets NODE_ENV on the build command only, never on npm ci`, () => {
    // npm reads NODE_ENV too: production skips devDependencies, vite and
    // typescript included. A job- or step-level NODE_ENV leaks onto installs.
    const uses = block.split("\n").filter((line) => line.includes("NODE_ENV"));
    assert.deepEqual(
      uses.map((line) => line.trim()),
      [buildLine],
      `${name} sets NODE_ENV somewhere other than the build command`,
    );
  });
}

test("CI's web-bundle job runs the task, on every change that can alter the bundle it verifies", () => {
  assert.match(CI_WEB, /\n {4}if: needs\.lanes\.outputs\.web == 'true' && github\.event_name != 'merge_group'\n/);
  assert.match(CI_WEB, /\n\s+run: task build:web:verify\n/);
  assert.ok(!CI_WEB.includes("NODE_ENV"), "ci.yml's web-bundle job must not set NODE_ENV: the task does, on the build command only");

  const lanes = read("scripts", "ci-lanes.sh");
  const m = /if grep -qE '([^']+)' <<< "\$changed"; then web=true; fi/.exec(lanes);
  assert.ok(m, "scripts/ci-lanes.sh no longer classifies the web lane with `if grep -qE '<regex>' ...; then web=true; fi`; update this test");
  const webLane = new RegExp(m[1]);
  for (const path of [".github/scripts/verify-bundle.mjs", "electron/release.config.json", "frontends/web/src/App.tsx", "frontends/web/pnpm-lock.yaml", "package.json"]) {
    assert.ok(webLane.test(path), `a change to ${path} must run the web-bundle job`);
  }
  for (const path of ["go.mod", "internal/db/db.go", "docs/index.mdx"]) {
    assert.ok(!webLane.test(path), `a change to ${path} alone cannot alter the bundle`);
  }
});
