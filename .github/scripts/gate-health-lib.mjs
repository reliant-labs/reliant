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
 * A SKIP IS NO EVIDENCE EITHER WAY. ci.yml selects its jobs by event and by
 * path: the merge gate runs only on pull requests, the release gate only off
 * them, and a web-only PR skips the electron lane. So a run of skips is the
 * selection working as designed, not a gate going dark, and skips neither
 * extend nor clear the streak. A job that should have run and did not is
 * caught elsewhere: ci.yml's `CI result` fails a run whose tier gate did not
 * run, or (off a pull request) any lane that was skipped, and
 * check-main-health.mjs fails main when its newest run has no green release
 * gate.
 */
export function badStreak(results) {
  let streak = 0;
  for (const r of results) {
    if (r.conclusion === "skipped") continue;
    if (!BAD_CONCLUSIONS.has(r.conclusion)) break;
    streak++;
  }
  return streak;
}

/**
 * Why a run's required job did not pass, or [] when it did.
 *
 * ci.yml runs the whole suite on main as ONE job, "Release gate" (`task
 * release:gate`). Main is green only when that job is present and succeeded:
 * a run without it is the release tier not running, which must not read as
 * green because every other job passed.
 *
 * jobs: [{ name, conclusion }] of one run.
 */
export function requiredJobProblems(jobs, name) {
  const matching = jobs.filter((job) => job.name === name);
  if (matching.length === 0) {
    return [`it has no \`${name}\` job: the tier did not run`];
  }
  return matching
    .filter((job) => job.conclusion !== "success")
    .map((job) => `\`${name}\` concluded \`${job.conclusion ?? "nothing"}\``);
}
