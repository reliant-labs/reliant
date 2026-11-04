// node --test .github/scripts/*.test.mjs
//
// The streak rule check-gate-health.mjs applies to each ci.yml job, and the
// required-job rule check-main-health.mjs applies to main's newest run.

import assert from "node:assert/strict";
import test from "node:test";

import { badStreak, requiredJobProblems } from "./gate-health-lib.mjs";

const pr = (conclusion) => ({ conclusion, event: "pull_request" });
const push = (conclusion) => ({ conclusion, event: "push" });
const nightly = (conclusion) => ({ conclusion, event: "schedule" });

test("consecutive failures from the newest run form the streak", () => {
  assert.equal(badStreak([push("failure"), pr("failure"), pr("cancelled"), push("success"), pr("failure")]), 3);
});

test("one success clears everything older", () => {
  assert.equal(badStreak([pr("success"), push("failure"), push("failure")]), 0);
});

test("a skip neither extends nor clears the streak, on any event", () => {
  // Five web-only PRs skipped the electron lane: that is the selection working.
  assert.equal(badStreak([pr("skipped"), pr("skipped"), pr("skipped"), pr("skipped"), pr("skipped")]), 0);
  // The merge gate is skipped on every main run by design.
  assert.equal(badStreak([push("skipped"), nightly("skipped"), push("skipped")]), 0);
  // ...and skips do not hide a red streak underneath them.
  assert.equal(badStreak([pr("skipped"), push("failure"), pr("skipped"), nightly("failure"), push("success")]), 2);
});

test("cancelled and timed_out stay bad on PRs", () => {
  assert.equal(badStreak([pr("cancelled"), pr("timed_out"), pr("success")]), 2);
});

// check-main-health.mjs: main is green only when its release gate ran and passed.
const job = (name, conclusion = "success") => ({ name, conclusion });
const GATE = "Release gate";

test("the required job present and green is a pass", () => {
  assert.deepEqual(requiredJobProblems([job("Lanes"), job(GATE), job("CI result")], GATE), []);
});

test("no required job at all is the tier not running", () => {
  const problems = requiredJobProblems([job("Lanes"), job("Merge gate")], GATE);
  assert.equal(problems.length, 1);
  assert.match(problems[0], /did not run/);
});

test("a red, cancelled or skipped required job fails, naming its conclusion", () => {
  assert.deepEqual(requiredJobProblems([job(GATE, "failure")], GATE), [`\`${GATE}\` concluded \`failure\``]);
  assert.deepEqual(requiredJobProblems([job(GATE, "cancelled")], GATE), [`\`${GATE}\` concluded \`cancelled\``]);
  assert.deepEqual(requiredJobProblems([job(GATE, "skipped")], GATE), [`\`${GATE}\` concluded \`skipped\``]);
});
