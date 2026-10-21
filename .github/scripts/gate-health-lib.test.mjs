// node --test .github/scripts/*.test.mjs
//
// The streak rule check-gate-health.mjs applies to each pr-ci.yml job.

import assert from "node:assert/strict";
import test from "node:test";

import { badStreak, shardedTierProblems } from "./gate-health-lib.mjs";

const pr = (conclusion) => ({ conclusion, event: "pull_request" });
const push = (conclusion) => ({ conclusion, event: "push" });
const nightly = (conclusion) => ({ conclusion, event: "schedule" });

test("consecutive failures from the newest run form the streak", () => {
  assert.equal(badStreak([push("failure"), pr("failure"), pr("cancelled"), push("success"), pr("failure")]), 3);
});

test("one success clears everything older", () => {
  assert.equal(badStreak([pr("success"), push("failure"), push("failure")]), 0);
});

test("a path-filtered skip on a PR neither extends nor clears the streak", () => {
  // Five web-only PRs skipped the Go lanes: that is the filter working.
  assert.equal(badStreak([pr("skipped"), pr("skipped"), pr("skipped"), pr("skipped"), pr("skipped")]), 0);
  // ...and they do not hide a red main underneath them.
  assert.equal(badStreak([pr("skipped"), push("failure"), pr("skipped"), nightly("failure"), push("success")]), 2);
});

test("a skip on main or the nightly is still bad: main never path-filters", () => {
  assert.equal(badStreak([push("skipped"), nightly("skipped"), push("skipped")]), 3);
});

test("cancelled and timed_out stay bad on PRs", () => {
  assert.equal(badStreak([pr("cancelled"), pr("timed_out"), pr("success")]), 2);
});

// check-main-health.mjs: main's full Go tier is green only when every shard is.
const job = (name, conclusion = "success") => ({ name, conclusion });
const FULL = "Backend (Go, full)";

test("every shard present and green is a pass", () => {
  assert.deepEqual(shardedTierProblems([job(`${FULL} 2/2`), job("Lint (forge)"), job(`${FULL} 1/2`)], FULL), []);
  assert.deepEqual(shardedTierProblems([job(`${FULL} 1/1`)], FULL), []);
});

test("no shard at all is the full tier not running", () => {
  const problems = shardedTierProblems([job("Lint (forge)"), job("Backend (Go, short) 1/1")], FULL);
  assert.equal(problems.length, 1);
  assert.match(problems[0], /did not run/);
});

test("a missing or red shard fails, naming it", () => {
  assert.deepEqual(shardedTierProblems([job(`${FULL} 1/3`), job(`${FULL} 3/3`)], FULL), [`\`${FULL} 2/3\` is missing`]);
  assert.deepEqual(shardedTierProblems([job(`${FULL} 1/2`), job(`${FULL} 2/2`, "cancelled")], FULL), [
    `\`${FULL} 2/2\` concluded \`cancelled\``,
  ]);
});

test("shards that disagree on the count fail", () => {
  assert.equal(shardedTierProblems([job(`${FULL} 1/2`), job(`${FULL} 2/4`)], FULL).length, 1);
});
