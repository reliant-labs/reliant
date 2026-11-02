// node --test .github/scripts/*.test.mjs
//
// main-red-issue.sh against a fake `gh` that answers `issue list` / `issue
// view` from fixtures (applying --jq as the real one does) and logs every
// call. Nothing here talks to GitHub.
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "main-red-issue.sh");
const RUN = "https://github.com/reliant-labs/reliant/actions/runs/42";
const hasJq = spawnSync("jq", ["--version"]).status === 0;

const FAKE_GH = `#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
case "$1 $2" in
  "issue list") fixture="$GH_LIST" ;;
  "issue view") fixture="$GH_VIEW" ;;
  *) exit 0 ;;
esac
q="" prev=""
for a in "$@"; do [ "$prev" = --jq ] && q="$a"; prev="$a"; done
jq -r "$q" "$fixture"
`;

// run(verdict, issues, view) -> the gh calls, one per line, minus the label upsert.
function run(verdict, issues, view = { body: "", comments: [] }) {
  const dir = mkdtempSync(join(tmpdir(), "main-red-"));
  writeFileSync(join(dir, "gh"), FAKE_GH);
  chmodSync(join(dir, "gh"), 0o755);
  writeFileSync(join(dir, "list.json"), JSON.stringify(issues));
  writeFileSync(join(dir, "view.json"), JSON.stringify(view));
  writeFileSync(join(dir, "report.md"), `main is red: ${RUN}\n`);
  writeFileSync(join(dir, "gh.log"), "");
  const r = spawnSync("bash", [SCRIPT], {
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${dir}:${process.env.PATH}`,
      GH_LOG: join(dir, "gh.log"),
      GH_LIST: join(dir, "list.json"),
      GH_VIEW: join(dir, "view.json"),
      VERDICT: verdict,
      RUN_URL: RUN,
      SHA: "abc123",
      REPORT: join(dir, "report.md"),
    },
  });
  assert.equal(r.status, 0, r.stderr);
  return readFileSync(join(dir, "gh.log"), "utf8")
    .split("\n")
    .filter((l) => l && !l.startsWith("label create") && !l.startsWith("issue list") && !l.startsWith("issue view"));
}

const verbs = (calls) => calls.map((c) => c.split(" ").slice(0, 3).join(" "));

test("red with no issue ever filed creates one", { skip: !hasJq && "jq not on PATH" }, () => {
  assert.deepEqual(verbs(run("red", [])), ["issue create --title"]);
});

test("red after a green reopens the last issue instead of creating another", { skip: !hasJq && "jq not on PATH" }, () => {
  const calls = run("red", [{ number: 1906, state: "CLOSED" }]);
  assert.deepEqual(verbs(calls), ["issue reopen 1906", "issue edit 1906", "issue comment 1906"]);
  assert.match(calls[1], /--title main is red: CI$/);
  assert.match(calls[2], /--body-file /);
});

test("red on an open issue comments a new run once", { skip: !hasJq && "jq not on PATH" }, () => {
  assert.deepEqual(verbs(run("red", [{ number: 2005, state: "OPEN" }])), ["issue comment 2005"]);
  const seen = { body: `first report: ${RUN}`, comments: [] };
  assert.deepEqual(run("red", [{ number: 2005, state: "OPEN" }], seen), []);
});

test("green closes the open issue and leaves a closed one alone", { skip: !hasJq && "jq not on PATH" }, () => {
  assert.deepEqual(verbs(run("green", [{ number: 2005, state: "OPEN" }])), ["issue close 2005"]);
  assert.deepEqual(run("green", [{ number: 2005, state: "CLOSED" }]), []);
  assert.deepEqual(run("green", []), []);
});
