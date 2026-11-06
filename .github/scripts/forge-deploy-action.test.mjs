// node --test .github/scripts/*.test.mjs
//
// .github/actions/forge-deploy/action.yml's deploy step, run as GitHub runs
// it (`shell: bash` is `bash --noprofile --norc -eo pipefail {0}`) against a
// fake `forge` on PATH. A deploy that exits nonzero after recording a
// promotion must still write promotion-id and exit-code, because the evidence
// step runs only when promotion-id is set: a red rollout is the one whose
// evidence matters most. Then the evidence step itself runs, with the output.

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const ACTION = readFileSync(join(REPO_ROOT, ".github", "actions", "forge-deploy", "action.yml"), "utf8");

/** The `run: |` block of the step whose header line matches `header`, dedented. */
function runBlock(header) {
  const lines = ACTION.split("\n");
  const start = lines.findIndex((l) => header.test(l));
  assert.notEqual(start, -1, `no step matching ${header}`);
  const runAt = lines.findIndex((l, i) => i > start && /^\s+run: [|]\s*$/.test(l));
  const indent = lines[runAt + 1].match(/^\s*/)[0].length;
  const body = [];
  for (let i = runAt + 1; i < lines.length; i++) {
    if (lines[i].trim() !== "" && lines[i].match(/^\s*/)[0].length < indent) break;
    body.push(lines[i].slice(indent));
  }
  return body.join("\n");
}

/** The one-line `run:` of the step whose name matches `name`. */
function runLine(name) {
  const lines = ACTION.split("\n");
  const start = lines.findIndex((l) => name.test(l));
  assert.notEqual(start, -1, `no step matching ${name}`);
  const line = lines.slice(start).find((l) => /^\s+run: (?![|])/.test(l));
  return line.replace(/^\s+run: /, "");
}

/** A temp dir with a fake `forge` that logs its argv and answers per subcommand. */
function sandbox(deployExit) {
  const dir = mkdtempSync(join(tmpdir(), "forge-deploy-action-"));
  writeFileSync(
    join(dir, "forge"),
    `#!/usr/bin/env bash
echo "$*" >> "${dir}/forge.log"
if [ "$1 $2" = "env deploy" ]; then
  echo '{"recorded":{"id":"promo-123"},"exit_code":${deployExit}}'
  exit ${deployExit}
fi
exit 0
`,
  );
  chmodSync(join(dir, "forge"), 0o755);
  writeFileSync(join(dir, "output"), "");
  return dir;
}

function runBash(script, dir, env) {
  return spawnSync("bash", ["--noprofile", "--norc", "-eo", "pipefail", "-c", script], {
    cwd: dir,
    encoding: "utf8",
    env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, GITHUB_OUTPUT: join(dir, "output"), ...env },
  });
}

const DEPLOY_ENV = { ENV: "prod", VERSION: "v1.2.3", FROM: "", FROM_PROMOTION: "", EXPECT_CURRENT: "", GATES: "", TIMEOUT: "15m" };

function outputsOf(dir) {
  return Object.fromEntries(
    readFileSync(join(dir, "output"), "utf8")
      .split("\n")
      .filter(Boolean)
      .map((l) => l.split(/=(.*)/s).slice(0, 2)),
  );
}

for (const code of [1, 8]) {
  test(`a deploy exiting ${code} still writes its outputs, and the evidence step records it`, () => {
    const dir = sandbox(code);
    const deploy = runBash(runBlock(/^\s+- id: deploy\s*$/), dir, DEPLOY_ENV);
    assert.equal(deploy.status, code, deploy.stderr);
    assert.deepEqual(outputsOf(dir), { "promotion-id": "promo-123", "exit-code": String(code) });

    // The evidence step's `if:` is `always() && steps.deploy.outputs.promotion-id != ''`.
    assert.match(ACTION, /if: always\(\) && steps\.deploy\.outputs\.promotion-id != ''/);
    const evidence = runBash(runLine(/name: Record the rollout as evidence/), dir, { ENV: "prod", PROMOTION_ID: outputsOf(dir)["promotion-id"] });
    assert.equal(evidence.status, 0, evidence.stderr);
    assert.match(readFileSync(join(dir, "forge.log"), "utf8"), /^gate record prod --promotion promo-123 --from deploy\.json --name rollout$/m);
  });
}

test("a green deploy writes exit-code 0", () => {
  const dir = sandbox(0);
  const deploy = runBash(runBlock(/^\s+- id: deploy\s*$/), dir, DEPLOY_ENV);
  assert.equal(deploy.status, 0, deploy.stderr);
  assert.deepEqual(outputsOf(dir), { "promotion-id": "promo-123", "exit-code": "0" });
  assert.match(readFileSync(join(dir, "forge.log"), "utf8"), /^env deploy prod v1\.2\.3 --json --yes --timeout 15m$/m);
});
