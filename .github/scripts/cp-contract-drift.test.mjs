// cp-contract-drift.yml decides "behind" from sync-controlplane-proto.mjs's
// exit status 3. Run through `task`, a failing command is reported as 201 and
// the workflow would read every real drift as "could not decide", so the issue
// would never open. This pins the drift step to the script itself, and the
// script's "behind" status to the 3 the step's case statement expects.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const workflow = readFileSync(join(ROOT, ".github/workflows/cp-contract-drift.yml"), "utf8");
const script = readFileSync(join(ROOT, ".github/scripts/sync-controlplane-proto.mjs"), "utf8");

test("the drift step runs the script directly, not through task", () => {
  const step = workflow.slice(workflow.indexOf("id: drift"), workflow.indexOf("- name: Open, update or close"));
  assert.match(step, /node \.github\/scripts\/sync-controlplane-proto\.mjs --upstream/);
  assert.doesNotMatch(step.replace(/#.*$/gm, ""), /^\s*task\s/m, "Task rewrites exit 3 to 201");
  assert.match(step, /\n\s*3\) echo "verdict=behind"/);
});

test("the script reports behind as exit 3", () => {
  const upstream = script.slice(script.indexOf("// ── --upstream"), script.indexOf("// ── write"));
  assert.match(upstream, /process\.exit\(3\)/);
});
