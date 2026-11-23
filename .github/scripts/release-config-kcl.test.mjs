// renderReleaseConfig's choice of forge, against fake forge and reliant
// commands on a PATH holding nothing else: forge on PATH when there is one,
// else `reliant forge` (a machine with only reliant installed), else the
// render is unavailable (the fork case). A forge that runs and fails is a
// real defect, never "unavailable".
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, test } from "node:test";

import { KCL_ENTRYPOINT, ReleaseConfigRenderError, renderReleaseConfig } from "./release-config-kcl.mjs";

let tmp;
let controlPlane;
let log;
const savedPath = process.env.PATH;

// A fake forge: logs who ran it and with what, then prints the rendered
// config on stdout, as `forge env kcl eval --format json` does.
const fakeForge = (who, exit = 0) => `#!/bin/sh
echo "${who} $*" >>"${"$"}{FAKE_LOG}"
[ ${exit} = 0 ] || { echo "${who} failed" >&2; exit ${exit}; }
printf '{"env":"rendered"}'
`;

function bin(dir, name, body) {
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, name), body);
  chmodSync(join(dir, name), 0o755);
  return dir;
}

before(() => {
  tmp = mkdtempSync(join(tmpdir(), "release-config-kcl-test-"));
  controlPlane = join(tmp, "control-plane");
  mkdirSync(join(controlPlane, "deploy", "kcl"), { recursive: true });
  writeFileSync(join(controlPlane, KCL_ENTRYPOINT), "release_config = {}\n");
  // An empty tools dir: the fakes use only sh builtins, and nothing on PATH
  // could be forge or reliant.
  mkdirSync(join(tmp, "tools"));
  bin(join(tmp, "forge"), "forge", fakeForge("forge"));
  bin(join(tmp, "reliant"), "reliant", fakeForge("reliant"));
  bin(join(tmp, "reliant-fails"), "reliant", fakeForge("reliant", 3));
  log = join(tmp, "log");
  process.env.FAKE_LOG = log;
});

after(() => {
  process.env.PATH = savedPath;
  delete process.env.FAKE_LOG;
  rmSync(tmp, { recursive: true, force: true });
});

function render(...dirs) {
  writeFileSync(log, "");
  process.env.PATH = [join(tmp, "tools"), ...dirs.map((d) => join(tmp, d))].join(":");
  try {
    return renderReleaseConfig(controlPlane, "prod");
  } finally {
    process.env.PATH = savedPath;
  }
}

test("with only reliant installed, `reliant forge env kcl eval` renders it", () => {
  const got = render("reliant");
  assert.deepEqual(got, { config: { env: "rendered" } });
  assert.match(readFileSync(log, "utf8"), new RegExp(`^reliant forge env kcl eval ${KCL_ENTRYPOINT} -D env=prod -S release_config --format json\\n$`));
});

test("forge on PATH is preferred to reliant", () => {
  const got = render("forge", "reliant");
  assert.deepEqual(got, { config: { env: "rendered" } });
  assert.match(readFileSync(log, "utf8"), /^forge env kcl eval /);
  assert.doesNotMatch(readFileSync(log, "utf8"), /reliant/);
});

test("neither forge nor reliant: unavailable, not an error", () => {
  assert.deepEqual(render(), { unavailable: "neither forge nor reliant is installed" });
});

test("a reliant forge that runs and fails is a render error, not a skip", () => {
  assert.throws(() => render("reliant-fails"), (err) => err instanceof ReleaseConfigRenderError && /reliant failed/.test(err.message));
});
