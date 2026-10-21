// The decision logic of check-gate-health.mjs, kept free of `gh` calls so
// gate-health-lib.test.mjs can pin it.

/** A job outcome that means "this gate produced no pass signal". */
export const BAD_CONCLUSIONS = new Set(["failure", "cancelled", "timed_out", "skipped"]);

/**
 * The consecutive bad streak of one job, counted from its newest result.
 *
 * results: [{ conclusion, event }], newest first. `event` is the run's
 * triggering event (`pull_request`, `push`, `schedule`, ...).
 *
 * A single success anywhere clears the streak: one green run proves the gate
 * can still pass, which is the property being monitored.
 *
 * A SKIP ON A PULL REQUEST IS NO EVIDENCE EITHER WAY. pr-ci.yml path-filters
 * its PR jobs (a web-only PR skips the Go lanes, a Go-only PR skips the web
 * lanes), so a run of skipped PR results is the gate working as designed, not
 * going dark. Those results neither extend nor clear the streak. Runs on main
 * and the nightly never path-filter, so a skip THERE still counts as bad: that
 * is how a job whose `if:` broke and stopped running everywhere is caught.
 */
export function badStreak(results) {
  let streak = 0;
  for (const r of results) {
    if (r.conclusion === "skipped" && r.event === "pull_request") continue;
    if (!BAD_CONCLUSIONS.has(r.conclusion)) break;
    streak++;
  }
  return streak;
}

/**
 * Why a run's sharded test tier did not pass, or [] when it did.
 *
 * pr-ci.yml runs the Go tests as "<prefix> N/M" shard jobs ("Backend (Go,
 * full) 1/4" ... "4/4" on main) and M follows the selection. The tier passed
 * only when every shard 1..M is present and succeeded: a missing shard is a
 * slice of the module that never ran, and NO shard at all is the full tier
 * not running (the PR and full tiers silently merging).
 *
 * jobs: [{ name, conclusion }] of one run.
 */
export function shardedTierProblems(jobs, prefix) {
  const escaped = prefix.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const shardName = new RegExp(`^${escaped} (\\d+)/(\\d+)$`);
  const shards = [];
  for (const job of jobs) {
    const m = shardName.exec(job.name);
    if (m) shards.push({ index: Number(m[1]), total: Number(m[2]), conclusion: job.conclusion });
  }
  if (shards.length === 0) {
    return [`it has no \`${prefix} N/M\` job: the tier did not run`];
  }
  const totals = [...new Set(shards.map((s) => s.total))];
  if (totals.length !== 1) {
    return [`its \`${prefix}\` shards disagree on the shard count (${totals.join(", ")})`];
  }
  const total = totals[0];
  const problems = [];
  for (let i = 1; i <= total; i++) {
    const shard = shards.find((s) => s.index === i);
    if (!shard) problems.push(`\`${prefix} ${i}/${total}\` is missing`);
    else if (shard.conclusion !== "success") problems.push(`\`${prefix} ${i}/${total}\` concluded \`${shard.conclusion}\``);
  }
  return problems;
}
