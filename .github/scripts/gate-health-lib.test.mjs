// node --test .github/scripts/*.test.mjs
//
// The streak rule check-gate-health.mjs applies to each pr-ci.yml job.

import assert from "node:assert/strict";
import test from "node:test";

import { badStreak } from "./gate-health-lib.mjs";

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
