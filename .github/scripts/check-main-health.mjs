#!/usr/bin/env node
// Is main green, and did the full test tier actually run on it?
//
// ## Why this exists
//
// PR CI runs Go tests in two tiers (pr-ci.yml): `-short` on pull requests, the
// full suite on pushes to main and nightly. Every testing.Short()-gated test —
// the ones that wait out real timeouts, start a NATS cluster, and so on — now
// runs ONLY on main. So a red main is no longer "a PR that slipped through"; it
// is the one place those tests can fail at all, and it has to be seen.
//
// A red push run notifies only the person who merged, and a red scheduled run
// notifies only whoever last edited the cron. Neither is a durable signal. This
// script produces the verdict; gate-health.yml turns it into one open issue
// that stays open until main is green again.
//
// ## What counts as red
//
// It reads the newest COMPLETED pr-ci.yml run on the branch that has a verdict
// (push, schedule or workflow_dispatch; a run cancelled because a newer one
// superseded it has no verdict and is passed over) and reports red when:
//
//   - the run's conclusion is anything but success, or
//   - the required job (default "Backend (Go, full)") is missing from that run
//     or did not succeed. "Missing" is the tiers silently merging: if main ever
//     stops running the full tier, this fails rather than reporting a green run
//     that ran only the short one.
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

const args = process.argv.slice(2);

function argValue(flag, fallback) {
  const i = args.indexOf(flag);
  return i !== -1 && args[i + 1] !== undefined ? args[i + 1] : fallback;
}

const workflow = argValue("--workflow", "pr-ci.yml");
const branch = argValue("--branch", "main");
const requiredJob = argValue("--require-job", "Backend (Go, full)");
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
const tierJob = jobs.find((job) => job.name === requiredJob);

const reasons = [];
if (run.conclusion !== "success") {
  reasons.push(`the run concluded \`${run.conclusion}\``);
}
if (!tierJob) {
  reasons.push(
    `it has no \`${requiredJob}\` job: the full test tier did not run on ${branch}, so every ` +
      `short-gated test went unrun (did the backend job's tier selection change?)`,
  );
} else if (tierJob.conclusion !== "success") {
  reasons.push(`\`${requiredJob}\` concluded \`${tierJob.conclusion}\``);
}

const red = reasons.length > 0;
const sha = run.headSha.slice(0, 12);

const body = [
  red
    ? `PR CI is **red** on \`${branch}\` at \`${sha}\` (${run.event}): ${reasons.join("; ")}.`
    : `PR CI is green on \`${branch}\` at \`${sha}\` (${run.event}).`,
  "",
  `Run: ${run.url}`,
  ...(failedJobs.length
    ? ["", "Failed jobs:", ...failedJobs.map((job) => `- [${job.name}](${job.url}) — \`${job.conclusion}\``)]
    : []),
  "",
  "The full Go test tier runs only on `main` (push and nightly); pull requests run `go test -short`. " +
    "A test that fails here may never have run on the PR that broke it. Reproduce locally with " +
    "`go test ./<pkg>/...` (no `-short`), or run the full tier on a branch with PR CI's " +
    "`workflow_dispatch`. This issue is opened and closed by `.github/workflows/gate-health.yml`.",
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
