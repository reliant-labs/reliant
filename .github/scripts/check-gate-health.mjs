#!/usr/bin/env node
// Detects a CI gate that has gone DARK — failing, cancelled, or skipped for N
// consecutive runs — and fails loudly so somebody notices.
//
// ## Why this exists
//
// `pr-ci.yml`'s own comment records the incident this is built for: the
// Playwright job "was cancelled at 15m in every one of the last 50 runs that
// reached the test step," and "was never a slow-but-passing job; the Playwright
// suite itself was genuinely failing." Fifty consecutive runs of zero signal,
// and nothing anywhere noticed. Several regressions shipped during that window.
//
// A red job is loud on the PR that caused it. A PERMANENTLY red job is silent:
// everyone learns it is always red, stops reading it, and it becomes decoration.
// Cancellation is worse still, because a timeout reads as infrastructure noise
// rather than a product defect. Same for a job that has quietly become a no-op
// skip. All three are the same failure: a gate that cannot fail is
// indistinguishable from a gate that passes.
//
// ## Why a script and not a GitHub Actions feature
//
// Actions has no native expression of "this job has been red for N runs." A
// workflow only ever sees its own run. Branch protection distinguishes required
// from not-required, never healthy from permanently broken, and a required check
// that always fails just blocks merges instead of raising an alarm — which is
// how it gets marked not-required and goes fully dark.
//
// So the smallest honest thing is a scheduled job that reads run history back
// out of the API. That is what this is: `gh run list` for recent runs of a
// workflow on the default branch, `gh run view --json jobs` for each one's job
// conclusions, then a per-job consecutive-bad-streak count. No new
// infrastructure, no external monitoring service, no secrets beyond the
// automatic GITHUB_TOKEN.
//
// ## Usage
//
//   node .github/scripts/check-gate-health.mjs --workflow pr-ci.yml
//   node .github/scripts/check-gate-health.mjs --workflow pr-ci.yml --runs 20 --threshold 5
//   node .github/scripts/check-gate-health.mjs --workflow pr-ci.yml --json
//
// Exits 1 when any job's consecutive bad streak reaches --threshold.

import { execFileSync } from "node:child_process";

const args = process.argv.slice(2);

function argValue(flag, fallback) {
  const i = args.indexOf(flag);
  return i !== -1 && args[i + 1] !== undefined ? args[i + 1] : fallback;
}

const workflow = argValue("--workflow", "pr-ci.yml");
// Empty means "any branch", which is what PR-triggered workflows need:
// pr-ci.yml runs on `pull_request` only, so it has NO runs on main and a
// main-scoped audit would silently find nothing — the same do-nothing gate this
// script exists to detect.
const branch = argValue("--branch", "");
// How far back to look, and how many consecutive bad results constitute "dark".
// 5 is deliberately well below the 50 that went unnoticed: the point is to fire
// while somebody still remembers changing something.
const runCount = Number(argValue("--runs", "15"));
const threshold = Number(argValue("--threshold", "5"));
const asJson = args.includes("--json");
const repo = argValue("--repo", "");

/** A job outcome that means "this gate produced no pass signal". */
const BAD_CONCLUSIONS = new Set(["failure", "cancelled", "timed_out", "skipped"]);

function gh(ghArgs) {
  const full = repo ? [...ghArgs, "--repo", repo] : ghArgs;
  try {
    return execFileSync("gh", full, { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
  } catch (err) {
    // A raw execFileSync throw dumps a Node stack trace that buries the actual
    // gh message (typically a 404 for a mistyped workflow name). Exit non-zero
    // with the cause legible instead.
    const detail = (err.stderr || err.message || "").toString().trim();
    console.error(`gate-health: \`gh ${full.join(" ")}\` failed:\n${detail}`);
    process.exit(1);
  }
}

function listRuns() {
  const out = gh([
    "run", "list",
    "--workflow", workflow,
    ...(branch ? ["--branch", branch] : []),
    "--limit", String(runCount),
    "--json", "databaseId,conclusion,status,createdAt,headSha",
  ]);
  return JSON.parse(out)
    // Only completed runs carry meaningful job conclusions. An in-flight run is
    // not evidence of anything and must not reset or extend a streak.
    .filter((run) => run.status === "completed")
    // Newest first is what `gh` returns, and the streak is counted from newest.
    .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
}

function jobsForRun(runId) {
  const out = gh(["run", "view", String(runId), "--json", "jobs"]);
  return JSON.parse(out).jobs ?? [];
}

const scope = branch ? `on ${branch}` : "on any branch";
const runs = listRuns();
if (runs.length === 0) {
  // Not a pass. Finding no runs means either the workflow name is wrong or the
  // branch filter excludes every run — in both cases this check verified
  // nothing, and reporting success would make it the very thing it detects.
  console.error(
    `gate-health: found NO completed runs of ${workflow} ${scope}.\n` +
      `This check verified nothing. Either --workflow names a file that does not\n` +
      `exist, or --branch filters out every run (a pull_request-triggered\n` +
      `workflow has no runs on the default branch — pass no --branch for those).`,
  );
  process.exit(1);
}

// job name -> [{runId, conclusion}], newest first.
const history = new Map();
for (const run of runs) {
  for (const job of jobsForRun(run.databaseId)) {
    if (!history.has(job.name)) history.set(job.name, []);
    history.get(job.name).push({ runId: run.databaseId, conclusion: job.conclusion });
  }
}

const report = [];
for (const [name, results] of history) {
  // Consecutive bad results from the most recent run backwards. A single
  // success anywhere in the streak clears it — one green run proves the gate
  // can still pass, which is exactly the property being monitored.
  let streak = 0;
  for (const r of results) {
    if (!BAD_CONCLUSIONS.has(r.conclusion)) break;
    streak++;
  }
  report.push({
    job: name,
    streak,
    observed: results.length,
    latest: results[0]?.conclusion ?? "unknown",
    dark: streak >= threshold,
  });
}
report.sort((a, b) => b.streak - a.streak);

if (asJson) {
  console.log(JSON.stringify({ workflow, branch, threshold, jobs: report }, null, 2));
}

const dark = report.filter((r) => r.dark);

if (!asJson) {
  console.log(`gate-health: ${workflow} ${scope}, ${runs.length} completed run(s), threshold ${threshold}\n`);
  for (const r of report) {
    const mark = r.dark ? "DARK" : r.streak > 0 ? "warn" : "ok  ";
    console.log(`  ${mark}  ${r.job} — latest ${r.latest}, bad streak ${r.streak}/${r.observed}`);
  }
}

if (dark.length === 0) {
  console.log(`\ngate-health: no dark gates`);
  process.exit(0);
}

console.error(
  `\ngate-health: ${dark.length} gate(s) have produced NO pass signal for ${threshold}+ consecutive runs:\n` +
    dark.map((r) => `  - ${r.job} (${r.streak} consecutive ${r.latest}/bad results)`).join("\n") +
    `\n\nA gate in this state is not protecting anything, and its silence is the\n` +
    `problem: a permanently red or cancelled job stops being read long before\n` +
    `anyone decides to fix it. Either repair it or delete it — leaving it in\n` +
    `place manufactures belief in coverage that does not exist.`,
);
process.exit(1);
