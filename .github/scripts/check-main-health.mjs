#!/usr/bin/env node
// Is main green, and did the release gate actually run on it?
//
// ## Why this exists
//
// CI runs two tiers (ci.yml, dev-docs/merge-and-release-gates.md): the merge
// gate on pull requests (affected tests, `-short`, direct importers only) and
// the release gate on pushes to main and nightly (everything: `-race`, every
// slow test, integration, e2e). Every slow test and every transitive
// dependent runs ONLY in the release gate. So a red main is no longer "a PR
// that slipped through"; it is the one place those tests can fail at all, and
// it has to be seen.
//
// A red push run notifies only the person who merged, and a red scheduled run
// notifies only whoever last edited the cron. Neither is a durable signal. This
// script produces the verdict; gate-health.yml turns it into one open issue
// that stays open until main is green again.
//
// ## What counts as red
//
// It reads the newest COMPLETED ci.yml run on the branch that has a verdict
// (push, schedule or workflow_dispatch; a run cancelled because a newer one
// superseded it has no verdict and is passed over) and reports red when:
//
//   - the run's conclusion is anything but success, or
//   - the required job (default "Release gate") is missing from that run or
//     did not succeed. "Missing" is the tiers silently merging: if main ever
//     stops running the release gate, this fails rather than reporting a
//     green run that ran only the lanes.
//
// ## Usage
//
//   node .github/scripts/check-main-health.mjs
//   node .github/scripts/check-main-health.mjs --report /tmp/body.md --json
//
// Exits 0 when green, 1 when red, 2 when it could not decide (gh failed, no
// run with a verdict). Under GitHub Actions it also writes `verdict`
// (green|red), `run_url` and `sha` to $GITHUB_OUTPUT, so a caller can tell
// "main is red" from "the audit itself broke".

import { execFileSync } from "node:child_process";
import { appendFileSync, writeFileSync } from "node:fs";

import { requiredJobProblems } from "./gate-health-lib.mjs";

const args = process.argv.slice(2);

function argValue(flag, fallback) {
  const i = args.indexOf(flag);
  return i !== -1 && args[i + 1] !== undefined ? args[i + 1] : fallback;
}

const workflow = argValue("--workflow", "ci.yml");
const branch = argValue("--branch", "main");
const requiredJob = argValue("--require-job", "Release gate");
const runCount = Number(argValue("--runs", "15"));
const reportPath = argValue("--report", "");
const repo = argValue("--repo", "");
const asJson = args.includes("--json");

/** Conclusions that are a verdict on the code. Anything else is passed over. */
const VERDICT_CONCLUSIONS = new Set(["success", "failure", "timed_out", "startup_failure", "action_required"]);

function undecided(message) {
  console.error(`main-health: ${message}`);
  process.exit(2);
}

function gh(ghArgs) {
  const full = repo ? [...ghArgs, "--repo", repo] : ghArgs;
  try {
    return execFileSync("gh", full, { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
  } catch (err) {
    const detail = (err.stderr || err.message || "").toString().trim();
    undecided(`\`gh ${full.join(" ")}\` failed:\n${detail}`);
  }
}

function setOutput(name, value) {
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `${name}=${value}\n`);
}

const runs = JSON.parse(
  gh([
    "run", "list",
    "--workflow", workflow,
    "--branch", branch,
    "--limit", String(runCount),
    "--json", "databaseId,conclusion,status,event,headSha,createdAt,url,displayTitle",
  ]),
)
  // A pull_request run whose head branch happens to be named like the audited
  // branch (a fork's `main`) says nothing about the real one.
  .filter((run) => run.event !== "pull_request")
  .filter((run) => run.status === "completed" && VERDICT_CONCLUSIONS.has(run.conclusion))
  .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));

if (runs.length === 0) {
  // Not green. No verdict in the last N runs means main has gone unverified,
  // which is the condition this exists to surface, not a reason to pass.
  undecided(
    `no completed ${workflow} run with a verdict on ${branch} in the last ${runCount}.\n` +
      `Either every recent run was cancelled, or --workflow/--branch match nothing.`,
  );
}

const run = runs[0];
const jobs = JSON.parse(gh(["run", "view", String(run.databaseId), "--json", "jobs"])).jobs ?? [];
const failedJobs = jobs.filter((job) => job.conclusion && !["success", "skipped", "neutral"].includes(job.conclusion));
const tierProblems = requiredJobProblems(jobs, requiredJob);

const reasons = [];
if (run.conclusion !== "success") {
  reasons.push(`the run concluded \`${run.conclusion}\``);
}
if (tierProblems.length > 0) {
  reasons.push(
    `${tierProblems.join("; ")} (the release gate is the only place every slow test, the race ` +
      `detector and integration/e2e run; did ci.yml's release-gate job selection change?)`,
  );
}

const red = reasons.length > 0;
const sha = run.headSha.slice(0, 12);

const body = [
  red
    ? `CI is **red** on \`${branch}\` at \`${sha}\` (${run.event}): ${reasons.join("; ")}.`
    : `CI is green on \`${branch}\` at \`${sha}\` (${run.event}).`,
  "",
  `Run: ${run.url}`,
  ...(failedJobs.length
    ? ["", "Failed jobs:", ...failedJobs.map((job) => `- [${job.name}](${job.url}) — \`${job.conclusion}\``)]
    : []),
  "",
  "The release gate runs only on `main` (push and nightly); pull requests run the merge gate. " +
    "A test that fails here may never have run on the PR that broke it. Reproduce locally with " +
    "`task release:gate -- <sha>` (or `RG_ONLY=<step> scripts/release-gate.sh run <sha>`), or run " +
    "it on a branch with ci.yml's `workflow_dispatch`. This issue is opened and closed by " +
    "`.github/workflows/gate-health.yml`.",
].join("\n");

if (reportPath) writeFileSync(reportPath, `${body}\n`);
setOutput("verdict", red ? "red" : "green");
setOutput("run_url", run.url);
setOutput("sha", sha);

if (asJson) {
  console.log(JSON.stringify({ workflow, branch, requiredJob, red, reasons, run, failedJobs }, null, 2));
} else {
  console.log(body);
}
process.exit(red ? 1 : 0);
